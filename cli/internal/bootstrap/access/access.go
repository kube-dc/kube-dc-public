// Package access prints the post-install "where do I log in?" summary
// per installer-ux-prd §5.4: every public URL the platform serves +
// the LIST of secrets in the cluster overlay's secrets.enc.yaml (by
// NAME, never value) + the commands to reveal them.
//
// Read-only and decryption-free: reads `clusters/<name>/cluster-config.env`
// (key=value text) and greps `clusters/<name>/secrets.enc.yaml` for the
// stringData key names — both are plaintext (the SOPS .sops.yaml
// rule `encrypted_regex: '^(data|stringData)$'` keeps every other
// field decrypted). No SOPS age key needed to run this; safe to
// invoke from CI, ops chat, anywhere.
package access

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Options is the parameter bundle for Print.
type Options struct {
	ClusterName string
	FleetRepo   string
	Out         io.Writer
}

// Print renders the success-summary block to opts.Out.
func Print(opts Options) error {
	if opts.ClusterName == "" {
		return fmt.Errorf("access: empty ClusterName")
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("access: empty FleetRepo")
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	clusterDir := filepath.Join(opts.FleetRepo, "clusters", opts.ClusterName)
	if st, err := os.Stat(clusterDir); err != nil || !st.IsDir() {
		return fmt.Errorf("access: cluster overlay %s not found in fleet repo (%w)", clusterDir, err)
	}

	cfg, err := readEnv(filepath.Join(clusterDir, "cluster-config.env"))
	if err != nil {
		return fmt.Errorf("access: read cluster-config.env: %w", err)
	}
	domain := cfg["DOMAIN"]
	if domain == "" {
		return fmt.Errorf("access: DOMAIN not set in cluster-config.env")
	}

	keycloakHost := cfg["KEYCLOAK_HOSTNAME"]
	if keycloakHost == "" {
		keycloakHost = "login." + domain
	}
	apiURL := cfg["KUBE_API_EXTERNAL_URL"]
	if apiURL == "" {
		apiURL = "https://kube-api." + domain + ":6443"
	}
	s3Host := cfg["S3_HOSTNAME"]
	if s3Host == "" {
		s3Host = "s3." + domain
	}

	secretKeys, _ := stringDataKeys(filepath.Join(clusterDir, "secrets.enc.yaml"))

	w := bufio.NewWriter(out)
	defer w.Flush()

	hr := strings.Repeat("=", 79)
	fmt.Fprintln(w, hr)
	fmt.Fprintf(w, "  Kube-DC cluster access — %s\n", opts.ClusterName)
	fmt.Fprintln(w, hr)
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "  Cluster:        %s   (domain %s)\n", opts.ClusterName, domain)
	fmt.Fprintf(w, "  Console:        https://console.%s\n", domain)
	fmt.Fprintf(w, "  Keycloak:       https://%s\n", keycloakHost)
	fmt.Fprintf(w, "  Grafana:        https://grafana.%s    (SSO + local admin)\n", domain)
	fmt.Fprintf(w, "  Flux Web UI:    https://flux.%s\n", domain)
	fmt.Fprintf(w, "  OpenBao UI:     https://bao.%s\n", domain)
	fmt.Fprintf(w, "  S3 (Rook RGW):  https://%s\n", s3Host)
	fmt.Fprintf(w, "  API:            %s\n", apiURL)
	fmt.Fprintf(w, "  Logs (Loki):    https://loki-query.%s\n", domain)
	fmt.Fprintf(w, "  Metrics:        https://mimir-query.%s   (rules: https://mimir-ruler.%s, AM: https://mimir-alertmanager.%s)\n",
		domain, domain, domain)
	fmt.Fprintln(w, "")

	fmt.Fprintln(w, "  ─ How to sign in ────────────────────────────────────────────────")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  Grafana / Flux Web UI (SSO via Keycloak):")
	fmt.Fprintln(w, "    → click the OIDC button on the login page")
	fmt.Fprintln(w, "    → user: admin")
	fmt.Fprintln(w, "    → password: reveal KEYCLOAK_ADMIN_PASSWORD (commands below)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  Grafana local admin (fallback if SSO is broken):")
	fmt.Fprintln(w, "    user: admin / password: reveal GRAFANA_ADMIN_PASSWORD")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  kubectl admin (cluster-admin via platform:admin group):")
	fmt.Fprintf(w, "    kube-dc login --domain %s --admin\n", domain)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  OpenBao (after the operator runs unseal):")
	fmt.Fprintf(w, "    https://bao.%s\n", domain)
	fmt.Fprintln(w, "    root token: revoked at init time; use 'kube-dc bootstrap openbao generate-root' to mint a new one if needed")
	fmt.Fprintln(w, "")

	if len(secretKeys) > 0 {
		fmt.Fprintln(w, "  ─ Generated secrets ─────────────────────────────────────────────")
		fmt.Fprintln(w, "")
		fmt.Fprintf(w, "  Stored encrypted in clusters/%s/secrets.enc.yaml:\n", opts.ClusterName)
		fmt.Fprintln(w, "")
		for _, k := range secretKeys {
			fmt.Fprintf(w, "    %s\n", k)
		}
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "  Reveal one value (requires your age key + a typed REVEAL confirmation):")
		fmt.Fprintf(w, "    sops -d %s/clusters/%s/secrets.enc.yaml | grep <KEY_NAME>\n",
			opts.FleetRepo, opts.ClusterName)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "  Or via the OpenBao share-reveal ceremony (REVEAL gate):")
		fmt.Fprintf(w, "    kube-dc bootstrap openbao reveal-shares %s\n", opts.ClusterName)
		fmt.Fprintln(w, "")
	}

	fmt.Fprintln(w, "  ─ Next steps ────────────────────────────────────────────────────")
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "  kube-dc login --domain %s --admin     # cluster-admin kubeconfig\n", domain)
	fmt.Fprintln(w, "  kubectl get organization                                       # see existing orgs")
	fmt.Fprintf(w, "  kube-dc bootstrap status %s                              # health check\n", opts.ClusterName)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, hr)
	return nil
}

// readEnv parses a simple key=value file (the format
// add-cluster.sh emits + flux configMapGenerator consumes).
// Ignores blank lines and `#` comments. Value runs to end of line.
func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// strip surrounding quotes if any
		val = strings.Trim(val, `"'`)
		out[key] = val
	}
	return out, sc.Err()
}

// stringDataKeys greps a SOPS-encrypted-regex file for stringData
// keys without decrypting. The encrypted_regex='^(data|stringData)$'
// rule keeps the keys plaintext and only encrypts the values, so
// `KEY_NAME: ENC[AES256_GCM,data:...]` lines are scannable.
var stringDataKeyRE = regexp.MustCompile(`(?m)^\s{4}([A-Z][A-Z0-9_]+):\s*(ENC\[|.+)`)

func stringDataKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Only look at lines after `stringData:` so we don't pick up
	// metadata or sops housekeeping.
	start := strings.Index(string(data), "stringData:")
	if start < 0 {
		return nil, nil
	}
	rest := data[start:]
	var keys []string
	seen := map[string]bool{}
	for _, m := range stringDataKeyRE.FindAllSubmatch(rest, -1) {
		k := string(m[1])
		if seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}
