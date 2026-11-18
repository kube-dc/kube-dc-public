package clusterinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	k8syaml "sigs.k8s.io/yaml"
)

// A flash class for block and bucket indexes, a capacity class for bucket data
// (EC 4+2): the layout the tier keys exist for.
func tieredSets() map[string]string {
	return map[string]string{
		KeyCephOSDDeviceClass:          "ssd",
		KeyCephCapacityOSDStorageClass: "sc-osd",
		KeyCephCapacityOSDCount:        "6",
		KeyCephCapacityOSDVolumeSizeGB: "6144",
		KeyCephBlockPoolDeviceClass:    "ssd",
		KeyCephRGWMetadataDeviceClass:  "ssd",
		KeyCephRGWDataDeviceClass:      "hdd",
		KeyCephRGWDataErasureCoding:    "4+2",
	}
}

func pvcOptions(sets map[string]string) *InitOptions {
	return &InitOptions{RookMode: RookCephPVC, CephStorageClass: "unity-osd", CephOSDCount: 4, CephOSDVolumeSizeGB: 2800, Sets: sets}
}

func TestParseCephTiers(t *testing.T) {
	got := parseCephTiers(tieredSets())
	want := CephTiers{OSDDeviceClass: "ssd", CapacityStorageClass: "sc-osd", CapacityOSDCount: 6, CapacityOSDSizeGB: 6144,
		CapacityDeviceClass: "hdd", BlockPoolDeviceClass: "ssd", RGWMetadataDeviceClass: "ssd", RGWDataDeviceClass: "hdd",
		RGWDataECDataChunks: 4, RGWDataECCodingChunks: 2}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if parseCephTiers(nil).active() {
		t.Fatal("no keys must mean no tiers")
	}
	if parseCephTiers(map[string]string{KeyCephCapacityOSDPortable: "yes", KeyCephCapacityOSDStorageClass: "x"}).CapacityPortable != true {
		t.Fatal("portable spelling not parsed")
	}
	for _, v := range []string{"4", "1+1", "4+0", "10+7", "a+b", "4+2+1", "",
		"9223372036854775807+2", "2+9223372036854775807", "9223372036854775807+9223372036854775807"} {
		if _, _, ok := parseErasureCoding(v); ok {
			t.Fatalf("%q accepted", v)
		}
	}
}

