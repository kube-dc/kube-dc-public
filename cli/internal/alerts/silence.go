package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Silences are how Alertmanager expresses "I know, stop paging me" — there is
// no separate acknowledgement concept in Alertmanager, so `alerts ack` is a
// silence with an operator-supplied comment and a short default duration.
// Modelling ack as a distinct thing would have meant inventing state to store
// somewhere; a silence is already durable, already visible in the web UI, and
// already expires on its own.

// Matcher is one label condition of a silence.
type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	// IsEqual is a *bool on the wire because the v2 contract defaults it to
	// TRUE when absent. Decoding into a plain bool would turn every silence
	// created by another client — which usually omits the field — into a
	// negative matcher, i.e. display the opposite of what it does.
	IsEqual bool `json:"isEqual"`
}

// UnmarshalJSON applies the contract's `isEqual: true` default.
func (m *Matcher) UnmarshalJSON(b []byte) error {
	var raw struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		IsRegex bool   `json:"isRegex"`
		IsEqual *bool  `json:"isEqual"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Name, m.Value, m.IsRegex = raw.Name, raw.Value, raw.IsRegex
	m.IsEqual = raw.IsEqual == nil || *raw.IsEqual
	return nil
}

// Operator renders the matcher the way Alertmanager means it. isEqual and
// isRegex are INDEPENDENT flags with four combinations, not a single regex
// switch: reading only isRegex displays `severity!="critical"` as
// `severity=critical` — the exact opposite condition — to anyone auditing a
// silence somebody else created.
func (m Matcher) Operator() string {
	switch {
	case m.IsEqual && m.IsRegex:
		return "=~"
	case m.IsEqual:
		return "="
	case m.IsRegex:
		return "!~"
	default:
		return "!="
	}
}

// String renders one matcher as Alertmanager would express it.
func (m Matcher) String() string { return m.Name + m.Operator() + m.Value }

// Silence is Alertmanager's v2 silence as we need it.
type Silence struct {
	ID        string    `json:"id,omitempty"`
	Matchers  []Matcher `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
	Status    struct {
		State string `json:"state"`
	} `json:"status,omitempty"`
}

// Alertmanager has THREE silence states. Treating anything non-active as
// expired hides a scheduled silence that is about to start suppressing — the
// one an operator most needs to see before deciding a cluster is quiet.
const (
	SilenceActive  = "active"
	SilencePending = "pending"
	SilenceExpired = "expired"
)

// Active reports whether the silence is currently suppressing anything.
func (s Silence) Active() bool { return s.Status.State == SilenceActive }

// Pending reports a silence that has not started yet but will.
func (s Silence) Pending() bool { return s.Status.State == SilencePending }

// Live reports a silence that suppresses now or is scheduled to.
func (s Silence) Live() bool { return s.Active() || s.Pending() }

// StartsIn renders how long until a pending silence takes effect.
func (s Silence) StartsIn() string {
	d := time.Until(s.StartsAt)
	if d <= 0 {
		return "now"
	}
	return humanizeDuration(d)
}

// Remaining renders how long an active silence still has to run.
func (s Silence) Remaining() string {
	d := time.Until(s.EndsAt)
	if d <= 0 {
		return "expired"
	}
	return humanizeDuration(d)
}

// MatchersForAlert builds the matcher set that silences exactly one alert
// instance and nothing else.
//
// The fingerprint is deliberately NOT used: Alertmanager computes it from the
// label set and does not accept it as a matcher, so a silence has to be
// expressed in the labels that produced it. Every label is matched for
// equality, which keeps the silence as narrow as the alert itself — a silence
// on `alertname` alone would also swallow the same alert firing for a
// different tenant, namespace or node, which is how a silence intended for one
// noisy target quietly hides a real incident somewhere else.
func MatchersForAlert(a Alert) []Matcher {
	names := make([]string, 0, len(a.Labels))
	for k := range a.Labels {
		names = append(names, k)
	}
	sort.Strings(names) // deterministic: silences are compared and logged
	ms := make([]Matcher, 0, len(names))
	for _, k := range names {
		ms = append(ms, Matcher{Name: k, Value: a.Labels[k], IsEqual: true})
	}
	return ms
}

// MatchersFromLabelSelectors parses `key=value` / `key=~regex` pairs.
func MatchersFromLabelSelectors(sel []string) ([]Matcher, error) {
	ms := make([]Matcher, 0, len(sel))
	for _, raw := range sel {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if k, v, ok := strings.Cut(s, "=~"); ok {
			if strings.TrimSpace(k) == "" {
				return nil, fmt.Errorf("matcher %q: empty label name", raw)
			}
			ms = append(ms, Matcher{Name: strings.TrimSpace(k), Value: v, IsRegex: true, IsEqual: true})
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("matcher %q: want key=value or key=~regex", raw)
		}
		ms = append(ms, Matcher{Name: strings.TrimSpace(k), Value: v, IsEqual: true})
	}
	if len(ms) == 0 {
		// Alertmanager itself rejects this (the v2 contract sets minItems: 1,
		// and it additionally requires at least one matcher that does not
		// match the empty string). We refuse locally anyway so the operator
		// gets the reason in their own terms rather than a 400, and so a
		// forgotten argument never becomes a round trip that might succeed
		// against some other implementation.
		return nil, fmt.Errorf("no matchers given — a silence with no matchers would suppress every alert in the cluster; pass --matcher key=value (repeatable) or a fingerprint")
	}
	return ms, nil
}

