package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A silence is only as safe as its matchers. Silencing on alertname alone also
// swallows the same alert firing for a different tenant, namespace or node —
// which is how a silence meant for one noisy target quietly hides a real
// incident somewhere else. So a per-alert silence matches the FULL label set.
func TestMatchersForAlert_MatchesTheWholeLabelSet(t *testing.T) {
	a := Alert{Labels: map[string]string{
		"alertname": "KubeDCManagerDown",
		"namespace": "kube-dc",
		"severity":  "critical",
	}}
	ms := MatchersForAlert(a)
	if len(ms) != 3 {
		t.Fatalf("got %d matchers, want one per label: %+v", len(ms), ms)
	}
	// Deterministic order — silences get compared and logged.
	if ms[0].Name != "alertname" || ms[1].Name != "namespace" || ms[2].Name != "severity" {
		t.Fatalf("matchers are not in stable label order: %+v", ms)
	}
	for _, m := range ms {
		if m.IsRegex {
			t.Errorf("per-alert matchers must be exact, not regex: %+v", m)
		}
		if !m.IsEqual {
			t.Errorf("per-alert matchers must be equality: %+v", m)
		}
	}
}

func TestMatchersFromLabelSelectors(t *testing.T) {
	ms, err := MatchersFromLabelSelectors([]string{"severity=critical", "namespace=~kube-.*", "  "})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("blank entries should be skipped, got %+v", ms)
	}
	if ms[0].IsRegex {
		t.Error("severity=critical must be an exact matcher")
	}
	if !ms[1].IsRegex || ms[1].Value != "kube-.*" {
		t.Errorf("=~ must produce a regex matcher, got %+v", ms[1])
	}
}

// The dangerous default: Alertmanager happily accepts a silence with no
// matchers and it suppresses EVERYTHING. Forgetting an argument must not be
// able to do that.
func TestMatchersFromLabelSelectors_RefusesEmpty(t *testing.T) {
	for _, in := range [][]string{nil, {}, {""}, {"   "}} {
		if _, err := MatchersFromLabelSelectors(in); err == nil {
			t.Fatalf("input %q must be refused — it would silence every alert", in)
		} else if !strings.Contains(err.Error(), "every alert") {
			t.Errorf("refusal should explain the blast radius, got: %v", err)
		}
	}
}

func TestMatchersFromLabelSelectors_RejectsMalformed(t *testing.T) {
	for _, in := range []string{"noequals", "=novalue", "=~alsobad"} {
		if _, err := MatchersFromLabelSelectors([]string{in}); err == nil {
			t.Errorf("matcher %q should be rejected", in)
		}
	}
}

func TestCreateSilence_PostsAndReturnsID(t *testing.T) {
	var got Silence
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/silences" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]string{"silenceID": "sil-123"})
	}))
	defer srv.Close()

	id, err := NewAlertmanagerClient(srv.URL).CreateSilence(context.Background(), Silence{
		Matchers:  []Matcher{{Name: "alertname", Value: "X", IsEqual: true}},
		StartsAt:  time.Now(),
		EndsAt:    time.Now().Add(time.Hour),
		CreatedBy: "ops",
		Comment:   "investigating",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "sil-123" {
		t.Fatalf("id = %q, want sil-123", id)
	}
	if got.Comment != "investigating" || got.CreatedBy != "ops" {
		t.Errorf("author and comment must reach Alertmanager: %+v", got)
	}
}

// Client-side guards fire before any request is made.
func TestCreateSilence_LocalGuards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made when the silence is locally invalid")
	}))
	defer srv.Close()
	c := NewAlertmanagerClient(srv.URL)
	now := time.Now()

	if _, err := c.CreateSilence(context.Background(), Silence{StartsAt: now, EndsAt: now.Add(time.Hour)}); err == nil {
		t.Error("a matcher-less silence must be refused")
	}
	if _, err := c.CreateSilence(context.Background(), Silence{
		Matchers: []Matcher{{Name: "a", Value: "b", IsEqual: true}},
		StartsAt: now, EndsAt: now.Add(-time.Minute),
	}); err == nil {
		t.Error("a silence ending before it starts must be refused")
	}
}