func TestValidateCephTiers(t *testing.T) {
	with := func(mut func(map[string]string)) map[string]string {
		s := tieredSets()
		mut(s)
		return s
	}
	cases := []struct {
		name string
		o    *InitOptions
		want string // substring of one error; "" = valid
	}{
		{"valid tiered layout", pvcOptions(tieredSets()), ""},
		{"no keys", pvcOptions(nil), ""},
		{"device class only", pvcOptions(map[string]string{KeyCephOSDDeviceClass: "ssd", KeyCephBlockPoolDeviceClass: "ssd"}), ""},
		{"other mode", &InitOptions{RookMode: RookCephMultiNode, Sets: map[string]string{KeyCephOSDDeviceClass: "ssd"}}, "apply only to --object-storage-mode=rook-ceph-pvc"},
		{"count without class", pvcOptions(map[string]string{KeyCephCapacityOSDCount: "3"}), "requires " + KeyCephCapacityOSDStorageClass},
		{"missing count", pvcOptions(with(func(s map[string]string) { delete(s, KeyCephCapacityOSDCount) })), KeyCephCapacityOSDCount + " must be a decimal whole number"},
		{"missing size", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDVolumeSizeGB] = "big" })), KeyCephCapacityOSDVolumeSizeGB + " must be a decimal whole number"},
		{"no primary class", pvcOptions(with(func(s map[string]string) { delete(s, KeyCephOSDDeviceClass) })), KeyCephOSDDeviceClass + " is required"},
		{"same classes", pvcOptions(with(func(s map[string]string) { s[KeyCephOSDDeviceClass] = "hdd" })), "must differ"},
		{"bad class name", pvcOptions(with(func(s map[string]string) { s[KeyCephBlockPoolDeviceClass] = "SSD!" })), "not a valid Ceph device class"},
		{"unknown pool class", pvcOptions(with(func(s map[string]string) { s[KeyCephRGWMetadataDeviceClass] = "nvme" })), "not provided by any OSD set"},
		{"bad ec", pvcOptions(with(func(s map[string]string) { s[KeyCephRGWDataErasureCoding] = "4-2" })), "must be K+M"},
		{"ec wider than class", pvcOptions(with(func(s map[string]string) { s[KeyCephRGWDataErasureCoding] = "6+2" })), "needs 8 OSDs"},
		{"replicated pool on one osd", pvcOptions(with(func(s map[string]string) {
			s[KeyCephCapacityOSDCount] = "1"
			delete(s, KeyCephRGWDataErasureCoding)
		})), "needs 2 OSDs"},
		{"bad portable", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDPortable] = "sometimes" })), "must be true or false"},
		{"bad capacity class", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDStorageClass] = "SC_OSD" })), "not a valid StorageClass name"},
		// Counts are substituted and re-read as YAML 1.1: only canonical decimals.
		{"octal-looking capacity count", pvcOptions(with(func(s map[string]string) {
			s[KeyCephCapacityOSDCount] = "010"
			s[KeyCephRGWDataErasureCoding] = "8+2"
		})), "without leading zeros"},
		{"signed capacity count", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDCount] = "+6" })), "without leading zeros"},
		{"exponent capacity size", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDVolumeSizeGB] = "6e3" })), "without leading zeros"},
		{"zero-padded capacity size", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDVolumeSizeGB] = "06144" })), "without leading zeros"},
		{"numeric capacity class", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDStorageClass] = "123" })), ""},
		{"empty dns label in capacity class", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDStorageClass] = "fast..san" })), "not a valid StorageClass name"},
		{"dotted capacity class", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDStorageClass] = "fast.san" })), ""},
		{"portable numeric spelling", pvcOptions(with(func(s map[string]string) { s[KeyCephCapacityOSDPortable] = "1" })), ""},
		// Replicated pools use the effective CEPH_REPLICATION_SIZE, not a fixed 2.
		{"three copies on two flash osds", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", CephOSDCount: 2, Sets: map[string]string{
			"CEPH_REPLICATION_SIZE": "3", KeyCephOSDDeviceClass: "ssd", KeyCephBlockPoolDeviceClass: "ssd"}}, "needs 3 OSDs"},
		{"three copies on three flash osds", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", CephOSDCount: 3, Sets: map[string]string{
			"CEPH_REPLICATION_SIZE": "3", KeyCephOSDDeviceClass: "ssd", KeyCephBlockPoolDeviceClass: "ssd"}}, ""},
		{"bad replication size", pvcOptions(with(func(s map[string]string) { s["CEPH_REPLICATION_SIZE"] = "5" })), "CEPH_REPLICATION_SIZE must be 1, 2, or 3"},
		// The primary count follows the scaffold: flag, then --set, then the Fleet default.
		{"primary count from --set", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", Sets: map[string]string{
			"CEPH_OSD_COUNT": "6", KeyCephOSDDeviceClass: "ssd", KeyCephRGWDataDeviceClass: "ssd", KeyCephRGWDataErasureCoding: "4+2"}}, ""},
		{"fleet default primary count", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", Sets: map[string]string{
			KeyCephOSDDeviceClass: "ssd", KeyCephRGWDataDeviceClass: "ssd", KeyCephRGWDataErasureCoding: "4+2"}}, "the class has 2"},
		{"flag beats --set", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", CephOSDCount: 3, Sets: map[string]string{
			"CEPH_OSD_COUNT": "6", KeyCephOSDDeviceClass: "ssd", KeyCephRGWDataDeviceClass: "ssd", KeyCephRGWDataErasureCoding: "4+2"}}, "the class has 3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateCephTiers(tc.o)
			if tc.want == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
				return
			}
			if !strings.Contains(strings.Join(errs, "\n"), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, errs)
			}
		})
	}
}

// Without tier keys the generated overlay must stay byte-identical to the
// pre-tier output, so existing clusters see no change.
func TestPVCOverlayUnchangedWithoutTiers(t *testing.T) {
	plain := objectStorageOverlayYAML("eu/test", ObjectStorageSpec{Mode: RookCephPVC, StorageClass: "x"})
	if strings.Contains(plain, "storageClassDeviceSets") || strings.Contains(plain, "deviceClass") || strings.Contains(plain, "erasureCoded") {
		t.Fatalf("tier patches emitted without tier keys:\n%s", plain)
	}
	tiered := objectStorageOverlayYAML("eu/test", ObjectStorageSpec{Mode: RookCephPVC, StorageClass: "x", Tiers: parseCephTiers(tieredSets())})
	if !strings.HasPrefix(tiered, plain) {
		t.Fatal("tier patches must only append to the plain overlay")
	}
	// Other modes never get them.
	for _, m := range []RookMode{RookCephLocal, RookCephMultiNode} {
		if strings.Contains(objectStorageOverlayYAML("eu/test", ObjectStorageSpec{Mode: m, Tiers: parseCephTiers(tieredSets())}), "osd-capacity") {
			t.Fatalf("%s got pvc tier patches", m)
		}
	}
}

