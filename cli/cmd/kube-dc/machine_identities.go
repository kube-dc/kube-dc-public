// `kube-dc machine-identities` — non-human identities of an Organization
// (MachineIdentity, kube-dc.com/v1, in the organization namespace). Each is a
// Keycloak client-credentials client whose tokens reach the Kubernetes API as
// an organization user in the listed groups, for Terraform, MCP servers and
// CI. Native Kubernetes calls only.
//
// Verbs:
//
//   create      <name> --group g...   create and wait until ready
//   list | get  <name>                show identities and their state
//   rotate      <name>                issue a new client secret (the old one stops working)
//   delete      <name>                remove the identity and its client
//   kubeconfig  <name>                write a kubeconfig that signs in as the identity
//   token                             exec credential plugin used by that kubeconfig

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shalb/kube-dc/cli/internal/k8sapi"
	"github.com/spf13/cobra"
)

func machineIdentitiesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "machine-identities",
		Short:   "Manage the Organization's machine identities (Terraform, MCP, CI)",
		Aliases: []string{"machine-identity", "mid"},
		Long: `A machine identity signs in without a person: a client ID and secret
get short-lived tokens that the Kubernetes API accepts as an Organization
user in the identity's groups (org-admin, user, or an OrganizationGroup).
Organization admins manage them and read their credentials.

  kube-dc machine-identities create ci --group org-admin
  kube-dc machine-identities kubeconfig ci --out ci.kubeconfig
  KUBECONFIG=ci.kubeconfig terraform apply`,
	}
	cmd.AddCommand(midCreateCmd(), midListCmd(), midGetCmd(), midRotateCmd(), midDeleteCmd(), midKubeconfigCmd(), midTokenCmd())
	return cmd
}

// midScope is the organization namespace and a Kubernetes client.
func midScope(orgFlag string) (*secretsScope, string, *k8sapi.Client, error) {
	org, err := orgFromContextOrFlag(orgFlag)
	if err != nil {
		return nil, "", nil, err
	}
	scope, err := resolveScope("")
	if err != nil {
		return nil, "", nil, err
	}
	k8s, err := scope.k8s()
	if err != nil {
		return nil, "", nil, err
	}
	return scope, org, k8s, nil
}

func orDash(v any) any {
	if v == nil {
		return "—"
	}
	return v
}

func midReady(o *k8sapi.Object) bool { ready, _ := o.Status["ready"].(bool); return ready }

func midCondition(o *k8sapi.Object) string {
	conds, _ := o.Status["conditions"].([]any)
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" {
			return fmt.Sprintf("%v: %v", m["reason"], m["message"])
		}
	}
	return ""
}

// midWait polls until done(o) or the deadline, returning the last object.
func midWait(ctx context.Context, k8s *k8sapi.Client, org, name string, done func(*k8sapi.Object) bool) (*k8sapi.Object, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		o, err := k8s.Get(ctx, k8sapi.MachineIdentities, org, name)
		if err != nil {
			return nil, err
		}
		if done(o) {
			return o, nil
		}
		if time.Now().After(deadline) {
			return o, fmt.Errorf("machine identity %s not ready: %s", name, midCondition(o))
		}
		time.Sleep(2 * time.Second)
	}
}

func midCreateCmd() *cobra.Command {
	var org, description string
	var groups []string
	var lifetime time.Duration
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a machine identity and wait until it is ready",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(groups) == 0 {
				return fmt.Errorf("at least one --group is required (org-admin, user, or an OrganizationGroup)")
			}
			_, org, k8s, err := midScope(org)
			if err != nil {
				return err
			}
			// The CRD keeps groups as a set: repeats are refused.
			seen, unique := map[string]bool{}, []string{}
			for _, g := range groups {
				if !seen[g] {
					seen[g] = true
					unique = append(unique, g)
				}
			}
			spec := map[string]any{"groups": unique}
			if description != "" {
				spec["description"] = description
			}
			if lifetime > 0 {
				spec["tokenLifetimeSeconds"] = int(lifetime.Seconds())
			}
			ctx := context.Background()
			if _, err := k8s.Create(ctx, k8sapi.MachineIdentities, org, args[0], spec); err != nil {
				return err
			}
			o, err := midWait(ctx, k8s, org, args[0], midReady)
			if err != nil {
				return err
			}
			fmt.Printf("machine identity %s ready: Kubernetes user %v\n", args[0], o.Status["username"])
			fmt.Printf("next: kube-dc machine-identities kubeconfig %s --out %s.kubeconfig\n", args[0], args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: from the current context)")
	cmd.Flags().StringSliceVar(&groups, "group", nil, "Group to join: org-admin, user, or an OrganizationGroup (repeatable)")
	cmd.Flags().StringVar(&description, "description", "", "Description")
	cmd.Flags().DurationVar(&lifetime, "token-lifetime", 0, "Access-token lifetime, 1m to 1h (default: the realm's)")
	return cmd
}

