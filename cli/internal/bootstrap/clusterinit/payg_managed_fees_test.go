package clusterinit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCensusImage = "shalb/kube-dc-fee-census:writer-renewal.20261027.1@sha256:785c67a0ebaa2001d78c8165f5544667da07802a65fbb536117a242d5b1cb8ec"

// paygWiredEnv is cluster-config.env after --payg wired the installation,
// carrying the starter's fee census pin.
const paygWiredEnv = paygBaseEnv + "PAYG_ENABLED=true\nPAYG_INSTALLATION_UID=11111111-2222-4333-8444-555555555555\nPAYG_INSTALLATION_BINDING=c1/example.com\n" + PAYGFeeCensusImageKey + "=" + testCensusImage + "\n"

func writeManagedFeesStarter(t *testing.T, fleet string) {
	t.Helper()
	full := filepath.Join(fleet, paygManagedFeesBundlePath, "kustomization.yaml")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - scheduled-evidence.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func managedFeesFleet(t *testing.T, env string) string {
	t.Helper()
	fleet := paygFleet(t, env)
	writeManagedFeesStarter(t, fleet)
	return fleet
}

func TestWritePAYGManagedFees_DisabledWritesNothing(t *testing.T) {
	fleet := managedFeesFleet(t, paygWiredEnv)
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	changed, err := WritePAYGManagedFees(fleet, "c1", false, nil)
	if err != nil || changed {
		t.Fatalf("disabled: changed=%v %v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(clusterDir, paygManagedFeesLayerFileName)); !os.IsNotExist(err) {
		t.Fatal("disabled wrote the layer")
	}
	if readFile(t, filepath.Join(clusterDir, "cluster-config.env")) != paygWiredEnv || readFile(t, filepath.Join(clusterDir, "kustomization.yaml")) != scaffoldRootKustomization {
		t.Fatal("disabled changed the overlay")
	}
}

func TestWritePAYGManagedFees_PreparesASuspendedLayerOnce(t *testing.T) {
	fleet := managedFeesFleet(t, paygWiredEnv)
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	var out strings.Builder
	changed, err := WritePAYGManagedFees(fleet, "c1", true, &out)
	if err != nil || !changed {
		t.Fatalf("changed=%v %v", changed, err)
	}
	layer := readFile(t, filepath.Join(clusterDir, paygManagedFeesLayerFileName))
	if layer != paygManagedFeesLayerYAML(true) || !strings.Contains(layer, "\n  suspend: true\n") {
		t.Fatalf("layer:\n%s", layer)
	}
	if !strings.Contains(layer, "value: ${"+PAYGFeeCensusImageKey+"}") || strings.Count(layer, "- target:") != 2 {
		t.Fatalf("the census and the renewal must run the census image:\n%s", layer)
	}
	kust := readFile(t, filepath.Join(clusterDir, "kustomization.yaml"))
	if !paygKustomizationHas([]byte(kust), paygManagedFeesLayerFileName) {
		t.Fatalf("kustomization.yaml:\n%s", kust)
	}
	env := readFile(t, filepath.Join(clusterDir, "cluster-config.env"))
	if envValue(env, paygFeeCensusSuspendKey) != "false" || envValue(env, PAYGFeeCensusImageKey) != testCensusImage {
		t.Fatalf("env:\n%s", env)
	}
	for _, k := range paygFeeScopeKeys {
		if envValue(env, k) != "" {
			t.Fatalf("%s written at bootstrap", k)
		}
	}
	if !strings.Contains(out.String(), "SUSPENDED") || !strings.Contains(out.String(), "-render-scope") {
		t.Fatalf("output:\n%s", out.String())
	}
	// A re-run, and one after the operator unsuspended the layer, keep it.
	for _, layer := range []string{paygManagedFeesLayerYAML(true), paygManagedFeesLayerYAML(false)} {
		if err := os.WriteFile(filepath.Join(clusterDir, paygManagedFeesLayerFileName), []byte(layer), 0o644); err != nil {
			t.Fatal(err)
		}
		changed, err := WritePAYGManagedFees(fleet, "c1", true, nil)
		if err != nil || changed {
			t.Fatalf("re-run changed=%v %v", changed, err)
		}
		if readFile(t, filepath.Join(clusterDir, paygManagedFeesLayerFileName)) != layer || readFile(t, filepath.Join(clusterDir, "kustomization.yaml")) != kust || readFile(t, filepath.Join(clusterDir, "cluster-config.env")) != env {
			t.Fatal("a re-run changed the overlay")
		}
	}
}

func TestWritePAYGManagedFees_RefusalsLeaveTheOverlayAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    string
		setup  func(t *testing.T, fleet string)
		want   string
		bundle bool
	}{
		{"no bundle in the starter", paygWiredEnv, nil, "ships the managed-fee bundle", false},
		{"no PAYG wiring", paygBaseEnv + PAYGFeeCensusImageKey + "=" + testCensusImage + "\n", nil, "needs the PAYG wiring", true},
		{"partner billing", strings.Replace(paygWiredEnv, "BILLING_PROVIDER=stripe", "BILLING_PROVIDER=stripe\nCLOUDSIGMA_CHART_VERSION=0.1.0", 1), nil, "billed by a partner", true},
		{"no census pin", strings.Replace(paygWiredEnv, PAYGFeeCensusImageKey+"="+testCensusImage+"\n", "", 1), nil, "is not pinned", true},
		{"undigested census pin", strings.Replace(paygWiredEnv, testCensusImage, "shalb/kube-dc-fee-census:latest", 1), nil, "not digest-pinned", true},
		{"customised layer", paygWiredEnv, func(t *testing.T, fleet string) {
			p := filepath.Join(fleet, "clusters", "c1", paygManagedFeesLayerFileName)
			if err := os.WriteFile(p, []byte(strings.Replace(paygManagedFeesLayerYAML(false), "interval: 10m", "interval: 1m", 1)), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "only spec.suspend may change", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fleet := paygFleet(t, tc.env)
			if tc.bundle {
				writeManagedFeesStarter(t, fleet)
			}
			if tc.setup != nil {
				tc.setup(t, fleet)
			}
			clusterDir := filepath.Join(fleet, "clusters", "c1")
			layerBefore, _ := os.ReadFile(filepath.Join(clusterDir, paygManagedFeesLayerFileName))
			changed, err := WritePAYGManagedFees(fleet, "c1", true, nil)
			if err == nil || changed || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got changed=%v %v", tc.want, changed, err)
			}
			layerAfter, _ := os.ReadFile(filepath.Join(clusterDir, paygManagedFeesLayerFileName))
			if string(layerAfter) != string(layerBefore) || readFile(t, filepath.Join(clusterDir, "cluster-config.env")) != tc.env || readFile(t, filepath.Join(clusterDir, "kustomization.yaml")) != scaffoldRootKustomization {
				t.Fatal("a refusal changed the overlay")
			}
		})
	}
}