// fluxSubstitute mimics Flux postBuild substitution for ${VAR} and ${VAR:=default}.
func fluxSubstitute(in string, vars map[string]string) string {
	re := regexp.MustCompile(`\$\{([A-Z0-9_]+)(:=([^}]*))?\}`)
	return re.ReplaceAllStringFunc(in, func(m string) string {
		g := re.FindStringSubmatch(m)
		if v, ok := vars[g[1]]; ok {
			return v
		}
		return g[3]
	})
}

// renderedDocs decodes like Flux does after substitution (sigs.k8s.io/yaml,
// YAML 1.1 rules: 010 is octal, numbers become float64), so a value that only
// looks right as text is caught.
func renderedDocs(t *testing.T, out string) map[string]map[string]any {
	docs := map[string]map[string]any{}
	for _, raw := range strings.Split("\n"+out, "\n---\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var d map[string]any
		if err := k8syaml.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("decode: %v\n%s", err, raw)
		}
		if d == nil {
			continue
		}
		md, _ := d["metadata"].(map[string]any)
		docs[d["kind"].(string)+"/"+md["name"].(string)] = d
	}
	return docs
}

func dig(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("not a map at %v", p)
			}
			v = m[k]
		case int:
			l, ok := v.([]any)
			if !ok || k >= len(l) {
				t.Fatalf("no index %d", k)
			}
			v = l[k]
		}
	}
	return v
}

// Render the real Fleet mode with the tier patches, apply Flux substitution
// and check the result is what Rook needs, with real types (count is an int,
// portable a bool), not just strings in the right places.
func TestPVCTierOverlayAgainstFleet(t *testing.T) {
	fleet := os.Getenv("KUBE_DC_TEST_FLEET")
	if fleet == "" {
		t.Skip("set KUBE_DC_TEST_FLEET to a Fleet checkout")
	}
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "infrastructure/object-storage"), os.DirFS(filepath.Join(fleet, "infrastructure/object-storage"))); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "clusters/storage-test/object-storage")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	o := pvcOptions(tieredSets())
	spec := o.ObjectStorage()
	spec.NoS3Exposure = true
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(objectStorageOverlayYAML("storage-test", spec)), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(kustomize, "build", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("Fleet render: %v\n%s", err, out)
	}
	vars := map[string]string{"CEPH_OSD_STORAGE_CLASS": "unity-osd", "CEPH_OSD_COUNT": "4", "CEPH_OSD_VOLUME_SIZE_GB": "2800"}
	for k, v := range o.Sets {
		vars[k] = v
	}
	docs := renderedDocs(t, fluxSubstitute(string(out), vars))

	cc := docs["CephCluster/rook-ceph"]
	sets := dig(t, cc, "spec", "storage", "storageClassDeviceSets").([]any)
	if len(sets) != 2 {
		t.Fatalf("want 2 device sets, got %d", len(sets))
	}
	// Rook v1.19 reads a PVC-backed set's CRUSH class from the data claim
	// template's crushDeviceClass annotation; a device-set field of that
	// name is outside the CRD schema and ignored.
	for i, want := range []string{"ssd", "hdd"} {
		if _, ok := sets[i].(map[string]any)["crushDeviceClass"]; ok {
			t.Fatalf("set %d carries crushDeviceClass as a device-set field", i)
		}
		if got := dig(t, sets[i], "volumeClaimTemplates", 0, "metadata", "annotations", "crushDeviceClass"); got != want {
			t.Fatalf("set %d data claim annotation crushDeviceClass = %#v, want %q", i, got, want)
		}
		if dig(t, sets[i], "volumeClaimTemplates", 0, "metadata", "name") != "data" {
			t.Fatalf("set %d data claim renamed", i)
		}
	}
	if dig(t, sets[0], "name") != "osd-template" || dig(t, sets[0], "count") != float64(4) {
		t.Fatalf("primary set: %v", sets[0])
	}
	capSet := sets[1]
	if dig(t, capSet, "name") != "osd-capacity" {
		t.Fatalf("capacity set: %v", capSet)
	}
	if c := dig(t, capSet, "count"); c != float64(6) {
		t.Fatalf("capacity count must decode as the number 6, got %#v", c)
	}
	if p := dig(t, capSet, "portable"); p != false {
		t.Fatalf("portable must render as bool false, got %#v", p)
	}
	claim := dig(t, capSet, "volumeClaimTemplates", 0, "spec")
	if dig(t, claim, "storageClassName") != "sc-osd" || dig(t, claim, "resources", "requests", "storage") != "6144Gi" || dig(t, claim, "volumeMode") != "Block" {
		t.Fatalf("capacity claim: %v", claim)
	}

	if dc := dig(t, docs["CephBlockPool/rbd-pool"], "spec", "deviceClass"); dc != "ssd" {
		t.Fatalf("rbd-pool deviceClass %v", dc)
	}
	store := docs["CephObjectStore/my-store"]
	if dig(t, store, "spec", "metadataPool", "deviceClass") != "ssd" || dig(t, store, "spec", "metadataPool", "replicated", "size") != float64(2) {
		t.Fatalf("metadata pool: %v", dig(t, store, "spec", "metadataPool"))
	}
	data := dig(t, store, "spec", "dataPool").(map[string]any)
	if _, ok := data["replicated"]; ok {
		t.Fatal("EC data pool must not keep the replicated block")
	}
	if dig(t, data, "deviceClass") != "hdd" || dig(t, data, "failureDomain") != "host" ||
		dig(t, data, "erasureCoded", "dataChunks") != float64(4) || dig(t, data, "erasureCoded", "codingChunks") != float64(2) {
		t.Fatalf("data pool: %v", data)
	}
	// The ceph-block class still points at rbd-pool.
	if p := dig(t, docs["StorageClass/ceph-block"], "parameters", "pool"); p != "rbd-pool" {
		t.Fatalf("ceph-block pool %v", p)
	}
}

