package clusterinit

import (
	"errors"
	"strings"
	"testing"
)

// validBase returns an InitOptions that passes Validate. Used as the
// table baseline — each test case mutates one or two fields.
func validBase() InitOptions {
	return InitOptions{
		Preset:         PresetCloudPublicVLAN,
		Mode:           ModeInstall,
		Name:           "cloudacropolis",
		Domain:         "kdc.acropolis.example.com",
		NodeExternalIP: "217.117.26.52",
		Email:          "ops@acropolis.example.com",
		FleetMode:      FleetExistingFleet,
		// Required for existing-fleet (review-pass — P2/P3 rule):
		// pointing at an existing path so Validate accepts it. Tests
		// that exercise the "missing repo" path mutate this to "".
		Repo:     "/tmp",
		RookMode: RookCephMultiNode,
		Yes:      true, // satisfies CI apply gate when NoTTY is set; harmless otherwise
	}
}

func TestValidate_BaselineOK(t *testing.T) {
	o := validBase()
	if err := o.Validate(); err != nil {
		t.Fatalf("baseline should validate, got %v", err)
	}
}

func TestValidate_Structural(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*InitOptions)
		wantSub string // substring that must appear in the error message
	}{
		{"missing name", func(o *InitOptions) { o.Name = "" }, "--name is required"},
		{"bad name uppercase", func(o *InitOptions) { o.Name = "Cloud" }, "--name"},
		{"bad name leading dash", func(o *InitOptions) { o.Name = "-cloud" }, "--name"},
		{"nested name OK", func(o *InitOptions) { o.Name = "cs/zrh" }, ""},
		{"missing domain", func(o *InitOptions) { o.Domain = "" }, "--domain is required"},
		{"bad domain URL", func(o *InitOptions) { o.Domain = "https://foo.example" }, "--domain"},
		{"bad domain no dot", func(o *InitOptions) { o.Domain = "localhost" }, "--domain"},
		{"missing IP", func(o *InitOptions) { o.NodeExternalIP = "" }, "--node-external-ip is required"},
		{"bad IP", func(o *InitOptions) { o.NodeExternalIP = "not-an-ip" }, "not a valid IP"},
		{"ipv6 IP", func(o *InitOptions) { o.NodeExternalIP = "2a0c:d880:1100::11" }, ""},
		{"missing email", func(o *InitOptions) { o.Email = "" }, "--email is required"},
		{"bad email", func(o *InitOptions) { o.Email = "not-an-email" }, "--email"},
		{"missing preset", func(o *InitOptions) { o.Preset = "" }, "--preset is required"},
		{"bad preset", func(o *InitOptions) { o.Preset = Preset("super-vlan") }, "--preset"},
		{"missing mode", func(o *InitOptions) { o.Mode = "" }, "--mode is required"},
		{"bad mode", func(o *InitOptions) { o.Mode = Mode("upgrade") }, "--mode"},
		{"missing fleet-mode", func(o *InitOptions) { o.FleetMode = "" }, "--fleet-mode is required"},
		{"bad fleet-mode", func(o *InitOptions) { o.FleetMode = FleetMode("bare-metal-fleet") }, "--fleet-mode"},
		{"missing rook-mode", func(o *InitOptions) { o.RookMode = "" }, "--rook-mode unset"},
		{"bad rook-mode", func(o *InitOptions) { o.RookMode = RookMode("hyperconverged") }, "--rook-mode"},
		{"rook-ceph-local needs osd-size", func(o *InitOptions) { o.RookMode = RookCephLocal; o.RookOSDSizeGB = 0 }, "rook-osd-size-gb"},
		{"rook-ceph-local with osd-size OK", func(o *InitOptions) { o.RookMode = RookCephLocal; o.RookOSDSizeGB = 500 }, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := validBase()
			tc.mutate(&o)
			err := o.Validate()
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q missing substring %q", err.Error(), tc.wantSub)
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("expected ErrValidation, got %v", err)
			}
		})
	}
}

