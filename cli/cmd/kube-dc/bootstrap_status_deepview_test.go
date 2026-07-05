package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/discover"
)

func TestRenderHelmReleases(t *testing.T) {
	var out bytes.Buffer
	renderHelmReleases(&out, []discover.HelmReleaseStatus{
		{Name: "keycloak", Namespace: "keycloak", Ready: true, Revision: "25.1.0"},
		{Name: "cert-manager", Namespace: "cert-manager", Ready: false, Reason: "InstallFailed", Message: "timed out\nline2"},
		{Name: "kube-ovn", Namespace: "kube-system", Suspended: true},
	})
	s := out.String()
	if !strings.Contains(s, "HelmReleases (3, 1 not-ready)") {
		t.Errorf("header/count wrong:\n%s", s)
	}
	if !strings.Contains(s, "✓ keycloak/keycloak  Ready  rev=25.1.0") {
		t.Errorf("ready HR line wrong:\n%s", s)
	}
	if !strings.Contains(s, "✗ cert-manager/cert-manager  NotReady") || !strings.Contains(s, "timed out") {
		t.Errorf("notready HR line/message wrong:\n%s", s)
	}
	if strings.Contains(s, "line2") {
		t.Error("message should be trimmed to first line")
	}
	if !strings.Contains(s, "⏸ kube-system/kube-ovn  Suspended") {
		t.Errorf("suspended HR line wrong:\n%s", s)
	}
}

func TestRenderHelmReleases_EmptyIsNoOp(t *testing.T) {
	var out bytes.Buffer
	renderHelmReleases(&out, nil)
	if out.Len() != 0 {
		t.Errorf("empty HR list must print nothing, got %q", out.String())
	}
}

func TestRenderOpenBao(t *testing.T) {
	var out bytes.Buffer
	renderOpenBao(&out, &discover.OpenBaoStatus{
		ReadyPods: 2, TotalPods: 3, Finalized: true, AuthSetup: false,
		Pods: []discover.OpenBaoPod{
			{Name: "openbao-0", Ready: true},
			{Name: "openbao-1", Ready: true},
			{Name: "openbao-2", Ready: false},
		},
	})
	s := out.String()
	if !strings.Contains(s, "OpenBao: 2/3 pods Ready (partially sealed), bootstrap-finalized=true, controller-auth=false") {
		t.Errorf("summary wrong:\n%s", s)
	}
	if !strings.Contains(s, "✗ openbao-2") || !strings.Contains(s, "✓ openbao-0") {
		t.Errorf("per-pod lines wrong:\n%s", s)
	}
	if !strings.Contains(s, "openbao status` is authoritative") {
		t.Errorf("should note the authoritative command:\n%s", s)
	}
}

func TestRenderOpenBao_SealStates(t *testing.T) {
	cases := []struct {
		ready, total int
		want         string
	}{
		{3, 3, "unsealed"},
		{0, 3, "sealed/not-serving"},
		{1, 3, "partially sealed"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		renderOpenBao(&out, &discover.OpenBaoStatus{ReadyPods: c.ready, TotalPods: c.total})
		if !strings.Contains(out.String(), "("+c.want+")") {
			t.Errorf("%d/%d → want %q:\n%s", c.ready, c.total, c.want, out.String())
		}
	}
	// nil → no-op
	var out bytes.Buffer
	renderOpenBao(&out, nil)
	if out.Len() != 0 {
		t.Errorf("nil OpenBao must print nothing, got %q", out.String())
	}
}
