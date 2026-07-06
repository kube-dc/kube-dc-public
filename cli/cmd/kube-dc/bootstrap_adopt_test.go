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
		"clusters/acme/",
		"advisory only",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q:\n%s", want, s)
		}
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

func TestBootstrapAdopt_HasKubeconfigFlag(t *testing.T) {
	repo := ""
	cmd := bootstrapAdoptCmd(&repo)
	if cmd.Flags().Lookup("kubeconfig") == nil {
		t.Error("adopt should expose --kubeconfig")
	}
}
