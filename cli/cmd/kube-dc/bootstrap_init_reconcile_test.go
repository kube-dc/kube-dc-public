package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// --- fakes ---

type fakeFlux struct {
	ks chan ports.KustomizationEvent
	hr chan ports.HelmReleaseEvent
}

func (f *fakeFlux) WatchKustomizations(context.Context) (<-chan ports.KustomizationEvent, error) {
	return f.ks, nil
}
func (f *fakeFlux) WatchHelmReleases(context.Context) (<-chan ports.HelmReleaseEvent, error) {
	return f.hr, nil
}

type fakeSOPS struct {
	data []byte
	err  error
}

func (f fakeSOPS) Decrypt(context.Context, string) ([]byte, error) { return f.data, f.err }

type fakeAnno struct {
	bootstrap string
	auth      string
	err       error
}

type recordingStepReporter struct {
	started map[clusterinit.StepID]bool
	done    map[clusterinit.StepID]error
	skipped map[clusterinit.StepID]string
}

func newRecordingStepReporter() *recordingStepReporter {
	return &recordingStepReporter{
		started: map[clusterinit.StepID]bool{},
		done:    map[clusterinit.StepID]error{},
		skipped: map[clusterinit.StepID]string{},
	}
}

func (*recordingStepReporter) Plan([]clusterinit.Step) {}
func (r *recordingStepReporter) Start(id clusterinit.StepID) {
	r.started[id] = true
}
func (r *recordingStepReporter) Done(id clusterinit.StepID, err error) {
	r.done[id] = err
}
func (r *recordingStepReporter) Skip(id clusterinit.StepID, reason string) {
	r.skipped[id] = reason
}

func (f fakeAnno) GetServiceAnnotation(_ context.Context, _, _, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if key == openbao.AnnotationBootstrapFinalized {
		return f.bootstrap, nil
	}
	if key == openbao.AnnotationControllerAuthInstalled {
		return f.auth, nil
	}
	return "", nil
}

// --- reconcile watch ---

func TestWatchReconcile_ConvergesOnPlatformReady(t *testing.T) {
	f := &fakeFlux{
		ks: make(chan ports.KustomizationEvent, 8),
		hr: make(chan ports.HelmReleaseEvent, 8),
	}
	go func() {
		f.hr <- ports.HelmReleaseEvent{Namespace: "kube-system", Name: "kube-ovn", Ready: true}
		f.ks <- ports.KustomizationEvent{Name: "infra-cni", Ready: true}
		f.ks <- ports.KustomizationEvent{Name: "platform", Ready: true}
	}()
	var buf bytes.Buffer
	if err := watchReconcile(context.Background(), &buf, f, 5*time.Second); err != nil {
		t.Fatalf("expected convergence, got %v", err)
	}
	if !strings.Contains(buf.String(), "platform converged") {
		t.Fatalf("expected 'platform converged' in output, got:\n%s", buf.String())
	}
}

func TestWatchReconcile_TimesOut(t *testing.T) {
	f := &fakeFlux{
		ks: make(chan ports.KustomizationEvent), // never fed → never converges
		hr: make(chan ports.HelmReleaseEvent),
	}
	var buf bytes.Buffer
	err := watchReconcile(context.Background(), &buf, f, 40*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error when platform never converges")
	}
	if !strings.Contains(err.Error(), "still reconciling") {
		t.Fatalf("expected 'still reconciling' error, got %v", err)
	}
}