func TestCreateSilence_SurfacesServerRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid matcher"))
	}))
	defer srv.Close()
	_, err := NewAlertmanagerClient(srv.URL).CreateSilence(context.Background(), Silence{
		Matchers: []Matcher{{Name: "a", Value: "b", IsEqual: true}},
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid matcher") {
		t.Fatalf("server rejection must be surfaced verbatim, got: %v", err)
	}
}

func TestListSilences_SortedByExpiry(t *testing.T) {
	now := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Silence{
			{ID: "late", EndsAt: now.Add(4 * time.Hour)},
			{ID: "soon", EndsAt: now.Add(time.Hour)},
		})
	}))
	defer srv.Close()
	got, err := NewAlertmanagerClient(srv.URL).ListSilences(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != "soon" {
		t.Fatalf("silences must be ordered by expiry, got %+v", got)
	}
}

func TestExpireSilence(t *testing.T) {
	var rawURI, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RequestURI is the on-the-wire form. r.URL.Path is already decoded,
		// so asserting on it would pass even with no escaping at all.
		rawURI, method = r.RequestURI, r.Method
	}))
	defer srv.Close()
	if err := NewAlertmanagerClient(srv.URL).ExpireSilence(context.Background(), "sil 1/2"); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", method)
	}
	// The id must be path-escaped, or an id containing a slash would address
	// the wrong endpoint entirely.
	if !strings.Contains(rawURI, "sil%201%2F2") {
		t.Errorf("silence id must be path-escaped on the wire, got %q", rawURI)
	}
	if err := NewAlertmanagerClient(srv.URL).ExpireSilence(context.Background(), "  "); err == nil {
		t.Error("empty id must be refused")
	}
}

func TestFindAlertByFingerprint(t *testing.T) {
	list := []Alert{{Fingerprint: "aaa"}, {Fingerprint: "bbb"}}
	if a, err := FindAlertByFingerprint(list, "bbb"); err != nil || a.Fingerprint != "bbb" {
		t.Fatalf("lookup failed: %v %+v", err, a)
	}
	if _, err := FindAlertByFingerprint(list, "zzz"); err == nil {
		t.Error("unknown fingerprint must error")
	}
	if _, err := FindAlertByFingerprint(list, ""); err == nil {
		t.Error("empty fingerprint must error")
	}
}

func TestSilenceRemaining(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		end  time.Time
		want string
	}{
		{now.Add(-time.Minute), "expired"},
		{now.Add(48 * time.Hour), "2d"},
		{now.Add(3 * time.Hour), "3h"},
	} {
		if got := (Silence{EndsAt: tc.end}).Remaining(); got != tc.want {
			t.Errorf("Remaining() = %q, want %q", got, tc.want)
		}
	}
}

// All FOUR matcher combinations. Reading only isRegex renders `severity!=x` as
// `severity=x` — the exact opposite condition — to anyone auditing a silence
// somebody else created.
func TestMatcherOperator_AllFourCombinations(t *testing.T) {
	for _, tc := range []struct {
		isEqual, isRegex bool
		want             string
	}{
		{true, false, "="},
		{true, true, "=~"},
		{false, false, "!="},
		{false, true, "!~"},
	} {
		m := Matcher{Name: "severity", Value: "critical", IsEqual: tc.isEqual, IsRegex: tc.isRegex}
		if got := m.Operator(); got != tc.want {
			t.Errorf("isEqual=%v isRegex=%v → %q, want %q", tc.isEqual, tc.isRegex, got, tc.want)
		}
		if want := "severity" + tc.want + "critical"; m.String() != want {
			t.Errorf("String() = %q, want %q", m.String(), want)
		}
	}
}