// Every accepted spelling of CEPH_CAPACITY_OSD_PORTABLE must reach Rook as a
// YAML boolean; a substituted "1" would be an integer the CRD rejects.
func TestCapacityPortableIsLiteralBoolean(t *testing.T) {
	for spelling, want := range map[string]bool{"1": true, "yes": true, "on": true, "true": true, "0": false, "off": false, "": false} {
		s := tieredSets()
		s[KeyCephCapacityOSDPortable] = spelling
		set := capacityDeviceSet(parseCephTiers(s))
		if got, ok := set["portable"].(bool); !ok || got != want {
			t.Fatalf("%q: portable = %#v, want bool %v", spelling, set["portable"], want)
		}
		patches := pvcTierPatches(parseCephTiers(s))
		if strings.Contains(patches, KeyCephCapacityOSDPortable) {
			t.Fatalf("%q: portable must not be a Flux substitution:\n%s", spelling, patches)
		}
	}
}

// The managed-services budget derives from the OSDs that hold rbd-pool.
func TestManagedServicesBudgetFollowsBlockPoolClass(t *testing.T) {
	base := func(sets map[string]string) *InitOptions {
		return &InitOptions{InstallationKind: "kube-dc", ManagedServicesMode: "on", RookMode: RookCephPVC,
			CephStorageClass: "flash", CephOSDCount: 4, CephOSDVolumeSizeGB: 2800, Sets: sets}
	}
	small := func(blockClass string) map[string]string {
		s := map[string]string{KeyCephOSDDeviceClass: "ssd", KeyCephCapacityOSDStorageClass: "slow",
			KeyCephCapacityOSDCount: "2", KeyCephCapacityOSDVolumeSizeGB: "100"}
		if blockClass != "" {
			s[KeyCephBlockPoolDeviceClass] = blockClass
		}
		return s
	}
	for _, tc := range []struct {
		name string
		o    *InitOptions
		want string
	}{
		{"no tiers: primary set", base(nil), "1400Gi"},              // 4 x 2800 / 2 / 4
		{"pinned to the primary set", base(small("ssd")), "1400Gi"}, // unchanged
		{"pinned to a smaller second set", base(small("hdd")), "25Gi"},
		{"unpinned: both sets", base(small("")), "1425Gi"},
		{"primary count from --set", &InitOptions{InstallationKind: "kube-dc", ManagedServicesMode: "on", RookMode: RookCephPVC,
			CephStorageClass: "flash", Sets: map[string]string{"CEPH_OSD_COUNT": "4", "CEPH_OSD_VOLUME_SIZE_GB": "400"}}, "200Gi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveManagedServices(tc.o)
			if err != nil || got.StorageBudget != tc.want {
				t.Fatalf("budget %q, err %v; want %s", got.StorageBudget, err, tc.want)
			}
		})
	}
}