func TestValidatePAYGManagedFees(t *testing.T) {
	servicesOn := func(o *InitOptions) {
		o.InstallationKind = "kube-dc"
		o.ManagedServicesMode = "on"
		o.ServicesDatabaseClass = "ceph-block"
		o.ServicesStorageBudget = "100Gi"
		o.ServicesEgressProbeURLs = []string{"https://example.com"}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*InitOptions)
		want   string
	}{
		{"off", func(*InitOptions) {}, ""},
		{"on with PAYG and services", func(o *InitOptions) { servicesOn(o); o.PAYG = true; o.PAYGManagedFees = true }, ""},
		{"without PAYG", func(o *InitOptions) { servicesOn(o); o.PAYGManagedFees = true }, "requires --payg"},
		{"without managed services", func(o *InitOptions) {
			o.InstallationKind = "kube-dc"
			o.ManagedServicesMode = "off"
			o.PAYG = true
			o.PAYGManagedFees = true
		}, "requires managed services"},
		{"scope via --set", func(o *InitOptions) { o.Sets["PAYG_FEE_SCOPE_JSON"] = "{}" }, "rendered from the running installation"},
		{"undigested census image", func(o *InitOptions) { o.Sets[PAYGFeeCensusImageKey] = "shalb/kube-dc-fee-census:latest" }, "digest-pinned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &InitOptions{Name: "east1", Domain: "kdc.east1.example.com", Sets: map[string]string{}}
			tc.mutate(o)
			errs := strings.Join(validatePAYGManagedFees(o), "; ")
			if tc.want == "" && errs != "" {
				t.Fatalf("unexpected: %s", errs)
			}
			if tc.want != "" && !strings.Contains(errs, tc.want) {
				t.Fatalf("want %q, got %q", tc.want, errs)
			}
		})
	}
}

// Scaffold end to end: the managed-fee layer follows the PAYG wiring and is
// absent without the flag.
func TestScaffold_PAYGManagedFees(t *testing.T) {
	for _, fees := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[fees], func(t *testing.T) {
			fakeSopsOnPath(t)
			fixPAYGClock(t)
			repo := t.TempDir()
			writeStarterScaffoldSources(t, repo)
			writePAYGStarter(t, repo)
			writeManagedFeesStarter(t, repo)
			sets := paygSets("stripe")
			sets[PAYGFeeCensusImageKey] = testCensusImage
			var out bytes.Buffer
			err := Scaffold(context.Background(), ScaffoldOptions{
				Plan:            &Plan{ClusterName: "east1", Domain: "kdc.east1.example.com", Preset: PresetCloudPublicVLAN},
				FleetRepo:       repo,
				NodeExternalIP:  "203.0.113.52",
				Sets:            sets,
				PAYG:            true,
				PAYGManagedFees: fees,
				Runner:          paygAddClusterRunner(t, repo),
				Out:             &out,
			})
			if err != nil {
				t.Fatalf("Scaffold: %v\n%s", err, out.String())
			}
			clusterDir := filepath.Join(repo, "clusters", "east1")
			_, layerErr := os.Stat(filepath.Join(clusterDir, paygManagedFeesLayerFileName))
			kust := readFile(t, filepath.Join(clusterDir, "kustomization.yaml"))
			env := readFile(t, filepath.Join(clusterDir, "cluster-config.env"))
			if !fees {
				if !os.IsNotExist(layerErr) || strings.Contains(kust, paygManagedFeesLayerFileName) || envValue(env, paygFeeCensusSuspendKey) != "" {
					t.Fatal("managed-fee wiring without --payg-managed-fees")
				}
				return
			}
			if layerErr != nil || !paygKustomizationHas([]byte(kust), paygManagedFeesLayerFileName) || envValue(env, paygFeeCensusSuspendKey) != "false" {
				t.Fatalf("managed-fee layer not prepared: %v\n%s\n%s", layerErr, kust, env)
			}
		})
	}
}

