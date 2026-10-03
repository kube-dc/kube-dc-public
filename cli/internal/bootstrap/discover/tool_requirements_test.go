package discover

import (
	"context"
	"os/exec"
	"reflect"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func toolNames(reqs []ToolRequirement) []string {
	names := make([]string, len(reqs))
	for i, req := range reqs {
		names[i] = req.Name
	}
	return names
}

func TestToolRequirementsFollowOperation(t *testing.T) {
	for _, tc := range []struct {
		name string
		sel  ToolSelection
		want []string
	}{
		{"local-no-push", ToolSelection{Operation: ToolInit}, []string{"age", "age-keygen", "kubectl", "sops"}},
		{"github-new-repo", ToolSelection{Operation: ToolInit, Push: true, CreateRepo: true, Provider: "github", SSH: true}, []string{"age", "age-keygen", "flux", "gh", "git", "helm", "kubectl", "sops", "ssh"}},
		{"gitlab-new-repo", ToolSelection{Operation: ToolInit, Push: true, CreateRepo: true, Provider: "gitlab"}, []string{"age", "age-keygen", "flux", "git", "glab", "helm", "kubectl", "sops"}},
		{"existing-repo", ToolSelection{Operation: ToolInit, Push: true}, []string{"age", "age-keygen", "flux", "git", "helm", "kubectl", "sops"}},
		{"prepare", ToolSelection{Operation: ToolPrepare}, []string{"curl", "sudo"}},
		{"vendor", ToolSelection{Operation: ToolVendor}, []string{"clusterctl", "curl", "helm", "yq"}},
		{"vendor-capi", ToolSelection{Operation: ToolVendor, Component: "capi"}, []string{"clusterctl", "curl"}},
		{"vendor-crds", ToolSelection{Operation: ToolVendor, Component: "crds"}, []string{"helm", "yq"}},
		{"vendor-kyverno", ToolSelection{Operation: ToolVendor, Component: "kyverno"}, []string{"curl"}},
		{"render", ToolSelection{Operation: ToolRender}, []string{"kubectl"}},
		{"render-standalone", ToolSelection{Operation: ToolRender, StandaloneKustomize: true}, []string{"kustomize"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqs, err := ToolRequirements(tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			if got := toolNames(reqs); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tools = %v, want %v", got, tc.want)
			}
			probes, err := ToolProbesFor(tc.sel)
			if err != nil || len(probes) != len(reqs) {
				t.Fatalf("probes = %v, err = %v", probes, err)
			}
			for i, p := range probes {
				if p.Name() != reqs[i].Name {
					t.Fatalf("probe %d is %s, want %s", i, p.Name(), reqs[i].Name)
				}
			}
		})
	}
	for _, invalid := range []ToolSelection{
		{Operation: ToolInit, CreateRepo: true},
		{Operation: ToolInit, Push: true, CreateRepo: true, Provider: "unknown"},
		{Operation: "unknown"},
		{Operation: ToolVendor, Component: "typo"},
	} {
		if _, err := ToolRequirements(invalid); err == nil {
			t.Errorf("accepted invalid selection %+v", invalid)
		}
	}
	reqs, err := ToolRequirements(ToolSelection{Operation: ToolInit, Push: true, CreateRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range reqs {
		if (req.Name == "git" || req.Name == "gh") && req.AutoInstall {
			t.Errorf("Fleet script does not install %s", req.Name)
		}
		if (req.Name == "helm" || req.Name == "age-keygen") && !req.AutoInstall {
			t.Errorf("Fleet script installs %s", req.Name)
		}
	}
}

func TestSimpleVersionProbeReportsMissingAndVersion(t *testing.T) {
	fake := func(_ context.Context, name string, _ ...string) ([]byte, []byte, error) {
		if name == "absent" {
			return nil, nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return []byte("clusterctl version: GitVersion:\"v1.8.1\""), nil, nil
	}
	if r := newSimpleVersionProbe("clusterctl", fake, "version").Run(context.Background()); r.Status != ports.StatusInstalled || r.Version != "v1.8.1" {
		t.Fatalf("version probe = %+v", r)
	}
	if r := newSimpleVersionProbe("absent", fake, "version").Run(context.Background()); r.Status != ports.StatusMissing || r.Severity != ports.SeverityBlocker {
		t.Fatalf("missing probe = %+v", r)
	}
	oldYQ := func(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
		return []byte("yq version v3.4.1"), nil, nil
	}
	if r := newSimpleVersionProbe("yq", oldYQ, "--version").Run(context.Background()); r.Status != ports.StatusPartial {
		t.Fatalf("old yq version was accepted: %+v", r)
	}
}
