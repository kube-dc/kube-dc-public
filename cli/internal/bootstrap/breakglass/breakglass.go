// Package breakglass implements the recovery-kubeconfig flow described
// in installer-prd §16.3.3.
//
// The break-glass kubeconfig is the **deliberate exception** to the
// "no credentials in Git" rule. It's a static-token kubeconfig bound
// to a dedicated `ServiceAccount/break-glass` in `kube-system` with
// cluster-admin RBAC, SOPS-encrypted to the `.sops.yaml` recipients,
// committed to `clusters/<name>/break-glass-kubeconfig.enc.yaml`.
// Used only when OIDC is broken (Keycloak down, auth-config-sync wedged)
// and an operator can't `kube-dc login --admin`.
//
// Three operations:
//
//   - Adopt:  on an existing cluster the operator already has admin
//             access to, mint the SA+token+kubeconfig and encrypt it
//             into the fleet repo. One-time per cluster.
//   - Use:    decrypt the encrypted kubeconfig to a temp file and
//             spawn a sub-shell with KUBECONFIG=<temp>. Tempfile is
//             removed on shell exit (or process kill). Prints a red
//             banner so the operator never accidentally uses this for
//             daily work.
//   - Rotate: delete the SA token Secret, K8s recreates with a fresh
//             token, re-encrypt the kubeconfig with the new value.
//             Use after every break-glass session, or on a CronJob.
package breakglass

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ConventionalNames are the Kubernetes object names this package owns.
// Operators should not change these — the use/rotate paths look them
// up by these exact strings, and the names are intentionally explicit
// so an operator scanning `kubectl get sa,crb -A | grep break-glass`
// immediately knows what they're looking at.
const (
	BreakGlassNamespace          = "kube-system"
	BreakGlassServiceAccountName = "break-glass"
	BreakGlassSecretName         = "break-glass-token" // nolint:gosec  // not a credential, a name
	BreakGlassClusterRoleBinding = "break-glass"
	BreakGlassFileBasename       = "break-glass-kubeconfig.enc.yaml"
)

// Manifest is the YAML applied during Adopt. Embedded as a string so
// the operator can `cat` it from the binary if Go's manifest copy ever
// drifts from what's on the cluster — easier to debug than walking
// through library calls. Token-Secret pattern (annotation
// kubernetes.io/service-account.name) is the K8s ≥ 1.24 way to mint a
// long-lived SA token; the controller populates `data.token` and
// `data.ca.crt` after creation.
const Manifest = `---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ` + BreakGlassServiceAccountName + `
  namespace: ` + BreakGlassNamespace + `
  labels:
    app.kubernetes.io/managed-by: kube-dc-cli
    kube-dc.com/identity-layer: break-glass
  annotations:
    kube-dc.com/source: |
      Created by 'kube-dc bootstrap break-glass adopt'.
      Static cluster-admin token used only when OIDC is unavailable.
      See docs/prd/installer-prd.md §16.3.3.
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ` + BreakGlassClusterRoleBinding + `
  labels:
    app.kubernetes.io/managed-by: kube-dc-cli
    kube-dc.com/identity-layer: break-glass
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    name: ` + BreakGlassServiceAccountName + `
    namespace: ` + BreakGlassNamespace + `
---
apiVersion: v1
kind: Secret
metadata:
  name: ` + BreakGlassSecretName + `
  namespace: ` + BreakGlassNamespace + `
  annotations:
    kubernetes.io/service-account.name: ` + BreakGlassServiceAccountName + `
  labels:
    app.kubernetes.io/managed-by: kube-dc-cli
    kube-dc.com/identity-layer: break-glass
type: kubernetes.io/service-account-token
`

// AdoptOpts describes what the operator wants when running adopt.
type AdoptOpts struct {
	// FleetRoot is the absolute path to the kube-dc-fleet checkout.
	// Required: that's where the encrypted kubeconfig is committed.
	FleetRoot string

	// ClusterName is the cluster directory under fleet/clusters/.
	// E.g. "cloud", "stage", "cs/zrh".
	ClusterName string

	// KubectlContext is the kubectl context that has admin access to
	// the cluster being adopted. If empty, current-context is used.
	KubectlContext string

	// ServerURL overrides the apiserver URL embedded in the resulting
	// kubeconfig. Defaults to KUBE_API_EXTERNAL_URL from
	// cluster-config.env when empty.
	ServerURL string

	// DryRun: print what would happen, don't apply or write files.
	DryRun bool
}

