// `kube-dc secrets` subcommand — hybrid CLI for the M1 Secrets
// Manager (M1-T07). CRD reads / patches / soft deletes call the
// kube-apiserver directly with the user's Keycloak JWT (the OIDC
// webhook enforces RBAC the same way kubectl does). Value-plane
// writes, the import saga, the destroy/version-destroy paths, and
// the consumer scanner go via the backend at backend.<domain>/api/
// secrets/* — those operations carry orchestration + structured
// Loki audit emission the CLI shouldn't duplicate.
//
// Verbs:
//   create    — create a new managed secret (CR + optional first value) (k8s [+ backend])
//   list      — GET ManagedSecrets in the namespace                 (k8s)
//   get       — show one ManagedSecret; --value also shows KV data  (k8s + backend)
//   describe  — alias for `get` (AWS/GCP-style)                     (k8s + backend)
//   put       — upsert KV values from --from-literal/--from-file    (backend)
//   sync      — toggle/configure spec.sync                          (k8s)
//   import    — adopt an existing Secret                            (backend)
//   delete    — soft-delete; --destroy also wipes KV metadata       (k8s and/or backend)
//   destroy-version  — destroy a single KV version (admin policy)   (backend)
//   consumers — list workloads referencing the synced Secret        (backend)

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
	"github.com/shalb/kube-dc/cli/internal/kubeconfig"
	"github.com/shalb/kube-dc/cli/pkg/credential"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// secretsScope is the resolved (server domain, namespace, JWT) tuple
// each subcommand needs. Built by resolveScope() from the active
// kube-dc kubeconfig context + cached credentials.
//
// TLS knobs are split because the two endpoints can use different
// trust roots:
//
//   - kube-apiserver (kube-api.<domain>:6443) usually serves a cert
//     signed by the cluster's private CA; CACert/Insecure come from
//     the kubeconfig cluster block where kubectl gets them.
//   - backend.<domain> sits behind the Envoy Gateway and is fronted
//     by the gateway's public cert (Let's Encrypt on production
//     clusters); the system trust store accepts it. We deliberately
//     do NOT pass the kube-apiserver CA to the backend client, or the
//     mismatched chain would 502 every backend call.
type secretsScope struct {
	Domain      string // e.g. kube-dc.cloud (without https:// or backend. prefix)
	APIServer   string // e.g. https://kube-api.kube-dc.cloud:6443
	Namespace   string // K8s namespace (e.g. shalb-envoy)
	AccessToken string
	K8sCACert   string // CA bundle for kube-apiserver (from kubeconfig)
	K8sInsecure bool   // insecure-skip-tls-verify flag from kubeconfig
}

func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage project secrets",
		Long: `Manage project secrets stored in the platform's secrets store and
optionally synced to a Kubernetes Secret in the project namespace.

Permissions follow your project role: viewers see metadata, developers
can read and write values, admins can permanently destroy.`,
	}
	cmd.AddCommand(secretsCreateCmd())
	cmd.AddCommand(secretsListCmd())
	cmd.AddCommand(secretsGetCmd())
	cmd.AddCommand(secretsPutCmd())
	cmd.AddCommand(secretsSyncCmd())
	cmd.AddCommand(secretsImportCmd())
	cmd.AddCommand(secretsDeleteCmd())
	cmd.AddCommand(secretsDestroyVersionCmd())
	cmd.AddCommand(secretsConsumersCmd())
	return cmd
}

