package ports

import "context"

// OpenBaoClient is the contract for post-init OpenBao operations the
// bootstrap engine performs directly (via `kubectl exec` to the active
// pod). **Initialisation is NOT on this interface**; see the NB below.
//
// The real adapter shells out to `bao` inside the active OpenBao pod via
// K8sClient.PodExec. Active-pod detection (the first pod reporting `HA Mode
// active` from `bao status`) is cached for 30s by the adapter to avoid
// re-discovering on every call.
//
// The mock adapter returns canned data from scenario YAML (`openbao-sealed`,
// `openbao-uninit`, `openbao-policy-stale`, …).
//
// **NB: there is no Init method.** Initialisation is owned by
// `bootstrap/openbao-init.sh` (single source of truth for the Shamir-key
// generation contract, sentinel-payload emission, plaintext-on-disk
// cleanup, and root-token revocation). M5-T01 drives it via ScriptRunner
// + a SentinelCallback that captures the JSON payload into a
// `secrets.Buffer` (M0-T04). Adding a parallel Go init path would create
// two sources of truth for unseal-key generation — exactly the failure
// mode agent rule 7 of the plan is meant to prevent.
// **Secret material as []byte, not string.** Shares and root tokens are
// scrub-able only when held as `[]byte` (Go strings are immutable and
// the runtime is free to copy them anywhere). Methods that accept or
// return secret material on this interface use `[]byte` so callers can
// load them into a `secrets.Buffer` (M0-T04) and `Scrub()` after use.
// Adapters MUST NOT convert these to strings internally for any path
// that survives the method call (`string(share)` is OK if the resulting
// string is consumed within the same statement and never assigned to
// a long-lived variable).
type OpenBaoClient interface {
	// Status returns `bao status` for the named pod.
	Status(ctx context.Context, pod string) (BaoStatus, error)

	// PodList returns the OpenBao pod names in the `openbao` namespace,
	// in deterministic order (openbao-0, openbao-1, …).
	PodList(ctx context.Context) ([]string, error)

	// Unseal runs `bao operator unseal <share>` on the named pod once.
	// Caller invokes this 3× per sealed pod with 3 distinct shares.
	// Returns nil on success even if the pod was already unsealed
	// (idempotent — operationally useful when re-running after a
	// partial unseal).
	//
	// `share` is the raw share bytes (typically base64-encoded ASCII
	// as `bao` emits them). Caller scrubs the slice after use; the
	// adapter must not retain a reference.
	Unseal(ctx context.Context, pod string, share []byte) error

	// RaftJoin runs `bao operator raft join <leaderURL>` on the named
	// pod. Used to bring a fresh follower pod into the leader's Raft
	// cluster before it can be unsealed with the leader's shares.
	// Idempotent: an already-joined pod returns success.
	//
	// This closes the OrderedReady race in HA Raft mode: the
	// StatefulSet creates follower pods only after openbao-0 is
	// Ready, and those followers come up with empty storage
	// (Initialized=false, TotalShares=0). They must join the leader's
	// cluster before the shared unseal shares mean anything —
	// `bao operator unseal` against a non-joined follower exits 1
	// with "security barrier not initialized" because the follower
	// doesn't yet know about the shamir config.
	//
	// leaderURL is the in-cluster Service-DNS address of the leader
	// pod, typically "http://openbao-0.openbao-internal:8200".
	RaftJoin(ctx context.Context, pod, leaderURL string) error

	// GenerateRoot walks the `bao operator generate-root` ceremony with
	// the supplied shares (3-of-5 Shamir threshold) and returns the
	// generated single-use root token as raw bytes. Caller MUST scrub
	// both the input slice elements and the returned token after use,
	// and MUST defer RevokeSelf on the returned token before scrub.
	GenerateRoot(ctx context.Context, shares [][]byte) (token []byte, err error)

	// RevokeSelf revokes the supplied token. Idempotent (404 on an
	// already-revoked token is success). `token` is the raw token
	// bytes from GenerateRoot; adapter must not retain a reference
	// after this call.
	RevokeSelf(ctx context.Context, token []byte) error

	// ApplyPolicy / EnableAuthPath / ConfigureKubernetesAuth /
	// WriteAuthRole are the privileged OpenBao writes the controller-
	// auth setup needs (replaces hack/openbao-setup-controller-auth.sh).
	// Each takes a short-lived root token from GenerateRoot as its
	// first arg after ctx. The CALLER MUST defer RevokeSelf on that
	// token BEFORE the first privileged call, so any failure path
	// still revokes. Adapters MUST NOT retain a reference to the
	// token beyond the method body — same in-memory-only discipline
	// as RevokeSelf. Token goes via stdin + VAULT_TOKEN env inside
	// the in-pod shell wrapper, never argv.
	//
	// All four are idempotent: "already enabled" / "already exists"
	// is swallowed and returns nil (matches the shell script's
	// `|| true` pattern).
	EnableAuthPath(ctx context.Context, token []byte, mountType, path string) error
	ConfigureKubernetesAuth(ctx context.Context, token []byte, path string, opts KubernetesAuthConfig) error
	ApplyPolicy(ctx context.Context, token []byte, name, hcl string) error
	WriteAuthRole(ctx context.Context, token []byte, mount, role string, params map[string]string) error

	// GetAnnotation / SetAnnotation read/write one annotation on the
	// `openbao` Service. The bootstrap engine uses these for the
	// two operational markers:
	//   - kube-dc.com/openbao-bootstrap-finalized = <rfc3339>
	//   - kube-dc.com/openbao-controller-auth-installed = <rfc3339>
	//
	// (`svc` is the Service name — typically "openbao".)
	GetAnnotation(ctx context.Context, svc, key string) (string, error)
	SetAnnotation(ctx context.Context, svc, key, value string) error

	// SetAnnotations applies a batch of annotations on the Service in
	// a single patch — atomic from the apiserver's perspective. Used
	// by init Phase C to stamp both markers (bootstrap-finalized AND
	// controller-auth-installed) without an intermediate one-key-set
	// observable state.
	SetAnnotations(ctx context.Context, svc string, kv map[string]string) error
}

// KubernetesAuthConfig is the parameter bundle for
// ConfigureKubernetesAuth — fed into `bao write auth/<path>/config`.
// KubernetesCACert is a multi-line PEM bundle; the adapter pipes it
// via stdin into a one-shot file inside the pod (it can't ride argv)
// then references it with the `@/path` indirection bao accepts.
type KubernetesAuthConfig struct {
	KubernetesHost       string // e.g. "https://kubernetes.default.svc"
	KubernetesCACert     string // PEM bundle (multi-line)
	DisableISSValidation bool   // true on OpenBao 2.5.x (issuer drift)
}

// BaoStatus mirrors the fields from `bao status` we actually consume.
// Verbatim names from the bao CLI output where possible.
type BaoStatus struct {
	Pod          string
	Initialized  bool
	Sealed       bool
	Version      string // e.g. "2.5.3"
	HAMode       string // "active" | "standby" | "" (single-node)
	RaftIndex    uint64 // 0 when not using raft storage
	ActiveNodeID string // populated on standby pods; empty on the active node
}
