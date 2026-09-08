package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/alerts"
)

func amStub(t *testing.T, h http.HandlerFunc) *alerts.AlertmanagerClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return alerts.NewAlertmanagerClient(srv.URL)
}

// A silence with no reason is indistinguishable from a mistake when someone
// finds it three weeks later, so the comment is mandatory rather than a
// convention.
func TestBuildSilence_RequiresAComment(t *testing.T) {
	c := amStub(t, func(w http.ResponseWriter, r *http.Request) {})
	_, err := buildSilence(context.Background(), c, silenceCmdOpts{
		matchers: []string{"alertname=X"}, duration: time.Hour,
	}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "--comment is required") {
		t.Fatalf("want a comment-required error, got %v", err)
	}
}

// --fingerprint and --matcher are two different intents ("this one alert" vs
// "anything matching"). Silently merging them would produce a silence the
// operator did not describe.
func TestBuildSilence_RefusesBothTargetingModes(t *testing.T) {
	c := amStub(t, func(w http.ResponseWriter, r *http.Request) {})
	_, err := buildSilence(context.Background(), c, silenceCmdOpts{
		fingerprint: "abc", matchers: []string{"alertname=X"},
		duration: time.Hour, comment: "x",
	}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("want a mutually-exclusive error, got %v", err)
	}
}

// A fingerprint is resolved to the alert's real labels, because Alertmanager
// derives fingerprints FROM labels and will not accept one as a matcher.
func TestBuildSilence_FingerprintExpandsToTheAlertsLabels(t *testing.T) {
	c := amStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"fingerprint": "fp-1",
			"labels":      map[string]string{"alertname": "DiskFull", "node": "worker-3"},
			"status":      map[string]string{"state": "active"},
		}})
	})
	s, err := buildSilence(context.Background(), c, silenceCmdOpts{
		fingerprint: "fp-1", duration: time.Hour, comment: "replacing disk",
	}, time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(s.Matchers) != 2 {
		t.Fatalf("silence must carry the alert's full label set, got %+v", s.Matchers)
	}
	got := map[string]string{}
	for _, m := range s.Matchers {
		got[m.Name] = m.Value
	}
	if got["alertname"] != "DiskFull" || got["node"] != "worker-3" {
		t.Fatalf("matchers do not reflect the alert's labels: %+v", s.Matchers)
	}
}

func TestBuildSilence_UnknownFingerprintIsAnError(t *testing.T) {
	c := amStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	})
	_, err := buildSilence(context.Background(), c, silenceCmdOpts{
		fingerprint: "nope", duration: time.Hour, comment: "x",
	}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "no firing alert") {
		t.Fatalf("want a not-found error naming the remedy, got %v", err)
	}
}

func TestBuildSilence_RejectsNonPositiveDuration(t *testing.T) {
	c := amStub(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, d := range []time.Duration{0, -time.Minute} {
		if _, err := buildSilence(context.Background(), c, silenceCmdOpts{
			matchers: []string{"a=b"}, duration: d, comment: "x",
		}, time.Now()); err == nil {
			t.Errorf("duration %s must be rejected", d)
		}
	}
}

// createdBy is the only trace of who suppressed an alert, so it must never be
// empty even when the OS cannot tell us.
func TestCurrentAuthor_NeverEmpty(t *testing.T) {
	if got := currentAuthor("alice"); got != "alice" {
		t.Errorf("explicit author must win, got %q", got)
	}
	if got := currentAuthor("   "); got == "" {
		t.Error("author must never resolve to empty")
	}
}

// ack is a silence — the difference is a shorter default, so an acknowledgement
// expires and the alert returns if the work outlasts it.
func TestAckDefaultIsShorterThanSilence(t *testing.T) {
	if !(defaultAckDuration < defaultSilenceDuration) {
		t.Fatalf("ack default (%s) must be shorter than a silence default (%s)", defaultAckDuration, defaultSilenceDuration)
	}
}

// REGRESSION: silence and ack must not share option storage. pflag writes a
// flag's default into the bound variable at BIND time, so a shared struct let
// ack's 1h default overwrite silence's 2h — silence advertised 2h in its help
// and then actually silenced for 1h. This asserts the value each command
// RECEIVES, not the help string, so the bug cannot come back unseen.
func TestSilenceAndAckDoNotShareDurationDefaults(t *testing.T) {
	sub := map[string]*cobra.Command{}
	for _, c := range alertsCmd().Commands() {
		sub[c.Name()] = c
	}
	for name, want := range map[string]time.Duration{
		"silence": defaultSilenceDuration,
		"ack":     defaultAckDuration,
	} {
		c, ok := sub[name]
		if !ok {
			t.Fatalf("`alerts %s` missing", name)
		}
		got, err := c.Flags().GetDuration("duration")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Errorf("`alerts %s` --duration resolves to %s, want %s (shared option storage?)", name, got, want)
		}
	}
}