// resolveScope returns the resolved scope for a CLI request. If
// `nsOverride` is non-empty it wins; otherwise the current kube-dc
// kubeconfig context's `namespace` field is used. Errors when the
// user is not logged in OR the current context isn't kube-dc-managed.
func resolveScope(nsOverride string) (*secretsScope, error) {
	kubeMgr, err := kubeconfig.NewManager()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cfg, err := kubeMgr.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	if !strings.HasPrefix(cfg.CurrentContext, "kube-dc/") {
		return nil, fmt.Errorf("current kubeconfig context %q is not a kube-dc context — run `kube-dc login` first", cfg.CurrentContext)
	}
	var (
		serverURL, ctxNamespace string
		caCertPEM               string
		insecureSkip            bool
		foundCluster            bool
	)
	for _, ctx := range cfg.Contexts {
		if ctx.Name == cfg.CurrentContext {
			ctxNamespace = ctx.Context.Namespace
			for _, cl := range cfg.Clusters {
				if cl.Name == ctx.Context.Cluster {
					serverURL = cl.Cluster.Server
					insecureSkip = cl.Cluster.InsecureSkipTLSVerify
					// kubeconfig stores the CA as base64 of the
					// PEM bundle — decode so the typed clients can
					// hand it to crypto/tls directly.
					if cl.Cluster.CertificateAuthorityData != "" {
						decoded, derr := base64.StdEncoding.DecodeString(cl.Cluster.CertificateAuthorityData)
						if derr != nil {
							return nil, fmt.Errorf("decode certificate-authority-data for cluster %q: %w", cl.Name, derr)
						}
						caCertPEM = string(decoded)
					}
					foundCluster = true
					break
				}
			}
			break
		}
	}
	if !foundCluster || serverURL == "" {
		return nil, fmt.Errorf("could not resolve API server URL for context %q", cfg.CurrentContext)
	}
	// Realm-aware credential load (M1-T07 first-review-pass P2):
	// parse the realm out of the current context name so a machine
	// with both tenant and admin credentials cached doesn't load the
	// wrong file. Context shape per kubeconfig.AddKubeDCContext:
	//
	//   kube-dc/<domain>/admin              → realm "master"
	//   kube-dc/<domain>/<org>/<project>    → realm "<org>"
	realm := realmFromContext(cfg.CurrentContext)
	provider, err := credential.NewProvider()
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	// LoadAndRefresh auto-refreshes the access token if expired
	// (M1-T07 first-review-pass P2 — was previously a hard error).
	creds, err := provider.LoadAndRefresh(serverURL, realm)
	if err != nil {
		return nil, err
	}
	ns := nsOverride
	if ns == "" {
		ns = ctxNamespace
	}
	if ns == "" {
		return nil, fmt.Errorf("namespace required — pass -n / --namespace or run `kube-dc ns <ns>` first")
	}
	// Domain is parsed from the API-server URL:
	// https://kube-api.<DOMAIN>:6443 → <DOMAIN>.
	domain := strings.TrimPrefix(serverURL, "https://")
	domain = strings.TrimPrefix(domain, "kube-api.")
	if i := strings.LastIndex(domain, ":"); i >= 0 {
		domain = domain[:i]
	}
	return &secretsScope{
		Domain:      domain,
		APIServer:   serverURL,
		Namespace:   ns,
		AccessToken: creds.AccessToken,
		K8sCACert:   caCertPEM,
		K8sInsecure: insecureSkip,
	}, nil
}

// realmFromContext extracts the Keycloak realm from a kube-dc
// kubeconfig context name. Returns "" for non-kube-dc contexts (the
// caller has already validated the prefix). Pure — exported for
// unit tests.
func realmFromContext(ctx string) string {
	rest := strings.TrimPrefix(ctx, "kube-dc/")
	parts := strings.Split(rest, "/")
	// kube-dc/<domain>/admin                  → 2 parts after prefix
	// kube-dc/<domain>/<org>/<project>        → 3 parts
	switch len(parts) {
	case 2:
		if parts[1] == "admin" {
			return "master"
		}
	case 3:
		return parts[1] // org name == realm name
	}
	return ""
}

func (s *secretsScope) k8s() (*k8sapi.Client, error) {
	return k8sapi.New(s.APIServer, s.AccessToken, s.K8sCACert, s.K8sInsecure)
}

func (s *secretsScope) backend() (*backend.Client, error) {
	// Backend uses the system trust store — backend.<domain> is
	// publicly-trusted (Let's Encrypt) on every cluster we ship.
	return backend.New(s.Domain, s.AccessToken, "", false)
}

func ctxWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// outputFormat is the -o flag value: table (default), json, or yaml.
type outputFormat string

const (
	outTable outputFormat = "table"
	outJSON  outputFormat = "json"
	outYAML  outputFormat = "yaml"
)

