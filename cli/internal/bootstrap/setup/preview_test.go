package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func previewStage(p Preview, id string) (StagePreview, bool) {
	for _, stage := range p.Stages {
		if stage.ID == id {
			return stage, true
		}
	}
	return StagePreview{}, false
}

func TestPreviewBindsEffectsWithoutClaimingReadiness(t *testing.T) {
	s, dir := fixture(t)
	s.Hosts[0].Primary = true
	s.Hosts = append(s.Hosts, Host{ID: "worker-1", SSHAlias: "admin@worker-1", Role: "agent", ManagementAddress: "192.0.2.11"})
	caPath := filepath.Join(dir, "private-location.pem")
	writeTestCA(t, caPath)
	s.CredentialRefs = map[string]string{"trusted-ca-bundle": "file:" + caPath}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Operation != "plan" || p.State != "planned" || p.ReadyToApply || len(p.Unresolved) == 0 || len(p.PlanHash) != 64 {
		t.Fatalf("preview claimed readiness or omitted review fields: %+v", p)
	}
	for _, id := range []string{"rke2-first-server", "rke2-join-worker-1", "fleet-starter", "ingress-nodes", "scaffold", "verify-containers"} {
		if _, ok := previewStage(p, id); !ok {
			t.Errorf("missing stage %q", id)
		}
	}
	first, _ := previewStage(p, "rke2-first-server")
	if first.Target != "server-1" || first.Condition != "if the live target is a new cluster" {
		t.Fatalf("auto-mode host stage = %+v", first)
	}
	adopt, ok := previewStage(p, "adopt-gate")
	if !ok || adopt.Condition != "if the live target is adopted" {
		t.Fatalf("auto-mode adopt gate = %+v, present=%v", adopt, ok)
	}
	starter, ok := previewStage(p, "fleet-starter")
	if !ok || !hasScope(starter.Scopes, "git-local") || !strings.Contains(starter.Condition, "Git checkout needs repair") {
		t.Fatalf("starter repair effects are missing: %+v, present=%v", starter, ok)
	}
	for _, id := range []string{"break-glass", "openbao-init", "keycloak-oidc"} {
		stage, ok := previewStage(p, id)
		if !ok || !hasScope(stage.Scopes, "fleet") || !hasScope(stage.Scopes, "git-provider") || !stage.PotentialWrite {
			t.Errorf("finalization publish effects are missing for %s: %+v, present=%v", id, stage, ok)
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-location") || strings.Contains(string(encoded), s.Release.RecordFile) {
		t.Fatal("preview disclosed a credential or local release path")
	}

	s.Hosts[0], s.Hosts[1] = s.Hosts[1], s.Hosts[0]
	s.Verification.Capabilities[0], s.Verification.Capabilities[1] = s.Verification.Capabilities[1], s.Verification.Capabilities[0]
	c2, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := BuildPreview(c2)
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanHash != p2.PlanHash {
		t.Fatalf("host/capability ordering changed plan hash: %s != %s", p.PlanHash, p2.PlanHash)
	}
	s.Verification.Capabilities = append(s.Verification.Capabilities, "virtual-machines")
	c3, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	p3, err := BuildPreview(c3)
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanHash == p3.PlanHash {
		t.Fatal("verification scope did not change plan hash")
	}
}

func TestPreviewNoPushAndCompiledDrift(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Execution = Execution{NoPush: true, NoInstallPrereqs: true}
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := previewStage(p, "flux-install"); ok {
		t.Fatal("no-push preview includes Flux bootstrap")
	}
	if _, ok := previewStage(p, "install-prereqs"); ok {
		t.Fatal("no-install-prereqs preview includes package installation")
	}
	if stage, ok := previewStage(p, "commit-push"); !ok || stage.Title != "Commit (local only)" {
		t.Fatalf("local commit stage = %+v, present=%v", stage, ok)
	} else if hasScope(stage.Scopes, "git-provider") {
		t.Fatalf("no-push preview includes a remote Git write: %+v", stage)
	}
	c.Spec.Hosts[0].SSHAlias = "changed-host"
	if _, err := BuildPreview(c); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed compiled input was accepted: %v", err)
	}

	s.Execution.NoPush = false
	if err := os.WriteFile(s.Release.RecordFile, []byte(`{"version":"v2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c2, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := BuildPreview(c2)
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanHash == p2.PlanHash || p.ReleaseSHA256 == p2.ReleaseSHA256 {
		t.Fatal("release/effects change did not change plan identity")
	}
}

func TestPreviewJoinsServersBeforeWorkers(t *testing.T) {
	s, _ := fixture(t)
	s.Target.Intent = clusterinit.ModeInstall
	s.Hosts[0].Primary = true
	s.Hosts = append(s.Hosts,
		Host{ID: "a-worker", SSHAlias: "admin@worker", Role: "agent", ManagementAddress: "192.0.2.12"},
		Host{ID: "z-server", SSHAlias: "admin@server", Role: "server", ManagementAddress: "192.0.2.13"},
	)
	c, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildPreview(c)
	if err != nil {
		t.Fatal(err)
	}
	indices := map[string]int{}
	for i, stage := range p.Stages {
		indices[stage.ID] = i
	}
	ordered := []string{"prepare", "fleet-starter", "install-prereqs", "rke2-first-server", "rke2-join-z-server", "rke2-join-a-worker", "fetch-kubeconfig", "dns"}
	for i := 1; i < len(ordered); i++ {
		prev, prevOK := indices[ordered[i-1]]
		next, nextOK := indices[ordered[i]]
		if !prevOK || !nextOK || prev >= next {
			t.Fatalf("guided setup stage order is wrong near %s and %s: %+v", ordered[i-1], ordered[i], p.Stages)
		}
	}
}

func TestPreviewRawDiskCheckOnlyForNewCluster(t *testing.T) {
	for _, intent := range []clusterinit.Mode{clusterinit.ModeInstall, clusterinit.ModeAuto, clusterinit.ModeAdopt, clusterinit.ModeResume} {
		t.Run(string(intent), func(t *testing.T) {
			s, _ := fixture(t)
			s.Target.Intent = intent
			s.Hosts[0].Disk = "/dev/sdb"
			data := strings.Replace(baseConfig("demo"), "OBJECT_STORAGE_MODE=disabled", "OBJECT_STORAGE_MODE=rook-ceph-local", 1)
			if err := os.WriteFile(s.Platform.ConfigFile, []byte(data+"KUBE_DC_INIT_NODE_NICS=server-1=eth0\nCEPH_LOCAL_OSD_SIZE_GB=100\n"), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Compile(s)
			if err != nil {
				t.Fatal(err)
			}
			p, err := BuildPreview(c)
			if err != nil {
				t.Fatal(err)
			}
			stage, present := previewStage(p, string(clusterinit.StepStorageDev))
			if intent == clusterinit.ModeInstall && (!present || stage.Condition != "") {
				t.Fatalf("fresh install needs raw disk check: %+v, present=%t", stage, present)
			}
			if intent == clusterinit.ModeAuto && (!present || stage.Condition == "") {
				t.Fatalf("auto intent needs conditional raw disk check: %+v, present=%t", stage, present)
			}
			if (intent == clusterinit.ModeAdopt || intent == clusterinit.ModeResume) && present {
				t.Fatalf("existing OSD incorrectly has empty-disk stage: %+v", stage)
			}
		})
	}
}

func TestPreviewClassifiesGPUProductAsReadOnlyWatch(t *testing.T) {
	scopes, writes, err := previewStepEffect(clusterinit.StepGPUProduct)
	if err != nil || writes || !hasScope(scopes, "cluster") {
		t.Fatalf("GPU product reconcile watch must be read-only: scopes=%v writes=%v err=%v", scopes, writes, err)
	}
}
