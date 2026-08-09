// Package oidccutover wires a cluster's kube-apiserver to the
// oidc-webhook-authenticator — the step that turns a converged cluster into a
// USABLE one.
//
// # WHY THIS EXISTS AS CODE INSTEAD OF A RUNBOOK
//
// RKE2 boots cert-only: `kube-dc bootstrap install` deliberately omits
// kube-apiserver-arg, because the authenticator does not exist yet (Flux brings
// it up later, in infra-core). Until the apiserver is pointed at it, EVERY
// Keycloak JWT returns 401 — tenant `kubectl`, the console's
// Manage-Organization calls, and the k8-manager / db-manager operators all
// fail. The cluster looks healthy: Flux is green, every pod is Ready, and
// nothing anywhere says "nobody can log in".
//
// So it was a mandatory manual step in the install guide (§3.5.1): SSH to each
// control-plane node, append two flags, restart rke2-server, one node at a
// time, gating on the apiserver coming back. That is fine for us and hostile to
// anyone installing kube-dc for the first time — and getting it half-done is
// worse than not starting, see PartialCutoverHazard below.
//
// The text transform lives here, separate from the SSH orchestration, because
// it is the part that can silently corrupt a working cluster.
package oidccutover

import (
	"fmt"
	"strings"
)

const (
	// WebhookKubeconfigPath is written on every control-plane node by the
	// authenticator's kubeconfig-writer init container (infra-core).
	WebhookKubeconfigPath = "/etc/rancher/oidc-webhook-kubeconfig.yaml"

	// RKE2ConfigPath is the file the apiserver flags live in.
	RKE2ConfigPath = "/etc/rancher/rke2/config.yaml"

	// BackupSuffix marks the pre-cutover snapshot. Restoring it plus a
	// restart is the documented rollback.
	BackupSuffix = ".pre-oidc"

	// apiserverArgKey is the RKE2 config key holding apiserver flags.
	apiserverArgKey = "kube-apiserver-arg"

	// webhookFlag points the apiserver at the authenticator. Without it the
	// apiserver has no way to validate a Keycloak token at all.
	webhookFlag = "authentication-token-webhook-config-file=" + WebhookKubeconfigPath

	// cacheTTLFlag bounds how long a positive authentication decision is
	// reused. Two minutes is what the fleet has run since 2026-05.
	cacheTTLFlag = "authentication-token-webhook-cache-ttl=2m"

	// legacyAuthConfigPrefix is the RETIRED structured-authn path
	// (/etc/rancher/auth-conf.yaml). It must be removed in the same edit:
	// leaving both configured means the apiserver keeps resolving tokens
	// through a file that no longer tracks the Organization CRs, so a tenant
	// created after the cutover authenticates on one node and 401s on the
	// next. See docs/internal/oidc-webhook-cloud-rollout.md.
	legacyAuthConfigPrefix = "authentication-config="
)

// PartialCutoverHazard explains why this must not be done node-by-node by hand.
//
// On a multi-master cluster `kubectl` load-balances across apiservers. If one
// node has the webhook flag and another does not, a tenant's JWT is accepted or
// rejected depending on which apiserver answered — an intermittent 401 that
// looks like a Keycloak problem, a clock problem, or a flaky network, and sends
// people debugging in the wrong place for hours. So the orchestration refuses to
// start unless it can reach EVERY control-plane node.
const PartialCutoverHazard = "a partially cut-over cluster returns intermittent 401s: " +
	"kubectl load-balances across apiservers, so a tenant JWT is accepted only by the nodes already wired"

// ManagedFlags are the flags this package owns, in the order it writes them.
func ManagedFlags() []string { return []string{webhookFlag, cacheTTLFlag} }

// IsCutOver reports whether the config already points the apiserver at the
// authenticator. Used for idempotence: a re-run must skip a finished node
// rather than restart its apiserver for nothing.
func IsCutOver(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(stripComment(line), webhookFlag) {
			return true
		}
	}
	return false
}

