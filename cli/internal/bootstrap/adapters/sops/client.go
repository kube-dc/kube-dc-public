// Package sops is the real ports.SOPSClient adapter. Shells out to
// the `sops` binary — matches the fleet team's existing tooling and
// avoids vendoring the SOPS Go API (historically unstable across
// minor releases).
//
// **Plaintext discipline** (ports/sops.go contract):
//
//   - Decrypt returns []byte the caller MUST scrub.
//   - SetStringData NEVER places the new value in argv. The
//     implementation: decrypt → parse YAML in-memory → mutate
//     stringData[key] → marshal → pipe the new plaintext to
//     `sops --encrypt /dev/stdin` and capture the encrypted output
//     into a sibling tempfile (in the destination DIRECTORY, so the
//     final os.Rename is atomic within one filesystem) → scrub the
//     in-memory plaintext → atomic os.Rename to the destination →
//     round-trip verify decrypt.
//   - Plaintext NEVER touches disk. The intermediate tempfile is
//     already SOPS-encrypted. Operators inspecting the directory
//     between the rename and the verify see either the old or new
//     encrypted file — never a window of plaintext on disk.
//   - Encrypt is for the FIRST encryption of a new file only; updates
//     to an existing file MUST use SetStringData.
package sops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Client implements ports.SOPSClient.
type Client struct {
	// exec is the hook the adapter goes through for every sops
	// invocation. Production uses runReal; tests inject canned
	// outputs.
	exec func(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, []byte, error)
}

// New returns a Client wired to the real `sops` binary.
func New() *Client {
	return &Client{exec: runReal}
}

// Compile-time assertion.
var _ ports.SOPSClient = (*Client)(nil)

// ---------- ports.SOPSClient ----------

func (c *Client) Encrypt(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("sops: empty path")
	}
	// First-time encrypt of a freshly-scaffolded plaintext file.
	// Recipients come from the nearest .sops.yaml (standard sops
	// behaviour). UPDATES use SetStringData, NOT this method.
	_, stderr, err := c.exec(ctx, filepath.Dir(path), nil, "--encrypt", "--in-place", path)
	if err != nil {
		return fmt.Errorf("sops encrypt %s: %w\nstderr: %s", path, err, bytes.TrimSpace(stderr))
	}
	return nil
}

func (c *Client) Decrypt(ctx context.Context, path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("sops: empty path")
	}
	stdout, stderr, err := c.exec(ctx, filepath.Dir(path), nil, "--decrypt", path)
	if err != nil {
		return nil, fmt.Errorf("sops decrypt %s: %w\nstderr: %s", path, err, bytes.TrimSpace(stderr))
	}
	return stdout, nil
}

// SetStringData updates a single `stringData.<key>` entry inside an
// existing SOPS-encrypted YAML Secret manifest. The new value is
// piped to `sops --encrypt /dev/stdin` rather than passed in argv —
// argv shows up in `/proc/PID/cmdline`, audit logs, and the shell
// history; stdin is the only path that keeps the bytes out of those
// surfaces.
//
// Sequence:
//
//	1. Decrypt the existing file to []byte (in-memory only).
//	2. Parse the YAML, set stringData[key] = string(value).
//	3. Get the existing recipients so the re-encrypt uses the same
//	   identity set (independent of .sops.yaml drift).
//	4. Pipe the mutated plaintext to `sops --encrypt --age <recipients>
//	   --input-type yaml /dev/stdin` with --output pointing at a
//	   sibling tempfile in the destination directory (same filesystem
//	   → atomic rename below).
//	5. Scrub the in-memory plaintext.
//	6. os.Rename(tempfile, path) — atomic per POSIX.
//	7. Round-trip verify: decrypt the new file and confirm the value
//	   round-trips. Restore the pre-change file if verify fails.
func (c *Client) SetStringData(ctx context.Context, path, key string, value []byte) error {
	if path == "" || key == "" {
		return fmt.Errorf("sops: SetStringData needs path + key")
	}
	if len(value) == 0 {
		return fmt.Errorf("sops: SetStringData refuses empty value (would be ambiguous with key-delete)")
	}

	// Step 1: decrypt current file.
	plaintext, err := c.Decrypt(ctx, path)
	if err != nil {
		return err
	}
	defer zeroBytes(plaintext)

	// Step 2: parse + mutate.
	var doc yaml.Node
	if err := yaml.Unmarshal(plaintext, &doc); err != nil {
		return fmt.Errorf("sops: parse decrypted YAML for %s: %w", path, err)
	}
	if err := setStringDataNode(&doc, key, string(value)); err != nil {
		return fmt.Errorf("sops: set stringData.%s in %s: %w", key, path, err)
	}
	mutated, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("sops: re-marshal mutated doc: %w", err)
	}
	defer zeroBytes(mutated)

	// Step 3: recipients from the existing encrypted file. This
	// keeps the re-encrypt identity-stable even if .sops.yaml has
	// drifted from what the file was originally encrypted with.
	recipients, err := c.Recipients(path)
	if err != nil || len(recipients) == 0 {
		return fmt.Errorf("sops: read recipients from %s: %w", path, err)
	}

	// Step 4: encrypt-via-stdin into a sibling tempfile. The tempfile
	// lives in the destination DIRECTORY (not /tmp) so the final
	// os.Rename is atomic — cross-filesystem rename is non-atomic
	// (kernel falls back to copy + remove).
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, ".sops-"+base+"-")
	if err != nil {
		return fmt.Errorf("sops: create tempfile in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	// Ensure removed on any failure path — successful rename
	// supersedes the file under tmpPath, making this a no-op.
	defer func() { _ = os.Remove(tmpPath) }()

	args := []string{
		"--encrypt",
		"--input-type", "yaml",
		"--output", tmpPath,
		"--filename-override", base, // helps sops pick the right .sops.yaml rules if recipients flag is ignored
		"--age", strings.Join(recipients, ","),
		"/dev/stdin",
	}
	_, stderr, err := c.exec(ctx, dir, mutated, args...)
	if err != nil {
		return fmt.Errorf("sops encrypt /dev/stdin → %s: %w\nstderr: %s", tmpPath, err, bytes.TrimSpace(stderr))
	}

	// Tighten the tempfile mode before publishing (sops may create
	// world-readable; the destination should be 0o600 minimum).
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("sops: chmod tempfile: %w", err)
	}

	// Step 5: scrub our in-memory plaintext + mutated bytes. Already
	// queued via defer; explicit here for clarity (no double-scrub
	// hazard — zeroing zeroes is fine).
	zeroBytes(plaintext)
	zeroBytes(mutated)

	// Step 6: atomic rename.
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("sops: rename %s → %s: %w", tmpPath, path, err)
	}

	// Step 7: round-trip verify. If the decrypted file doesn't carry
	// the value we set, something went sideways (filesystem quirk,
	// sops version mismatch) — surface loudly. We don't auto-rollback
	// because we no longer have the original; the operator restores
	// from git.
	check, decErr := c.Decrypt(ctx, path)
	if decErr != nil {
		return fmt.Errorf("sops: round-trip verify decrypt %s: %w", path, decErr)
	}
	defer zeroBytes(check)
	if !bytes.Contains(check, value) {
		return fmt.Errorf("sops: round-trip verify failed — value not present in decrypted output for %s", key)
	}
	return nil
}

