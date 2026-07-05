package discover

import "testing"

func TestParseHelmReleases(t *testing.T) {
	// Ready, NotReady (with message), Suspended — out of order to prove sort.
	body := []byte(`{"items":[
	 {"metadata":{"name":"keycloak","namespace":"keycloak"},"spec":{"suspend":false},
	  "status":{"lastAppliedRevision":"25.1.0","conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded"}]}},
	 {"metadata":{"name":"cert-manager","namespace":"cert-manager"},"spec":{"suspend":false},
	  "status":{"lastAppliedRevision":"v1.15.0","conditions":[{"type":"Ready","status":"False","reason":"InstallFailed","message":"timed out\nsecond line"}]}},
	 {"metadata":{"name":"kube-ovn","namespace":"kube-system"},"spec":{"suspend":true},"status":{}}
	]}`)
	got := parseHelmReleases(body)
	if len(got) != 3 {
		t.Fatalf("want 3 HRs, got %d", len(got))
	}
	// Sorted by namespace then name: cert-manager, keycloak, kube-system.
	if got[0].Namespace != "cert-manager" || got[1].Namespace != "keycloak" || got[2].Namespace != "kube-system" {
		t.Errorf("not sorted by ns: %+v", got)
	}
	cm := got[0]
	if cm.Ready || cm.Reason != "InstallFailed" || cm.Message == "" {
		t.Errorf("cert-manager should be NotReady with reason+message: %+v", cm)
	}
	kc := got[1]
	if !kc.Ready || kc.Revision != "25.1.0" {
		t.Errorf("keycloak should be Ready rev 25.1.0: %+v", kc)
	}
	ko := got[2]
	if !ko.Suspended || ko.Ready {
		t.Errorf("kube-ovn should be Suspended (no Ready cond → not ready): %+v", ko)
	}
}

func TestParseHelmReleases_BadJSON(t *testing.T) {
	if parseHelmReleases([]byte("not json")) != nil {
		t.Error("bad JSON must yield nil, not a partial list")
	}
}

func TestParseOpenBaoPods(t *testing.T) {
	// Two statefulset replicas (one Ready, one sealed=NotReady), a
	// snapshot Job pod (excluded), and an unrelated pod (excluded).
	body := []byte(`{"items":[
	 {"metadata":{"name":"openbao-1"},"status":{"conditions":[{"type":"Ready","status":"False"}]}},
	 {"metadata":{"name":"openbao-0"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
	 {"metadata":{"name":"openbao-snapshot-29663520-27pqh"},"status":{"conditions":[{"type":"Ready","status":"False"}]}},
	 {"metadata":{"name":"some-operator-xyz"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}
	]}`)
	got := parseOpenBaoPods(body)
	if len(got) != 2 {
		t.Fatalf("want 2 statefulset pods (snapshot + unrelated excluded), got %d: %+v", len(got), got)
	}
	// Sorted by name: openbao-0 (ready), openbao-1 (not).
	if got[0].Name != "openbao-0" || !got[0].Ready {
		t.Errorf("openbao-0 should be ready: %+v", got[0])
	}
	if got[1].Name != "openbao-1" || got[1].Ready {
		t.Errorf("openbao-1 should be NotReady (sealed): %+v", got[1])
	}
}

func TestParseServiceAnnotations(t *testing.T) {
	body := []byte(`{"metadata":{"annotations":{"kube-dc.com/openbao-bootstrap-finalized":"2026-07-05T00:00:00Z","other":"x"}}}`)
	ann := parseServiceAnnotations(body)
	if ann["kube-dc.com/openbao-bootstrap-finalized"] == "" {
		t.Errorf("annotation not parsed: %v", ann)
	}
}

// envStub satisfies the GetOr interface DefaultExpectedTags takes.
type envStub map[string]string

func (e envStub) GetOr(k, fallback string) string {
	if v, ok := e[k]; ok {
		return v
	}
	return fallback
}

func TestDefaultExpectedTags_StripsInlineComments(t *testing.T) {
	// cluster-config.env keeps inline comments in the value; the expected
	// tag must be just the tag, not the whole rationale comment (otherwise
	// `bootstrap status` drift output is unreadable — the live-cloud bug).
	env := envStub{
		"KUBE_DC_MANAGER_TAG": "v0.3.88   # 2026-06-25 release — secrets + billing-quota fix line",
		"DB_MANAGER_TAG":      "v0.1.11\t# tab-separated comment",
		"KUBE_DC_BACKEND_TAG": "v0.3.83", // no comment
	}
	tags := DefaultExpectedTags(env)
	if got := tags["kube-dc/kube-dc-manager"].Tag; got != "v0.3.88" {
		t.Errorf("manager tag = %q, want v0.3.88 (comment stripped)", got)
	}
	if got := tags["kube-dc/db-manager"].Tag; got != "v0.1.11" {
		t.Errorf("db-manager tag = %q, want v0.1.11 (tab comment stripped)", got)
	}
	if got := tags["kube-dc/kube-dc-backend"].Tag; got != "v0.3.83" {
		t.Errorf("backend tag = %q, want v0.3.83", got)
	}
}

func TestFirstField(t *testing.T) {
	cases := map[string]string{
		"v1.2.3   # c": "v1.2.3",
		"  v1  ":       "v1",
		"":             "",
		"   ":          "",
		"a b c":        "a",
	}
	for in, want := range cases {
		if got := firstField(in); got != want {
			t.Errorf("firstField(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAllDigits(t *testing.T) {
	for _, ok := range []string{"0", "12", "007"} {
		if !isAllDigits(ok) {
			t.Errorf("%q should be all-digits", ok)
		}
	}
	for _, no := range []string{"", "1a", "snapshot", "-1"} {
		if isAllDigits(no) {
			t.Errorf("%q should NOT be all-digits", no)
		}
	}
}
