// `kube-dc kms` subcommand — M3-T03 CLI for the KMS Manager. Mirrors
// `kube-dc certificates`: scope resolution + JWT via the existing
// kubeconfig context, all calls go through the backend at
// backend.<domain>/api/kms/* so the audit + admission guard layers
// there are exercised for CLI users too.
//
// Surface (UX PRD §5.4):
//
//   kube-dc kms keys list
//   kube-dc kms keys describe <name>            (alias: get)
//   kube-dc kms keys create <name> [--rotation 90d ...]
//   kube-dc kms keys rotate <name>
//   kube-dc kms keys delete <name> --yes
//   kube-dc kms keys schedule-delete <name>
//   kube-dc kms keys cancel-delete <name>
//   kube-dc kms keys set-min-decryption-version <name> <version>
//   kube-dc kms encrypt <name> --plaintext-file <path> [--out <path>]
//   kube-dc kms decrypt <name> --ciphertext-file <path> [--out <path>]
//
// Encrypt/decrypt flags accept --plaintext / --ciphertext as inline
// alternatives to --plaintext-file / --ciphertext-file for short
// payloads. Inline + file are mutually exclusive.

package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/spf13/cobra"
)

func encodeBase64(b []byte) string         { return base64.StdEncoding.EncodeToString(b) }
func decodeBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// kmsCmd is the parent command. Sub-tree:
//   kms
//     keys
//       list / describe / create / rotate / delete /
//       schedule-delete / cancel-delete / set-min-decryption-version
//     encrypt / decrypt
func kmsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kms",
		Short: "Manage project encryption keys (KMSKey)",
		Long: `Manage project encryption keys backed by OpenBao Transit. Keys are
symmetric (aes256-gcm96 or chacha20-poly1305), Phase-1 non-exportable;
material never leaves OpenBao. Encrypt/decrypt go through the kube-dc
backend so the 64 KiB plaintext cap, audit emission, and per-role
policy gate are enforced uniformly across UI/CLI users.

Permissions follow your project role (cap matrix in UX PRD §4.3):
  viewer            encrypt only (no decrypt)
  developer         encrypt + decrypt
  project-manager   + rotate, set-min-decryption-version, schedule-delete
  project-admin     + destroy`,
	}
	cmd.AddCommand(kmsKeysCmd())
	cmd.AddCommand(kmsEncryptCmd())
	cmd.AddCommand(kmsDecryptCmd())
	return cmd
}

func kmsKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "KMSKey lifecycle (list / create / rotate / delete / ...)",
	}
	cmd.AddCommand(kmsKeysListCmd())
	cmd.AddCommand(kmsKeysDescribeCmd())
	cmd.AddCommand(kmsKeysCreateCmd())
	cmd.AddCommand(kmsKeysRotateCmd())
	cmd.AddCommand(kmsKeysDeleteCmd())
	cmd.AddCommand(kmsKeysScheduleDeleteCmd())
	cmd.AddCommand(kmsKeysCancelDeleteCmd())
	cmd.AddCommand(kmsKeysSetMinDecryptionVersionCmd())
	return cmd
}

// -------- keys list -------------------------------------------------

func kmsKeysListCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List KMSKeys in the project namespace",
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
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			list, err := cli.ListKMSKeys(ctx, scope.Namespace)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, list)
			}
			return printKMSKeysTable(list.Items)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

// -------- keys describe / get --------------------------------------

