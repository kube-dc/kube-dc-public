package mock

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// OpenBaoClient maintains in-memory seal state across calls so that
// Unseal can flip a pod from sealed=true to sealed=false and a
// subsequent Status reflects the change. This is the only mock with
// non-trivial mutating state; everything else is read-only per scenario.
//
// Concurrency: the share-custody + unseal flows in M5 are sequential
// by design (operator confirms, we unseal pod-by-pod). A single mutex
// guards `pods` against concurrent test access.
type OpenBaoClient struct {
	scenario *Scenario

	mu          sync.Mutex
	pods        map[string]*podState
	annotations map[string]string

	// activeRootTokens is the set of tokens GenerateRoot returned that
	// have not yet been revoked. RevokeSelf flips the entry. Used to
	// catch "caller forgot defer RevokeSelf" patterns in tests via the
	// ActiveRootTokens() inspector.
	activeRootTokens map[string]bool

	// M5-T08 controller-auth setup state. Tests inspect via the
	// EnabledAuthPaths / KubernetesAuthConfigs / Policies / AuthRoles
	// accessors below.
	enabledAuthPaths     map[string]string                       // path -> mountType
	kubernetesAuthCfgs   map[string]ports.KubernetesAuthConfig   // path -> config
	policies             map[string]string                       // name -> hcl
	authRoles            map[string]map[string]string            // "mount/role" -> params
}

type podState struct {
	sealed      bool
	initialized bool
	version     string
	haMode      string
	raftIndex   uint64
}

func NewOpenBaoClient(s *Scenario) *OpenBaoClient {
	c := &OpenBaoClient{
		scenario:           s,
		pods:               map[string]*podState{},
		annotations:        map[string]string{},
		activeRootTokens:   map[string]bool{},
		enabledAuthPaths:   map[string]string{},
		kubernetesAuthCfgs: map[string]ports.KubernetesAuthConfig{},
		policies:           map[string]string{},
		authRoles:          map[string]map[string]string{},
	}
	if s != nil && s.OpenBao != nil {
		for _, p := range s.OpenBao.Pods {
			c.pods[p.Name] = &podState{
				sealed:      p.Sealed,
				initialized: s.OpenBao.Initialized, // seeded scenario-wide; RaftJoin can flip per-pod
				version:     p.Version,
				haMode:      p.HAMode,
				raftIndex:   p.RaftIndex,
			}
		}
		if s.OpenBao.BootstrapFinalizedAnnotation != "" {
			c.annotations["kube-dc.com/openbao-bootstrap-finalized"] = s.OpenBao.BootstrapFinalizedAnnotation
		}
		if s.OpenBao.ControllerAuthAnnotation != "" {
			c.annotations["kube-dc.com/openbao-controller-auth-installed"] = s.OpenBao.ControllerAuthAnnotation
		}
	}
	return c
}

// ActiveRootTokens returns the set of tokens GenerateRoot has handed
// out that haven't been revoked yet. Test-only helper for asserting
// `defer RevokeSelf` discipline.
func (c *OpenBaoClient) ActiveRootTokens() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.activeRootTokens))
	for tok, active := range c.activeRootTokens {
		if active {
			out = append(out, tok)
		}
	}
	sort.Strings(out)
	return out
}

