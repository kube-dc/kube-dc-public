package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adopt"
)

func TestRenderAdopt_Greenfield(t *testing.T) {
	var out bytes.Buffer
	renderAdopt(&out, &adopt.Result{}, "acme")
	s := out.String()
	if !strings.Contains(s, "greenfield") || !strings.Contains(s, "bootstrap init") {
		t.Errorf("greenfield report wrong:\n%s", s)
	}
}

func TestRenderAdopt_WithFindings(t *testing.T) {
	var out bytes.Buffer
	renderAdopt(&out, &adopt.Result{
		FluxInstalled: true,
		Findings: []adopt.Finding{
			{
				Component: adopt.Component{Name: "cert-manager", FleetPath: "infrastructure/cert-manager"},
				Via:       "CRD certificates.cert-manager.io",
				Recommend: adopt.DecisionAdopt,
			},
			{
				Component: adopt.Component{Name: "ingress-nginx", FleetPath: "(none)", Note: "kube-dc uses envoy-gateway"},
				Via:       "namespace ingress-nginx",
				Recommend: adopt.DecisionAdopt,
			},
		},
	}, "acme")
	s := out.String()
	for _, want := range []string{
		"2 pre-existing component(s)",
		"Flux is already installed",
		"cert-manager  (detected via CRD certificates.cert-manager.io)",
		"infrastructure/cert-manager",
		"ingress-nginx",
		"kube-dc uses envoy-gateway",
		"recommended: adopt",
		"take the component over IN PLACE",     // corrected semantics
		"--pin-versions --yes",                 // points at the mutation
		"clusters/acme/<layer>",                // SKIP path uses the cluster name
	} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q:\n%s", want, s)
		}
	}
	// The old wrong "exclude from kube-dc's Flux" framing must be gone.
	if strings.Contains(s, "exclude from kube-dc's Flux") {
		t.Errorf("stale 'exclude' wording still present:\n%s", s)
	}
}

func TestRenderPinPlan(t *testing.T) {
	var out bytes.Buffer
	renderPinPlan(&out, "acme", &adopt.PinResult{
		Pins: []adopt.PinChange{
			{Component: "cert-manager", VersionKey: "CERT_MANAGER_VERSION", Current: "v1.20.1", Live: "v1.14.4"},
			{Component: "kube-ovn (CNI)", VersionKey: "KUBE_OVN_VERSION", Current: "", Live: "v1.15.0"},
			{Component: "kubevirt", VersionKey: "KUBEVIRT_VERSION", Current: "", Live: "v1.8.1", Manual: true},
		},
		AlreadyPinned: []string{"keycloak (KEYCLOAK_VERSION=24.3.0)"},
		Skipped:       []string{"ingress-nginx"},
		Undetected:    []string{"rook-ceph"},
		UnusedManual:  []string{"BOGUS_VERSION"},
	})
	s := out.String()
	for _, want := range []string{
		"adopt --pin-versions — acme (3 pin(s))",
		"~ CERT_MANAGER_VERSION: v1.20.1 → v1.14.4   (cert-manager, pin to LIVE)",
		"~ KUBE_OVN_VERSION: (unset) → v1.15.0", // unset current rendered
		"~ KUBEVIRT_VERSION: (unset) → v1.8.1   (kubevirt, pin to --manual-pin value)",
		"already pinned: keycloak",
		"skipped (--skip-component): ingress-nginx",
		"✗ rook-ceph: live version not readable",
		"--manual-pin BOGUS_VERSION matched no detected component",
		"adopts each in place",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("pin plan missing %q:\n%s", want, s)
		}
	}
}

func TestParseAdoptPinOpts(t *testing.T) {
	opts, err := parseAdoptPinOpts([]string{"ingress-nginx", "cdi"}, []string{"KUBEVIRT_VERSION=v1.8.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Skip["ingress-nginx"] || !opts.Skip["cdi"] {
		t.Errorf("skip set wrong: %v", opts.Skip)
	}
	if opts.Manual["KUBEVIRT_VERSION"] != "v1.8.1" {
		t.Errorf("manual pin wrong: %v", opts.Manual)
	}
	// A malformed --manual-pin (non-SCREAMING_SNAKE key) is rejected.
	if _, err := parseAdoptPinOpts(nil, []string{"lower=x"}); err == nil {
		t.Error("malformed --manual-pin should error")
	}
}

func TestBootstrapAdopt_RejectsTwoArgs(t *testing.T) {
	repo := ""
	cmd := bootstrapAdoptCmd(&repo)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"a", "b"})
	if err := cmd.Execute(); err == nil {
		t.Error("adopt should reject two positional args (MaximumNArgs 1)")
	}
}

func TestBootstrapAdopt_HasFlags(t *testing.T) {
	repo := ""
	cmd := bootstrapAdoptCmd(&repo)
	for _, f := range []string{"kubeconfig", "pin-versions", "yes", "no-push", "github-token", "provider"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("adopt missing --%s", f)
		}
	}
}

func TestBootstrapAdopt_RejectsBadProvider(t *testing.T) {
	// Provider is validated before any session build (no kubeconfig needed).
	repo := ""
	cmd := bootstrapAdoptCmd(&repo)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"acme", "--provider", "bitbucket"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "provider must be github or gitlab") {
		t.Errorf("bad --provider should be rejected, got %v", err)
	}
}

func TestBootstrapAdopt_PinVersionsRequiresCluster(t *testing.T) {
	// A mock session makes NewSession succeed without a real kubeconfig,
	// so we reach the --pin-versions cluster-required check.
	t.Setenv("KUBE_DC_MOCK", "cloud")
	repo := ""
	cmd := bootstrapAdoptCmd(&repo)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--pin-versions"}) // no cluster arg
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "<cluster> arg is required") {
		t.Errorf("--pin-versions without a cluster should error, got %v", err)
	}
}
