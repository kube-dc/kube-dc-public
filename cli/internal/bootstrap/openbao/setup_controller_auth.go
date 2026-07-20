package openbao

// M5-T08 — `kube-dc bootstrap openbao setup-controller-auth`.
//
// Shared engine for the controller-tier OpenBao setup (replaces
// hack/openbao-setup-controller-auth.sh). Called from two places:
//
//   1. init Phase C, via postInitSetupControllerAuth, with the root
//      token already captured by the init ceremony.
//   2. The standalone `bootstrap openbao setup-controller-auth`
//      subcommand, which decrypts shares + runs GenerateRoot to
//      obtain a fresh short-lived root token.
//
// In both cases the CALLER owns the root token's defer RevokeSelf —
// see SetupControllerAuthOptions.Token. This engine never logs the
// token, never retains it past return, and never passes it on argv
// (the adapter wraps every privileged write in a read-r-tok stdin
// pattern).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ExtractThresholdShares re-exports the unseal engine's share parser
// for callers outside the package (the setup-controller-auth
// subcommand reuses the same SOPS-decrypted-blob → 3-share extraction
// path). Caller MUST scrub the returned slices.
func ExtractThresholdShares(decrypted []byte) ([][]byte, error) {
	return extractThresholdShares(decrypted)
}

// RefreshMode chooses between full controller-auth setup (Full —
// enable + configure + policies + roles + annotate) and an
// HCL-rotation pass (Refresh — only policies + roles).
//
// Refresh skips enable + configure because those operations are
// one-shot at install time; rewriting them would either no-op (already
// enabled) or churn the apiserver-bound CA bundle (which doesn't
// change across chart upgrades). The policies + roles ARE rewritten
// even on Refresh because chart upgrades may extend HCL paths or
// rotate role params (SA names, TTLs, namespaces).
//
// M5-T07: The AnnotationPolicyGeneration stamp is written on BOTH
// modes — the whole point of Refresh is to close policy-generation
// drift, so it MUST bump the installed marker. Pre-M5-T07 the
// stamp was Full-only; that meant `refresh-policy` runs left the
// stamp untouched and drift never cleared. See policy_generation.go.
type RefreshMode int

const (
	RefreshFull   RefreshMode = iota // enable + configure + policies + roles + annotate
	RefreshPolicy                    // policies + roles only (skip enable + configure; keep annotation)
)

// SetupControllerAuthOptions parameterises the engine. Token is the
// caller's responsibility — they MUST defer RevokeSelf on it BEFORE
// calling this function. The engine never owns the token's
// lifetime.
type SetupControllerAuthOptions struct {
	Token       []byte      // root token, in-memory only
	RefreshMode RefreshMode // Full | Refresh
	OpenBao     ports.OpenBaoClient
	K8s         ports.K8sClient
	Out         io.Writer
}

// ErrSetupMissingDependency surfaces when SetupControllerAuth is
// called with a nil port adapter or other dependency. Programmer
// error — the wrappers (init Phase C + the subcommand RunE) populate
// all required fields. Distinct from runtime / adapter errors so
// tests can fail loudly if anything regresses.
var ErrSetupMissingDependency = errors.New("openbao setup-controller-auth: missing dependency")

// ErrSetupEmptyToken is returned when caller passes a zero-length
// token. Indicates a caller bug — the in-memory `secrets.Buffer`
// returned an empty slice without raising its own error.
var ErrSetupEmptyToken = errors.New("openbao setup-controller-auth: empty token (caller bug)")