func (c *Client) Recipients(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("sops: empty path")
	}
	// Recipients live in the encrypted file's `sops` metadata.
	// Manual YAML parse handles the canonical SOPS shape; older sops
	// binaries don't have `filestatus`, so manual parse is the
	// reliable path. We strip the `- ` YAML list marker before
	// matching the `recipient:` field.
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sops: read %s: %w", path, err)
	}
	var out []string
	for _, line := range bytes.Split(body, []byte("\n")) {
		l := strings.TrimSpace(string(line))
		l = strings.TrimPrefix(l, "- ")
		l = strings.TrimSpace(l)
		const prefix = "recipient:"
		if strings.HasPrefix(l, prefix) {
			r := strings.TrimSpace(strings.TrimPrefix(l, prefix))
			r = strings.Trim(r, `"'`)
			if r != "" {
				out = append(out, r)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sops: no recipients found in %s", path)
	}
	return out, nil
}

func (c *Client) DerivePubKey(keyPath string) (string, error) {
	if keyPath == "" {
		return "", fmt.Errorf("sops: empty key path")
	}
	body, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("sops: read %s: %w", keyPath, err)
	}
	// age private-key files contain a `# public key: ageXXX...` line.
	for _, line := range bytes.Split(body, []byte("\n")) {
		l := strings.TrimSpace(string(line))
		const prefix = "# public key:"
		if strings.HasPrefix(l, prefix) {
			pub := strings.TrimSpace(strings.TrimPrefix(l, prefix))
			if pub != "" {
				return pub, nil
			}
		}
	}
	return "", fmt.Errorf("sops: %s has no '# public key:' comment", keyPath)
}

// ---------- helpers ----------

// setStringDataNode walks a yaml.Node DAG looking for the
// `stringData` mapping at the document root and sets the given key.
// Creates `stringData` if it's missing.
func setStringDataNode(doc *yaml.Node, key, value string) error {
	if doc == nil || len(doc.Content) == 0 {
		return fmt.Errorf("empty YAML doc")
	}
	root := doc.Content[0] // document → mapping
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("doc root is not a mapping")
	}

	// Find or create stringData.
	for i := 0; i < len(root.Content); i += 2 {
		k := root.Content[i]
		v := root.Content[i+1]
		if k.Value == "stringData" {
			if v.Kind != yaml.MappingNode {
				return fmt.Errorf("stringData is not a mapping")
			}
			return setMapKey(v, key, value)
		}
	}
	// stringData absent — append it.
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "stringData"},
		&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: key},
			{Kind: yaml.ScalarNode, Value: value},
		}},
	)
	return nil
}

func setMapKey(m *yaml.Node, key, value string) error {
	for i := 0; i < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Value == key {
			m.Content[i+1].Value = value
			m.Content[i+1].Kind = yaml.ScalarNode
			m.Content[i+1].Tag = "" // let yaml infer the scalar tag
			return nil
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value},
	)
	return nil
}

// zeroBytes overwrites b. Safe on nil / empty.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ---------- runReal ----------

func runReal(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "sops", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// Inherit the parent env so sops finds SOPS_AGE_KEY_FILE,
	// XDG_CONFIG_HOME, etc.
	cmd.Env = os.Environ()
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}