// ack takes --fingerprint only. Acknowledging by label selector would suppress
// a CLASS of current and future alerts, which is a silence, not an ack.
func TestAckRequiresAFingerprint(t *testing.T) {
	var sub *cobra.Command
	for _, c := range alertsCmd().Commands() {
		if c.Name() == "ack" {
			sub = c
		}
	}
	if sub == nil {
		t.Fatal("ack missing")
	}
	if sub.Flags().Lookup("matcher") != nil {
		t.Error("ack must not offer --matcher; that is `alerts silence`")
	}
	// Drive the PARENT: cobra's Execute() on a child re-enters the root, which
	// would run the alerts TUI with the test binary's own args.
	parent := alertsCmd()
	var out bytes.Buffer
	parent.SetOut(&out)
	parent.SetErr(&out)
	parent.SetArgs([]string{"ack", "--comment", "x"})
	if err := parent.Execute(); err == nil || !strings.Contains(err.Error(), "--fingerprint") {
		t.Fatalf("ack without a fingerprint must be refused, got %v", err)
	}
}

// Control characters in wire-supplied strings must not corrupt the table or
// move the cursor.
func TestSanitizeForTable(t *testing.T) {
	got := sanitizeForTable("ok\tcol\nnew\x1b[2Jclear")
	for _, bad := range []string{"\t", "\n", "\x1b"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived sanitization: %q", bad, got)
		}
	}
}

// The suppression subcommands must actually be reachable from `alerts` — this
// fails if the wiring call is removed.
func TestAlertsCommandExposesSuppressionSubcommands(t *testing.T) {
	cmd := alertsCmd()
	want := map[string]bool{"silence": false, "ack": false, "silences": false, "unsilence": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("`alerts %s` is not registered", name)
		}
	}
}

// Listing tells the operator plainly when there is nothing rather than printing
// an empty table, and points at --all so an expired silence can still be found.
func TestRunListSilences_EmptyIsExplained(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]alerts.Silence{})
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := runListSilences(context.Background(), &out, silenceCmdOpts{url: srv.URL, portForward: false}, false); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "--all") {
		t.Errorf("empty output should mention --all; got %q", out.String())
	}
}

// Active-only is the default view; --all widens it.
func TestRunListSilences_FiltersExpiredByDefault(t *testing.T) {
	now := time.Now()
	body := []map[string]any{
		{"id": "active-1", "endsAt": now.Add(time.Hour), "matchers": []map[string]any{{"name": "a", "value": "b", "isEqual": true}}, "status": map[string]string{"state": "active"}},
		{"id": "expired-1", "endsAt": now.Add(-time.Hour), "matchers": []map[string]any{{"name": "a", "value": "b", "isEqual": true}}, "status": map[string]string{"state": "expired"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := runListSilences(context.Background(), &out, silenceCmdOpts{url: srv.URL}, false); err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out.String(), "expired-1") {
		t.Errorf("expired silences must be hidden by default; got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "active-1") {
		t.Errorf("active silence missing:\n%s", out.String())
	}

	out.Reset()
	if err := runListSilences(context.Background(), &out, silenceCmdOpts{url: srv.URL}, true); err != nil {
		t.Fatalf("list --all: %v", err)
	}
	if !strings.Contains(out.String(), "expired-1") {
		t.Errorf("--all must include expired silences; got:\n%s", out.String())
	}
}

func TestRunUnsilence_ReportsThatAlertsResume(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	var out bytes.Buffer
	if err := runUnsilence(context.Background(), &out, silenceCmdOpts{url: srv.URL}, "sil-9"); err != nil {
		t.Fatalf("unsilence: %v", err)
	}
	// Deliberately NOT "will page again": another silence, an inhibition rule,
	// a missing receiver or the alert having resolved can each still stop a
	// page, so the command promises only what it actually did.
	if !strings.Contains(out.String(), "no longer suppresses") {
		t.Errorf("output should state only that this silence is gone; got %q", out.String())
	}
	if strings.Contains(out.String(), "will page again") {
		t.Errorf("output must not promise paging it cannot guarantee; got %q", out.String())
	}
}

// A pending silence is shown by default. It is about to start suppressing, so
// hiding it is how someone concludes a cluster is quiet when it is not.
func TestRunListSilences_ShowsPendingByDefault(t *testing.T) {
	now := time.Now()
	body := []map[string]any{
		{"id": "pending-1", "startsAt": now.Add(10 * time.Minute), "endsAt": now.Add(time.Hour),
			"matchers": []map[string]any{{"name": "a", "value": "b", "isEqual": true}},
			"status":   map[string]string{"state": "pending"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := runListSilences(context.Background(), &out, silenceCmdOpts{url: srv.URL}, false); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "pending-1") {
		t.Errorf("pending silences must be visible by default:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "starts in") {
		t.Errorf("a pending row should say when it starts:\n%s", out.String())
	}
}

// A negative matcher created by another client must not display as its
// opposite.
func TestRunListSilences_RendersNegativeMatchers(t *testing.T) {
	now := time.Now()
	body := []map[string]any{{
		"id": "s1", "startsAt": now, "endsAt": now.Add(time.Hour),
		"matchers": []map[string]any{{"name": "severity", "value": "critical", "isEqual": false, "isRegex": false}},
		"status":   map[string]string{"state": "active"},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := runListSilences(context.Background(), &out, silenceCmdOpts{url: srv.URL}, false); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "severity!=critical") {
		t.Errorf("negative matcher rendered wrongly:\n%s", out.String())
	}
}