func (c *OpenBaoClient) Status(ctx context.Context, pod string) (ports.BaoStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.BaoStatus{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.pods[pod]
	if !ok {
		return ports.BaoStatus{}, fmt.Errorf("mock: openbao pod %q not in scenario %s", pod, c.scenarioName())
	}
	return ports.BaoStatus{
		Pod:         pod,
		Initialized: st.initialized,
		Sealed:      st.sealed,
		Version:     st.version,
		HAMode:      st.haMode,
		RaftIndex:   st.raftIndex,
	}, nil
}

func (c *OpenBaoClient) PodList(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.pods))
	for name := range c.pods {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Unseal flips the pod's sealed state to false (idempotent — already-
// unsealed is fine). Per the ports contract, the share content isn't
// validated by the mock; the M5 share-validity tests have to assert
// against the scenario's UnsealKeys explicitly if they care.
func (c *OpenBaoClient) Unseal(ctx context.Context, pod string, share []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.pods[pod]
	if !ok {
		return fmt.Errorf("mock: openbao pod %q not in scenario %s", pod, c.scenarioName())
	}
	st.sealed = false
	return nil
}

// RaftJoin flips the pod's Initialized state to true. Mirrors the
// real adapter's behavior: a follower that joins the leader's Raft
// cluster has its shamir barrier populated, so subsequent
// `bao status` reports Initialized=true even though the pod is
// still sealed (the unseal shares must still be applied). Idempotent.
func (c *OpenBaoClient) RaftJoin(ctx context.Context, pod, leaderURL string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.pods[pod]
	if !ok {
		return fmt.Errorf("mock: openbao pod %q not in scenario %s", pod, c.scenarioName())
	}
	st.initialized = true
	return nil
}

// GenerateRoot returns the scenario's declared root token (or a stub
// `mock-generated-root` if unset). Caller is expected to defer
// RevokeSelf; ActiveRootTokens() inspects the leak set.
func (c *OpenBaoClient) GenerateRoot(ctx context.Context, shares [][]byte) (token []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	rt := ""
	if c.scenario != nil && c.scenario.OpenBao != nil {
		rt = c.scenario.OpenBao.RootToken
	}
	if rt == "" {
		rt = "mock-generated-root"
	}
	c.activeRootTokens[rt] = true
	// Return a defensive copy — caller scrubs after defer RevokeSelf.
	out := make([]byte, len(rt))
	copy(out, rt)
	return out, nil
}

func (c *OpenBaoClient) RevokeSelf(ctx context.Context, token []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Idempotent: 404 on already-revoked is success in the real API.
	c.activeRootTokens[string(token)] = false
	return nil
}

// M5-T08 controller-auth setup methods. Each requires a non-empty
// token (mirrors the real adapter); empty token is a test bug, so we
// fail loudly. None of the methods retain the token in mock state —
// the mock validates the contract by INSPECTING that the token came
// in non-empty but never recording its bytes.

func (c *OpenBaoClient) EnableAuthPath(ctx context.Context, token []byte, mountType, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(token) == 0 {
		return fmt.Errorf("mock: EnableAuthPath called with empty token")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.enabledAuthPaths[path]; ok {
		if existing != mountType {
			return fmt.Errorf("mock: auth path %s already enabled with mountType=%s (got %s)", path, existing, mountType)
		}
		return nil // idempotent
	}
	c.enabledAuthPaths[path] = mountType
	return nil
}

func (c *OpenBaoClient) ConfigureKubernetesAuth(ctx context.Context, token []byte, path string, opts ports.KubernetesAuthConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(token) == 0 {
		return fmt.Errorf("mock: ConfigureKubernetesAuth called with empty token")
	}
	if path == "" {
		return fmt.Errorf("mock: ConfigureKubernetesAuth needs path")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Configure requires the mount to be enabled (matches OpenBao semantics).
	if _, ok := c.enabledAuthPaths[path]; !ok {
		return fmt.Errorf("mock: ConfigureKubernetesAuth: path %q not enabled", path)
	}
	c.kubernetesAuthCfgs[path] = opts
	return nil
}

func (c *OpenBaoClient) ApplyPolicy(ctx context.Context, token []byte, name, hcl string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(token) == 0 {
		return fmt.Errorf("mock: ApplyPolicy called with empty token")
	}
	if name == "" || hcl == "" {
		return fmt.Errorf("mock: ApplyPolicy needs name + hcl")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policies[name] = hcl // upsert — idempotent
	return nil
}

func (c *OpenBaoClient) WriteAuthRole(ctx context.Context, token []byte, mount, role string, params map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(token) == 0 {
		return fmt.Errorf("mock: WriteAuthRole called with empty token")
	}
	if mount == "" || role == "" {
		return fmt.Errorf("mock: WriteAuthRole needs mount + role")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Defensive copy of params so the caller scrubbing/mutating doesn't
	// corrupt our test inspection state.
	p := make(map[string]string, len(params))
	for k, v := range params {
		p[k] = v
	}
	c.authRoles[mount+"/"+role] = p
	return nil
}

// EnabledAuthPaths returns a snapshot of which paths are enabled +
// their mount types. Test inspector.
func (c *OpenBaoClient) EnabledAuthPaths() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.enabledAuthPaths))
	for k, v := range c.enabledAuthPaths {
		out[k] = v
	}
	return out
}

// KubernetesAuthConfigs returns the per-path Kubernetes auth configs.
// Test inspector.
func (c *OpenBaoClient) KubernetesAuthConfigs() map[string]ports.KubernetesAuthConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]ports.KubernetesAuthConfig, len(c.kubernetesAuthCfgs))
	for k, v := range c.kubernetesAuthCfgs {
		out[k] = v
	}
	return out
}

// Policies returns the map of policy name -> HCL body. Test inspector.
func (c *OpenBaoClient) Policies() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.policies))
	for k, v := range c.policies {
		out[k] = v
	}
	return out
}

// AuthRoles returns the map of "mount/role" -> params. Test inspector.
func (c *OpenBaoClient) AuthRoles() map[string]map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]map[string]string, len(c.authRoles))
	for k, v := range c.authRoles {
		copy := make(map[string]string, len(v))
		for kk, vv := range v {
			copy[kk] = vv
		}
		out[k] = copy
	}
	return out
}

func (c *OpenBaoClient) GetAnnotation(ctx context.Context, svc, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.annotations[key], nil
}

func (c *OpenBaoClient) SetAnnotation(ctx context.Context, svc, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.annotations[key] = value
	return nil
}

// SetAnnotations applies a batch of annotation writes atomically.
// Empty value clears the key (matches the real adapter's JSON-merge
// null semantics).
func (c *OpenBaoClient) SetAnnotations(ctx context.Context, svc string, kv map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range kv {
		if v == "" {
			delete(c.annotations, k)
		} else {
			c.annotations[k] = v
		}
	}
	return nil
}

func (c *OpenBaoClient) scenarioName() string {
	if c.scenario == nil {
		return "<nil>"
	}
	return c.scenario.Name
}
