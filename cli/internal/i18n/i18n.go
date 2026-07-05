// Package i18n is the C3 localisation seam
// (installer-agentic-implementation-plan §14). v1 ships English only —
// the point of the package is that the SEAM exists: new user-visible
// strings can route through T() from day one, so adding a locale later
// is a catalog change, not a codebase-wide string hunt.
//
// Adoption policy (v1): NEW user-facing strings in new code SHOULD use
// T(); retrofitting the existing surface is explicitly out of scope
// (the churn would dwarf the benefit while en is the only locale).
// One exemplar call site lives in cmd/kube-dc/bootstrap_add_node.go.
//
// Contract:
//   - T(key, args...) looks the key up in the active catalog and
//     fmt.Sprintf-formats it with args.
//   - A MISSING key returns the key itself (with args appended when
//     given) — never panics, never returns empty. A visible raw key
//     in output is a bug report that writes itself; an empty string
//     is silent data loss.
//   - The active locale is en and not currently switchable; the
//     lookup indirection is the whole v1 deliverable.
package i18n

import "fmt"

// catalogEN is the v1 (and only) message catalog. Keys are
// dot-namespaced by command/screen.
var catalogEN = map[string]string{
	// Exemplar entries — see the adoption policy above.
	"addnode.v2_notice": "add-node is a v2 feature. For now, join workers with the fleet repo's\nenv-driven script (runs ON the target node, as root):",
	"addnode.verify":    "Verify with:\n\n  kubectl get nodes -w",
}

// T translates `key` in the active locale, formatting with `args`.
// Missing keys return the key (plus args) — loud, greppable, never
// empty.
func T(key string, args ...any) string {
	msg, ok := catalogEN[key]
	if !ok {
		if len(args) == 0 {
			return key
		}
		return fmt.Sprintf("%s %v", key, args)
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}