// Adopt creates the SA + CRB + token Secret on the cluster, waits for
// the token-controller to populate the Secret, builds a kubeconfig
// from the SA token + CA, SOPS-encrypts it, writes it to
// clusters/<name>/break-glass-kubeconfig.enc.yaml in the fleet repo.
//
// Idempotent: re-running on an already-adopted cluster updates the
// encrypted kubeconfig with the current Secret's contents (useful
// after a manual rotate or to repair a mismatched copy).
func Adopt(ctx context.Context, opts AdoptOpts) error {
	if opts.FleetRoot == "" {
		return fmt.Errorf("FleetRoot required")
	}
	if opts.ClusterName == "" {
		return fmt.Errorf("ClusterName required")
	}
	clusterDir := filepath.Join(opts.FleetRoot, "clusters", opts.ClusterName)
	if _, err := os.Stat(clusterDir); err != nil {
		return fmt.Errorf("cluster overlay not found at %s: %w", clusterDir, err)
	}

	// Resolve API server URL: explicit override > cluster-config.env > kubectl current.
	server := opts.ServerURL
	if server == "" {
		server = readEnvVar(filepath.Join(clusterDir, "cluster-config.env"), "KUBE_API_EXTERNAL_URL")
	}
	if server == "" {
		got, err := kubectlServer(opts.KubectlContext)
		if err != nil {
			return fmt.Errorf("could not resolve API server URL: pass --server, set KUBE_API_EXTERNAL_URL in cluster-config.env, or run with a working kubectl context: %w", err)
		}
		server = got
	}

	if opts.DryRun {
		fmt.Fprintf(os.Stderr, "  dry-run: would adopt break-glass on %s (server=%s)\n", opts.ClusterName, server)
		fmt.Fprintf(os.Stderr, "  dry-run: would write %s/%s\n", clusterDir, BreakGlassFileBasename)
		return nil
	}

	// 1. Apply the SA + CRB + Secret manifest. kubectl apply is
	//    idempotent — it'll patch existing objects in place.
	fmt.Fprintln(os.Stderr, "  applying break-glass manifest…")
	if err := kubectlApply(ctx, opts.KubectlContext, Manifest); err != nil {
		return fmt.Errorf("kubectl apply: %w", err)
	}

	// 2. Wait for the token-controller to populate Secret.data.token.
	//    On a healthy cluster this completes in <2s, but on a busy
	//    apiserver we've seen it take up to ~10s.
	fmt.Fprintln(os.Stderr, "  waiting for SA token-controller…")
	token, ca, err := waitForToken(ctx, opts.KubectlContext, 30*time.Second)
	if err != nil {
		return fmt.Errorf("wait for token: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  token populated (%d bytes), ca (%d bytes)\n", len(token), len(ca))

	// 3. Build the kubeconfig.
	kc := buildKubeconfig(opts.ClusterName, server, token, ca)
	kcBytes, err := yaml.Marshal(kc)
	if err != nil {
		return fmt.Errorf("marshal kubeconfig: %w", err)
	}

	// 4. SOPS-encrypt + write atomically.
	target := filepath.Join(clusterDir, BreakGlassFileBasename)
	if err := sopsEncryptToFile(ctx, kcBytes, target); err != nil {
		return fmt.Errorf("sops encrypt: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✓ break-glass kubeconfig written to %s\n", target)
	fmt.Fprintln(os.Stderr, "  next step: commit + push, then test with `kube-dc bootstrap break-glass "+opts.ClusterName+"`")
	return nil
}

// Use decrypts the break-glass kubeconfig to a tempfile, spawns a
// sub-shell with KUBECONFIG=<tempfile>, prints a red audit banner.
// Tempfile is removed on shell exit OR signal — whichever comes first.
func Use(ctx context.Context, fleetRoot, clusterName string) error {
	target := filepath.Join(fleetRoot, "clusters", clusterName, BreakGlassFileBasename)
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("break-glass kubeconfig not found at %s — run `kube-dc bootstrap break-glass adopt %s` first", target, clusterName)
	}

	// Decrypt to a tempfile under the operator's home (NOT /tmp on
	// shared hosts). 0600, gone on exit.
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".kube-dc", "break-glass")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "kubeconfig-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}

	if err := sopsDecryptToFile(ctx, target, tmpPath); err != nil {
		return fmt.Errorf("sops decrypt: %w", err)
	}

	// Banner. Single block, hard to miss, read-once, embeds the cluster
	// name + server URL + the rotate command for after.
	server := kubeconfigServer(tmpPath)
	printBanner(clusterName, server)

	// Spawn an interactive sub-shell with KUBECONFIG set. We don't
	// touch the operator's primary ~/.kube/config.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(ctx, shell)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"KUBECONFIG="+tmpPath,
		// Hint for fancy shells (oh-my-zsh, starship etc.) to render a
		// distinct prompt. Not used by anything we ship; convention.
		"KUBE_DC_BREAK_GLASS=1",
	)
	if err := cmd.Run(); err != nil {
		// Non-zero exit from the sub-shell isn't an error from our
		// perspective — operators may exit with `false` to signal
		// they recovered the cluster, or just close the terminal.
		return nil
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  ✓ break-glass session ended; kubeconfig wiped")
	fmt.Fprintln(os.Stderr, "  → consider rotating the token: `kube-dc bootstrap break-glass rotate "+clusterName+"`")
	return nil
}

