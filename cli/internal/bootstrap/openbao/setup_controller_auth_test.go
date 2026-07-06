package openbao

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeSetupBao records every privileged call SetupControllerAuth
// makes — enough to verify ordering, idempotence semantics, refresh
// mode skipping, and token-discipline (token presence checked; bytes
// never asserted-on because the engine treats them as opaque).
type fakeSetupBao struct {
	mu sync.Mutex

	// Recording surface — tests assert on these.
	enableCalls    []enableCall
	configureCalls []configureCall
	policyCalls    []policyCall
	roleCalls      []roleCall
	annotateCalls  []map[string]string

	// Error injection. First step that returns non-nil short-circuits.
	enableErr    error
	configureErr error
	policyErr    map[string]error // policy name -> err
	roleErr      map[string]error // "<mount>/<role>" -> err
	annotateErr  error

	// Defensive: SetupControllerAuth should never call RevokeSelf
	// directly (caller owns it). If it does, this counter trips and
	// tests fail loudly.
	revokeCalls int
}

type enableCall struct {
	tokenPresent bool
	mountType    string
	path         string
}

type configureCall struct {
	tokenPresent bool
	path         string
	cfg          ports.KubernetesAuthConfig
}

type policyCall struct {
	tokenPresent bool
	name         string
	hcl          string
}

type roleCall struct {
	tokenPresent bool
	mount        string
	role         string
	params       map[string]string
}

func (f *fakeSetupBao) EnableAuthPath(_ context.Context, token []byte, mountType, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enableCalls = append(f.enableCalls, enableCall{
		tokenPresent: len(token) > 0,
		mountType:    mountType,
		path:         path,
	})
	return f.enableErr
}

func (f *fakeSetupBao) ConfigureKubernetesAuth(_ context.Context, token []byte, path string, cfg ports.KubernetesAuthConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configureCalls = append(f.configureCalls, configureCall{
		tokenPresent: len(token) > 0,
		path:         path,
		cfg:          cfg,
	})
	return f.configureErr
}