// PatchConfig returns the config with the webhook flags present exactly once
// and the retired structured-authn flag removed.
//
// It is a LINE transform rather than a YAML round-trip on purpose: this file is
// generated with substantial explanatory comments (kubelet reservations, the
// disabled-charts list, why cni is none), and a marshal/unmarshal cycle would
// drop every one of them.
//
// The hazard it exists to avoid is the runbook's `tee -a`: appending a second
// `kube-apiserver-arg:` block when the key is ALREADY present produces a
// duplicate YAML mapping key. RKE2 then honours one block and silently discards
// the other — so an operator who had added, say, audit-log flags loses them, or
// loses these, with no error anywhere. Instead the flags are merged INTO the
// existing block when there is one.
//
// changed is false when the file already says exactly this, so callers can skip
// the restart.
func PatchConfig(body string) (out string, changed bool, err error) {
	lines := strings.Split(body, "\n")

	blockStart, blockIndent, err := findAPIServerArgBlock(lines)
	if err != nil {
		return "", false, err
	}

	if blockStart < 0 {
		// No block, so there is nothing to drop the legacy flag from.
		patched := lines
		block := []string{apiserverArgKey + ":"}
		for _, flag := range ManagedFlags() {
			block = append(block, "  - "+flag)
		}
		patched = appendBlock(patched, block)
		return strings.Join(patched, "\n"), true, nil
	}

	blockEnd := endOfBlock(lines, blockStart, blockIndent)
	itemIndent := listItemIndent(lines, blockStart, blockEnd, blockIndent)

	patched, dropped := dropLegacyAuthConfig(lines, blockStart, blockIndent)
	// dropLegacyAuthConfig may have shortened the block.
	blockEnd -= dropped

	var missing []string
	for _, flag := range ManagedFlags() {
		if !blockHasFlag(patched, blockStart, blockEnd, flag) {
			missing = append(missing, flag)
		}
	}
	if len(missing) == 0 && dropped == 0 {
		return body, false, nil
	}

	insert := make([]string, 0, len(missing))
	for _, flag := range missing {
		insert = append(insert, itemIndent+"- "+flag)
	}
	patched = append(patched[:blockEnd:blockEnd], append(insert, patched[blockEnd:]...)...)
	return strings.Join(patched, "\n"), true, nil
}

// findAPIServerArgBlock locates a TOP-LEVEL kube-apiserver-arg key. Returns
// (-1, 0, nil) when absent.
//
// Only column-zero keys count. A nested `kube-apiserver-arg` (inside some other
// mapping) is not the apiserver's flag list, and treating it as one would inject
// flags into an unrelated structure. Two top-level occurrences mean the file is
// already broken by a previous blind append — refuse rather than make it worse.
func findAPIServerArgBlock(lines []string) (start, indent int, err error) {
	start = -1
	for i, line := range lines {
		trimmed := stripComment(line)
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		if leadingSpaces(trimmed) != 0 {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		key := unquote(strings.TrimSpace(parts[0]))
		if key != apiserverArgKey {
			continue
		}
		// Only a BLOCK sequence can be edited line-wise. An inline sequence
		// (`kube-apiserver-arg: [a=1]`), a flow mapping, or an anchor puts a
		// value on the key's own line — inserting `- ` items after that
		// produces YAML that does not parse, which would leave the node unable
		// to start. Refuse and say what to do; a wrong edit here costs a
		// control-plane node.
		if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
			return 0, 0, fmt.Errorf("oidccutover: %s line %d declares %q with an inline value (%s) — "+
				"only a block sequence can be edited safely. Convert it to a block list by hand:\n"+
				"  %s:\n    - <existing flag>\n"+
				"then re-run",
				RKE2ConfigPath, i+1, apiserverArgKey, strings.TrimSpace(parts[1]), apiserverArgKey)
		}
		if start >= 0 {
			return 0, 0, fmt.Errorf("oidccutover: %s declares %q twice (lines %d and %d) — "+
				"duplicate YAML keys mean RKE2 silently honours one block and discards the other. "+
				"Merge them by hand before re-running",
				RKE2ConfigPath, apiserverArgKey, start+1, i+1)
		}
		start = i
		indent = 0
	}
	return start, indent, nil
}

