package clusterinit

import (
	"strings"
	"testing"
)

func ids(steps []Step) string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = string(s.ID)
	}
	return strings.Join(out, ",")
}

func TestInstallSteps_Conditionals(t *testing.T) {
	tests := []struct {
		name string
		in   InstallStepInputs
		want string
	}{
		{
			name: "full flow (new-repo, ssh, prereqs, finalize)",
			in: InstallStepInputs{
				NoInstallPrereqs: false,
				SSH:              true,
				NewRepoCreate:    true,
				NewRepoRemote:    true,
				NoPush:           false,
				Finalize:         true,
			},
			want: "prepare,install-prereqs,dns,kubevirt-eligibility,nat-probe,create-repo,configure-remote,scaffold,commit-push,flux-install,fetch-kubeconfig,reconcile,openbao-init,keycloak-oidc",
		},
		{
			name: "GPU flow tracks ownership operator HAMi and product readiness",
			in: InstallStepInputs{
				NoInstallPrereqs: true,
				Finalize:         true,
				GPUEnabled:       true,
				HAMiEnabled:      true,
			},
			want: "prepare,dns,kubevirt-eligibility,scaffold,commit-push,flux-install,reconcile,gpu-inventory,gpu-operator,gpu-hami,gpu-product,openbao-init,keycloak-oidc",
		},
		{
			name: "no-push: no flux-install, no create-repo, no finalize",
			in: InstallStepInputs{
				SSH:           true,
				NewRepoCreate: false,
				NoPush:        true,
				Finalize:      false,
			},
			want: "prepare,install-prereqs,dns,kubevirt-eligibility,nat-probe,scaffold,commit-push,fetch-kubeconfig",
		},
		{
			name: "no ssh, no prereqs, adopt gate",
			in: InstallStepInputs{
				NoInstallPrereqs: true,
				Adopt:            true,
				SSH:              false,
				NewRepoCreate:    false,
				NoPush:           false,
				Finalize:         true,
			},
			want: "prepare,dns,kubevirt-eligibility,adopt-gate,scaffold,commit-push,flux-install,reconcile,openbao-init,keycloak-oidc",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(InstallSteps(tc.in))
			if got != tc.want {
				t.Errorf("InstallSteps mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestGPUInstallStepIDs(t *testing.T) {
	if got := strings.Join(stepIDStrings(GPUInstallStepIDs(false)), ","); got != "gpu-inventory,gpu-operator,gpu-product" {
		t.Fatalf("without HAMi: %s", got)
	}
	if got := strings.Join(stepIDStrings(GPUInstallStepIDs(true)), ","); got != "gpu-inventory,gpu-operator,gpu-hami,gpu-product" {
		t.Fatalf("with HAMi: %s", got)
	}
}

func stepIDStrings(ids []StepID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// TestNopReporter_IsInert guards that the default (plain/CI) reporter is
// a genuine no-op — the whole point of the reporter split is that
// --no-tty output stays byte-for-byte unchanged.
func TestNopReporter_IsInert(t *testing.T) {
	var r StepReporter = NopReporter{}
	// Must not panic / must not require any wiring.
	r.Plan([]Step{{ID: StepScaffold, Title: "x"}})
	r.Start(StepScaffold)
	r.Done(StepScaffold, nil)
	r.Skip(StepFluxInstall, "because")

	if got := reporterOrNop(nil); got == nil {
		t.Fatal("reporterOrNop(nil) must return a usable reporter, got nil")
	}
}