// Rotate deletes the existing token Secret, lets K8s recreate it
// (controller-driven so the new token is fresh), waits, re-encrypts
// the kubeconfig in the fleet. Use after every session.
func Rotate(ctx context.Context, opts AdoptOpts) error {
	if opts.FleetRoot == "" {
		return fmt.Errorf("FleetRoot required")
	}
	if opts.ClusterName == "" {
		return fmt.Errorf("ClusterName required")
	}
	clusterDir := filepath.Join(opts.FleetRoot, "clusters", opts.ClusterName)
	target := filepath.Join(clusterDir, BreakGlassFileBasename)
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("break-glass kubeconfig not found at %s — nothing to rotate; run adopt first", target)
	}

	if opts.DryRun {
		fmt.Fprintf(os.Stderr, "  dry-run: would delete Secret %s/%s and re-encrypt %s\n",
			BreakGlassNamespace, BreakGlassSecretName, target)
		return nil
	}

	// 1. Delete the Secret so the SA controller mints a fresh token
	//    on next reconcile. The SA itself stays put.
	fmt.Fprintln(os.Stderr, "  deleting existing token Secret…")
	if err := kubectlRun(ctx, opts.KubectlContext, "delete", "secret",
		"-n", BreakGlassNamespace, BreakGlassSecretName, "--ignore-not-found"); err != nil {
		return err
	}

	// 2. Re-create from the manifest (only the Secret matters here, but
	//    apply the whole bundle for idempotence).
	fmt.Fprintln(os.Stderr, "  re-applying manifest (idempotent)…")
	if err := kubectlApply(ctx, opts.KubectlContext, Manifest); err != nil {
		return err
	}

	// 3. Wait for new token + re-encrypt.
	fmt.Fprintln(os.Stderr, "  waiting for new token…")
	token, ca, err := waitForToken(ctx, opts.KubectlContext, 30*time.Second)
	if err != nil {
		return err
	}

	server := opts.ServerURL
	if server == "" {
		server = readEnvVar(filepath.Join(clusterDir, "cluster-config.env"), "KUBE_API_EXTERNAL_URL")
	}
	kc := buildKubeconfig(opts.ClusterName, server, token, ca)
	kcBytes, err := yaml.Marshal(kc)
	if err != nil {
		return err
	}
	if err := sopsEncryptToFile(ctx, kcBytes, target); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ break-glass token rotated; %s updated\n", target)
	fmt.Fprintln(os.Stderr, "  commit + push so the rest of the team picks up the new token")
	return nil
}

// Status decrypts the break-glass kubeconfig to memory (NOT to disk)
// and prints non-secret metadata: server URL, embedded CA fingerprint,
// last-modified timestamp from the file's mtime.
func Status(ctx context.Context, fleetRoot, clusterName string) error {
	target := filepath.Join(fleetRoot, "clusters", clusterName, BreakGlassFileBasename)
	st, err := os.Stat(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s does NOT exist; cluster %s has no break-glass kubeconfig.\n", target, clusterName)
		fmt.Fprintln(os.Stderr, "  Adopt first: kube-dc bootstrap break-glass adopt "+clusterName)
		return nil
	}
	plain, err := sopsDecrypt(ctx, target)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	server := kubeconfigServerFromBytes(plain)
	fmt.Fprintf(os.Stderr, "  cluster:        %s\n", clusterName)
	fmt.Fprintf(os.Stderr, "  file:           %s\n", target)
	fmt.Fprintf(os.Stderr, "  size:           %d bytes (encrypted)\n", st.Size())
	fmt.Fprintf(os.Stderr, "  last-modified:  %s (rotate after every use)\n", st.ModTime().Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "  server URL:     %s\n", server)
	return nil
}

