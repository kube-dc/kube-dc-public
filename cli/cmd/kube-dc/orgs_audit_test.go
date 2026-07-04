// Pure-function tests for the helpers under `kube-dc orgs` and
// `kube-dc audit`. The cobra commands themselves are exercised by
// live smoke against the cloud backend; this file pins the parsing
// contract for context-name resolution, kubeconfig prefix splitting,
// and audit query string assembly. None of these talk to kubeconfig
// or the backend at test time.

package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/backend"
)

func TestSplitKubeDC(t *testing.T) {
	cases := map[string][]string{
		"kube-dc/cloud.example/shalb/docs": {"cloud.example", "shalb", "docs"},
		"kube-dc/kube-dc.cloud/admin":      {"kube-dc.cloud", "admin"},
		"kube-dc/single":                   {"single"},
		"":                                 nil,
		"prod-cluster":                     nil, // missing prefix
		"kube-dc/":                         nil,
		"kube-dc//empty-segment":           {"", "empty-segment"},
	}
	for in, want := range cases {
		got := splitKubeDC(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("splitKubeDC(%q) = %#v; want %#v", in, got, want)
		}
	}
}

// realmFromContext lives in secrets.go but is the canonical source
// for org-name extraction; re-test for the org-detect cases here too
// so a regression in either flow is obvious from the failing test
// name (TestRealmFromContext_OrgDetect is paired with TestProjectFromContextName).
func TestRealmFromContext_OrgDetect(t *testing.T) {
	// Tenant context → realm is the org name.
	if got := realmFromContext("kube-dc/cloud.example/shalb/docs"); got != "shalb" {
		t.Errorf("tenant realm = %q; want shalb", got)
	}
	// Admin context → master realm.
	if got := realmFromContext("kube-dc/cloud.example/admin"); got != "master" {
		t.Errorf("admin realm = %q; want master", got)
	}
	// Non-kube-dc → empty (orgFromContextOrFlag falls back to --org).
	if got := realmFromContext("docker-desktop"); got != "" {
		t.Errorf("non-kube-dc realm = %q; want empty", got)
	}
}

func TestAuditQuery_QueryString_OmitsZeroFields(t *testing.T) {
	q := backend.AuditQuery{}
	if got := q.QueryString(); got != "" {
		t.Errorf("empty query = %q; want \"\"", got)
	}
}

// Reusable test wrapper: the helper is unexported on the package, so
// we go through the JSON shape it produces in the request URL.
// (We re-test queryString here by re-encoding into a typed map to
// avoid coupling to url.Values key ordering.)
func TestAuditQuery_QueryString_AllFields(t *testing.T) {
	q := backend.AuditQuery{
		Service: "secrets",
		Actor:   "voa@shalb.com",
		Result:  "denied",
		Since:   "2026-05-21T13:00:00Z",
		Until:   "2026-05-21T14:00:00Z",
		Limit:   100,
	}
	got := q.QueryString()
	if !strings.HasPrefix(got, "?") {
		t.Fatalf("missing leading '?': %q", got)
	}
	// We don't pin key order — url.Values.Encode() sorts alphabetically
	// but that's an implementation detail. Assert each pair is present.
	for _, want := range []string{
		"service=secrets",
		"actor=voa%40shalb.com",
		"result=denied",
		"since=2026-05-21T13%3A00%3A00Z",
		"until=2026-05-21T14%3A00%3A00Z",
		"limit=100",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("query = %q; missing %q", got, want)
		}
	}
}

func TestAuditQuery_QueryString_PartialFilters(t *testing.T) {
	q := backend.AuditQuery{Service: "secrets", Limit: 50}
	got := q.QueryString()
	if !strings.Contains(got, "service=secrets") || !strings.Contains(got, "limit=50") {
		t.Errorf("query = %q; expected service+limit only", got)
	}
	if strings.Contains(got, "actor=") || strings.Contains(got, "result=") {
		t.Errorf("query = %q; should NOT include empty filters", got)
	}
}

func TestFormatEpochNs(t *testing.T) {
	if got := backend.FormatEpochNs(""); got != "" {
		t.Errorf("empty -> %q", got)
	}
	// Backend already sends ISO 8601 in `ts` (loki.js); the helper
	// is the secondary path for raw ns. An ISO-shaped input is
	// returned verbatim because it doesn't parse as int64.
	if got := backend.FormatEpochNs("2026-05-21T17:16:24.796Z"); got != "2026-05-21T17:16:24.796Z" {
		t.Errorf("ISO 8601 should pass through, got %q", got)
	}
	if got := backend.FormatEpochNs("garbage"); got != "garbage" {
		t.Errorf("malformed should pass through, got %q", got)
	}
	// Raw epoch-ns → RFC3339Nano (reviewer P2: prior seconds.ns
	// shape didn't match the "human-readable" docstring).
	// 1700000000123456789 ns == 2023-11-14T22:13:20.123456789Z UTC.
	if got := backend.FormatEpochNs("1700000000123456789"); got != "2023-11-14T22:13:20.123456789Z" {
		t.Errorf("epoch ns format = %q; want 2023-11-14T22:13:20.123456789Z", got)
	}
	// Sub-second epoch keeps the nanosecond precision visible.
	if got := backend.FormatEpochNs("1700000000000000001"); got != "2023-11-14T22:13:20.000000001Z" {
		t.Errorf("sub-second epoch = %q; want 2023-11-14T22:13:20.000000001Z", got)
	}
}

func TestStringField(t *testing.T) {
	m := map[string]any{
		"service":      "secrets",
		"missing-type": 42,
		"nullish":      nil,
	}
	if got := stringField(m, "service"); got != "secrets" {
		t.Errorf("present string = %q", got)
	}
	if got := stringField(m, "absent"); got != "" {
		t.Errorf("missing key should return empty, got %q", got)
	}
	if got := stringField(m, "missing-type"); got != "" {
		t.Errorf("non-string value should return empty, got %q", got)
	}
	if got := stringField(m, "nullish"); got != "" {
		t.Errorf("nil value should return empty, got %q", got)
	}
	if got := stringField(nil, "x"); got != "" {
		t.Errorf("nil map should return empty, got %q", got)
	}
}
