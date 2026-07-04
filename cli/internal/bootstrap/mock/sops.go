package mock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

// SOPSClient is an in-memory keyed store. Files are addressed by path;
// each file has stringData entries (map[string][]byte) and a recipient
// set (age pubkeys). Encrypt is a marker (the file becomes "encrypted"
// once written); SetStringData updates a single key; Decrypt returns
// the concatenated YAML-ish representation of the file's data.
type SOPSClient struct {
	scenario *Scenario

	mu    sync.Mutex
	files map[string]*mockSOPSFile
}

type mockSOPSFile struct {
	encrypted  bool
	recipients []string
	data       map[string][]byte
}

func NewSOPSClient(s *Scenario) *SOPSClient {
	c := &SOPSClient{
		scenario: s,
		files:    map[string]*mockSOPSFile{},
	}
	// Seed any scenario-declared SOPS recipients onto a default
	// secrets.enc.yaml path for the existing-fleet case.
	if s != nil && s.Fleet != nil && len(s.Fleet.AgeRecipients) > 0 {
		// We don't pre-create files; tests Set values explicitly.
		_ = s.Fleet.AgeRecipients
	}
	return c
}

func (c *SOPSClient) Encrypt(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.fileLocked(path)
	f.encrypted = true
	if len(f.recipients) == 0 && c.scenario != nil && c.scenario.Fleet != nil {
		f.recipients = append(f.recipients, c.scenario.Fleet.AgeRecipients...)
	}
	return nil
}

func (c *SOPSClient) Decrypt(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.files[path]
	if !ok {
		return nil, fmt.Errorf("mock: sops Decrypt: file %s not found", path)
	}
	// Render a stable YAML-ish stringData block. Keys sorted for
	// determinism so tests can assert byte-for-byte.
	var keys []string
	for k := range f.data {
		keys = append(keys, k)
	}
	// inline sort to avoid an import for one call
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	var out []byte
	out = append(out, []byte("apiVersion: v1\nkind: Secret\nstringData:\n")...)
	for _, k := range keys {
		out = append(out, []byte("  "+k+": ")...)
		out = append(out, f.data[k]...)
		out = append(out, '\n')
	}
	return out, nil
}

func (c *SOPSClient) SetStringData(ctx context.Context, path, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.fileLocked(path)
	// Defensive copy — caller may scrub the slice.
	cp := make([]byte, len(value))
	copy(cp, value)
	f.data[key] = cp
	f.encrypted = true
	return nil
}

func (c *SOPSClient) Recipients(path string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.files[path]
	if !ok {
		return nil, fmt.Errorf("mock: sops Recipients: file %s not found", path)
	}
	out := make([]string, len(f.recipients))
	copy(out, f.recipients)
	return out, nil
}

// DerivePubKey derives a deterministic stub pubkey from the private-
// key file contents. The age private key format is "AGE-SECRET-KEY-…"
// but the mock doesn't validate; it just hashes whatever bytes the
// caller wrote at keyPath.
//
// For unit tests, the typical pattern is: the test seeds the file at
// keyPath with a known byte string, then asserts DerivePubKey returns
// the expected stub. No actual age-crypto runs.
func (c *SOPSClient) DerivePubKey(keyPath string) (string, error) {
	// Read from the OS — the mock SOPSClient is in-memory for files
	// it manages, but the operator's age.key lives on disk in real
	// invocations. For mock-driven smoke tests we keep this simple
	// and synthesise a stub from the path itself so tests don't have
	// to write a real keyfile.
	h := sha256.Sum256([]byte("mock-age-key:" + keyPath))
	return "age1mock" + hex.EncodeToString(h[:8]), nil
}

// SetRecipients is a test-only helper.
func (c *SOPSClient) SetRecipients(path string, recipients []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.fileLocked(path)
	f.recipients = append([]string(nil), recipients...)
}

func (c *SOPSClient) fileLocked(path string) *mockSOPSFile {
	f, ok := c.files[path]
	if !ok {
		f = &mockSOPSFile{data: map[string][]byte{}}
		c.files[path] = f
	}
	return f
}
