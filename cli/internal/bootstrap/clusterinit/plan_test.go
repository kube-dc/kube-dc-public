package clusterinit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helpers ---------------------------------------------------------

// withConsentDir redirects the package-level consentDir() to a tmpdir
// so consent-marker tests don't pollute the operator's home.
func withConsentDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := consentDir
	consentDir = func() string { return dir }
	t.Cleanup(func() { consentDir = old })
	return dir
}

func atlantisOpts() *InitOptions {
	o := validBase()
	o.NodeNICs = map[string]string{
		"HOST5-A": "enp1s0",
		"HOST6-A": "enp1s0",
		"HOST7-A": "enp1s0",
	}
	o.Sets = map[string]string{
		"EXT_NET_VLAN_ID":   "1103",
		"EXT_NET_INTERFACE": "bond0",
	}
	return &o
}

func atlantisFleet() FleetState {
	return FleetState{
		PriorClusters:   []string{"cloud", "stage", "eu/dc1"},
		SOPSRecipients:  3,
		PlatformVersion: "v0.4.0",
	}
}

// determinism + hash equality -----------------------------------

func TestBuildPlan_Deterministic(t *testing.T) {
	o := atlantisOpts()
	fleet := atlantisFleet()

	p1, err := BuildPlan(o, fleet)
	if err != nil {
		t.Fatalf("first BuildPlan: %v", err)
	}
	p2, err := BuildPlan(o, fleet)
	if err != nil {
		t.Fatalf("second BuildPlan: %v", err)
	}

	// GeneratedAt is the one non-deterministic field — strip it
	// before comparison.
	p1.GeneratedAt = p2.GeneratedAt
	b1, _ := canonicalJSON(p1)
	b2, _ := canonicalJSON(p2)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("plan not deterministic:\nFIRST:\n%s\nSECOND:\n%s", b1, b2)
	}

	// Hashes must agree across runs.
	if p1.InputHash != p2.InputHash {
		t.Errorf("InputHash drifted: %s vs %s", p1.InputHash, p2.InputHash)
	}
	if p1.PlanHash != p2.PlanHash {
		t.Errorf("PlanHash drifted: %s vs %s", p1.PlanHash, p2.PlanHash)
	}
}

func TestComputeInputHash_StableAcrossEquivalentOptions(t *testing.T) {
	// Two InitOptions that differ only in map insertion order (Go
	// map iteration is randomised) must still produce the same
	// InputHash. encoding/json sorts map keys at marshal time so
	// this should hold automatically — this test guards against a
	// regression where some future change uses a non-marshalled
	// representation.
	o1 := atlantisOpts()
	o2 := atlantisOpts()
	// Re-shuffle by rebuilding the maps in a different declaration
	// order. Go's map literal is order-irrelevant but this still
	// exercises the marshalling path.
	o2.NodeNICs = map[string]string{
		"HOST7-A": "enp1s0",
		"HOST5-A": "enp1s0",
		"HOST6-A": "enp1s0",
	}
	h1, _ := ComputeInputHash(o1)
	h2, _ := ComputeInputHash(o2)
	if h1 != h2 {
		t.Fatalf("hash differs across map iteration order: %s vs %s", h1, h2)
	}
}

