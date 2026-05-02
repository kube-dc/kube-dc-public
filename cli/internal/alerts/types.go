package alerts

import (
	"fmt"
	"sort"
	"time"
)

// Alert represents an alert from Alertmanager
type Alert struct {
	Fingerprint  string            `json:"fingerprint"`
	AlertName    string            `json:"-"` // extracted from Labels
	Severity     string            `json:"-"` // extracted from Labels
	State        string            `json:"-"` // populated from status.state
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	GeneratorURL string            `json:"generatorURL"`
}

// Age returns the time elapsed since the alert started
func (a Alert) Age() string {
	d := time.Since(a.StartsAt)
	if d.Hours() > 24 {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return d.Round(time.Minute).String()
}

func SeverityPriority(sev string) int {
	switch sev {
	case "critical":
		return 4
	case "warning":
		return 3
	case "info":
		return 2
	case "none":
		return 1
	default:
		return 0
	}
}

// SortAlerts sorts alerts by severity (critical first) then by name
func SortAlerts(alertList []Alert) {
	sort.Slice(alertList, func(i, j int) bool {
		pi := SeverityPriority(alertList[i].Severity)
		pj := SeverityPriority(alertList[j].Severity)
		if pi != pj {
			return pi > pj
		}
		// Secondary sort: source / alert name
		if alertList[i].AlertName != alertList[j].AlertName {
			return alertList[i].AlertName < alertList[j].AlertName
		}
		return alertList[i].StartsAt.After(alertList[j].StartsAt) // newest first
	})
}