func parseOutput(s string) (outputFormat, error) {
	switch strings.ToLower(s) {
	case "", "table":
		return outTable, nil
	case "json":
		return outJSON, nil
	case "yaml", "yml":
		return outYAML, nil
	default:
		return "", fmt.Errorf("unsupported -o output format %q (want table|json|yaml)", s)
	}
}

func printSerialized(out outputFormat, v any) error {
	switch out {
	case outJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case outYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer enc.Close()
		return enc.Encode(v)
	}
	return nil
}

// -------- create ---------------------------------------------------

// validSecretTypes is the allow-list for `--type` (matches the CRD
// enum: kubebuilder:validation:Enum=opaque;password;api-key;tls;db-static).
// We validate client-side so typos fail with a friendly message before
// the round-trip to the kube-apiserver.
var validSecretTypes = map[string]struct{}{
	"opaque":    {},
	"password":  {},
	"api-key":   {},
	"tls":       {},
	"db-static": {},
}

// createOpts is the parsed CLI flag set for `secrets create`. Kept as a
// dedicated type so buildCreateManagedSecret stays a pure function we
// can table-test without touching the kubeconfig/backend.
type createOpts struct {
	Name, Namespace, Type, Description string
	SyncDisabled                       bool
	SyncTarget, SyncRefresh, SyncKeysCSV string
}

// buildCreateManagedSecret assembles the ManagedSecret payload sent to
// the kube-apiserver. Pure — no I/O. Two behaviours worth knowing about:
//
//   - `SyncDisabled=true` builds Sync={Enabled:false} ONLY — no
//     targetSecretName / refreshInterval / keys. Sending those alongside
//     enabled=false would leave the object carrying active-looking
//     state and confuse the UI (M1-T07a reviewer P2).
//   - `SyncRefresh` is intentionally NOT defaulted CLI-side. Empty string
//     means "let the API webhook apply its 1h default"; matches the
//     Kubernetes convention of server-side defaulting and avoids the CLI
//     forcing 1m on every entry point (M1-T07a reviewer P2).
//
// Returns an error for invalid --type so the caller can fail before any
// kube-apiserver call.
func buildCreateManagedSecret(opts createOpts) (*k8sapi.ManagedSecret, error) {
	if _, ok := validSecretTypes[opts.Type]; !ok {
		return nil, fmt.Errorf("invalid --type %q (want one of: opaque, password, api-key, tls, db-static)", opts.Type)
	}
	syncSpec := k8sapi.ManagedSecretSyncSpec{
		Enabled: !opts.SyncDisabled,
	}
	if !opts.SyncDisabled {
		target := opts.SyncTarget
		if target == "" {
			target = opts.Name
		}
		syncSpec.TargetSecretName = target
		syncSpec.RefreshInterval = opts.SyncRefresh
		if opts.SyncKeysCSV != "" {
			for _, k := range strings.Split(opts.SyncKeysCSV, ",") {
				k = strings.TrimSpace(k)
				if k != "" {
					syncSpec.Keys = append(syncSpec.Keys, k)
				}
			}
		}
	}
	return &k8sapi.ManagedSecret{
		Metadata: k8sapi.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
		},
		Spec: k8sapi.ManagedSecretSpec{
			Type:        opts.Type,
			Description: opts.Description,
			// Rotation is intentionally omitted on create — the CRD
			// shape defaults `spec.rotation.enabled` to false, so an
			// absent block is semantically identical and keeps the
			// wire payload clean (T07a second-pass reviewer P2).
			// `kube-dc secrets rotation enable/disable` would be the
			// natural verb to add when we expose user-driven rotation
			// configuration; for now CLI only ever creates non-rotating
			// secrets and the server defaulting owns the rest.
			Sync: syncSpec,
		},
	}, nil
}