func TestComputeInputHash_SensitiveToChange(t *testing.T) {
	// Changing any meaningful field must change the hash.
	o := atlantisOpts()
	base, _ := ComputeInputHash(o)

	cases := []struct {
		name   string
		mutate func(*InitOptions)
	}{
		{"name", func(o *InitOptions) { o.Name = "different" }},
		{"domain", func(o *InitOptions) { o.Domain = "other.example.com" }},
		{"set value", func(o *InitOptions) { o.Sets["EXT_NET_VLAN_ID"] = "999" }},
		{"add nic", func(o *InitOptions) { o.NodeNICs["SRV8"] = "eno1" }},
		{"toggle allow-dns-not-ready", func(o *InitOptions) { o.AllowDNSNotReady = true }},
		{"change rook-osd-size", func(o *InitOptions) { o.RookOSDSizeGB = 750 }},
		// C4 reviewer P1 (48d57b8f follow-up): NoPush changes
		// apply-time behavior (skips push + flux-install), so a
		// dry-run/apply-plan flag mismatch MUST fire
		// ErrPlanInputDrift instead of silently applying a
		// commit+push+flux-install run that the operator reviewed
		// as a local-only preview (or vice versa).
		{"toggle no-push", func(o *InitOptions) { o.NoPush = true }},
		// OS-1: the object-storage mode + companions select which
		// fleet manifests get scaffolded — all substantive.
		{"object-storage mode", func(o *InitOptions) { o.RookMode = RookCephLocal }},
		{"ceph node added", func(o *InitOptions) { o.CephNodes = map[string]string{"srv6": "sdb"} }},
		{"ceph storage class", func(o *InitOptions) { o.CephStorageClass = "fast-ssd" }},
		{"s3 hostname", func(o *InitOptions) { o.S3Hostname = "s3.other.example.com" }},
		{"toggle no-s3-exposure", func(o *InitOptions) { o.NoS3Exposure = true }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mod := *atlantisOpts()
			// deep-copy the maps so the parent's case-by-case
			// mutation doesn't leak through to the next iteration.
			mod.Sets = copyMap(mod.Sets)
			mod.NodeNICs = copyMap(mod.NodeNICs)
			tc.mutate(&mod)
			got, _ := ComputeInputHash(&mod)
			if got == base {
				t.Fatalf("hash unchanged after mutating %s", tc.name)
			}
		})
	}
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestComputeInputHash_ExcludesApplyFlowFlags(t *testing.T) {
	// Apply-flow flags (--dry-run / --apply-plan / --plan-file /
	// --yes / --no-tty) MUST NOT affect InputHash — otherwise the
	// canonical flow ("dry-run, then apply-plan with --yes") would
	// always fail with ErrPlanInputDrift. Plus --github-token
	// (review-pass — P2): rotating credentials between dry-run and
	// apply must not invalidate the plan.
	//
	// NOTE: --no-push is NOT excluded — it changes apply-time
	// behavior (skips push + flux-install) so a dry-run/apply
	// mismatch there IS a real drift. See the "toggle no-push"
	// case in TestComputeInputHash_SensitiveToChange.
	base := atlantisOpts()
	baseHash, _ := ComputeInputHash(base)

	cases := []struct {
		name   string
		mutate func(*InitOptions)
	}{
		{"DryRun on", func(o *InitOptions) { o.DryRun = true }},
		{"ApplyPlan set", func(o *InitOptions) { o.ApplyPlan = "/tmp/p.json" }},
		{"PlanFile set", func(o *InitOptions) { o.PlanFile = "/tmp/p.json" }},
		{"Yes on", func(o *InitOptions) { o.Yes = true }},
		{"NoTTY on", func(o *InitOptions) { o.NoTTY = true }},
		{"GitHubToken set", func(o *InitOptions) { o.GitHubToken = "ghp_TEST_TOKEN_NEW" }},
		{"GitHubToken rotated", func(o *InitOptions) { o.GitHubToken = "ghp_TEST_TOKEN_ROTATED" }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := *atlantisOpts()
			o.Sets = copyMap(o.Sets)
			o.NodeNICs = copyMap(o.NodeNICs)
			tc.mutate(&o)
			got, _ := ComputeInputHash(&o)
			if got != baseHash {
				t.Fatalf("apply-flow flag %q must NOT affect InputHash (was %s, got %s)", tc.name, baseHash, got)
			}
		})
	}
}

// PlanHash semantics --------------------------------------------

func TestBuildPlan_InheritedDefaults_DefensiveCopy(t *testing.T) {
	// Review-pass P3: BuildPlan must defensively-copy
	// fleet.InheritedDefaults so a caller mutating the original
	// map after BuildPlan returns can't silently corrupt the
	// plan's contents (which would also bypass PlanHash detection
	// since the hash was computed before the mutation).
	fleet := atlantisFleet()
	fleet.InheritedDefaults = map[string]string{
		"KUBE_DC_VERSION": "v0.3.63",
	}
	p, err := BuildPlan(atlantisOpts(), fleet)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	// Mutate the source map post-BuildPlan.
	fleet.InheritedDefaults["KUBE_DC_VERSION"] = "v9.9.9-tampered"
	fleet.InheritedDefaults["NEW_KEY"] = "injected"
	// Plan's InheritedDefaults must be unchanged.
	if got := p.InheritedDefaults["KUBE_DC_VERSION"]; got != "v0.3.63" {
		t.Errorf("plan's KUBE_DC_VERSION mutated through shared map: got %q, want v0.3.63", got)
	}
	if _, present := p.InheritedDefaults["NEW_KEY"]; present {
		t.Errorf("plan's InheritedDefaults shouldn't gain keys via map sharing")
	}
}