// postableSilence is the POST shape. The GET shape carries `status`, which is
// not part of postableSilence — and a Go struct is never "empty" for
// encoding/json, so reusing one type would send `"status":{"state":""}` on
// every create.
type postableSilence struct {
	Matchers  []Matcher `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
}

// ErrSilenceOutcomeUnknown marks the case where the request may have reached
// Alertmanager but we cannot say whether it committed. Retrying blindly can
// create a duplicate silence, so the caller must reconcile instead.
var ErrSilenceOutcomeUnknown = errors.New("silence outcome unknown")

// CreateSilence posts a silence and returns its ID.
func (c *AlertmanagerClient) CreateSilence(ctx context.Context, s Silence) (string, error) {
	if len(s.Matchers) == 0 {
		return "", fmt.Errorf("refusing to create a silence with no matchers (it would suppress every alert)")
	}
	if !s.EndsAt.After(s.StartsAt) {
		return "", fmt.Errorf("silence ends at or before it starts (%s → %s)", s.StartsAt.Format(time.RFC3339), s.EndsAt.Format(time.RFC3339))
	}
	body, err := json.Marshal(postableSilence{
		Matchers:  s.Matchers,
		StartsAt:  s.StartsAt,
		EndsAt:    s.EndsAt,
		CreatedBy: s.CreatedBy,
		Comment:   s.Comment,
	})
	if err != nil {
		return "", fmt.Errorf("encode silence: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/silences", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create silence request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// The request may have been transmitted and committed before the
		// connection died (a port-forward dropping mid-call does exactly
		// this). Saying "failed" would invite a retry that silences twice.
		return "", fmt.Errorf("%w: create silence: %v — the silence may already exist; check `kube-dc alerts silences` before retrying", ErrSilenceOutcomeUnknown, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return "", fmt.Errorf("alertmanager rejected the silence (status %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		SilenceID string `json:"silenceID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("%w: alertmanager accepted the silence but its response could not be read (%v) — it is probably active; find it with `kube-dc alerts silences`", ErrSilenceOutcomeUnknown, err)
	}
	if strings.TrimSpace(out.SilenceID) == "" {
		// A silence with no id cannot be expired by the operator, so an
		// empty one is a failure even though the status said 200.
		return "", fmt.Errorf("%w: alertmanager returned no silence id — the silence may be active but cannot be addressed; find it with `kube-dc alerts silences`", ErrSilenceOutcomeUnknown)
	}
	return out.SilenceID, nil
}

// ListSilences returns silences, newest-expiring last.
func (c *AlertmanagerClient) ListSilences(ctx context.Context) ([]Silence, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/silences", nil)
	if err != nil {
		return nil, fmt.Errorf("list silences request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list silences: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("alertmanager returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out []Silence
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode silences: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EndsAt.Before(out[j].EndsAt) })
	return out, nil
}

// ExpireSilence ends a silence now (Alertmanager's DELETE is an expire, not a
// delete — the record stays visible, which is what you want for an audit).
func (c *AlertmanagerClient) ExpireSilence(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("empty silence id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/api/v2/silence/"+url.PathEscape(id), nil)
	if err != nil {
		return fmt.Errorf("expire silence request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("expire silence: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("alertmanager returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// FindAlertByFingerprint locates one alert so a silence can be built from its
// real label set.
func FindAlertByFingerprint(list []Alert, fingerprint string) (Alert, error) {
	fp := strings.TrimSpace(fingerprint)
	if fp == "" {
		return Alert{}, fmt.Errorf("empty fingerprint")
	}
	for _, a := range list {
		if a.Fingerprint == fp {
			return a, nil
		}
	}
	return Alert{}, fmt.Errorf("no firing alert with fingerprint %q — list them with `kube-dc alerts --output json`", fp)
}

// humanizeDuration renders a countdown by CEILING, so a silence created for
// "2d" never reads as "1d" the instant it is made (48h minus a few
// nanoseconds), and five minutes left never reads as six.
func humanizeDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", ceilDiv(int64(d), int64(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", ceilDiv(int64(d), int64(time.Hour)))
	default:
		return fmt.Sprintf("%dm", ceilDiv(int64(d), int64(time.Minute)))
	}
}

func ceilDiv(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	q := a / b
	if a%b != 0 {
		q++
	}
	return q
}