// The template set's own keys reach the manifest through Flux substitution.
func TestValidatePVCPrimaryKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    *InitOptions
		want string
	}{
		{"no overrides", pvcOptions(nil), ""},
		{"explicit zero count", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", Sets: map[string]string{"CEPH_OSD_COUNT": "0"}}, "--set CEPH_OSD_COUNT"},
		{"octal-looking count", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", Sets: map[string]string{"CEPH_OSD_COUNT": "010"}}, "--set CEPH_OSD_COUNT"},
		{"zero-padded size", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", Sets: map[string]string{"CEPH_OSD_VOLUME_SIZE_GB": "0200"}}, "--set CEPH_OSD_VOLUME_SIZE_GB"},
		{"decimal count", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", Sets: map[string]string{"CEPH_OSD_COUNT": "6"}}, ""},
		// The dedicated flag's value replaces the --set value in cluster-config.env.
		{"flag replaces zero", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "flash", CephOSDCount: 3, Sets: map[string]string{"CEPH_OSD_COUNT": "0"}}, ""},
		{"numeric class", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "123"}, "would reach Rook as a number"},
		{"yaml boolean class", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "yes"}, "would reach Rook as a boolean"},
		{"null class", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "null"}, "would reach Rook as a null"},
		{"ordinary class", &InitOptions{RookMode: RookCephPVC, CephStorageClass: "fast-san"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := strings.Join(validateObjectStorage(tc.o), "\n")
			if tc.want == "" {
				if errs != "" {
					t.Fatalf("unexpected: %s", errs)
				}
				return
			}
			if !strings.Contains(errs, tc.want) {
				t.Fatalf("want %q in %q", tc.want, errs)
			}
		})
	}
}

// Through Kustomize and Flux-style decoding: a decimal count stays that count,
// the 010 the validator now refuses really would become 8, and a literal
// StorageClass named 123 stays a string.
func TestPVCTierValuesSurviveSubstitution(t *testing.T) {
	fleet := os.Getenv("KUBE_DC_TEST_FLEET")
	if fleet == "" {
		t.Skip("set KUBE_DC_TEST_FLEET to a Fleet checkout")
	}
	kustomize, err := exec.LookPath("kustomize")
	if err != nil {
		t.Fatal(err)
	}
	render := func(sets map[string]string) map[string]any {
		root := t.TempDir()
		if err := os.CopyFS(filepath.Join(root, "infrastructure/object-storage"), os.DirFS(filepath.Join(fleet, "infrastructure/object-storage"))); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "clusters/storage-test/object-storage")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		spec := pvcOptions(sets).ObjectStorage()
		spec.NoS3Exposure = true
		if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(objectStorageOverlayYAML("storage-test", spec)), 0644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(kustomize, "build", dir).CombinedOutput()
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		vars := map[string]string{"CEPH_OSD_STORAGE_CLASS": "unity-osd", "CEPH_OSD_COUNT": "4"}
		for k, v := range sets {
			vars[k] = v
		}
		return dig(t, renderedDocs(t, fluxSubstitute(string(out), vars))["CephCluster/rook-ceph"], "spec", "storage", "storageClassDeviceSets", 1).(map[string]any)
	}
	s := tieredSets()
	s[KeyCephCapacityOSDCount], s[KeyCephRGWDataErasureCoding], s[KeyCephCapacityOSDStorageClass] = "10", "8+2", "123"
	if errs := validateObjectStorage(pvcOptions(s)); len(errs) > 0 {
		t.Fatalf("valid layout refused: %v", errs)
	}
	set := render(s)
	if c := dig(t, set, "count"); c != float64(10) {
		t.Fatalf("count 10 decoded as %#v", c)
	}
	if sc := dig(t, set, "volumeClaimTemplates", 0, "spec", "storageClassName"); sc != "123" {
		t.Fatalf("StorageClass 123 decoded as %#v, want the string", sc)
	}
	s[KeyCephCapacityOSDCount] = "010"
	if c := dig(t, render(s), "count"); c != float64(8) {
		t.Fatalf("expected YAML 1.1 to read 010 as 8 (the reason it is refused), got %#v", c)
	}
	if errs := validateObjectStorage(pvcOptions(s)); !strings.Contains(strings.Join(errs, "\n"), "without leading zeros") {
		t.Fatalf("010 not refused: %v", errs)
	}
}