func TestPlan_PlanHashExcludesGeneratedAt(t *testing.T) {
	// Review-pass — P1: PlanHash must NOT change when GeneratedAt
	// changes. Previously `computeHash` only zeroed PlanHash, so
	// two BuildPlans from the same inputs at different seconds
	// produced different PlanHash values — a silent determinism
	// bug. Force two different timestamps and assert identical
	// hashes.
	o := atlantisOpts()
	p1, err := BuildPlan(o, atlantisFleet())
	if err != nil {
		t.Fatalf("first BuildPlan: %v", err)
	}
	// Build the second plan, then forcibly drift GeneratedAt by 12
	// hours and recompute its PlanHash. The recomputed hash must
	// match the first plan's.
	p2, err := BuildPlan(o, atlantisFleet())
	if err != nil {
		t.Fatalf("second BuildPlan: %v", err)
	}
	p2.GeneratedAt = p1.GeneratedAt.Add(12 * time.Hour)
	recomputed, err := p2.computeHash()
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if recomputed != p1.PlanHash {
		t.Fatalf("GeneratedAt drift broke PlanHash determinism:\n  p1.PlanHash=%s\n  p2 recomputed=%s", p1.PlanHash, recomputed)
	}
	// And LoadPlan must accept a plan whose GeneratedAt differs
	// from the BuildPlan time as long as PlanHash matches.
	p2.PlanHash = recomputed
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlanFile(p2, path); err != nil {
		t.Fatalf("WritePlanFile: %v", err)
	}
	if _, err := LoadPlan(path); err != nil {
		t.Fatalf("LoadPlan on time-drifted plan: %v", err)
	}
}

func TestPlan_PlanHashExcludesItself(t *testing.T) {
	p, err := BuildPlan(atlantisOpts(), atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	// Recompute manually to confirm the stored hash matches the
	// "PlanHash zeroed" canonical form.
	want, err := p.computeHash()
	if err != nil {
		t.Fatalf("computeHash: %v", err)
	}
	if want != p.PlanHash {
		t.Fatalf("stored PlanHash %s ≠ recomputed %s", p.PlanHash, want)
	}
	// And mutating any non-hash field changes the hash.
	p.ClusterMutations = append(p.ClusterMutations, Mutation{Kind: "Test", Name: "drift"})
	after, _ := p.computeHash()
	if after == want {
		t.Fatalf("PlanHash didn't change after mutating ClusterMutations")
	}
}

// File I/O round-trip + atomic write ---------------------------

func TestWritePlanFile_AtomicAndLoadable(t *testing.T) {
	p, err := BuildPlan(atlantisOpts(), atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	if err := WritePlanFile(p, path); err != nil {
		t.Fatalf("WritePlanFile: %v", err)
	}

	// Mode 0644 expected — no secret material in the plan.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("plan mode = %o, want 0644", st.Mode().Perm())
	}

	// Atomic-write contract: no .tmp residue after success.
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file leaked: %v", err)
	}

	loaded, err := LoadPlan(path)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if loaded.PlanHash != p.PlanHash {
		t.Errorf("loaded PlanHash %s ≠ original %s", loaded.PlanHash, p.PlanHash)
	}
	if loaded.ClusterName != p.ClusterName {
		t.Errorf("loaded ClusterName %s ≠ original %s", loaded.ClusterName, p.ClusterName)
	}
}