// SetupControllerAuth runs the controller-tier OpenBao setup using
// the caller-supplied root token. Idempotent: each step swallows
// "already enabled" / "already exists" and returns nil. Returns the
// first non-idempotent error encountered; the caller's deferred
// RevokeSelf still fires.
//
// Steps (in order):
//  1. EnableAuthPath  (RefreshFull only) — enable kubernetes auth at k8s-host
//  2. ConfigureKubernetesAuth (RefreshFull only) — point at the apiserver
//  3. ApplyPolicy     — kube-dc-controller-manager (cross-Org admin)
//  4. WriteAuthRole   — kube-dc-controller-manager bound to kube-dc-manager SA
//  5. ApplyPolicy     — db-manager (Transit encrypt/decrypt + database engine)
//  6. WriteAuthRole   — db-manager bound to kube-dc-db-manager SA
//  7. ApplyPolicy + WriteAuthRole — read-only Raft snapshot job
//  8. SetAnnotations — installed policy generation and Full-mode timestamp
//
// On RefreshPolicy, steps 1 and 2 are skipped and the original Full-mode
// installed timestamp is preserved; policy generation is still updated.
func SetupControllerAuth(ctx context.Context, opts SetupControllerAuthOptions) error {
	if err := validateSetupOptions(opts); err != nil {
		return err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	if opts.RefreshMode == RefreshFull {
		// Step 1
		fmt.Fprintf(out, "[openbao] enabling auth/%s (idempotent)\n", KubernetesAuthPath)
		if err := opts.OpenBao.EnableAuthPath(ctx, opts.Token, "kubernetes", KubernetesAuthPath); err != nil {
			return fmt.Errorf("enable auth/%s: %w", KubernetesAuthPath, err)
		}

		// Step 2
		cfg, err := loadK8sAuthConfig(ctx, opts.K8s)
		if err != nil {
			return fmt.Errorf("load k8s-auth config: %w", err)
		}
		fmt.Fprintf(out, "[openbao] configuring auth/%s against in-cluster apiserver\n", KubernetesAuthPath)
		if err := opts.OpenBao.ConfigureKubernetesAuth(ctx, opts.Token, KubernetesAuthPath, cfg); err != nil {
			return fmt.Errorf("configure auth/%s: %w", KubernetesAuthPath, err)
		}
	}

	// Step 3
	fmt.Fprintf(out, "[openbao] applying policy %s\n", ManagerPolicyName)
	if err := opts.OpenBao.ApplyPolicy(ctx, opts.Token, ManagerPolicyName, ManagerPolicyHCL); err != nil {
		return fmt.Errorf("apply policy %s: %w", ManagerPolicyName, err)
	}

	// Step 4
	fmt.Fprintf(out, "[openbao] writing role auth/%s/role/%s (bound to %s/%s)\n",
		KubernetesAuthPath, ManagerRoleName, ManagerSAns, ManagerSAName)
	if err := opts.OpenBao.WriteAuthRole(ctx, opts.Token, KubernetesAuthPath, ManagerRoleName, ManagerRoleParams()); err != nil {
		return fmt.Errorf("write role %s: %w", ManagerRoleName, err)
	}

	// Step 5
	fmt.Fprintf(out, "[openbao] applying policy %s\n", DBManagerPolicyName)
	if err := opts.OpenBao.ApplyPolicy(ctx, opts.Token, DBManagerPolicyName, DBManagerPolicyHCL); err != nil {
		return fmt.Errorf("apply policy %s: %w", DBManagerPolicyName, err)
	}

	// Step 6
	fmt.Fprintf(out, "[openbao] writing role auth/%s/role/%s (bound to %s/%s)\n",
		KubernetesAuthPath, DBManagerRoleName, DBManagerSAns, DBManagerSAName)
	if err := opts.OpenBao.WriteAuthRole(ctx, opts.Token, KubernetesAuthPath, DBManagerRoleName, DBManagerRoleParams()); err != nil {
		return fmt.Errorf("write role %s: %w", DBManagerRoleName, err)
	}

	// Snapshot policy/role: short-lived Kubernetes-auth token scoped only to
	// GET sys/storage/raft/snapshot. No static token Secret is provisioned.
	fmt.Fprintf(out, "[openbao] applying policy %s\n", SnapshotPolicyName)
	if err := opts.OpenBao.ApplyPolicy(ctx, opts.Token, SnapshotPolicyName, SnapshotPolicyHCL); err != nil {
		return fmt.Errorf("apply policy %s: %w", SnapshotPolicyName, err)
	}
	fmt.Fprintf(out, "[openbao] writing role auth/%s/role/%s (bound to %s/%s)\n",
		KubernetesAuthPath, SnapshotRoleName, SnapshotSAns, SnapshotSAName)
	if err := opts.OpenBao.WriteAuthRole(ctx, opts.Token, KubernetesAuthPath, SnapshotRoleName, SnapshotRoleParams()); err != nil {
		return fmt.Errorf("write role %s: %w", SnapshotRoleName, err)
	}

	// Step 7 — annotation stamps. Two markers:
	//
	//   - AnnotationControllerAuthInstalled (RFC3339 timestamp of last
	//     Full run) — Full mode only. Refresh runs preserve the
	//     original timestamp (Refresh doesn't re-run auth-enable /
	//     -configure, so the "when did controller-auth become
	//     truthfully installed" answer is unchanged).
	//
	//   - AnnotationPolicyGeneration (M5-T07 compile-time generation
	//     integer) — BOTH modes. Refresh's whole purpose is to close
	//     policy-generation drift; skipping the stamp on Refresh
	//     would leave drift persistent even after a successful
	//     refresh-policy run. Full mode also stamps so the initial
	//     install-time annotation is present.
	stamps := map[string]string{
		AnnotationPolicyGeneration: strconv.Itoa(PolicyGeneration),
	}
	if opts.RefreshMode == RefreshFull {
		stamp := time.Now().UTC().Format(time.RFC3339)
		stamps[AnnotationControllerAuthInstalled] = stamp
		fmt.Fprintf(out, "[openbao] stamping %s=%s on svc/openbao\n", AnnotationControllerAuthInstalled, stamp)
	}
	fmt.Fprintf(out, "[openbao] stamping %s=%d on svc/openbao\n", AnnotationPolicyGeneration, PolicyGeneration)
	if err := opts.OpenBao.SetAnnotations(ctx, "openbao", stamps); err != nil {
		return fmt.Errorf("annotate svc/openbao: %w", err)
	}

	fmt.Fprintln(out, "[openbao] controller-auth setup complete")
	return nil
}

// loadK8sAuthConfig reads the in-cluster kube-apiserver CA bundle
// from the `kube-root-ca.crt` ConfigMap that lives in every namespace
// (a default surface kubelet maintains). The bundle is the canonical
// CA the apiserver presents — every workload's SA token validates
// against it, so OpenBao's K8s-auth must trust it too.
//
// kubernetes_host is hardcoded to https://kubernetes.default.svc —
// the in-cluster Service that resolves via cluster DNS. Matches the
// shell script's line 252 and every Kubernetes-auth deployment
// guide we've published.
//
// disable_iss_validation=true is required on OpenBao 2.5.x — the
// default issuer expected by bao doesn't match the apiserver's
// actual issuer claim shape. Setting it false makes login 403 with
// "invalid issuer".
func loadK8sAuthConfig(ctx context.Context, k8s ports.K8sClient) (ports.KubernetesAuthConfig, error) {
	const (
		caNamespace = "kube-dc"
		caCMName    = "kube-root-ca.crt"
		caCMKey     = "ca.crt"
	)
	caCert, err := k8s.GetConfigMapData(ctx, caNamespace, caCMName, caCMKey)
	if err != nil {
		return ports.KubernetesAuthConfig{}, fmt.Errorf("read %s/%s key=%s: %w", caNamespace, caCMName, caCMKey, err)
	}
	if caCert == "" {
		return ports.KubernetesAuthConfig{}, fmt.Errorf("configmap %s/%s has no %s key (cluster malformed?)", caNamespace, caCMName, caCMKey)
	}
	return ports.KubernetesAuthConfig{
		KubernetesHost:       "https://kubernetes.default.svc",
		KubernetesCACert:     caCert,
		DisableISSValidation: true,
	}, nil
}

func validateSetupOptions(opts SetupControllerAuthOptions) error {
	if len(opts.Token) == 0 {
		return ErrSetupEmptyToken
	}
	if opts.OpenBao == nil {
		return fmt.Errorf("%w: OpenBao", ErrSetupMissingDependency)
	}
	if opts.K8s == nil {
		return fmt.Errorf("%w: K8s", ErrSetupMissingDependency)
	}
	return nil
}