func (f *fakeSetupBao) ApplyPolicy(_ context.Context, token []byte, name, hcl string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policyCalls = append(f.policyCalls, policyCall{
		tokenPresent: len(token) > 0,
		name:         name,
		hcl:          hcl,
	})
	if f.policyErr != nil {
		if err, ok := f.policyErr[name]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeSetupBao) WriteAuthRole(_ context.Context, token []byte, mount, role string, params map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Defensive copy so the caller scrubbing/mutating doesn't
	// corrupt our recording state.
	p := make(map[string]string, len(params))
	for k, v := range params {
		p[k] = v
	}
	f.roleCalls = append(f.roleCalls, roleCall{
		tokenPresent: len(token) > 0,
		mount:        mount,
		role:         role,
		params:       p,
	})
	if f.roleErr != nil {
		if err, ok := f.roleErr[mount+"/"+role]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeSetupBao) SetAnnotations(_ context.Context, _ string, kv map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cpy := make(map[string]string, len(kv))
	for k, v := range kv {
		cpy[k] = v
	}
	f.annotateCalls = append(f.annotateCalls, cpy)
	return f.annotateErr
}

// Unused-by-engine methods. Stubbed to satisfy the OpenBaoClient
// interface — if SetupControllerAuth ever starts calling one of
// these, the tests will catch the new dependency surface.
func (f *fakeSetupBao) Status(_ context.Context, _ string) (ports.BaoStatus, error) {
	return ports.BaoStatus{}, fmt.Errorf("fakeSetupBao.Status: not stubbed")
}
func (f *fakeSetupBao) PodList(_ context.Context) ([]string, error) {
	return nil, fmt.Errorf("fakeSetupBao.PodList: not stubbed")
}
func (f *fakeSetupBao) Unseal(_ context.Context, _ string, _ []byte) error {
	return fmt.Errorf("fakeSetupBao.Unseal: SetupControllerAuth must not unseal")
}
func (f *fakeSetupBao) RaftJoin(_ context.Context, _, _ string) error {
	return fmt.Errorf("fakeSetupBao.RaftJoin: SetupControllerAuth must not raft-join")
}
func (f *fakeSetupBao) GenerateRoot(_ context.Context, _ [][]byte) ([]byte, error) {
	return nil, fmt.Errorf("fakeSetupBao.GenerateRoot: SetupControllerAuth must not generate-root (caller owns)")
}
func (f *fakeSetupBao) RevokeSelf(_ context.Context, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeCalls++
	return nil
}
func (f *fakeSetupBao) GetAnnotation(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("fakeSetupBao.GetAnnotation: not stubbed")
}
func (f *fakeSetupBao) SetAnnotation(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("fakeSetupBao.SetAnnotation: SetupControllerAuth must use SetAnnotations (atomic batch)")
}

// fakeSetupK8s returns a canned CA bundle for the kube-root-ca.crt
// ConfigMap path and refuses everything else.
type fakeSetupK8s struct {
	caCert string
	caErr  error
}

func (f *fakeSetupK8s) GetConfigMapData(_ context.Context, ns, name, key string) (string, error) {
	if f.caErr != nil {
		return "", f.caErr
	}
	if ns == "kube-dc" && name == "kube-root-ca.crt" && key == "ca.crt" {
		return f.caCert, nil
	}
	return "", fmt.Errorf("fakeSetupK8s: unexpected configmap %s/%s key=%s", ns, name, key)
}

// Stubs for the rest of the K8sClient port. SetupControllerAuth only
// uses GetConfigMapData; if a future change reaches further the test
// suite fails fast.
func (f *fakeSetupK8s) DiscoverFluxGraph(context.Context) (ports.Graph, error) {
	panic("fakeSetupK8s: DiscoverFluxGraph not stubbed")
}
func (f *fakeSetupK8s) NodeLabels(context.Context) (map[string]map[string]string, error) {
	panic("fakeSetupK8s: NodeLabels not stubbed")
}
func (f *fakeSetupK8s) DeploymentImages(context.Context, string) (map[string]string, error) {
	panic("fakeSetupK8s: DeploymentImages not stubbed")
}
func (f *fakeSetupK8s) ListNamespaces(context.Context) ([]string, error) {
	panic("fakeSetupK8s: ListNamespaces not stubbed")
}
func (f *fakeSetupK8s) ListCRDs(context.Context) ([]string, error) {
	panic("fakeSetupK8s: ListCRDs not stubbed")
}
func (f *fakeSetupK8s) HelmReleaseChartVersions(context.Context) (map[string]string, error) {
	panic("fakeSetupK8s: HelmReleaseChartVersions not stubbed")
}
func (f *fakeSetupK8s) PodExec(context.Context, string, string, []string, []byte) ([]byte, error) {
	panic("fakeSetupK8s: PodExec not stubbed")
}
func (f *fakeSetupK8s) PodExecViaKubectl(context.Context, string, string, []string, []byte) ([]byte, error) {
	panic("fakeSetupK8s: PodExecViaKubectl not stubbed (engine should call OpenBao port, not direct K8s exec)")
}
func (f *fakeSetupK8s) GetServiceAnnotation(context.Context, string, string, string) (string, error) {
	panic("fakeSetupK8s: GetServiceAnnotation not stubbed")
}
func (f *fakeSetupK8s) SetServiceAnnotation(context.Context, string, string, string, string) error {
	panic("fakeSetupK8s: SetServiceAnnotation not stubbed")
}
func (f *fakeSetupK8s) SetServiceAnnotations(context.Context, string, string, map[string]string) error {
	panic("fakeSetupK8s: SetServiceAnnotations not stubbed (engine should call OpenBao.SetAnnotations)")
}

// --- helpers ---

func canonicalOpts(bao *fakeSetupBao, k8s *fakeSetupK8s, out *bytes.Buffer, mode RefreshMode) SetupControllerAuthOptions {
	opts := SetupControllerAuthOptions{
		Token:       []byte("test-root-token-abc"),
		RefreshMode: mode,
		OpenBao:     bao,
		K8s:         k8s,
	}
	// Avoid the typed-nil-pointer-stored-in-interface trap: only
	// assign Out when the caller supplied a non-nil *bytes.Buffer.
	if out != nil {
		opts.Out = out
	}
	return opts
}

// --- happy path ---

func TestSetupControllerAuth_RefreshFull_Hits6CallsInOrder(t *testing.T) {
	bao := &fakeSetupBao{}
	k8s := &fakeSetupK8s{caCert: "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n"}
	var out bytes.Buffer
	if err := SetupControllerAuth(context.Background(), canonicalOpts(bao, k8s, &out, RefreshFull)); err != nil {
		t.Fatalf("SetupControllerAuth: %v\n%s", err, out.String())
	}
	// 1 EnableAuthPath, 1 ConfigureKubernetesAuth, 2 ApplyPolicy, 2 WriteAuthRole, 1 SetAnnotations
	if len(bao.enableCalls) != 1 {
		t.Errorf("expected 1 EnableAuthPath call, got %d", len(bao.enableCalls))
	}
	if len(bao.configureCalls) != 1 {
		t.Errorf("expected 1 ConfigureKubernetesAuth call, got %d", len(bao.configureCalls))
	}
	if len(bao.policyCalls) != 2 {
		t.Errorf("expected 2 ApplyPolicy calls, got %d", len(bao.policyCalls))
	}
	if len(bao.roleCalls) != 2 {
		t.Errorf("expected 2 WriteAuthRole calls, got %d", len(bao.roleCalls))
	}
	if len(bao.annotateCalls) != 1 {
		t.Errorf("expected 1 SetAnnotations call, got %d", len(bao.annotateCalls))
	}
	// All calls received the token.
	for _, c := range bao.enableCalls {
		if !c.tokenPresent {
			t.Errorf("EnableAuthPath called with empty token")
		}
	}
	for _, c := range bao.configureCalls {
		if !c.tokenPresent {
			t.Errorf("ConfigureKubernetesAuth called with empty token")
		}
	}
	for _, c := range bao.policyCalls {
		if !c.tokenPresent {
			t.Errorf("ApplyPolicy %s called with empty token", c.name)
		}
	}
	for _, c := range bao.roleCalls {
		if !c.tokenPresent {
			t.Errorf("WriteAuthRole %s/%s called with empty token", c.mount, c.role)
		}
	}
	// SetupControllerAuth must NEVER call RevokeSelf (caller owns).
	if bao.revokeCalls != 0 {
		t.Errorf("SetupControllerAuth must not call RevokeSelf; got %d calls", bao.revokeCalls)
	}
}

func TestSetupControllerAuth_RefreshFull_ProperPathsAndNames(t *testing.T) {
	bao := &fakeSetupBao{}
	k8s := &fakeSetupK8s{caCert: "FAKE_CA"}
	if err := SetupControllerAuth(context.Background(), canonicalOpts(bao, k8s, nil, RefreshFull)); err != nil {
		t.Fatalf("SetupControllerAuth: %v", err)
	}
	if bao.enableCalls[0].mountType != "kubernetes" || bao.enableCalls[0].path != KubernetesAuthPath {
		t.Errorf("EnableAuthPath wrong args: %+v", bao.enableCalls[0])
	}
	if bao.configureCalls[0].path != KubernetesAuthPath {
		t.Errorf("ConfigureKubernetesAuth wrong path: %s", bao.configureCalls[0].path)
	}
	if bao.configureCalls[0].cfg.KubernetesHost != "https://kubernetes.default.svc" {
		t.Errorf("ConfigureKubernetesAuth wrong host: %s", bao.configureCalls[0].cfg.KubernetesHost)
	}
	if bao.configureCalls[0].cfg.KubernetesCACert != "FAKE_CA" {
		t.Errorf("ConfigureKubernetesAuth CA not propagated: %s", bao.configureCalls[0].cfg.KubernetesCACert)
	}
	if !bao.configureCalls[0].cfg.DisableISSValidation {
		t.Errorf("ConfigureKubernetesAuth must set DisableISSValidation=true on OpenBao 2.5.x")
	}
	// Policy names + role names match hardcoded constants
	if bao.policyCalls[0].name != ManagerPolicyName || bao.policyCalls[1].name != DBManagerPolicyName {
		t.Errorf("policy names wrong: %s, %s", bao.policyCalls[0].name, bao.policyCalls[1].name)
	}
	if bao.roleCalls[0].role != ManagerRoleName || bao.roleCalls[1].role != DBManagerRoleName {
		t.Errorf("role names wrong: %s, %s", bao.roleCalls[0].role, bao.roleCalls[1].role)
	}
	// Role params have correct SA bindings
	if bao.roleCalls[0].params["bound_service_account_names"] != ManagerSAName {
		t.Errorf("manager role SA name wrong: %v", bao.roleCalls[0].params)
	}
	if bao.roleCalls[1].params["bound_service_account_names"] != DBManagerSAName {
		t.Errorf("db-manager role SA name wrong: %v", bao.roleCalls[1].params)
	}
	// Annotations — Full mode stamps both:
	//   - AnnotationControllerAuthInstalled (RFC3339 timestamp)
	//   - AnnotationPolicyGeneration (M5-T07 compile-time int)
	if _, ok := bao.annotateCalls[0][AnnotationControllerAuthInstalled]; !ok {
		t.Errorf("controller-auth-installed annotation not stamped: %v", bao.annotateCalls[0])
	}
	if _, ok := bao.annotateCalls[0][AnnotationPolicyGeneration]; !ok {
		t.Errorf("policy-generation annotation not stamped: %v", bao.annotateCalls[0])
	}
}

// --- refresh-policy path ---

func TestSetupControllerAuth_RefreshPolicy_SkipsEnableConfigureAnnotate(t *testing.T) {
	bao := &fakeSetupBao{}
	k8s := &fakeSetupK8s{} // not consulted in RefreshPolicy mode
	if err := SetupControllerAuth(context.Background(), canonicalOpts(bao, k8s, nil, RefreshPolicy)); err != nil {
		t.Fatalf("SetupControllerAuth refresh: %v", err)
	}
	if len(bao.enableCalls) != 0 {
		t.Errorf("RefreshPolicy must skip EnableAuthPath; got %d calls", len(bao.enableCalls))
	}
	if len(bao.configureCalls) != 0 {
		t.Errorf("RefreshPolicy must skip ConfigureKubernetesAuth; got %d calls", len(bao.configureCalls))
	}
	// M5-T07: RefreshPolicy DOES stamp AnnotationPolicyGeneration
	// (closing drift is Refresh's whole purpose) but MUST NOT touch
	// AnnotationControllerAuthInstalled (that timestamp represents
	// the WHEN of the last Full install; Refresh doesn't re-run
	// auth-enable/-configure so the timestamp meaning is unchanged).
	if len(bao.annotateCalls) != 1 {
		t.Fatalf("RefreshPolicy must stamp exactly 1 annotate batch; got %d", len(bao.annotateCalls))
	}
	kv := bao.annotateCalls[0]
	if _, ok := kv[AnnotationPolicyGeneration]; !ok {
		t.Errorf("RefreshPolicy must stamp %s; batch=%v", AnnotationPolicyGeneration, kv)
	}
	if _, ok := kv[AnnotationControllerAuthInstalled]; ok {
		t.Errorf("RefreshPolicy must NOT stamp %s (Full-only); batch=%v", AnnotationControllerAuthInstalled, kv)
	}
	// Policies + roles ARE rewritten.
	if len(bao.policyCalls) != 2 {
		t.Errorf("RefreshPolicy must rewrite 2 policies; got %d", len(bao.policyCalls))
	}
	if len(bao.roleCalls) != 2 {
		t.Errorf("RefreshPolicy must rewrite 2 roles; got %d", len(bao.roleCalls))
	}
}

// --- error propagation ---

func TestSetupControllerAuth_EnableError_ShortCircuits(t *testing.T) {
	bao := &fakeSetupBao{enableErr: errors.New("simulated enable failure")}
	k8s := &fakeSetupK8s{caCert: "FAKE_CA"}
	err := SetupControllerAuth(context.Background(), canonicalOpts(bao, k8s, nil, RefreshFull))
	if err == nil || !strings.Contains(err.Error(), "enable auth/") {
		t.Fatalf("expected enable-error to propagate, got %v", err)
	}
	// Nothing downstream ran
	if len(bao.configureCalls)+len(bao.policyCalls)+len(bao.roleCalls)+len(bao.annotateCalls) != 0 {
		t.Errorf("downstream steps ran after enable error: configure=%d policy=%d role=%d annotate=%d",
			len(bao.configureCalls), len(bao.policyCalls), len(bao.roleCalls), len(bao.annotateCalls))
	}
}

func TestSetupControllerAuth_PolicyError_ShortCircuitsAtThatStep(t *testing.T) {
	bao := &fakeSetupBao{
		policyErr: map[string]error{ManagerPolicyName: errors.New("simulated manager policy failure")},
	}
	k8s := &fakeSetupK8s{caCert: "FAKE_CA"}
	err := SetupControllerAuth(context.Background(), canonicalOpts(bao, k8s, nil, RefreshFull))
	if err == nil || !strings.Contains(err.Error(), ManagerPolicyName) {
		t.Fatalf("expected manager policy error to propagate, got %v", err)
	}
	// enable + configure DID run; manager policy was attempted;
	// downstream (manager role, db-manager policy/role, annotate) didn't.
	if len(bao.enableCalls) != 1 || len(bao.configureCalls) != 1 {
		t.Errorf("enable + configure must run before policy: %d %d", len(bao.enableCalls), len(bao.configureCalls))
	}
	if len(bao.policyCalls) != 1 {
		t.Errorf("only manager policy should have been attempted: %d", len(bao.policyCalls))
	}
	if len(bao.roleCalls)+len(bao.annotateCalls) != 0 {
		t.Errorf("downstream steps ran after policy error: role=%d annotate=%d",
			len(bao.roleCalls), len(bao.annotateCalls))
	}
}

// --- validation ---

func TestSetupControllerAuth_EmptyToken_Rejected(t *testing.T) {
	bao := &fakeSetupBao{}
	k8s := &fakeSetupK8s{caCert: "FAKE_CA"}
	opts := canonicalOpts(bao, k8s, nil, RefreshFull)
	opts.Token = []byte{}
	err := SetupControllerAuth(context.Background(), opts)
	if !errors.Is(err, ErrSetupEmptyToken) {
		t.Fatalf("expected ErrSetupEmptyToken, got %v", err)
	}
	// No port methods called
	if len(bao.enableCalls)+len(bao.policyCalls)+len(bao.roleCalls) != 0 {
		t.Errorf("empty token must short-circuit before any port call")
	}
}

func TestSetupControllerAuth_NilOpenBao_Rejected(t *testing.T) {
	k8s := &fakeSetupK8s{caCert: "FAKE_CA"}
	opts := SetupControllerAuthOptions{
		Token:       []byte("tok"),
		RefreshMode: RefreshFull,
		OpenBao:     nil,
		K8s:         k8s,
	}
	err := SetupControllerAuth(context.Background(), opts)
	if !errors.Is(err, ErrSetupMissingDependency) {
		t.Fatalf("expected ErrSetupMissingDependency, got %v", err)
	}
}

func TestSetupControllerAuth_NilK8s_Rejected(t *testing.T) {
	bao := &fakeSetupBao{}
	opts := SetupControllerAuthOptions{
		Token:       []byte("tok"),
		RefreshMode: RefreshFull,
		OpenBao:     bao,
		K8s:         nil,
	}
	err := SetupControllerAuth(context.Background(), opts)
	if !errors.Is(err, ErrSetupMissingDependency) {
		t.Fatalf("expected ErrSetupMissingDependency, got %v", err)
	}
}

// --- ConfigureKubernetesAuth CA loading ---

func TestSetupControllerAuth_MissingCABundle_Errors(t *testing.T) {
	bao := &fakeSetupBao{}
	k8s := &fakeSetupK8s{caCert: ""} // empty ca.crt — well-formed-absent path
	err := SetupControllerAuth(context.Background(), canonicalOpts(bao, k8s, nil, RefreshFull))
	if err == nil || !strings.Contains(err.Error(), "no ca.crt") {
		t.Fatalf("expected error about missing ca.crt, got %v", err)
	}
}

// --- HCL embed sanity ---

func TestEmbeddedHCL_HasExpectedPaths(t *testing.T) {
	// Spot-check critical capabilities so an accidental HCL edit that
	// drops a path doesn't ship silently. These are the load-bearing
	// per-Org grants the tenant reconcilers depend on.
	for _, want := range []string{
		`path "sys/namespaces/*"`,        // Org provisioning
		`path "+/transit/*"`,             // M3 KMS
		`path "+/pki_int/*"`,             // M2 cert-manager
		`path "+/database/roles/+"`,      // M4 DBCP
		`path "+/database/static-roles/+"`,
		`path "+/database/static-creds/+"`,
	} {
		if !strings.Contains(ManagerPolicyHCL, want) {
			t.Errorf("ManagerPolicyHCL missing %q (HCL extraction may have dropped it)", want)
		}
	}
	for _, want := range []string{
		`path "+/transit/encrypt/+"`,     // db-manager DEK wrap
		`path "+/transit/decrypt/+"`,     // db-manager DEK unwrap
		`path "+/sys/mounts/database"`,   // M4-T01 database engine mount
		`path "+/database/config/+"`,     // M4-T01 per-DB config
	} {
		if !strings.Contains(DBManagerPolicyHCL, want) {
			t.Errorf("DBManagerPolicyHCL missing %q (HCL extraction may have dropped it)", want)
		}
	}
}
