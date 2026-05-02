package alerts

import "strings"

// FilterSpec is a declarative filter applied against a list of alerts.
// Empty / "all" fields are ignored.
type FilterSpec struct {
	Severity  string // "", "all", "critical", …
	State     string // "", "all", "active", "suppressed"
	Namespace string
	Source    string // substring against job/namespace/alertname
	Search    string // case-insensitive substring across all labels/annotations
}

// ApplyFilter returns a new slice containing only alerts matching the spec.
func ApplyFilter(all []Alert, f FilterSpec) []Alert {
	sev := strings.ToLower(f.Severity)
	state := strings.ToLower(f.State)
	search := strings.ToLower(strings.TrimSpace(f.Search))
	source := strings.ToLower(strings.TrimSpace(f.Source))

	out := make([]Alert, 0, len(all))
	for _, a := range all {
		if sev != "" && sev != "all" && a.Severity != sev {
			continue
		}
		if state != "" && state != "all" && a.State != state {
			continue
		}
		if f.Namespace != "" && f.Namespace != "All" && a.Labels["namespace"] != f.Namespace {
			continue
		}
		if source != "" {
			src := a.Labels["job"]
			if src == "" {
				src = a.Labels["namespace"]
			}
			if src == "" {
				src = a.AlertName
			}
			if !strings.Contains(strings.ToLower(src), source) {
				continue
			}
		}
		if search != "" && !matchesSearch(a, search) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func matchesSearch(a Alert, needle string) bool {
	if strings.Contains(strings.ToLower(a.AlertName), needle) {
		return true
	}
	for k, v := range a.Labels {
		if strings.Contains(strings.ToLower(k+"="+v), needle) {
			return true
		}
	}
	for _, v := range a.Annotations {
		if strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}