func TestRunReconcileWatchWithGPU_ResumeCompletesFromReadySnapshot(t *testing.T) {
	f := &fakeFlux{
		ks: make(chan ports.KustomizationEvent, 8),
		hr: make(chan ports.HelmReleaseEvent, 1),
	}
	// Platform arrives first: GPU-enabled convergence must still wait for the
	// generated ownership/operator/HAMi chain instead of declaring success.
	f.ks <- ports.KustomizationEvent{Name: "platform", Ready: true}
	f.ks <- ports.KustomizationEvent{Name: "gpu-node-mode", Ready: true}
	f.ks <- ports.KustomizationEvent{Name: "gpu-operator", Ready: true}
	f.ks <- ports.KustomizationEvent{Name: "hami", Ready: true}
	rep := newRecordingStepReporter()
	oldBudget := reconcileBudget
	reconcileBudget = time.Second
	t.Cleanup(func() { reconcileBudget = oldBudget })

	err := runReconcileWatchWithGPU(context.Background(), &bytes.Buffer{}, f, rep, clusterinit.GPUConfig{
		Platform: clusterinit.GPUPlatformEnabled, HAMiEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range append(clusterinit.GPUInstallStepIDs(true), clusterinit.StepReconcile) {
		doneErr, done := rep.done[id]
		if !rep.started[id] || !done || doneErr != nil {
			t.Fatalf("step %s start=%t done=%t error=%v", id, rep.started[id], done, doneErr)
		}
	}
}

func TestRunReconcileWatchWithGPU_InterruptedMarksUnresolvedStages(t *testing.T) {
	f := &fakeFlux{
		ks: make(chan ports.KustomizationEvent, 2),
		hr: make(chan ports.HelmReleaseEvent),
	}
	f.ks <- ports.KustomizationEvent{Name: "gpu-node-mode", Ready: true}
	rep := newRecordingStepReporter()
	oldBudget := reconcileBudget
	reconcileBudget = 30 * time.Millisecond
	t.Cleanup(func() { reconcileBudget = oldBudget })

	err := runReconcileWatchWithGPU(context.Background(), &bytes.Buffer{}, f, rep, clusterinit.GPUConfig{
		Platform: clusterinit.GPUPlatformEnabled, HAMiEnabled: true,
	})
	if err == nil {
		t.Fatal("expected interrupted reconciliation to time out")
	}
	if got, ok := rep.done[clusterinit.StepGPUInventory]; !ok || got != nil {
		t.Fatalf("ready inventory stage=%v present=%t", got, ok)
	}
	for _, id := range []clusterinit.StepID{clusterinit.StepGPUOperator, clusterinit.StepGPUHAMi, clusterinit.StepGPUProduct, clusterinit.StepReconcile} {
		if got, ok := rep.done[id]; !ok || got == nil {
			t.Fatalf("unresolved step %s error=%v present=%t", id, got, ok)
		}
	}
}

// --- keycloak password extraction ---

func TestReadKeycloakAdminPassword(t *testing.T) {
	o := &clusterinit.InitOptions{Repo: "/tmp/fleet", Name: "eu-dc1"}
	tests := []struct {
		name    string
		yaml    string
		want    string
		wantErr bool
	}{
		{
			name: "stringData plaintext",
			yaml: "apiVersion: v1\nkind: Secret\nstringData:\n  KEYCLOAK_ADMIN_PASSWORD: s3cret-pw\n  OTHER: x\n",
			want: "s3cret-pw",
		},
		{
			name: "data base64",
			yaml: "data:\n  KEYCLOAK_ADMIN_PASSWORD: " + base64.StdEncoding.EncodeToString([]byte("b64-pw")) + "\n",
			want: "b64-pw",
		},
		{
			name:    "missing",
			yaml:    "stringData:\n  OTHER: x\n",
			wantErr: true,
		},
		{
			// P3: invalid base64 in Secret .data is a malformed secret,
			// not a password — must error, not return the raw value.
			name:    "data invalid base64",
			yaml:    "data:\n  KEYCLOAK_ADMIN_PASSWORD: \"not-valid-base64!!!\"\n",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readKeycloakAdminPassword(context.Background(), o, fakeSOPS{data: []byte(tc.yaml)})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAccessBlock(t *testing.T) {
	o := &clusterinit.InitOptions{Repo: "/tmp/fleet", Name: "eu-dc1", Domain: "eu-dc1.example.com"}
	sops := fakeSOPS{data: []byte("stringData:\n  KEYCLOAK_ADMIN_PASSWORD: shown-pw\n")}
	urls := []string{
		"https://login.eu-dc1.example.com/admin",
		"https://console.eu-dc1.example.com",
		"https://grafana.eu-dc1.example.com",
		"https://flux.eu-dc1.example.com",
		"https://admin.eu-dc1.example.com",
	}

	// withPassword=true (interactive terminal) → the real password shows.
	withPw := accessBlock(context.Background(), o, sops, true, false, false, false)
	for _, want := range append([]string{"shown-pw"}, urls...) {
		if !strings.Contains(withPw, want) {
			t.Fatalf("withPassword block missing %q:\n%s", want, withPw)
		}
	}

	// withPassword=false (plain/CI) → MUST NOT leak the password; shows
	// the kubectl retrieval hint instead. (P1 fix.)
	noPw := accessBlock(context.Background(), o, sops, false, false, false, false)
	if strings.Contains(noPw, "shown-pw") {
		t.Fatalf("plain/CI access block leaked the password:\n%s", noPw)
	}
	if !strings.Contains(noPw, "kubectl -n keycloak get secret keycloak") {
		t.Fatalf("plain/CI block missing kubectl retrieval hint:\n%s", noPw)
	}
	for _, want := range urls {
		if !strings.Contains(noPw, want) {
			t.Fatalf("plain/CI block missing %q:\n%s", want, noPw)
		}
	}

	// Deferred specificity (P2 fix): only the deferred step's rerun cmd.
	obOnly := accessBlock(context.Background(), o, sops, true, true /*ob*/, false /*kc*/, false)
	if !strings.Contains(obOnly, "openbao status eu-dc1") {
		t.Fatalf("openbao-deferred block missing safe status check:\n%s", obOnly)
	}
	if strings.Contains(obOnly, "keycloak init eu-dc1") {
		t.Fatalf("openbao-deferred block wrongly told operator to rerun keycloak:\n%s", obOnly)
	}
	kcOnly := accessBlock(context.Background(), o, sops, true, false /*ob*/, true /*kc*/, false)
	if !strings.Contains(kcOnly, "keycloak init eu-dc1") {
		t.Fatalf("keycloak-deferred block missing keycloak rerun:\n%s", kcOnly)
	}
	if strings.Contains(kcOnly, "openbao init eu-dc1") {
		t.Fatalf("keycloak-deferred block wrongly told operator to rerun openbao:\n%s", kcOnly)
	}
	both := accessBlock(context.Background(), o, sops, true, true, true, false)
	if !strings.Contains(both, "openbao status eu-dc1") || !strings.Contains(both, "keycloak init eu-dc1") {
		t.Fatalf("both-deferred block missing a rerun cmd:\n%s", both)
	}

	// Decrypt failure with withPassword=true → kubectl hint, no crash.
	fallback := accessBlock(context.Background(), o, fakeSOPS{err: context.DeadlineExceeded}, true, false, false, false)
	if !strings.Contains(fallback, "kubectl -n keycloak get secret keycloak") {
		t.Fatalf("expected kubectl fallback on decrypt failure:\n%s", fallback)
	}
}

// --- openbao finalized (resume gate) ---

func TestOpenBaoResumeMarkers(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name               string
		fake               fakeAnno
		wantInit, wantAuth bool
		wantErr            bool
	}{
		{"complete", fakeAnno{bootstrap: "2026-07-08T00:00:00Z", auth: "2026-07-08T00:10:00Z"}, true, true, false},
		{"controller auth missing", fakeAnno{bootstrap: "2026-07-08T00:00:00Z"}, true, false, false},
		{"fresh", fakeAnno{}, false, false, false},
		{"unreadable", fakeAnno{err: context.DeadlineExceeded}, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			init, auth, err := openBaoResumeMarkers(ctx, tc.fake)
			if init != tc.wantInit || auth != tc.wantAuth || (err != nil) != tc.wantErr {
				t.Fatalf("got initialized=%v auth=%v error=%v, want initialized=%v auth=%v error=%v", init, auth, err, tc.wantInit, tc.wantAuth, tc.wantErr)
			}
		})
	}
	o := &clusterinit.InitOptions{Name: "example", Repo: "/fleet"}
	status := accessBlock(ctx, o, fakeSOPS{}, false, true, false, true)
	if !strings.Contains(status, "openbao setup-controller-auth example") || strings.Contains(status, "openbao init example") {
		t.Fatalf("partial resume needs controller-auth recovery, got:\n%s", status)
	}
}

func TestRunOpenBaoResumeOrInitFailClosed(t *testing.T) {
	tests := []struct {
		name              string
		state             fakeAnno
		wantErr           bool
		wantWait, wantRun int
	}{
		{name: "fresh", state: fakeAnno{}, wantWait: 1, wantRun: 1},
		{name: "complete", state: fakeAnno{bootstrap: "done", auth: "done"}},
		{name: "controller auth missing", state: fakeAnno{bootstrap: "done"}, wantErr: true},
		{name: "inconsistent", state: fakeAnno{auth: "done"}, wantErr: true},
		{name: "unreadable", state: fakeAnno{err: context.DeadlineExceeded}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			waits, runs := 0, 0
			var out bytes.Buffer
			err := runOpenBaoResumeOrInit(context.Background(), &out, "example", tc.state,
				func() error { waits++; return nil }, func() error { runs++; return nil })
			if (err != nil) != tc.wantErr || waits != tc.wantWait || runs != tc.wantRun {
				t.Fatalf("err=%v waits=%d init runs=%d; want err=%v waits=%d runs=%d", err, waits, runs, tc.wantErr, tc.wantWait, tc.wantRun)
			}
		})
	}
}