func midListCmd() *cobra.Command {
	var org, outFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the Organization's machine identities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := parseOutput(outFlag)
			if err != nil {
				return err
			}
			_, org, k8s, err := midScope(org)
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			list, err := k8s.List(ctx, k8sapi.MachineIdentities, org)
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, list)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tGROUPS\tREADY\tSECRET ROTATED\tDESCRIPTION")
			for i := range list.Items {
				o := &list.Items[i]
				groups, _ := o.Spec["groups"].([]any)
				names := make([]string, 0, len(groups))
				for _, g := range groups {
					names = append(names, fmt.Sprint(g))
				}
				fmt.Fprintf(tw, "%s\t%s\t%v\t%v\t%v\n", o.Metadata.Name, strings.Join(names, ","), midReady(o), o.Status["secretRotatedAt"], o.Spec["description"])
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: from the current context)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

func midGetCmd() *cobra.Command {
	var org, outFlag string
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show one machine identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := parseOutput(outFlag)
			if err != nil {
				return err
			}
			_, org, k8s, err := midScope(org)
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			o, err := k8s.Get(ctx, k8sapi.MachineIdentities, org, args[0])
			if err != nil {
				return err
			}
			if out != outTable {
				return printSerialized(out, o)
			}
			fmt.Printf("Name:           %s\nUser:           %v\nClient ID:      %v\nToken URL:      %v\nGroups:         %v\nRefused groups: %v\nReady:          %v (%s)\nSecret rotated: %v\n",
				o.Metadata.Name, orDash(o.Status["username"]), orDash(o.Status["clientId"]), orDash(o.Status["tokenUrl"]), orDash(o.Status["groups"]), orDash(o.Status["refusedGroups"]), midReady(o), midCondition(o), orDash(o.Status["secretRotatedAt"]))
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: from the current context)")
	cmd.Flags().StringVarP(&outFlag, "output", "o", "table", "Output format: table|json|yaml")
	return cmd
}

func midRotateCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "rotate <name>",
		Short: "Issue a new client secret; the previous one stops working at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, org, k8s, err := midScope(org)
			if err != nil {
				return err
			}
			ctx := context.Background()
			o, err := k8s.Get(ctx, k8sapi.MachineIdentities, org, args[0])
			if err != nil {
				return err
			}
			gen, _ := o.Spec["secretGeneration"].(float64)
			next := int64(gen) + 1
			if err := k8s.Patch(ctx, k8sapi.MachineIdentities, org, args[0], map[string]any{"spec": map[string]any{"secretGeneration": next}}); err != nil {
				return err
			}
			if _, err := midWait(ctx, k8s, org, args[0], func(o *k8sapi.Object) bool {
				observed, _ := o.Status["observedSecretGeneration"].(float64)
				return int64(observed) >= next
			}); err != nil {
				return err
			}
			fmt.Printf("secret of %s rotated; regenerate kubeconfigs with: kube-dc machine-identities kubeconfig %s\n", args[0], args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: from the current context)")
	return cmd
}

func midDeleteCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a machine identity and its client",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, org, k8s, err := midScope(org)
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			if err := k8s.Delete(ctx, k8sapi.MachineIdentities, org, args[0]); err != nil {
				return err
			}
			fmt.Printf("machine identity %s deleted\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: from the current context)")
	return cmd
}