func TestValidate_Addons(t *testing.T) {
	cases := []struct {
		name    string
		addons  []string
		wantErr bool
		wantSub string
	}{
		{"empty ok", nil, false, ""},
		{"single ok", []string{"metallb"}, false, ""},
		{"all known ok", []string{"metallb", "sso-google", "stripe-billing", "velero"}, false, ""},
		{"unknown rejected", []string{"foo-addon"}, true, "not in registry"},
		{"duplicate rejected", []string{"metallb", "metallb"}, true, "specified more than once"},
		{"mixed unknown + known", []string{"metallb", "foo-addon"}, true, "not in registry"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := validBase()
			o.Addons = tc.addons
			err := o.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error containing %q", tc.wantSub)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q missing substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidate_Sets(t *testing.T) {
	cases := []struct {
		name    string
		sets    map[string]string
		wantSub string
	}{
		{"empty ok", nil, ""},
		{"upper ok", map[string]string{"EXT_NET_VLAN_ID": "1103"}, ""},
		{"lowercase rejected", map[string]string{"domain": "foo"}, "SCREAMING_SNAKE_CASE"},
		{"mixed case rejected", map[string]string{"Foo_Bar": "x"}, "SCREAMING_SNAKE_CASE"},
		{"empty key rejected", map[string]string{"": "x"}, "--set key cannot be empty"},
		{"leading digit rejected", map[string]string{"1FOO": "x"}, "SCREAMING_SNAKE_CASE"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := validBase()
			o.Sets = tc.sets
			err := o.Validate()
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error with %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestValidate_NodeNICs(t *testing.T) {
	// M4-T11 review-pass — P2: `--node-nic` iface values are
	// written into infrastructure.yaml's ProviderNetwork patch
	// (M4-T11). They must pass the same NIC-name sanity check as
	// EXT_NET_INTERFACE so shell metacharacters and too-long
	// names can't reach disk.
	cases := []struct {
		name    string
		nics    map[string]string
		wantSub string
	}{
		// Happy paths.
		{"empty ok", nil, ""},
		{"single ok", map[string]string{"SRV5-Kub1": "enp1s0"}, ""},
		{"multi ok", map[string]string{
			"SRV5-Kub1": "enp1s0",
			"SRV6-Kub1": "bond0",
			"SRV7-Kub1": "eno2",
		}, ""},
		{"with dot", map[string]string{"SRV5-Kub1": "eth0.100"}, ""},
		// Failure paths.
		{"empty iface rejected", map[string]string{"SRV5-Kub1": ""}, "empty iface"},
		{"shell metachar rejected", map[string]string{"SRV5-Kub1": "enp1s0;rm"}, "unsupported character"},
		{"whitespace rejected", map[string]string{"SRV5-Kub1": "enp 1s0"}, "unsupported character"},
		{"too long rejected", map[string]string{"SRV5-Kub1": "this-interface-name-is-too-long"}, "IFNAMSIZ"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := validBase()
			o.NodeNICs = tc.nics
			err := o.Validate()
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error with %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestValidate_FleetModeNewRepo_NeedsGitHub(t *testing.T) {
	cases := []struct {
		name        string
		owner, repo string
		wantErr     error
	}{
		{"both missing", "", "", ErrFleetModeNewRepo},
		{"owner missing", "", "kube-dc-fleet", ErrFleetModeNewRepo},
		{"repo missing", "kube-dc", "", ErrFleetModeNewRepo},
		{"both present", "kube-dc", "kube-dc-fleet", nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := validBase()
			o.FleetMode = FleetNewRepo
			o.GitHubOwner = tc.owner
			o.GitHubRepo = tc.repo
			err := o.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_ModeAutoSafetyNet(t *testing.T) {
	// M4-T03 wired the auto-detector (cobra layer calls ResolveMode
	// BEFORE Validate), so the only way ModeAuto reaches Validate
	// now is a programmatic-construction bug. Validate refuses it
	// loudly as a safety net — surfaces ErrModeAutoUnresolved (the
	// old ErrModeAutoNotImplemented alias still works for one
	// release).
	o := validBase()
	o.Mode = ModeAuto
	err := o.Validate()
	if !errors.Is(err, ErrModeAutoUnresolved) {
		t.Fatalf("expected ErrModeAutoUnresolved, got %v", err)
	}
	// Deprecated alias still resolves so external errors.Is checks
	// don't break in the cobra-layer window.
	if !errors.Is(err, ErrModeAutoNotImplemented) {
		t.Errorf("deprecated alias ErrModeAutoNotImplemented should still match")
	}
}

func TestValidate_ExistingFleetRequiresRepo(t *testing.T) {
	// Review-pass — P2/P3: existing-fleet without --repo would
	// silently render a misleading plan with an empty prior-cluster
	// list. Refuse at Validate time.
	cases := []struct {
		name      string
		fleetMode FleetMode
		repo      string
		wantErr   error
	}{
		{"existing-fleet missing repo", FleetExistingFleet, "", ErrFleetModeExistingRepo},
		{"existing-fleet with repo OK", FleetExistingFleet, "/tmp", nil},
		{"new-repo OK without repo", FleetNewRepo, "", ErrFleetModeNewRepo}, // hits the new-repo rule (owner/repo missing) before the existing-fleet rule — that's fine
		{"existing-repo OK without repo", FleetExistingRepo, "", nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := validBase()
			o.FleetMode = tc.fleetMode
			o.Repo = tc.repo
			if tc.fleetMode == FleetNewRepo {
				// new-repo also needs github-owner+repo so the
				// validation message it triggers isn't the existing-
				// fleet one; that's the assertion below.
				o.GitHubOwner = ""
				o.GitHubRepo = ""
			}
			err := o.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_DryRunApplyPlanMutex(t *testing.T) {
	o := validBase()
	o.DryRun = true
	o.ApplyPlan = "/tmp/plan.json"
	err := o.Validate()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestValidate_PlanFileConflictsWithApplyPlan(t *testing.T) {
	o := validBase()
	o.ApplyPlan = "/tmp/applied.json"
	o.PlanFile = "/tmp/other.json"
	err := o.Validate()
	if err == nil || !strings.Contains(err.Error(), "--plan-file conflicts with --apply-plan") {
		t.Fatalf("expected plan-file conflict error, got %v", err)
	}

	// Same path is allowed — operator may pass both with identical
	// values from a script.
	o.PlanFile = o.ApplyPlan
	if err := o.Validate(); err != nil {
		t.Fatalf("matching --plan-file should be tolerated, got %v", err)
	}
}

func TestValidate_CIApplyGate(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*InitOptions)
		wantGate bool
	}{
		{"tty no flags", func(o *InitOptions) { o.NoTTY = false; o.Yes = false }, false},
		{"no-tty no satisfier", func(o *InitOptions) { o.NoTTY = true; o.Yes = false }, true},
		{"no-tty + yes", func(o *InitOptions) { o.NoTTY = true; o.Yes = true }, false},
		{"no-tty + apply-plan", func(o *InitOptions) { o.NoTTY = true; o.Yes = false; o.ApplyPlan = "/tmp/plan.json" }, false},
		{"no-tty + dry-run", func(o *InitOptions) { o.NoTTY = true; o.Yes = false; o.DryRun = true }, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := validBase()
			// Clear Yes before mutating so we test gate semantics
			// from a clean state.
			o.Yes = false
			tc.mutate(&o)
			err := o.Validate()
			if tc.wantGate {
				if !errors.Is(err, ErrApplyGate) {
					t.Fatalf("expected ErrApplyGate, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
		})
	}
}

func TestParseSetPairs(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    map[string]string
		wantErr string
	}{
		{"empty", nil, map[string]string{}, ""},
		{"single", []string{"FOO=bar"}, map[string]string{"FOO": "bar"}, ""},
		{"multi", []string{"FOO=bar", "BAZ=qux"}, map[string]string{"FOO": "bar", "BAZ": "qux"}, ""},
		{"trim whitespace", []string{"  FOO  =  bar  "}, map[string]string{"FOO": "bar"}, ""},
		{"missing equals", []string{"FOO"}, nil, "expected KEY=VALUE"},
		{"empty key", []string{"=bar"}, nil, "empty key"},
		{"empty value OK", []string{"FOO="}, map[string]string{"FOO": ""}, ""},
		{"value contains equals", []string{"FOO=a=b=c"}, map[string]string{"FOO": "a=b=c"}, ""},
		{"duplicate", []string{"FOO=bar", "FOO=baz"}, nil, "duplicate key"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSetPairs(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error with %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
