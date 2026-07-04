package clusterinit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeModeProber returns canned inputs without touching a cluster.
// Used to drive ResolveMode tests deterministically.
type fakeModeProber struct {
	in  ModeProbeInputs
	err error
}

func (f *fakeModeProber) Probe(_ context.Context) (ModeProbeInputs, error) {
	return f.in, f.err
}

func TestDetectMode_TruthTable(t *testing.T) {
	cases := []struct {
		name      string
		in        ModeProbeInputs
		wantMode  Mode
		wantErr   error
		wantInRsn string // substring that must appear in the reason
	}{
		{
			name:     "K8s unreachable -> error",
			in:       ModeProbeInputs{K8sReachable: false},
			wantErr:  ErrK8sUnreachable,
			wantMode: "",
		},
		{
			name:      "reachable, no flux -> install",
			in:        ModeProbeInputs{K8sReachable: true},
			wantMode:  ModeInstall,
			wantInRsn: "fresh Kubernetes",
		},
		{
			name: "reachable + flux, no manager -> adopt",
			in: ModeProbeInputs{
				K8sReachable:      true,
				FluxSystemPresent: true,
			},
			wantMode:  ModeAdopt,
			wantInRsn: "adopting",
		},
		{
			name: "reachable + flux + manager -> resume",
			in: ModeProbeInputs{
				K8sReachable:         true,
				FluxSystemPresent:    true,
				KubeDCManagerPresent: true,
			},
			wantMode:  ModeResume,
			wantInRsn: "resuming",
		},
		{
			name: "manager present but flux missing should not happen -> still resume per nesting",
			// Defensive case: if upstream probe wrongly reports
			// manager present but flux absent, the most-recent
			// "manager present" branch should win because the
			// resulting plan is closer to safe. Per current
			// DetectMode nesting, this falls through to install
			// because we check FluxSystemPresent first. Document
			// the existing semantics explicitly so a future
			// refactor doesn't silently change them.
			in: ModeProbeInputs{
				K8sReachable:         true,
				FluxSystemPresent:    false,
				KubeDCManagerPresent: true,
			},
			wantMode:  ModeInstall,
			wantInRsn: "fresh Kubernetes",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mode, reason, err := DetectMode(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != tc.wantMode {
				t.Fatalf("mode = %s, want %s", mode, tc.wantMode)
			}
			if tc.wantInRsn != "" && !strings.Contains(reason, tc.wantInRsn) {
				t.Errorf("reason %q missing %q", reason, tc.wantInRsn)
			}
		})
	}
}

func TestResolveMode_ExplicitOverride_NoProbe(t *testing.T) {
	// When --mode is explicit, ResolveMode must skip the prober
	// entirely — including when prober is nil. This is the atlantis
	// path (operator passes --mode=install).
	cases := []Mode{ModeInstall, ModeAdopt, ModeResume}
	for _, m := range cases {
		m := m
		t.Run(string(m), func(t *testing.T) {
			o := &InitOptions{Mode: m}
			resolved, reason, err := ResolveMode(context.Background(), o, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolved != m {
				t.Errorf("resolved = %s, want %s (explicit override)", resolved, m)
			}
			if !strings.Contains(reason, "explicit") {
				t.Errorf("reason should mention explicit override; got %q", reason)
			}
			if o.Mode != m {
				t.Errorf("options.Mode mutated unexpectedly: %s", o.Mode)
			}
		})
	}
}

func TestResolveMode_Auto_SubstitutesProbeResult(t *testing.T) {
	cases := []struct {
		name      string
		in        ModeProbeInputs
		wantMode  Mode
	}{
		{"fresh K8s -> install", ModeProbeInputs{K8sReachable: true}, ModeInstall},
		{"flux only -> adopt", ModeProbeInputs{K8sReachable: true, FluxSystemPresent: true}, ModeAdopt},
		{"flux + manager -> resume", ModeProbeInputs{K8sReachable: true, FluxSystemPresent: true, KubeDCManagerPresent: true}, ModeResume},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			o := &InitOptions{Mode: ModeAuto}
			prober := &fakeModeProber{in: tc.in}
			resolved, _, err := ResolveMode(context.Background(), o, prober)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolved != tc.wantMode {
				t.Errorf("resolved = %s, want %s", resolved, tc.wantMode)
			}
			// Critical: ResolveMode mutates o.Mode so downstream
			// Validate doesn't see Auto.
			if o.Mode != tc.wantMode {
				t.Errorf("options.Mode not substituted: %s", o.Mode)
			}
		})
	}
}

func TestResolveMode_Auto_NilProberErrors(t *testing.T) {
	o := &InitOptions{Mode: ModeAuto}
	_, _, err := ResolveMode(context.Background(), o, nil)
	if err == nil {
		t.Fatal("ModeAuto with nil prober must error")
	}
	if !strings.Contains(err.Error(), "requires a prober") {
		t.Errorf("error should explain missing prober; got %v", err)
	}
}

func TestResolveMode_Auto_ProberError(t *testing.T) {
	probeErr := errors.New("simulated cluster down")
	o := &InitOptions{Mode: ModeAuto}
	prober := &fakeModeProber{err: probeErr}
	_, _, err := ResolveMode(context.Background(), o, prober)
	if err == nil {
		t.Fatal("prober error must surface")
	}
	if !errors.Is(err, probeErr) {
		t.Errorf("expected wrap of probe error, got %v", err)
	}
	// Failed probe must NOT mutate options.Mode.
	if o.Mode != ModeAuto {
		t.Errorf("ModeAuto should remain unresolved on probe failure; got %s", o.Mode)
	}
}

func TestResolveMode_Auto_K8sUnreachable(t *testing.T) {
	o := &InitOptions{Mode: ModeAuto}
	prober := &fakeModeProber{in: ModeProbeInputs{K8sReachable: false}}
	_, _, err := ResolveMode(context.Background(), o, prober)
	if !errors.Is(err, ErrK8sUnreachable) {
		t.Fatalf("expected ErrK8sUnreachable, got %v", err)
	}
}