// endOfBlock returns the index one past the last line belonging to the block
// that starts at start.
//
// A sequence item at EXACTLY the key's indentation still belongs to the block.
// That is valid YAML and it is what RKE2 itself writes — verified on a live
// production control-plane node:
//
//	kube-apiserver-arg:
//	- authentication-token-webhook-config-file=/etc/rancher/oidc-webhook-kubeconfig.yaml
//	- authentication-token-webhook-cache-ttl=2m
//
// Treating `leadingSpaces <= indent` as the end of the block made every such
// file look like it had an EMPTY apiserver-arg list, so an already-wired node
// was reported as needing both flags and would have had them appended a second
// time. Caught by running the dry run against a real cluster; the shape never
// appears in a file this tool generated itself.
func endOfBlock(lines []string, start, indent int) int {
	for i := start + 1; i < len(lines); i++ {
		content := stripComment(lines[i])
		trimmed := strings.TrimSpace(content)
		if trimmed == "" {
			continue // blank/comment-only lines may sit inside a block
		}
		spaces := leadingSpaces(content)
		if spaces > indent {
			continue // nested under the key
		}
		if spaces == indent && strings.HasPrefix(trimmed, "- ") {
			continue // sequence flush with its parent key — still the block
		}
		return i
	}
	return len(lines)
}

// listItemIndent reuses the block's existing item indentation so the result
// matches the file's own style; falls back to two spaces.
func listItemIndent(lines []string, start, end, indent int) string {
	for i := start + 1; i < end; i++ {
		content := stripComment(lines[i])
		if strings.HasPrefix(strings.TrimSpace(content), "- ") {
			return strings.Repeat(" ", leadingSpaces(content))
		}
	}
	return strings.Repeat(" ", indent+2)
}

func blockHasFlag(lines []string, start, end int, flag string) bool {
	for i := start + 1; i < end && i < len(lines); i++ {
		item := strings.TrimSpace(stripComment(lines[i]))
		if !strings.HasPrefix(item, "- ") {
			continue
		}
		if unquote(strings.TrimSpace(strings.TrimPrefix(item, "- "))) == flag {
			return true
		}
	}
	return false
}

// dropLegacyAuthConfig removes any `- authentication-config=...` item from the
// apiserver block. Returns the new lines and how many were removed (so the
// caller can adjust its end index).
func dropLegacyAuthConfig(lines []string, blockStart, blockIndent int) ([]string, int) {
	if blockStart < 0 {
		return lines, 0
	}
	end := endOfBlock(lines, blockStart, blockIndent)
	out := make([]string, 0, len(lines))
	removed := 0
	for i, line := range lines {
		if i > blockStart && i < end {
			item := strings.TrimSpace(stripComment(line))
			if strings.HasPrefix(item, "- ") &&
				strings.HasPrefix(unquote(strings.TrimSpace(strings.TrimPrefix(item, "- "))), legacyAuthConfigPrefix) {
				removed++
				continue
			}
		}
		out = append(out, line)
	}
	return out, removed
}

// appendBlock adds a block at the end of the file, keeping exactly one trailing
// newline.
func appendBlock(lines, block []string) []string {
	out := append([]string{}, lines...)
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, block...)
	return append(out, "")
}

// stripComment removes a trailing `#` comment. Deliberately naive: RKE2 config
// values are flags and paths, not strings containing '#'. It exists so a
// commented-out flag is never mistaken for a live one — the case that would
// make IsCutOver report success on a node that is still cert-only.
func stripComment(line string) string {
	if i := strings.Index(line, "#"); i >= 0 {
		return line[:i]
	}
	return line
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t"))
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