func TestManagedFeesPlanAndSavedConfigKeepTheOptIn(t *testing.T) {
	o := validBase()
	o.PAYG = true
	o.InstallationKind = "kube-dc"
	o.ManagedServicesMode = "on"
	o.RookMode = RookCephPVC
	o.CephStorageClass = "local-path"
	o.ServicesStorageBudget = "100Gi"
	o.Sets = paygSets("stripe")
	o.Sets[PAYGFeeCensusImageKey] = testCensusImage
	without, err := BuildPlan(&o, FleetState{})
	if err != nil {
		t.Fatal(err)
	}
	o.PAYGManagedFees = true
	with, err := BuildPlan(&o, FleetState{})
	if err != nil {
		t.Fatal(err)
	}
	if without.InputHash == with.InputHash {
		t.Fatal("managed fees must participate in apply drift protection")
	}
	found := false
	for _, f := range with.FilesToWrite {
		if strings.HasSuffix(f.Path, "/"+paygManagedFeesLayerFileName) {
			found = true
		}
	}
	if !found {
		t.Fatal("reviewed plan omits the managed-fee layer")
	}
	config := ExportMap(&o)
	if config[KeyPAYGManagedFees] != "true" {
		t.Fatal("saved configuration drops managed fees")
	}
	var restored InitOptions
	warnings := ImportMap(&restored, config, func(string) bool { return false })
	if !restored.PAYGManagedFees || !restored.PAYG {
		t.Fatalf("saved opt-in did not round trip: %v", warnings)
	}
	var explicit InitOptions
	ImportMap(&explicit, config, func(flag string) bool { return flag == "payg-managed-fees" })
	if explicit.PAYGManagedFees {
		t.Fatal("saved configuration overrides explicit --payg-managed-fees=false")
	}
}

// A source installation's runtime config is not an install specification.
// Cloning it cannot carry writer/payer authority or fail the explicit --set guard.
func TestManagedFeesCloneDropsLiveScopeAndActivation(t *testing.T) {
	keys := append(append([]string{}, paygFeeScopeKeys...),
		"PAYG_FEE_ADAPTER", "PAYG_FEE_ORG_UIDS", "PAYG_FEE_PAYABLE_ENROLLMENT",
		"PAYG_FEE_CENSUS_SUSPEND", "PAYG_FEE_PAYABLE_SUSPEND", "PAYG_FEE_RENEWAL_SUSPEND",
		PAYGFeeCensusImageKey)
	sibling := map[string]string{PAYGEnabledKey: "true", PAYGInstallationUIDKey: "11111111-2222-4333-8444-555555555555"}
	for _, key := range keys {
		sibling[key] = "old-installation-value"
	}
	for _, fees := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[fees], func(t *testing.T) {
			o := validBase()
			o.PAYG, o.PAYGManagedFees = fees, fees
			o.InstallationKind, o.ManagedServicesMode = "kube-dc", "on"
			o.RookMode, o.CephStorageClass = RookCephPVC, "local-path"
			o.ServicesStorageBudget = "100Gi"
			o.Sets = paygSets("stripe")
			// An explicit release pin wins; the source installation's pin is denied.
			o.Sets[PAYGFeeCensusImageKey] = testCensusImage
			ignored := ImportMap(&o, sibling, func(flag string) bool { return flag == "payg" || flag == "payg-managed-fees" })
			if o.PAYG != fees || o.PAYGManagedFees != fees {
				t.Fatal("cloned runtime changed explicit opt-ins")
			}
			for _, key := range keys {
				if key == PAYGFeeCensusImageKey {
					if o.Sets[key] != testCensusImage {
						t.Fatal("source image replaced selected release pin")
					}
				} else if _, exists := o.Sets[key]; exists {
					t.Fatalf("copied runtime authority %s", key)
				}
				if !strings.Contains(strings.Join(ignored, ","), key) {
					t.Fatalf("%s is not reported ignored", key)
				}
			}
			if errs := validatePAYGManagedFees(&o); len(errs) != 0 {
				t.Fatalf("ordinary clone fails fee validation: %v", errs)
			}
			if _, err := BuildPlan(&o, FleetState{}); err != nil {
				t.Fatalf("ordinary clone cannot plan: %v", err)
			}
		})
	}
}