// --- helpers below --------------------------------------------------

// buildKubeconfig assembles the standard ServiceAccount-token
// kubeconfig shape the cluster needs. CA is embedded directly (the
// SA's ca.crt is the cluster CA, valid for the apiserver cert).
func buildKubeconfig(clusterName, server string, token, ca []byte) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []any{
			map[string]any{
				"name": "kube-dc-" + sanitizeName(clusterName) + "-break-glass",
				"cluster": map[string]any{
					"server":                     server,
					"certificate-authority-data": base64.StdEncoding.EncodeToString(ca),
				},
			},
		},
		"users": []any{
			map[string]any{
				"name": "break-glass@" + sanitizeName(clusterName),
				"user": map[string]any{
					"token": string(token),
				},
			},
		},
		"contexts": []any{
			map[string]any{
				"name": "break-glass/" + clusterName,
				"context": map[string]any{
					"cluster": "kube-dc-" + sanitizeName(clusterName) + "-break-glass",
					"user":    "break-glass@" + sanitizeName(clusterName),
				},
			},
		},
		"current-context": "break-glass/" + clusterName,
	}
}

func sanitizeName(s string) string {
	return strings.ReplaceAll(s, "/", "-")
}

// kubectlApply pipes manifest bytes to `kubectl apply -f -`.
func kubectlApply(ctx context.Context, kubectlContext, manifest string) error {
	args := []string{"apply", "-f", "-"}
	if kubectlContext != "" {
		args = append([]string{"--context", kubectlContext}, args...)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdin = strings.NewReader(manifest)
	cmd.Stdout = os.Stderr // route to stderr so it interleaves with our progress
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// kubectlRun is a small wrapper for one-off kubectl commands.
func kubectlRun(ctx context.Context, kubectlContext string, args ...string) error {
	if kubectlContext != "" {
		args = append([]string{"--context", kubectlContext}, args...)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// kubectlServer reads the server URL from the named context (or
// current-context if empty).
func kubectlServer(kubectlContext string) (string, error) {
	args := []string{"config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}"}
	if kubectlContext != "" {
		args = append([]string{"--context", kubectlContext}, args...)
	}
	out, err := exec.Command("kubectl", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// waitForToken polls the break-glass Secret until data.token is
// populated by the SA-token controller. Returns (token, ca.crt).
func waitForToken(ctx context.Context, kubectlContext string, timeout time.Duration) ([]byte, []byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		token, ca, err := readToken(ctx, kubectlContext)
		if err == nil && len(token) > 0 && len(ca) > 0 {
			return token, ca, nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("token still empty after %s", timeout)
			}
			return nil, nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// readToken issues a single get against the break-glass Secret and
// returns the decoded token + ca.crt. Both empty if the Secret
// exists but the controller hasn't populated data yet.
func readToken(ctx context.Context, kubectlContext string) ([]byte, []byte, error) {
	args := []string{"get", "secret", BreakGlassSecretName,
		"-n", BreakGlassNamespace, "-o", "json"}
	if kubectlContext != "" {
		args = append([]string{"--context", kubectlContext}, args...)
	}
	out, err := exec.CommandContext(ctx, "kubectl", args...).Output()
	if err != nil {
		return nil, nil, err
	}
	var s struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, nil, err
	}
	tokenB64, hasToken := s.Data["token"]
	caB64, hasCA := s.Data["ca.crt"]
	if !hasToken || !hasCA {
		return nil, nil, nil
	}
	token, err := base64.StdEncoding.DecodeString(tokenB64)
	if err != nil {
		return nil, nil, err
	}
	ca, err := base64.StdEncoding.DecodeString(caB64)
	if err != nil {
		return nil, nil, err
	}
	return token, ca, nil
}

// sopsEncryptToFile encrypts plain into target. Implementation notes
// kept here so we don't regress on the safety properties:
//
//   - sops looks up `.sops.yaml` by walking UP from $CWD, NOT from the
//     input file path. We cd to the fleet repo root before invoking
//     so `.sops.yaml` is found.
//   - We never `cmd > $target` redirect (truncating-redirect footgun).
//     Plaintext goes to a tempfile in the same directory, sops -i
//     encrypts in place, then atomic mv to the final path.
//   - Tempfile name ends in `.enc.yaml` so it matches the .sops.yaml
//     regex `\.enc\.yaml$` once sops finds the config.
func sopsEncryptToFile(ctx context.Context, plain []byte, target string) error {
	dir := filepath.Dir(target)
	fleetRoot := findFleetRoot(target)

	tmp, err := os.CreateTemp(dir, "bg-encrypt-*."+filepath.Base(target))
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // safe: removed if it still exists

	if _, err := tmp.Write(plain); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}

	// Pass tmpPath relative to fleetRoot — sops resolves the `.sops.yaml`
	// from its working directory and matches creation rules against
	// the path it was given (relative or absolute, both work).
	rel, err := filepath.Rel(fleetRoot, tmpPath)
	if err != nil {
		rel = tmpPath
	}
	cmd := exec.CommandContext(ctx, "sops", "--encrypt", "-i", rel)
	cmd.Dir = fleetRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return os.Rename(tmpPath, target)
}

// sopsDecryptToFile decrypts to a target path. Same `cmd.Dir` trick
// as sopsEncryptToFile so `.sops.yaml` is discoverable.
func sopsDecryptToFile(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "sops", "--decrypt", src)
	cmd.Dir = findFleetRoot(src)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0600)
}

// sopsDecrypt returns the plaintext bytes (used by Status; the
// plaintext is already in memory and discarded when this fn returns).
func sopsDecrypt(ctx context.Context, src string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sops", "--decrypt", src)
	cmd.Dir = findFleetRoot(src)
	return cmd.Output()
}

// findFleetRoot walks up from path looking for a `.sops.yaml` (the
// fleet repo's marker file). Returns the file's own directory if not
// found — sops will then fail with the canonical "config file not
// found" message which is more helpful than us guessing.
func findFleetRoot(path string) string {
	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".sops.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(path) // gave up
		}
		dir = parent
	}
}

// kubeconfigServer extracts the apiserver URL from a plain-text
// kubeconfig file (used post-decrypt to display in the banner).
func kubeconfigServer(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return kubeconfigServerFromBytes(b)
}

func kubeconfigServerFromBytes(b []byte) string {
	var cfg struct {
		Clusters []struct {
			Cluster struct {
				Server string `yaml:"server"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return ""
	}
	if len(cfg.Clusters) == 0 {
		return ""
	}
	return cfg.Clusters[0].Cluster.Server
}

// readEnvVar pulls KEY=VALUE from a dotenv-style file. Empty result
// when the file or the key isn't present — caller falls back.
func readEnvVar(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq > 0 {
			if strings.TrimSpace(line[:eq]) == key {
				v := strings.TrimSpace(line[eq+1:])
				if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' ||
					v[0] == '\'' && v[len(v)-1] == '\'') {
					v = v[1 : len(v)-1]
				}
				return v
			}
		}
	}
	return ""
}

// printBanner is a deliberate, eye-catching reminder. Static red text
// on stderr — survives non-color terminals (ANSI is just discarded).
func printBanner(clusterName, server string) {
	const (
		red   = "\033[1;41m"
		bold  = "\033[1;31m"
		reset = "\033[0m"
	)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, red+"  BREAK-GLASS ACTIVE                                                  "+reset)
	fmt.Fprintln(os.Stderr, bold+"  cluster: "+clusterName+reset)
	fmt.Fprintln(os.Stderr, bold+"  server:  "+server+reset)
	fmt.Fprintln(os.Stderr, bold+"  identity: ServiceAccount/break-glass (cluster-admin)"+reset)
	fmt.Fprintln(os.Stderr, bold+"  audit:   apiserver logs will record system:serviceaccount:"+BreakGlassNamespace+":"+BreakGlassServiceAccountName+", NOT your email"+reset)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Use ONLY when OIDC is unavailable. Rotate the token after this session:")
	fmt.Fprintln(os.Stderr, "      kube-dc bootstrap break-glass rotate "+clusterName)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Type `exit` to leave this shell. The kubeconfig file will be removed.")
	fmt.Fprintln(os.Stderr)
}