func TestLoadPlan_RejectsTamperedFile(t *testing.T) {
	p, err := BuildPlan(atlantisOpts(), atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlanFile(p, path); err != nil {
		t.Fatalf("WritePlanFile: %v", err)
	}
	// Tamper: parse, mutate ClusterName, leave PlanHash as-is,
	// re-write. LoadPlan must catch the hash mismatch.
	raw, _ := os.ReadFile(path)
	var pj map[string]any
	if err := json.Unmarshal(raw, &pj); err != nil {
		t.Fatalf("parse: %v", err)
	}
	pj["clusterName"] = "evil-cluster"
	tampered, _ := json.Marshal(pj)
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	_, err = LoadPlan(path)
	if !errors.Is(err, ErrPlanHashMismatch) {
		t.Fatalf("expected ErrPlanHashMismatch, got %v", err)
	}
}

func TestLoadPlan_RejectsWrongSchemaVersion(t *testing.T) {
	p, err := BuildPlan(atlantisOpts(), atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.Version = "v999"
	// Re-hash so the file is self-consistent but version drifts.
	hash, _ := p.computeHash()
	p.PlanHash = hash
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WritePlanFile(p, path); err != nil {
		t.Fatalf("WritePlanFile: %v", err)
	}
	_, err = LoadPlan(path)
	if !errors.Is(err, ErrPlanSchemaVersion) {
		t.Fatalf("expected ErrPlanSchemaVersion, got %v", err)
	}
}

// VerifyApplyPlanInput ------------------------------------------

func TestVerifyApplyPlanInput(t *testing.T) {
	o := atlantisOpts()
	p, err := BuildPlan(o, atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	// Same options — verify passes.
	if err := VerifyApplyPlanInput(p, o); err != nil {
		t.Fatalf("matching opts should verify, got %v", err)
	}

	// Mutate any meaningful field — verify fails with ErrPlanInputDrift.
	o2 := *atlantisOpts()
	o2.Sets = copyMap(o2.Sets)
	o2.Sets["EXT_NET_VLAN_ID"] = "9999"
	if err := VerifyApplyPlanInput(p, &o2); !errors.Is(err, ErrPlanInputDrift) {
		t.Fatalf("expected ErrPlanInputDrift on --set drift, got %v", err)
	}
}

// Render output --------------------------------------------------

func TestRender_GreenfieldShape(t *testing.T) {
	o := atlantisOpts()
	o.FleetMode = FleetNewRepo
	o.GitHubOwner = "shalb"
	o.GitHubRepo = "kube-dc-fleet"
	p, err := BuildPlan(o, FleetState{}) // no priors
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var buf bytes.Buffer
	p.Render(&buf)
	body := buf.String()
	for _, want := range []string{
		"Files to write",
		"cluster-config.env",
		"secrets.enc.yaml",
		"Scripts to run",
		"bootstrap/add-cluster.sh atlantis",
		"bootstrap/flux-install.sh atlantis --new-cluster",
		"deferred until HelmRelease/openbao Ready",
		"Cluster mutations",
		"Confirmations required",
		"--yes to apply",
		"InputHash:",
		"PlanHash:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("greenfield render missing %q\nFULL:\n%s", want, body)
		}
	}
	// Greenfield must NOT include the existing-fleet detection
	// header.
	if strings.Contains(body, "== Detected: existing-fleet ==") {
		t.Errorf("greenfield render leaked existing-fleet header")
	}
}

// TestRender_ObjectStorageOS1Shape — OS-1 render contract:
// (1) the vaporware "(in-process) apply Rook CephObjectStore + system
// OBCs" deferred step is GONE — it was never implemented and
// contradicted the GitOps model (Flux applies the data plane via the
// infra-object-storage Kustomization; the CLI only generates files);
// (2) an explicit --object-storage-mode=disabled surfaces the
// monitoring-will-not-converge warning prominently in the render AND
// in the hashed plan (a reviewed warning can't vanish between dry-run
// and apply).
func TestRender_ObjectStorageOS1Shape(t *testing.T) {
	o := atlantisOpts() // baseline is explicit disabled
	p, err := BuildPlan(o, atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var buf bytes.Buffer
	p.Render(&buf)
	body := buf.String()

	if strings.Contains(body, "CephObjectStore") {
		t.Errorf("render leaked the deleted vaporware CephObjectStore step:\n%s", body)
	}
	for _, want := range []string{
		"== WARNINGS ==",
		"object storage disabled",
		"Mimir + Loki are SUSPENDED",
		"UNPROTECTED",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("disabled-mode render missing %q\nFULL:\n%s", want, body)
		}
	}
	if len(p.Warnings) != 1 {
		t.Errorf("plan should carry exactly 1 warning, got %v", p.Warnings)
	}
}

// TestRender_NoPushShape — C4 reviewer P1: the render must match
// what apply.go actually does under --no-push. Two invariants:
// (1) the commit line reflects local-only (no "+ push"), and (2)
// flux-install.sh is absent from the script list (Flux needs a
// pushed commit to reconcile; apply skips the script). Before this
// slice the render showed "+ push" AND flux-install even under
// --no-push, so an operator dry-run+reviewed a preview that didn't
// match the apply behavior.
func TestRender_NoPushShape(t *testing.T) {
	o := atlantisOpts()
	o.NoPush = true
	o.FleetMode = FleetNewRepo
	o.GitHubOwner = "kube-dc"
	o.GitHubRepo = "kube-dc-fleet"
	p, err := BuildPlan(o, FleetState{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var buf bytes.Buffer
	p.Render(&buf)
	body := buf.String()

	// (1) commit line is local-only + explicitly labels the skipped
	// downstream steps. Must NOT include "+ push".
	if !strings.Contains(body, "(in-process) git add + commit") {
		t.Errorf("--no-push render missing local commit step:\n%s", body)
	}
	if strings.Contains(body, "git add + commit + push") {
		t.Errorf("--no-push render leaked '+ push' step:\n%s", body)
	}
	if !strings.Contains(body, "--no-push set; push + flux-install skipped") {
		t.Errorf("--no-push render missing skip explanation:\n%s", body)
	}

	// (2) flux-install.sh must NOT appear — apply.go skips it under
	// --no-push (see TestApply_NoPush_SkipsFluxInstall).
	if strings.Contains(body, "bootstrap/flux-install.sh") {
		t.Errorf("--no-push render leaked flux-install.sh (apply.go skips it):\n%s", body)
	}
}

func TestRender_ExistingFleetShape(t *testing.T) {
	p, err := BuildPlan(atlantisOpts(), atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var buf bytes.Buffer
	p.Render(&buf)
	body := buf.String()
	for _, want := range []string{
		"== Detected: existing-fleet ==",
		"Prior clusters: cloud, eu/dc1, stage",
		"KUBE_DC_PLATFORM_VERSION=v0.4.0",
		"Files NOT touched",
		"clusters/cloud/ (untouched)",
		"clusters/eu/dc1/ (untouched)",
		"(in-process) scaffold from sibling templates",
		"(in-process) render customInterfaces into infrastructure.yaml",
		"3 --node-nic mapping",
		"3 age key(s)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("existing-fleet render missing %q\nFULL:\n%s", want, body)
		}
	}
	// Greenfield-only steps must NOT appear.
	if strings.Contains(body, "bootstrap/add-cluster.sh") {
		t.Errorf("existing-fleet leaked add-cluster.sh step")
	}
	if strings.Contains(body, "bootstrap/generate-age-key.sh") {
		t.Errorf("existing-fleet leaked generate-age-key.sh step")
	}
}

func TestRender_PreservesScriptExecutionOrder(t *testing.T) {
	// The script list is order-sensitive (T10 scaffold must run
	// before T12 commit which must run before flux-install). Verify
	// the Render output reflects the BuildPlan order verbatim.
	p, err := BuildPlan(atlantisOpts(), atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var buf bytes.Buffer
	p.Render(&buf)
	body := buf.String()

	steps := []string{
		"(in-process) scaffold from sibling templates",
		"(in-process) apply form deltas to cluster-config.env",
		"(in-process) render customInterfaces into infrastructure.yaml",
		"(in-process) sops encrypt clusters/atlantis/secrets.enc.yaml",
		"(in-process) git add + commit + push",
		"bootstrap/flux-install.sh atlantis --new-cluster",
		"kube-dc bootstrap openbao init atlantis",
	}
	last := -1
	for _, s := range steps {
		idx := strings.Index(body, s)
		if idx < 0 {
			t.Fatalf("step %q not in render", s)
		}
		if idx < last {
			t.Errorf("step %q appears before earlier step (idx %d < %d)\nFULL:\n%s", s, idx, last, body)
		}
		last = idx
	}
}

// Consent marker -------------------------------------------------

func TestConsentMarker_WriteAndLookup(t *testing.T) {
	_ = withConsentDir(t)

	p, err := BuildPlan(atlantisOpts(), atlantisFleet())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	ok, err := LookupConsentMarker(p.ClusterName, p.PlanHash)
	if err != nil {
		t.Fatalf("Lookup before write: %v", err)
	}
	if ok {
		t.Fatalf("no marker should exist yet")
	}

	if err := WriteConsentMarker(p); err != nil {
		t.Fatalf("WriteConsentMarker: %v", err)
	}

	ok, err = LookupConsentMarker(p.ClusterName, p.PlanHash)
	if err != nil {
		t.Fatalf("Lookup after write: %v", err)
	}
	if !ok {
		t.Fatalf("marker should match after write")
	}

	// Drift the hash — lookup must report no-match (false, nil), NOT
	// an error. The cobra layer surfaces the no-match with a clean
	// "re-run --dry-run" message.
	ok, err = LookupConsentMarker(p.ClusterName, "different-hash")
	if err != nil {
		t.Fatalf("Lookup hash-mismatch: %v", err)
	}
	if ok {
		t.Fatalf("hash mismatch should be (false, nil), got ok=true")
	}
}

func TestConsentMarker_FlattensNestedClusterName(t *testing.T) {
	dir := withConsentDir(t)
	// eu/dc1-shape cluster name — the marker path must flatten the
	// slash so it's one path component.
	p := &Plan{ClusterName: "eu/dc1", PlanHash: "abc123"}
	if err := WriteConsentMarker(p); err != nil {
		t.Fatalf("WriteConsentMarker: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	if name := entries[0].Name(); !strings.Contains(name, "eu_dc1") {
		t.Errorf("marker filename should flatten eu/dc1 -> eu_dc1, got %q", name)
	}
}