func kmsKeysDescribeCmd() *cobra.Command {
	var namespace, outFlag string
	cmd := &cobra.Command{
		Use:     "describe <name>",
		Aliases: []string{"get"},
		Short:   "Show one KMSKey + status mirror",
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
			cli, err := scope.backend()
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			k, err := cli.GetKMSKey(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, k)
			}
			return printKMSKeyDetail(k)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

// -------- keys create ----------------------------------------------

func kmsKeysCreateCmd() *cobra.Command {
	var (
		namespace, purpose, algorithm, deletionPolicy, rotation string
		enableRotation                                          bool
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new KMSKey",
		Example: `  # Application key with 90-day rotation:
  kube-dc kms keys create app-data --rotation 90d

  # Backup key, no rotation, chacha20:
  kube-dc kms keys create backup-key --purpose backup --algorithm chacha20-poly1305`,
		Args: cobra.ExactArgs(1),
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
			rot := backend.KMSKeyRotation{}
			if enableRotation || rotation != "" {
				rot.Enabled = true
				if rotation == "" {
					return fmt.Errorf("--rotation interval is required when rotation is enabled")
				}
				rot.Interval = rotation
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			k, err := cli.CreateKMSKey(ctx, scope.Namespace, name, backend.CreateKMSKeyOptions{
				Purpose:        purpose,
				Algorithm:      algorithm,
				DeletionPolicy: deletionPolicy,
				Rotation:       rot,
			})
			if err != nil {
				return err
			}
			fmt.Printf("Created KMSKey %s/%s (purpose=%s algorithm=%s rotation=%s deletionPolicy=%s)\n",
				k.Namespace, k.Name, k.Purpose, k.Algorithm,
				fmtCoalesce(rotationDescription(k.Rotation), "disabled"), k.DeletionPolicy)
			fmt.Printf("Watch progress: kube-dc kms keys describe %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVar(&purpose, "purpose", "application", "Key purpose: application|backup|etcd")
	cmd.Flags().StringVar(&algorithm, "algorithm", "aes256-gcm96", "Symmetric algorithm: aes256-gcm96|chacha20-poly1305")
	cmd.Flags().StringVar(&deletionPolicy, "deletion-policy", "retain", "Deletion policy: retain|schedule")
	cmd.Flags().StringVar(&rotation, "rotation", "", "Auto-rotate interval (e.g. 30d, 12h). Empty = disabled")
	cmd.Flags().BoolVar(&enableRotation, "enable-rotation", false, "Enable rotation (--rotation also enables when set)")
	return cmd
}

// -------- keys rotate ----------------------------------------------

func kmsKeysRotateCmd() *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "rotate <name>",
		Short: "Rotate the key — adds a new Transit version",
		Long: `Trigger an explicit rotation. Existing ciphertext still decrypts
because OpenBao Transit retains all key versions until you advance
min-decryption-version with kube-dc kms keys set-min-decryption-version.`,
		Args: cobra.ExactArgs(1),
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
			res, err := cli.RotateKMSKey(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			fmt.Printf("Rotated KMSKey %s/%s (transit key %s)\n", res.Namespace, res.Name, res.KeyName)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	return cmd
}

// -------- keys delete ----------------------------------------------

func kmsKeysDeleteCmd() *cobra.Command {
	var namespace string
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a KMSKey",
		Long: `Delete a KMSKey. Default deletionPolicy=retain leaves the Transit
key in place; schedule starts a 30d countdown handled by the controller.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !yes {
				fmt.Fprintf(os.Stderr, "Delete %s? Re-run with --yes to confirm.\n", name)
				return fmt.Errorf("not confirmed")
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
			res, err := cli.DeleteKMSKey(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			fmt.Printf("Deleted %s/%s\n", res.Namespace, res.Name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the deletion")
	return cmd
}

// -------- keys schedule-delete / cancel-delete ---------------------

func kmsKeysScheduleDeleteCmd() *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "schedule-delete <name>",
		Short: "Flip spec.deletionPolicy to schedule (30d countdown)",
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
			res, err := cli.ScheduleDeleteKMSKey(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			fmt.Printf("Scheduled deletion for %s/%s (deletionPolicy=%s)\n", res.Namespace, res.Name, res.DeletionPolicy)
			fmt.Printf("Cancel before the countdown elapses: kube-dc kms keys cancel-delete %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	return cmd
}

func kmsKeysCancelDeleteCmd() *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "cancel-delete <name>",
		Short: "Cancel a scheduled deletion (flip back to retain)",
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
			res, err := cli.CancelDeleteKMSKey(ctx, scope.Namespace, name)
			if err != nil {
				return err
			}
			fmt.Printf("Cancelled scheduled deletion for %s/%s (deletionPolicy=%s)\n", res.Namespace, res.Name, res.DeletionPolicy)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	return cmd
}

// -------- keys set-min-decryption-version --------------------------

func kmsKeysSetMinDecryptionVersionCmd() *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "set-min-decryption-version <name> <version>",
		Short: "Set the minimum key version still allowed to decrypt",
		Long: `Advance min_decryption_version on the Transit key. Lowering is
allowed; raising above current_version is rejected by OpenBao. Requires
project-manager+ role (transit/keys/+/config policy).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			version, err := strconv.Atoi(args[1])
			if err != nil || version < 1 {
				return fmt.Errorf("version must be an integer >= 1")
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
			res, err := cli.SetMinDecryptionVersion(ctx, scope.Namespace, name, version)
			if err != nil {
				return err
			}
			fmt.Printf("Set min decryption version=%d for %s/%s (transit key %s)\n",
				res.MinDecryptionVersion, res.Namespace, res.Name, res.KeyName)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	return cmd
}

// -------- encrypt / decrypt ---------------------------------------

func kmsEncryptCmd() *cobra.Command {
	var (
		namespace, plaintext, plaintextFile, context, outFile string
	)
	cmd := &cobra.Command{
		Use:   "encrypt <key>",
		Short: "Encrypt ≤64 KiB plaintext under the key",
		Long: `Encrypt plaintext under a KMSKey. Inline (--plaintext) is for short
strings; --plaintext-file is for larger blobs up to 64 KiB. Output is
the OpenBao "vault:v<n>:<base64>" ciphertext form — store it as-is, the
version prefix is required at decrypt time.`,
		Example: `  kube-dc kms encrypt app-data --plaintext-file payload.json --out payload.enc
  echo "secret123" | kube-dc kms encrypt app-data --plaintext-file - --out -`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if (plaintext != "" && plaintextFile != "") || (plaintext == "" && plaintextFile == "") {
				return fmt.Errorf("provide exactly one of --plaintext or --plaintext-file")
			}
			b, err := readInline(plaintext, plaintextFile)
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
			res, err := cli.EncryptKMS(ctx, scope.Namespace, name, backend.EncryptKMSOptions{
				// Always use the base64 field — covers binary payloads
				// + utf-8 alike, no risk of misinterpretation of
				// embedded null bytes.
				PlaintextB64: encodeBase64(b),
				Context:      context,
			})
			if err != nil {
				return err
			}
			return writeOutput(outFile, []byte(res.Ciphertext))
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVar(&plaintext, "plaintext", "", "Inline plaintext (mutually exclusive with --plaintext-file)")
	cmd.Flags().StringVar(&plaintextFile, "plaintext-file", "", "File to read plaintext from. '-' = stdin")
	cmd.Flags().StringVar(&context, "context", "", "Optional encryption context (base64)")
	cmd.Flags().StringVar(&outFile, "out", "-", "File to write ciphertext to. '-' = stdout")
	return cmd
}

func kmsDecryptCmd() *cobra.Command {
	var (
		namespace, ciphertext, ciphertextFile, context, outFile string
	)
	cmd := &cobra.Command{
		Use:   "decrypt <key>",
		Short: "Decrypt a vault:v* ciphertext under the key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if (ciphertext != "" && ciphertextFile != "") || (ciphertext == "" && ciphertextFile == "") {
				return fmt.Errorf("provide exactly one of --ciphertext or --ciphertext-file")
			}
			b, err := readInline(ciphertext, ciphertextFile)
			if err != nil {
				return err
			}
			ctTrim := strings.TrimSpace(string(b))
			if !strings.HasPrefix(ctTrim, "vault:v") {
				return fmt.Errorf("ciphertext must be in OpenBao 'vault:v<n>:...' form (got %q...)", truncate(ctTrim, 16))
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
			res, err := cli.DecryptKMS(ctx, scope.Namespace, name, backend.DecryptKMSOptions{
				Ciphertext: ctTrim,
				Context:    context,
			})
			if err != nil {
				return err
			}
			// Always write the base64-decoded raw bytes; callers can
			// cast to string when they expect utf-8.
			raw, err := decodeBase64(res.PlaintextB64)
			if err != nil {
				return fmt.Errorf("decrypt: bad base64 in backend response: %w", err)
			}
			return writeOutput(outFile, raw)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Project namespace (default: current context's namespace)")
	cmd.Flags().StringVar(&ciphertext, "ciphertext", "", "Inline ciphertext (mutually exclusive with --ciphertext-file)")
	cmd.Flags().StringVar(&ciphertextFile, "ciphertext-file", "", "File to read ciphertext from. '-' = stdin")
	cmd.Flags().StringVar(&context, "context", "", "Optional encryption context (must match the encrypt-time value)")
	cmd.Flags().StringVar(&outFile, "out", "-", "File to write plaintext to. '-' = stdout")
	return cmd
}

// -------- rendering helpers ----------------------------------------

// printKMSKeysTable renders the list in column form. Pure — no
// network — extracted so unit tests exercise it with canned inputs.
func printKMSKeysTable(items []backend.KMSKeySummary) error {
	if len(items) == 0 {
		fmt.Println("No KMSKeys.")
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPURPOSE\tALGORITHM\tVERSION\tROTATION\tDELETION\tREADY\tAGE")
	for _, it := range items {
		ready := kmsReadyFromConditions(it.Status.Conditions)
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			it.Name,
			fmtCoalesce(it.Purpose, "application"),
			fmtCoalesce(it.Algorithm, "aes256-gcm96"),
			it.Status.CurrentVersion,
			rotationDescription(it.Rotation),
			fmtCoalesce(it.DeletionPolicy, "retain"),
			ready,
			formatAge(it.CreationTimestamp),
		)
	}
	return w.Flush()
}

func printKMSKeyDetail(k *backend.KMSKeySummary) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintf(w, "Name:\t%s/%s\n", k.Namespace, k.Name)
	fmt.Fprintf(w, "Purpose:\t%s\n", fmtCoalesce(k.Purpose, "application"))
	fmt.Fprintf(w, "Algorithm:\t%s\n", fmtCoalesce(k.Algorithm, "aes256-gcm96"))
	fmt.Fprintf(w, "Deletion Policy:\t%s\n", fmtCoalesce(k.DeletionPolicy, "retain"))
	fmt.Fprintf(w, "Rotation:\t%s\n", rotationDescription(k.Rotation))
	fmt.Fprintf(w, "Created:\t%s\n", fmtCoalesce(k.CreationTimestamp, "-"))
	if k.Status.KeyId != "" {
		fmt.Fprintf(w, "Transit Key:\t%s\n", k.Status.KeyId)
	}
	if k.Status.CurrentVersion > 0 {
		fmt.Fprintf(w, "Current Version:\t%d\n", k.Status.CurrentVersion)
	}
	if k.Status.MinDecryptionVersion > 0 {
		fmt.Fprintf(w, "Min Decryption Version:\t%d\n", k.Status.MinDecryptionVersion)
	}
	if k.Status.LastRotatedTime != "" {
		fmt.Fprintf(w, "Last Rotated:\t%s\n", k.Status.LastRotatedTime)
	}
	if k.Status.NextRotationTime != "" {
		fmt.Fprintf(w, "Next Rotation:\t%s\n", k.Status.NextRotationTime)
	}
	if k.Status.ScheduledDeletion != "" {
		fmt.Fprintf(w, "Scheduled Deletion:\t%s\n", k.Status.ScheduledDeletion)
	}
	fmt.Fprintf(w, "Ready:\t%s\n", kmsReadyFromConditions(k.Status.Conditions))
	return nil
}

// rotationDescription renders the rotation block for tables /
// detail. Pure — extracted so unit tests can pin formatting.
func rotationDescription(r backend.KMSKeyRotation) string {
	if !r.Enabled {
		return "disabled"
	}
	if r.Interval == "" {
		return "enabled"
	}
	return r.Interval
}

// kmsReadyFromConditions extracts the Ready condition into a terse
// string for table cells. Includes the reason for non-True states so
// operators see DeletionScheduled / DeletionCompleted at a glance.
func kmsReadyFromConditions(conds []map[string]any) string {
	for _, c := range conds {
		if t, _ := c["type"].(string); t == "Ready" {
			st, _ := c["status"].(string)
			rs, _ := c["reason"].(string)
			if st == "True" {
				return "True"
			}
			if rs != "" {
				return fmt.Sprintf("%s/%s", st, rs)
			}
			return st
		}
	}
	return "-"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// readInline picks the right source for plaintext/ciphertext given
// either inline string or file path. '-' = stdin / stdout.
func readInline(inline, file string) ([]byte, error) {
	if inline != "" {
		return []byte(inline), nil
	}
	if file == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(file)
}

// writeOutput sends bytes either to stdout ('-') or a file path.
func writeOutput(file string, b []byte) error {
	if file == "" || file == "-" {
		_, err := os.Stdout.Write(b)
		if err != nil {
			return err
		}
		// Ensure a trailing newline on stdout for terminal hygiene.
		if len(b) == 0 || b[len(b)-1] != '\n' {
			fmt.Println()
		}
		return nil
	}
	return os.WriteFile(file, b, 0o600)
}