// secretsCreateCmd implements `kube-dc secrets create <name>`. CR
// creation goes via the kube-apiserver direct path (RBAC-gated by
// `create managedsecrets` in the namespace); if --from-literal /
// --from-file / --from-env-file flags are present, the first version is
// written through the backend AFTER the CR lands. The two ops are NOT
// transactional: if the value-write fails the CR is left in place and
// the error message gives the user explicit retry / cleanup commands
// (M1-T07a reviewer P1 — earlier help text mis-described this).
// The flag set mirrors AWS `secretsmanager create-secret` and `gcloud
// secrets create` so platform users don't have to learn a third dialect.
func secretsCreateCmd() *cobra.Command {
	var namespace, secretType, description, syncTarget, syncRefresh, syncKeysCSV string
	var syncDisabled bool
	var literals, files []string
	var envFile string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new managed secret (optionally with the first version inline).",
		Long: `Create a new managed secret in the project namespace.

Sync to a Kubernetes Secret is enabled by default and the target name
defaults to the secret name itself; pass --sync-disabled to keep the
secret in the platform store only.

If you provide one or more --from-literal=KEY=VAL, --from-file=KEY=path,
or --from-env-file=path flags, the ManagedSecret CR is created first,
then the initial value version is written through the backend. The two
steps are NOT transactional — if the value-write fails the CR remains
and the error tells you exactly how to retry (kube-dc secrets put) or
clean up (kube-dc secrets delete --destroy).

Examples:
  # Empty secret, with sync to a Kubernetes Secret of the same name:
  kube-dc secrets create db-creds

  # Seed the first version inline (single keys):
  kube-dc secrets create app-config \
    --from-literal=DATABASE_URL=postgres://... \
    --from-file=tls.crt=./tls.crt

  # Seed from a .env file (kubectl-style KEY=VALUE lines):
  kube-dc secrets create app-env --from-env-file=./app.env

  # No sync — only readable via "kube-dc secrets get --value":
  kube-dc secrets create api-keys --sync-disabled`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			// Validate value flags up-front so a typo in --from-file
			// doesn't leave behind an empty CR the user has to clean up.
			data, err := buildPutData(literals, files)
			if err != nil {
				return err
			}
			if envFile != "" {
				envData, err := parseEnvFile(envFile)
				if err != nil {
					return err
				}
				for k, v := range envData {
					if _, dup := data[k]; dup {
						return fmt.Errorf("key %q appears in both --from-env-file and --from-literal/--from-file", k)
					}
					data[k] = v
				}
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			ms, err := buildCreateManagedSecret(createOpts{
				Name:         name,
				Namespace:    scope.Namespace,
				Type:         secretType,
				Description:  description,
				SyncDisabled: syncDisabled,
				SyncTarget:   syncTarget,
				SyncRefresh:  syncRefresh,
				SyncKeysCSV:  syncKeysCSV,
			})
			if err != nil {
				return err
			}
			k8s, err := scope.k8s()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			created, err := k8s.CreateManagedSecret(ctx, ms)
			if err != nil {
				return err
			}
			fmt.Printf("Created secret %s/%s (type=%s, sync=%v, target=%s)\n",
				created.Metadata.Namespace, created.Metadata.Name,
				created.Spec.Type, created.Spec.Sync.Enabled, fmtCoalesce(created.Spec.Sync.TargetSecretName, "-"))

			if len(data) == 0 {
				fmt.Printf("Stored values are empty — run `kube-dc secrets put %s --from-literal=KEY=VAL` to populate.\n", name)
				return nil
			}
			cli, err := scope.backend()
			if err != nil {
				return fmt.Errorf("secret created but value-write client init failed: %w (run `kube-dc secrets put %s ...` to retry)", err, name)
			}
			res, err := cli.PutSecretValues(ctx, scope.Namespace, name, data)
			if err != nil {
				// CR is created; tell the caller exactly how to recover.
				return fmt.Errorf("secret created but first value-write failed: %w\n"+
					"  Retry:   kube-dc secrets put %s --from-literal=KEY=VAL\n"+
					"  Cleanup: kube-dc secrets delete %s --destroy", err, name, name)
			}
			fmt.Printf("Wrote %s/%s v%d (%d keys)\n", scope.Namespace, name, res.Version, len(data))
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVar(&secretType, "type", "opaque", "Secret type: opaque|password|api-key|tls|db-static")
	cmd.Flags().StringVar(&description, "description", "", "Free-text description")
	cmd.Flags().BoolVar(&syncDisabled, "sync-disabled", false, "Don't sync to a Kubernetes Secret (default: sync enabled)")
	cmd.Flags().StringVar(&syncTarget, "sync-target", "", "Target Kubernetes Secret name (default: same as <name>)")
	cmd.Flags().StringVar(&syncRefresh, "sync-refresh", "", "Sync refresh interval (default: API-side 1h)")
	cmd.Flags().StringVar(&syncKeysCSV, "sync-keys", "", "Comma-separated list of keys to sync (default: all)")
	cmd.Flags().StringArrayVar(&literals, "from-literal", nil, "KEY=VALUE pair to seed the first version (repeatable)")
	cmd.Flags().StringArrayVar(&files, "from-file", nil, "KEY=path; file contents become VALUE (repeatable)")
	cmd.Flags().StringVar(&envFile, "from-env-file", "", "Path to a .env file (KEY=VALUE lines) to seed the first version")
	return cmd
}

// -------- list -----------------------------------------------------

func secretsListCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List secrets in the project namespace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := parseOutput(outFlag)
			if err != nil {
				return err
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			k8s, err := scope.k8s()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			list, err := k8s.ListManagedSecrets(ctx, scope.Namespace)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, list)
			}
			if len(list.Items) == 0 {
				fmt.Println("No secrets in", scope.Namespace)
				return nil
			}
			fmt.Printf("%-30s  %-12s  %-7s  %-20s\n", "NAME", "TYPE", "SYNC", "TARGET")
			for _, it := range list.Items {
				syncFlag := "no"
				if it.Spec.Sync.Enabled {
					syncFlag = "yes"
				}
				fmt.Printf("%-30s  %-12s  %-7s  %-20s\n",
					truncCLI(it.Metadata.Name, 30),
					truncCLI(it.Spec.Type, 12),
					syncFlag,
					truncCLI(it.Spec.Sync.TargetSecretName, 20),
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

// -------- get ------------------------------------------------------

func secretsGetCmd() *cobra.Command {
	var namespace, outFlag string
	var withValue bool
	cmd := &cobra.Command{
		Use:     "get <name>",
		Aliases: []string{"describe", "show"},
		Short:   "Show one secret. --value also reveals the stored values (requires developer role).",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			out, err := parseOutput(outFlag)
			if err != nil {
				return err
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			if withValue {
				// Value plane: backend orchestrates OpenBao login +
				// KV read + audit emit.
				cli, err := scope.backend()
				if err != nil {
					return err
				}
				ctx, cancel := ctxWithTimeout()
				defer cancel()
				s, err := cli.GetSecret(ctx, scope.Namespace, name, true)
				if err != nil {
					return err
				}
				if out != outTable {
					return printSerialized(out, s)
				}
				printSecretSummary(s, true)
				return nil
			}
			k8s, err := scope.k8s()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			ms, err := k8s.GetManagedSecret(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, ms)
			}
			printManagedSecret(ms)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	cmd.Flags().BoolVar(&withValue, "value", false, "Also fetch the stored values (requires developer role)")
	return cmd
}

// -------- put ------------------------------------------------------

func secretsPutCmd() *cobra.Command {
	var namespace string
	var literals, files []string
	var envFile string
	cmd := &cobra.Command{
		Use:   "put <name>",
		Short: "Write or update a secret's values (requires developer role).",
		Long: `Write a new version of the secret. Provide one or more keys with
--from-literal=KEY=VAL, --from-file=KEY=path, or --from-env-file=path.
File contents become the value as a UTF-8 string.

Examples:
  kube-dc secrets put app-config \
    --from-literal=DATABASE_URL=postgres://... \
    --from-file=tls.crt=./tls.crt

  kube-dc secrets put app-env --from-env-file=./app.env`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			data, err := buildPutData(literals, files)
			if err != nil {
				return err
			}
			if envFile != "" {
				envData, err := parseEnvFile(envFile)
				if err != nil {
					return err
				}
				for k, v := range envData {
					if _, dup := data[k]; dup {
						return fmt.Errorf("key %q appears in both --from-env-file and --from-literal/--from-file", k)
					}
					data[k] = v
				}
			}
			if len(data) == 0 {
				return fmt.Errorf("at least one --from-literal, --from-file, or --from-env-file is required")
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			res, err := cli.PutSecretValues(ctx, scope.Namespace, name, data)
			if err != nil {
				return err
			}
			fmt.Printf("Wrote %s/%s v%d (%d keys)\n", scope.Namespace, name, res.Version, len(data))
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace")
	cmd.Flags().StringArrayVar(&literals, "from-literal", nil, "KEY=VALUE pair (repeatable)")
	cmd.Flags().StringArrayVar(&files, "from-file", nil, "KEY=path; file contents become VALUE (repeatable)")
	cmd.Flags().StringVar(&envFile, "from-env-file", "", "Path to a .env file (KEY=VALUE lines) to seed values")
	return cmd
}

// parseEnvFile reads a kubectl-style .env file: each non-blank,
// non-comment line is `KEY=VALUE`. KEY has surrounding whitespace
// trimmed (kubectl's behaviour); VALUE is taken VERBATIM from the
// original line — including any trailing whitespace — so a password
// like "secret123 " (with a trailing space) survives the parse. The
// reviewer P2 on T07a flagged the previous version, which trimmed the
// whole line before Cut(), and silently dropped trailing whitespace.
//
// Comment lines start with `#`. Blank lines (whitespace-only) are
// ignored. CRLF is normalised — the `\r` if present is stripped only
// for the comment / blank-line check, then the original byte stays in
// the value (matches `kubectl create secret --from-env-file`).
// Returns an error on duplicate keys so copy-paste typos surface
// instead of last-line-wins.
func parseEnvFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --from-env-file %s: %w", path, err)
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(raw), "\n") {
		// Strip a single trailing CR so a file with CRLF endings still
		// classifies blank/comment lines correctly. The CR is dropped
		// for both detection AND value (it's an artifact of the line
		// ending, not part of the secret).
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Cut the ORIGINAL line (not the trimmed copy) so trailing
		// whitespace in the value is preserved.
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("%s:%d: invalid line %q (want KEY=VALUE)", path, i+1, line)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate key %q", path, i+1, k)
		}
		out[k] = v
	}
	return out, nil
}

// buildSyncPatch turns the four --enabled / --target / --refresh /
// --keys flag values into the sparse map[string]any payload the
// backend + k8sapi sync helpers expect. Only fields the user
// explicitly set appear in the map — that's what makes
// `--enabled=false` survive json.Marshal instead of being dropped
// by an `omitempty` struct tag (M1-T07 first-review-pass P1).
//
// Pure function — exported for unit tests.
func buildSyncPatch(enabledFlag, target, refresh, keysCSV string) (map[string]any, error) {
	out := map[string]any{}
	if enabledFlag != "" {
		switch strings.ToLower(enabledFlag) {
		case "true", "yes", "on":
			out["enabled"] = true
		case "false", "no", "off":
			out["enabled"] = false
		default:
			return nil, fmt.Errorf("--enabled must be true|false (got %q)", enabledFlag)
		}
	}
	if target != "" {
		out["targetSecretName"] = target
	}
	if refresh != "" {
		out["refreshInterval"] = refresh
	}
	if keysCSV != "" {
		var keys []string
		for _, k := range strings.Split(keysCSV, ",") {
			k = strings.TrimSpace(k)
			if k != "" {
				keys = append(keys, k)
			}
		}
		// Allow explicit empty list (clear keys) when the flag was
		// passed as just `--keys=,` or similar — fall through to
		// the assignment below so the value is present in the patch.
		if keys == nil {
			keys = []string{}
		}
		out["keys"] = keys
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one of --enabled, --target, --refresh, --keys is required")
	}
	return out, nil
}

// buildPutData parses --from-literal=K=V and --from-file=K=path
// flags into the {KEY: VALUE} map the backend expects. Exported via
// the package-private helper for unit testing.
func buildPutData(literals, files []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range literals {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --from-literal %q (want KEY=VALUE)", kv)
		}
		out[k] = v
	}
	for _, kp := range files {
		k, p, ok := strings.Cut(kp, "=")
		if !ok || k == "" || p == "" {
			return nil, fmt.Errorf("invalid --from-file %q (want KEY=path)", kp)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read --from-file %s: %w", p, err)
		}
		out[k] = string(b)
	}
	return out, nil
}

// -------- sync -----------------------------------------------------

func secretsSyncCmd() *cobra.Command {
	var namespace, target, refresh, keysCSV string
	var enabledFlag string
	cmd := &cobra.Command{
		Use:   "sync <name>",
		Short: "Configure how the secret syncs to a Kubernetes Secret.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			syncPatch, err := buildSyncPatch(enabledFlag, target, refresh, keysCSV)
			if err != nil {
				return err
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			k8s, err := scope.k8s()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			res, err := k8s.PatchManagedSecretSync(ctx, scope.Namespace, name, syncPatch)
			if err != nil {
				return err
			}
			fmt.Printf("Patched %s/%s sync: enabled=%v target=%q refresh=%q keys=%v\n",
				scope.Namespace, name,
				res.Spec.Sync.Enabled, res.Spec.Sync.TargetSecretName,
				res.Spec.Sync.RefreshInterval, res.Spec.Sync.Keys,
			)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace")
	cmd.Flags().StringVar(&enabledFlag, "enabled", "", "true|false (omit to leave unchanged)")
	cmd.Flags().StringVar(&target, "target", "", "Target K8s Secret name")
	cmd.Flags().StringVar(&refresh, "refresh", "", "ESO refresh interval (e.g. 1m)")
	cmd.Flags().StringVar(&keysCSV, "keys", "", "Comma-separated list of keys to project (empty = all)")
	return cmd
}

// -------- import ---------------------------------------------------

func secretsImportCmd() *cobra.Command {
	var namespace, fromName, fromNs, secretType, desc string
	var allowCross bool
	cmd := &cobra.Command{
		Use:   "import <name>",
		Short: "Import an existing Kubernetes Secret as a managed secret.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			res, err := cli.ImportSecret(ctx, scope.Namespace, name, backend.ImportSecretOptions{
				SourceSecretName:      fromName,
				SourceSecretNamespace: fromNs,
				Type:                  secretType,
				Description:           desc,
				AllowCrossNamespace:   allowCross,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Imported %s/%s (version %d) from %s/%s\n",
				scope.Namespace, name, res.KvVersion,
				fmtCoalesce(fromNs, scope.Namespace), fromName)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Target project namespace")
	cmd.Flags().StringVar(&fromName, "from", "", "Source K8s Secret name (required)")
	cmd.Flags().StringVar(&fromNs, "from-namespace", "", "Source namespace (default: target namespace)")
	cmd.Flags().StringVar(&secretType, "type", "opaque", "Secret type: opaque|password|api-key|tls|db-static")
	cmd.Flags().StringVar(&desc, "description", "", "Free-text description")
	cmd.Flags().BoolVar(&allowCross, "cross-namespace", false, "Allow source from a different namespace (audit-visible)")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}

// -------- delete ---------------------------------------------------

func secretsDeleteCmd() *cobra.Command {
	var namespace string
	var destroy bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a secret. --destroy also permanently wipes stored values (admin only).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			if destroy {
				// Backend owns the KV-first-then-CR ordering + audit;
				// delegate so the CLI never has to reproduce that saga.
				cli, err := scope.backend()
				if err != nil {
					return err
				}
				ctx, cancel := ctxWithTimeout()
				defer cancel()
				res, err := cli.DeleteSecret(ctx, scope.Namespace, name, true)
				if err != nil {
					return err
				}
				fmt.Printf("Permanently destroyed %s/%s (secret removed=%v, stored values destroyed=%v)\n",
					scope.Namespace, name, res.Deleted, res.Destroyed)
				return nil
			}
			k8s, err := scope.k8s()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			if err := k8s.DeleteManagedSecret(ctx, scope.Namespace, name); err != nil {
				return err
			}
			fmt.Printf("Deleted secret %s/%s (stored values preserved; pass --destroy to remove them too)\n", scope.Namespace, name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace")
	cmd.Flags().BoolVar(&destroy, "destroy", false, "Also permanently destroy every stored version (irreversible, admin only)")
	return cmd
}

// -------- destroy-version ------------------------------------------

func secretsDestroyVersionCmd() *cobra.Command {
	var namespace string
	var version int
	cmd := &cobra.Command{
		Use:   "destroy-version <name>",
		Short: "Permanently destroy a single version of a secret (admin only).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if version <= 0 {
				return fmt.Errorf("--version must be > 0")
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			res, err := cli.DestroyKVVersion(ctx, scope.Namespace, name, version)
			if err != nil {
				return err
			}
			fmt.Printf("Destroyed %s/%s version %d\n", res.Namespace, res.Name, res.DestroyedVersion)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace")
	cmd.Flags().IntVar(&version, "version", 0, "Version number to destroy (required, > 0)")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

// -------- consumers ------------------------------------------------

func secretsConsumersCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:   "consumers <name>",
		Short: "List workloads that reference the synced Kubernetes Secret.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			out, err := parseOutput(outFlag)
			if err != nil {
				return err
			}
			scope, err := resolveScope(namespace)
			if err != nil {
				return err
			}
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			list, err := cli.ListConsumers(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, list)
			}
			fmt.Printf("Secret %s (scanner %s, cached=%v):\n", list.Secret, list.ScannerVersion, list.Cached)
			if len(list.Items) == 0 {
				fmt.Println("  (no consumers found)")
			} else {
				fmt.Printf("  %-14s  %-30s  %s\n", "KIND", "NAME", "REFERENCES")
				for _, it := range list.Items {
					sort.Strings(it.References)
					fmt.Printf("  %-14s  %-30s  %s\n", it.Kind, truncCLI(it.Name, 30), strings.Join(it.References, ", "))
				}
			}
			if len(list.Errors) > 0 {
				fmt.Println("Partial errors:")
				for _, e := range list.Errors {
					fmt.Printf("  - %v\n", e)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

// -------- output helpers -------------------------------------------

func truncCLI(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func fmtCoalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func printSecretSummary(s *backend.SecretSummary, withValue bool) {
	fmt.Printf("Name:        %s\n", s.Name)
	fmt.Printf("Namespace:   %s\n", s.Namespace)
	fmt.Printf("Type:        %s\n", s.Type)
	if s.Description != "" {
		fmt.Printf("Description: %s\n", s.Description)
	}
	fmt.Printf("Sync:        enabled=%v target=%q refresh=%q\n",
		s.Sync.Enabled, s.Sync.TargetSecretName, s.Sync.RefreshInterval)
	if s.OpenBao != nil {
		fmt.Printf("Storage:     currentVersion=%d created=%s\n",
			s.OpenBao.CurrentVersion, s.OpenBao.CreatedTime)
	}
	if withValue && s.Value != nil {
		fmt.Printf("Value (v%d):\n", s.Value.Metadata.Version)
		keys := make([]string, 0, len(s.Value.Data))
		for k := range s.Value.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %s = %s\n", k, s.Value.Data[k])
		}
	}
}

func printManagedSecret(ms *k8sapi.ManagedSecret) {
	fmt.Printf("Name:        %s\n", ms.Metadata.Name)
	fmt.Printf("Namespace:   %s\n", ms.Metadata.Namespace)
	fmt.Printf("Type:        %s\n", ms.Spec.Type)
	if ms.Spec.Description != "" {
		fmt.Printf("Description: %s\n", ms.Spec.Description)
	}
	fmt.Printf("Sync:        enabled=%v target=%q refresh=%q\n",
		ms.Spec.Sync.Enabled, ms.Spec.Sync.TargetSecretName, ms.Spec.Sync.RefreshInterval)
	if ms.Status.SyncedSecretName != "" {
		fmt.Printf("Synced as:   %s\n", ms.Status.SyncedSecretName)
	}
}
