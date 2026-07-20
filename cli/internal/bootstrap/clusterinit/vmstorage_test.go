package clusterinit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// seedGoldenCatalog creates the Phase-1 per-OS FS golden catalog the
// resolver reads (platform/rbd-vm/goldens-fs/os/<name>.yaml).
func seedGoldenCatalog(t *testing.T, repo string, names ...string) {
	t.Helper()
	osDir := filepath.Join(repo, "platform", "rbd-vm", "goldens-fs", "os")
	if err := os.MkdirAll(osDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(osDir, n+".yaml"), []byte("# golden "+n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// --- goldens overlay rendering ---

func TestVMGoldensOverlay_SelectedAndDepth(t *testing.T) {
	body := vmGoldensOverlayYAML("atlantis", []string{"debian-12", "alpine-3.21"})
	for _, want := range []string{
		"- ../../../platform/rbd-vm/goldens-fs/os/debian-12.yaml",
		"- ../../../platform/rbd-vm/goldens-fs/os/alpine-3.21.yaml",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay missing %q\nFULL:\n%s", want, body)
		}
	}
	if strings.Contains(body, "os/windows-11-golden.yaml") {
		t.Error("only selected goldens must appear (windows not requested)")
	}
	if strings.Contains(body, "../../../../") {
		t.Errorf("flat name must use exactly 3 ups\nFULL:\n%s", body)
	}
}

func TestVMGoldensOverlay_NestedDepth(t *testing.T) {
	body := vmGoldensOverlayYAML("eu/dc1", []string{"debian-12"})
	if !strings.Contains(body, "- ../../../../platform/rbd-vm/goldens-fs/os/debian-12.yaml") {
		t.Errorf("nested name must use 4 ups\nFULL:\n%s", body)
	}
}

// --- rbd-vm.yaml shape ---

func TestRBDVMYAML_Shape(t *testing.T) {
	body := rbdVMYAML("eu/dc1")
	for _, want := range []string{
		"name: rbd-vm-base",
		"path: ./platform/rbd-vm/base",
		"name: rbd-vm-goldens",
		"path: ./clusters/eu/dc1/rbd-vm-goldens", // subset overlay, NOT the full catalog
		"- name: rbd-vm-base",                    // goldens dependsOn base
		"- name: infra-object-storage",           // base dependsOn object-storage
		"- name: platform",
		"prune: false",
		"name: cluster-config", // goldens keep postBuild for ${S3_HOSTNAME}
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rbd-vm.yaml missing %q\nFULL:\n%s", want, body)
		}
	}
	// A fresh cluster must NOT point rbd-vm-goldens at the full shared set.
	if strings.Contains(body, "path: ./platform/rbd-vm/goldens-fs") {
		t.Error("a generated cluster must use its own subset overlay, not the full goldens-fs")
	}
}

// --- kustomization patcher ---

func TestPatchKustomizationRBDVM_GeneratedShape(t *testing.T) {
	out, changed, err := patchKustomizationRBDVM(strings.Split(genKustomizationYAML, "\n"))
	if err != nil || !changed {
		t.Fatalf("patch: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(strings.Join(out, "\n"), "  - platform.yaml\n  - rbd-vm.yaml") {
		t.Errorf("rbd-vm.yaml must insert after platform.yaml:\n%s", strings.Join(out, "\n"))
	}
}

func TestPatchKustomizationRBDVM_IdempotentAndMissing(t *testing.T) {
	once, _, _ := patchKustomizationRBDVM(strings.Split(genKustomizationYAML, "\n"))
	_, changed, err := patchKustomizationRBDVM(once)
	if err != nil || changed {
		t.Fatalf("second patch must no-op cleanly: changed=%v err=%v", changed, err)
	}
	if _, _, err := patchKustomizationRBDVM([]string{"resources:", "  - infrastructure.yaml"}); err == nil {
		t.Fatal("missing `- platform.yaml` entry must error (file shape drift)")
	}
}

func TestPatchKustomizationRBDVM_CommentNoFalsePositive(t *testing.T) {
	commented := "# rbd-vm.yaml is added post-install\n" + genKustomizationYAML
	out, changed, err := patchKustomizationRBDVM(strings.Split(commented, "\n"))
	if err != nil || !changed {
		t.Fatalf("a comment must not suppress patching: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(strings.Join(out, "\n"), "  - rbd-vm.yaml") {
		t.Error("entry not inserted despite the comment")
	}
}

// --- golden resolver ---

func TestResolveVMGoldens(t *testing.T) {
	repo := t.TempDir()
	seedGoldenCatalog(t, repo, "debian-12", "alpine-3.21", "windows-11-golden", "ubuntu-24.04")

	// default → the single containerdisk default, never all
	got, err := resolveVMGoldens(repo, nil)
	if err != nil || len(got) != 1 || got[0] != defaultVMGolden {
		t.Fatalf("default: got=%v err=%v (want [%s])", got, err, defaultVMGolden)
	}
	// explicit subset: deduped + sorted, windows opt-in works
	got, err = resolveVMGoldens(repo, []string{"windows-11-golden", "alpine-3.21", "alpine-3.21"})
	if err != nil {
		t.Fatalf("subset: %v", err)
	}
	if strings.Join(got, ",") != "alpine-3.21,windows-11-golden" {
		t.Errorf("subset dedup/sort wrong: %v", got)
	}
	// unknown golden errors (not silently dropped)
	if _, err := resolveVMGoldens(repo, []string{"nope-1.0"}); err == nil {
		t.Fatal("unknown golden must error")
	}
	// missing catalog errors (Phase-1 split absent)
	if _, err := resolveVMGoldens(t.TempDir(), nil); err == nil {
		t.Fatal("missing golden catalog must error, not silently produce an empty subset")
	}
}

func TestResolveVMGoldens_DefaultMissingErrors(t *testing.T) {
	repo := t.TempDir()
	seedGoldenCatalog(t, repo, "alpine-3.21") // no debian-12 (the default)
	if _, err := resolveVMGoldens(repo, nil); err == nil {
		t.Fatal("default golden absent from catalog must error, not silently pick another OS")
	}
}

// --- end-to-end writer ---

func TestWriteVMStorage_Local_NoOp(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	seedGoldenCatalog(t, repo, "debian-12")
	for _, m := range []VMStorageMode{"", VMStorageLocal} {
		if err := WriteVMStorage(repo, "atlantis", VMStorageSpec{Mode: m}, nil); err != nil {
			t.Fatalf("local(%q): %v", m, err)
		}
		if _, err := os.Stat(filepath.Join(repo, "clusters", "atlantis", "rbd-vm.yaml")); !os.IsNotExist(err) {
			t.Errorf("mode %q must write no rbd-vm.yaml (VMs use local-path)", m)
		}
	}
}

func TestWriteVMStorage_SharedRBD_EndToEnd(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	seedGoldenCatalog(t, repo, "debian-12", "alpine-3.21", "windows-11-golden")
	spec := VMStorageSpec{Mode: VMStorageSharedRBD, Goldens: []string{"debian-12", "alpine-3.21"}}
	if err := WriteVMStorage(repo, "atlantis", spec, nil); err != nil {
		t.Fatalf("WriteVMStorage: %v", err)
	}
	dir := filepath.Join(repo, "clusters", "atlantis")

	overlay, err := os.ReadFile(filepath.Join(dir, "rbd-vm-goldens", "kustomization.yaml"))
	if err != nil {
		t.Fatalf("goldens overlay not written: %v", err)
	}
	if !strings.Contains(string(overlay), "os/debian-12.yaml") || !strings.Contains(string(overlay), "os/alpine-3.21.yaml") {
		t.Errorf("goldens overlay missing selected OSes:\n%s", overlay)
	}
	if strings.Contains(string(overlay), "os/windows-11-golden.yaml") {
		t.Error("windows golden must NOT be selected (not requested — it's the 70Gi opt-in)")
	}

	rbdvm, err := os.ReadFile(filepath.Join(dir, "rbd-vm.yaml"))
	if err != nil {
		t.Fatalf("rbd-vm.yaml not written: %v", err)
	}
	if !strings.Contains(string(rbdvm), "path: ./clusters/atlantis/rbd-vm-goldens") {
		t.Errorf("rbd-vm-goldens must point at the cluster subset overlay:\n%s", rbdvm)
	}

	kust, _ := os.ReadFile(filepath.Join(dir, "kustomization.yaml"))
	if !strings.Contains(string(kust), "- rbd-vm.yaml") {
		t.Errorf("kustomization resources not patched:\n%s", kust)
	}
}

func TestWriteVMStorage_SharedRBD_DefaultGolden(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	seedGoldenCatalog(t, repo, "debian-12", "windows-11-golden")
	if err := WriteVMStorage(repo, "atlantis", VMStorageSpec{Mode: VMStorageSharedRBD}, nil); err != nil {
		t.Fatalf("default golden: %v", err)
	}
	overlay, _ := os.ReadFile(filepath.Join(repo, "clusters", "atlantis", "rbd-vm-goldens", "kustomization.yaml"))
	if !strings.Contains(string(overlay), "os/"+defaultVMGolden+".yaml") {
		t.Errorf("default subset must be the containerdisk default %q:\n%s", defaultVMGolden, overlay)
	}
	if strings.Contains(string(overlay), "os/windows-11-golden.yaml") {
		t.Error("default must never include the 70Gi windows golden")
	}
}

func TestWriteVMStorage_LiveMigration_FailsClosed(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	seedGoldenCatalog(t, repo, "debian-12")
	err := WriteVMStorage(repo, "atlantis", VMStorageSpec{Mode: VMStorageSharedRBDLiveMigration}, nil)
	if err == nil {
		t.Fatal("shared-rbd-live-migration must fail closed at scaffold time (runtime egress + Block catalog unknown)")
	}
	if _, statErr := os.Stat(filepath.Join(repo, "clusters", "atlantis", "rbd-vm.yaml")); !os.IsNotExist(statErr) {
		t.Error("a live-migration failure must not leave a partial rbd-vm.yaml")
	}
}

func TestWriteVMStorage_UnknownGoldenErrors(t *testing.T) {
	repo := seedScaffold(t, "atlantis")
	seedGoldenCatalog(t, repo, "debian-12")
	err := WriteVMStorage(repo, "atlantis", VMStorageSpec{Mode: VMStorageSharedRBD, Goldens: []string{"not-a-golden"}}, nil)
	if err == nil {
		t.Fatal("a --vm-golden not in the catalog must error")
	}
}

func TestWriteVMStorage_NestedName(t *testing.T) {
	repo := seedScaffold(t, "eu/dc1")
	seedGoldenCatalog(t, repo, "debian-12")
	if err := WriteVMStorage(repo, "eu/dc1", VMStorageSpec{Mode: VMStorageSharedRBD}, nil); err != nil {
		t.Fatalf("nested: %v", err)
	}
	overlay, err := os.ReadFile(filepath.Join(repo, "clusters", "eu", "dc1", "rbd-vm-goldens", "kustomization.yaml"))
	if err != nil {
		t.Fatalf("nested overlay not written: %v", err)
	}
	if !strings.Contains(string(overlay), "../../../../platform/rbd-vm/goldens-fs/os/debian-12.yaml") {
		t.Errorf("nested overlay depth wrong:\n%s", overlay)
	}
}

// --- structural validation ---

func TestValidateVMStorage(t *testing.T) {
	cases := []struct {
		name    string
		o       InitOptions
		wantErr bool
	}{
		{"empty ok (optional)", InitOptions{}, false},
		{"local ok", InitOptions{VMStorageMode: VMStorageLocal}, false},
		{"shared-rbd ok", InitOptions{VMStorageMode: VMStorageSharedRBD}, false},
		{"unknown mode", InitOptions{VMStorageMode: "nope"}, true},
		{"golden without shared-rbd", InitOptions{VMGoldens: []string{"debian-12"}}, true},
		{"golden with shared-rbd ok", InitOptions{VMStorageMode: VMStorageSharedRBD, VMGoldens: []string{"debian-12", "alpine-3.21"}}, false},
		{"bad golden slug", InitOptions{VMStorageMode: VMStorageSharedRBD, VMGoldens: []string{"BAD_Name"}}, true},
		{"block golden outside live-migration", InitOptions{VMStorageMode: VMStorageSharedRBD, VMGoldensBlock: []string{"ubuntu-24.04"}}, true},
		{"block golden in live-migration ok", InitOptions{VMStorageMode: VMStorageSharedRBDLiveMigration, VMGoldensBlock: []string{"ubuntu-24.04"}}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			errs := validateVMStorage(&tc.o)
			if (len(errs) > 0) != tc.wantErr {
				t.Errorf("errs=%v wantErr=%v", errs, tc.wantErr)
			}
		})
	}
}

// --- cross-flag validation (through Validate) ---

func TestVMStorageCrossFlag(t *testing.T) {
	rook := func(o *InitOptions) { o.RookMode = RookCephLocal; o.RookOSDNode = "host6-a"; o.RookOSDSizeGB = 500 }
	cases := []struct {
		name    string
		mutate  func(*InitOptions)
		wantSub string // error substring; "" = expect pass
	}{
		{"local always OK", func(o *InitOptions) { o.VMStorageMode = VMStorageLocal }, ""},
		{"shared-rbd needs rook (disabled fails)", func(o *InitOptions) { o.VMStorageMode = VMStorageSharedRBD }, "rbd-pool"},
		{"shared-rbd with rook-ceph-local OK", func(o *InitOptions) { rook(o); o.VMStorageMode = VMStorageSharedRBD }, ""},
		{"live-migration fails closed even with rook", func(o *InitOptions) { rook(o); o.VMStorageMode = VMStorageSharedRBDLiveMigration }, "installer-scaffoldable"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := validBase()
			tc.mutate(&o)
			err := o.Validate()
			if tc.wantSub == "" {
				if err != nil {
					t.Errorf("want pass, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("want error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

// --- config round-trip (--save-config / --config) ---

func TestVMStorage_ConfigRoundTrip(t *testing.T) {
	src := InitOptions{
		VMStorageMode:  VMStorageSharedRBD,
		VMGoldens:      []string{"debian-12", "alpine-3.21"},
		VMGoldensBlock: []string{"ubuntu-24.04"},
	}
	m := ExportMap(&src)
	if m[KeyVMStorageMode] != "shared-rbd" {
		t.Fatalf("export mode: %q", m[KeyVMStorageMode])
	}
	// goldens export canonicalized (sorted + deduped) for a stable diff.
	if m[KeyVMGolden] != "alpine-3.21,debian-12" {
		t.Errorf("export goldens not canonical: %q", m[KeyVMGolden])
	}
	if m[KeyVMGoldenBlock] != "ubuntu-24.04" {
		t.Errorf("export block goldens: %q", m[KeyVMGoldenBlock])
	}

	// Re-import into a fresh options (nothing flagged) — the fix for the
	// silent-drop bug: shared-rbd must survive a save/load round-trip.
	dst := InitOptions{}
	ImportMap(&dst, m, func(string) bool { return false })
	if dst.VMStorageMode != VMStorageSharedRBD {
		t.Errorf("round-trip mode lost: %q (want shared-rbd)", dst.VMStorageMode)
	}
	if strings.Join(dst.VMGoldens, ",") != "alpine-3.21,debian-12" {
		t.Errorf("round-trip goldens lost: %v", dst.VMGoldens)
	}
	if strings.Join(dst.VMGoldensBlock, ",") != "ubuntu-24.04" {
		t.Errorf("round-trip block goldens lost: %v", dst.VMGoldensBlock)
	}
}

func TestVMStorage_ConfigRoundTrip_LocalOmitted(t *testing.T) {
	// local (the default) must NOT emit VM keys — omitting them reproduces it.
	for _, mode := range []VMStorageMode{"", VMStorageLocal} {
		m := ExportMap(&InitOptions{VMStorageMode: mode})
		if _, ok := m[KeyVMStorageMode]; ok {
			t.Errorf("mode %q must not export %s", mode, KeyVMStorageMode)
		}
	}
}

func TestVMStorage_ImportFlagWins(t *testing.T) {
	// An explicit --vm-storage-mode flag must NOT be overridden by a config
	// value (precedence: defaults < prefill < flags).
	dst := InitOptions{VMStorageMode: VMStorageLocal}
	ImportMap(&dst, map[string]string{KeyVMStorageMode: "shared-rbd"},
		func(f string) bool { return f == "vm-storage-mode" })
	if dst.VMStorageMode != VMStorageLocal {
		t.Errorf("config overrode an explicit flag: %q", dst.VMStorageMode)
	}
}

// --- hash canonicalization (P3) ---

func TestVMStorage_HashCanonical(t *testing.T) {
	o1 := InitOptions{VMStorageMode: VMStorageSharedRBD, VMGoldens: []string{"alpine-3.21", "debian-12"}}
	o2 := InitOptions{VMStorageMode: VMStorageSharedRBD, VMGoldens: []string{"debian-12", "alpine-3.21", "debian-12"}}
	if !reflect.DeepEqual(o1.inputsForHash(), o2.inputsForHash()) {
		t.Error("golden order/dupes must not drift the plan-input hash (dry-run vs apply must agree)")
	}
}

func TestCanonicalGoldens(t *testing.T) {
	if g := canonicalGoldens([]string{"b", "a", "b", " a ", "c"}); strings.Join(g, ",") != "a,b,c" {
		t.Errorf("canonicalGoldens dedup/sort/trim wrong: %v", g)
	}
	if canonicalGoldens(nil) != nil {
		t.Error("nil in → nil out")
	}
}

// --- plan preview ---

func TestVMStorageFiles_Preview(t *testing.T) {
	if f := vmStorageFiles("atlantis", VMStorageSpec{Mode: VMStorageLocal}); f != nil {
		t.Errorf("local previews no files, got %v", f)
	}
	if f := vmStorageFiles("atlantis", VMStorageSpec{Mode: VMStorageSharedRBDLiveMigration}); f != nil {
		t.Errorf("deferred live-migration previews no files, got %v", f)
	}
	f := vmStorageFiles("atlantis", VMStorageSpec{Mode: VMStorageSharedRBD})
	if len(f) != 3 {
		t.Fatalf("shared-rbd previews 3 files, got %v", f)
	}
	joined := strings.Join(f, "\n")
	for _, want := range []string{"rbd-vm-goldens/kustomization.yaml", "rbd-vm.yaml", "kustomization.yaml"} {
		if !strings.Contains(joined, want) {
			t.Errorf("preview missing %q\n%s", want, joined)
		}
	}
}