// The v2 contract defaults isEqual to TRUE when absent, and most clients omit
// it. Decoding into a plain bool would turn every externally-created silence
// into a negative matcher — displaying the opposite of what it does.
func TestMatcherUnmarshal_IsEqualDefaultsToTrue(t *testing.T) {
	var m Matcher
	if err := json.Unmarshal([]byte(`{"name":"a","value":"b","isRegex":false}`), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !m.IsEqual {
		t.Fatal("absent isEqual must default to true")
	}
	if err := json.Unmarshal([]byte(`{"name":"a","value":"b","isEqual":false}`), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.IsEqual {
		t.Fatal("explicit isEqual:false must be honoured")
	}
}

// A pending silence is not an expired one: it is about to start suppressing,
// which is exactly what someone deciding "is this cluster quiet?" must see.
func TestSilenceStates(t *testing.T) {
	pending := Silence{}
	pending.Status.State = SilencePending
	active := Silence{}
	active.Status.State = SilenceActive
	expired := Silence{}
	expired.Status.State = SilenceExpired

	if !pending.Pending() || pending.Active() || !pending.Live() {
		t.Error("pending must be Live but not Active")
	}
	if !active.Live() || !active.Active() {
		t.Error("active must be Live")
	}
	if expired.Live() {
		t.Error("expired must not be Live")
	}
}

// POST must not carry `status`: it is not part of postableSilence, and a Go
// struct is never "empty" for encoding/json, so a shared type would send it.
func TestCreateSilence_DoesNotPostStatus(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_ = json.NewEncoder(w).Encode(map[string]string{"silenceID": "s1"})
	}))
	defer srv.Close()
	if _, err := NewAlertmanagerClient(srv.URL).CreateSilence(context.Background(), Silence{
		Matchers: []Matcher{{Name: "a", Value: "b", IsEqual: true}},
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, present := raw["status"]; present {
		t.Errorf("POST body must not include status: %v", raw)
	}
}

// A 200 with no id leaves a silence that cannot be expired by the operator.
func TestCreateSilence_EmptyIDIsAnError(t *testing.T) {
	for _, body := range []string{`{}`, `{"silenceID":""}`, `{"silenceID":"   "}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := NewAlertmanagerClient(srv.URL).CreateSilence(context.Background(), Silence{
			Matchers: []Matcher{{Name: "a", Value: "b", IsEqual: true}},
			StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour),
		})
		srv.Close()
		if err == nil {
			t.Fatalf("body %s: an id-less silence must be reported as a failure", body)
		}
		if !errors.Is(err, ErrSilenceOutcomeUnknown) {
			t.Errorf("body %s: want ErrSilenceOutcomeUnknown, got %v", body, err)
		}
	}
}

// Transport failure after the request may have been sent is ambiguous, not a
// clean failure: a blind retry could silence twice.
func TestCreateSilence_AmbiguousOutcomeIsFlagged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // refuse the connection

	_, err := NewAlertmanagerClient(srv.URL).CreateSilence(context.Background(), Silence{
		Matchers: []Matcher{{Name: "a", Value: "b", IsEqual: true}},
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrSilenceOutcomeUnknown) {
		t.Fatalf("want ErrSilenceOutcomeUnknown, got %v", err)
	}
	if !strings.Contains(err.Error(), "before retrying") {
		t.Errorf("the operator must be warned off a blind retry; got %v", err)
	}
}

// Countdowns round UP. A silence created for 2d must never read "1d" the
// instant it is made, and 5 minutes left must not read "6m".
func TestHumanizedDurationsCeil(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		end  time.Time
		want string
	}{
		{now.Add(48*time.Hour - time.Millisecond), "2d"},
		{now.Add(3*time.Hour - time.Millisecond), "3h"},
		{now.Add(5 * time.Minute), "5m"},
		{now.Add(-time.Second), "expired"},
	} {
		if got := (Silence{EndsAt: tc.end}).Remaining(); got != tc.want {
			t.Errorf("Remaining() = %q, want %q", got, tc.want)
		}
	}
}
