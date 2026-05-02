package alerts

import (
	"hash/fnv"
	"sort"

	"github.com/charmbracelet/lipgloss"
)

// Severity constants (lower-case, matching Prometheus convention).
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
	SeverityNone     = "none"
)

// AlertState constants (Alertmanager v2 statuses).
const (
	StateActive      = "active"
	StateSuppressed  = "suppressed" // silenced or inhibited
	StateUnprocessed = "unprocessed"
)

// SeverityColor returns a lipgloss ANSI color string for a severity label.
func SeverityColor(sev string) lipgloss.Color {
	switch sev {
	case SeverityCritical:
		return lipgloss.Color("#F2495C") // Grafana red
	case SeverityWarning:
		return lipgloss.Color("#FF9830") // Grafana orange
	case SeverityInfo:
		return lipgloss.Color("#5794F2") // Grafana blue
	case SeverityNone:
		return lipgloss.Color("#8E8E8E")
	default:
		return lipgloss.Color("#8E8E8E")
	}
}

// SeverityIcon returns an iconic glyph for a severity.
func SeverityIcon(sev string) string {
	switch sev {
	case SeverityCritical:
		return "●"
	case SeverityWarning:
		return "●"
	case SeverityInfo:
		return "●"
	case SeverityNone:
		return "○"
	default:
		return "○"
	}
}

// labelPalette is a stable, readable set of pastel-like colors used to
// pick a consistent background color for a label key. Keeps output
// deterministic across renders (no map-iteration randomness).
var labelPalette = []lipgloss.Color{
	lipgloss.Color("#5F86B9"), // steel blue
	lipgloss.Color("#7E5CAD"), // amethyst
	lipgloss.Color("#2F9E72"), // viridian
	lipgloss.Color("#C9853B"), // amber
	lipgloss.Color("#AA5486"), // plum
	lipgloss.Color("#3A8FB7"), // teal
	lipgloss.Color("#8F7A3B"), // olive
	lipgloss.Color("#4F7F5C"), // forest
	lipgloss.Color("#B36A5E"), // terracotta
	lipgloss.Color("#6B6FB2"), // periwinkle
	lipgloss.Color("#A66D4E"), // sienna
	lipgloss.Color("#56909C"), // slate
}

// ColorForKey returns a stable background color for a given label key
// by hashing the key into the palette.
func ColorForKey(key string) lipgloss.Color {
	// Special cases keep the most meaningful keys visually aligned with
	// Grafana's color choices.
	switch key {
	case "severity":
		return lipgloss.Color("#2F9E72")
	case "alertname":
		return lipgloss.Color("#B2473E")
	case "namespace":
		return lipgloss.Color("#5F86B9")
	case "job":
		return lipgloss.Color("#7E5CAD")
	case "prometheus":
		return lipgloss.Color("#3A8FB7")
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return labelPalette[int(h.Sum32())%len(labelPalette)]
}

// SortedKeys returns the keys of a map in a stable, human-friendly order:
// well-known keys first, then the rest alphabetically.
func SortedKeys(m map[string]string) []string {
	priority := map[string]int{
		"alertname":  0,
		"severity":   1,
		"namespace":  2,
		"job":        3,
		"pod":        4,
		"container":  5,
		"instance":   6,
		"prometheus": 7,
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, oi := priority[keys[i]]
		pj, oj := priority[keys[j]]
		if oi && oj {
			return pi < pj
		}
		if oi {
			return true
		}
		if oj {
			return false
		}
		return keys[i] < keys[j]
	})
	return keys
}