// midKubeconfig is a kubeconfig whose user runs `kube-dc machine-identities
// token` with the identity's credentials in its environment.
func midKubeconfig(apiServer, caPEM, namespace, name string, creds map[string]string) ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		self = "kube-dc"
	}
	cluster := map[string]any{"server": apiServer}
	if caPEM != "" {
		cluster["certificate-authority-data"] = base64.StdEncoding.EncodeToString([]byte(caPEM))
	}
	ctxName := "machine/" + name
	cfg := map[string]any{
		"apiVersion": "v1", "kind": "Config", "current-context": ctxName,
		"clusters": []any{map[string]any{"name": ctxName, "cluster": cluster}},
		"users": []any{map[string]any{"name": ctxName, "user": map[string]any{"exec": map[string]any{
			"apiVersion": "client.authentication.k8s.io/v1", "command": self, "args": []string{"machine-identities", "token"},
			"interactiveMode": "Never",
			"env": []any{
				map[string]string{"name": "KUBE_DC_TOKEN_URL", "value": creds["token-url"]},
				map[string]string{"name": "KUBE_DC_CLIENT_ID", "value": creds["client-id"]},
				map[string]string{"name": "KUBE_DC_CLIENT_SECRET", "value": creds["client-secret"]},
			},
		}}}},
		"contexts": []any{map[string]any{"name": ctxName, "context": map[string]any{"cluster": ctxName, "user": ctxName, "namespace": namespace}}},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func midKubeconfigCmd() *cobra.Command {
	var org, outPath, namespace string
	cmd := &cobra.Command{
		Use:   "kubeconfig <name>",
		Short: "Write a kubeconfig that signs in as the machine identity (holds its secret)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, org, k8s, err := midScope(org)
			if err != nil {
				return err
			}
			ctx, cancel := ctxWithTimeout()
			defer cancel()
			o, err := k8s.Get(ctx, k8sapi.MachineIdentities, org, args[0])
			if err != nil {
				return err
			}
			secretName, _ := o.Status["secretName"].(string)
			if !midReady(o) || secretName == "" {
				return fmt.Errorf("machine identity %s is not ready: %s", args[0], midCondition(o))
			}
			creds, err := k8s.SecretData(ctx, org, secretName)
			if err != nil {
				return err
			}
			if namespace == "" {
				namespace = org
			}
			data, err := midKubeconfig(scope.APIServer, scope.K8sCACert, namespace, args[0], creds)
			if err != nil {
				return err
			}
			if outPath == "" || outPath == "-" {
				_, err = os.Stdout.Write(append(data, '\n'))
				return err
			}
			return os.WriteFile(outPath, append(data, '\n'), 0o600)
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "Organization (default: from the current context)")
	cmd.Flags().StringVar(&outPath, "out", "", "File to write (mode 0600); default stdout")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Default namespace of the context (default: the organization namespace)")
	return cmd
}

// midToken issues a client-credentials token and prints an ExecCredential.
func midToken(ctx context.Context, tokenURL, clientID, secret string) ([]byte, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || resp.StatusCode != http.StatusOK || tok.AccessToken == "" {
		return nil, fmt.Errorf("token request refused (HTTP %d): %s", resp.StatusCode, tok.Error)
	}
	// Expire a little early so the client asks again before the API server refuses it.
	ttl := time.Duration(max(10, tok.ExpiresIn-10)) * time.Second
	return json.Marshal(map[string]any{
		"apiVersion": "client.authentication.k8s.io/v1", "kind": "ExecCredential",
		"status": map[string]any{"token": tok.AccessToken, "expirationTimestamp": time.Now().Add(ttl).UTC().Format(time.RFC3339)},
	})
}

func midTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Exec credential plugin: print a token for KUBE_DC_CLIENT_ID/KUBE_DC_CLIENT_SECRET",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tokenURL, id, secret := os.Getenv("KUBE_DC_TOKEN_URL"), os.Getenv("KUBE_DC_CLIENT_ID"), os.Getenv("KUBE_DC_CLIENT_SECRET")
			if tokenURL == "" || id == "" || secret == "" {
				return fmt.Errorf("KUBE_DC_TOKEN_URL, KUBE_DC_CLIENT_ID and KUBE_DC_CLIENT_SECRET must be set")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := midToken(ctx, tokenURL, id, secret)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(append(out, '\n'))
			return err
		},
	}
}
