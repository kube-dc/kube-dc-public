package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type AlertmanagerClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewAlertmanagerClient(url string) *AlertmanagerClient {
	return &AlertmanagerClient{
		baseURL: url,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetAlerts fetches alerts from Alertmanager v2 API
func (c *AlertmanagerClient) GetAlerts(ctx context.Context) ([]Alert, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v2/alerts", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch alerts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("alertmanager returned status %d: %s", resp.StatusCode, string(body))
	}

	var rawAlerts []struct {
		Fingerprint string            `json:"fingerprint"`
		StartsAt    time.Time         `json:"startsAt"`
		EndsAt      time.Time         `json:"endsAt"`
		UpdatedAt   time.Time         `json:"updatedAt"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		Status      struct {
			State string `json:"state"`
		} `json:"status"`
		GeneratorURL string `json:"generatorURL"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rawAlerts); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	alerts := make([]Alert, 0, len(rawAlerts))
	for _, ra := range rawAlerts {
		a := Alert{
			Fingerprint:  ra.Fingerprint,
			AlertName:    ra.Labels["alertname"],
			Severity:     ra.Labels["severity"],
			State:        ra.Status.State,
			StartsAt:     ra.StartsAt,
			EndsAt:       ra.EndsAt,
			UpdatedAt:    ra.UpdatedAt,
			Labels:       ra.Labels,
			Annotations:  ra.Annotations,
			GeneratorURL: ra.GeneratorURL,
		}
		if a.Severity == "" {
			a.Severity = "none"
		}
		alerts = append(alerts, a)
	}

	return alerts, nil
}
