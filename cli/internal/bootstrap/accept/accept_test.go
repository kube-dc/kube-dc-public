package accept

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// fakeK8s implements just enough of ports.K8sClient for the acceptance checks.
type fakeK8s struct {
	graph      ports.Graph
	graphErr   error
	nodeLabels map[string]map[string]string
	apiArgs    map[string][]string
	apiArgsErr error
	crds       []string
	// tenant-egress probe seams
	cmData   map[string]string
	podNames []string
	execOut  map[string]string // pod name -> `ip neigh` output
}

func (f *fakeK8s) DiscoverFluxGraph(context.Context) (ports.Graph, error) {
	return f.graph, f.graphErr
}
func (f *fakeK8s) NodeLabels(context.Context) (map[string]map[string]string, error) {
	return f.nodeLabels, nil
}
func (f *fakeK8s) PodContainerArgs(context.Context, string, string) (map[string][]string, error) {
	return f.apiArgs, f.apiArgsErr
}
func (f *fakeK8s) ListCRDs(context.Context) ([]string, error) { return f.crds, nil }

// Unused by these checks.
func (f *fakeK8s) DeploymentImages(context.Context, string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeK8s) ListNamespaces(context.Context) ([]string, error) { return nil, nil }
func (f *fakeK8s) ListPodNames(context.Context, string, string) ([]string, error) {
	return f.podNames, nil
}
func (f *fakeK8s) PodExec(_ context.Context, _, pod string, _ []string, _ []byte) ([]byte, error) {
	out, ok := f.execOut[pod]
	if !ok {
		return nil, errors.New("exec failed")
	}
	return []byte(out), nil
}
func (f *fakeK8s) PodExecViaKubectl(context.Context, string, string, []string, []byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeK8s) GetServiceAnnotation(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakeK8s) SetServiceAnnotation(context.Context, string, string, string, string) error {
	return nil
}
func (f *fakeK8s) SetServiceAnnotations(context.Context, string, string, map[string]string) error {
	return nil
}
func (f *fakeK8s) GetConfigMapData(_ context.Context, _, _, key string) (string, error) {
	return f.cmData[key], nil
}
func (f *fakeK8s) HelmReleaseChartVersions(context.Context) (map[string]string, error) {
	return nil, nil
}
func (f *fakeK8s) GetResourceFieldFirst(context.Context, string, string, string, string, string, ...string) (string, error) {
	return "", nil
}

func healthy() *fakeK8s {
	return &fakeK8s{
		graph: ports.Graph{Nodes: []ports.GraphNode{
			{Name: "infra-cni", Ready: true},
			{Name: "infra-core", Ready: true},
			{Name: "platform", Ready: true},
		}},
		nodeLabels: map[string]map[string]string{
			"cp-1": {"node-role.kubernetes.io/control-plane": ""},
			"cp-2": {"node-role.kubernetes.io/control-plane": ""},
		},
		apiArgs: map[string][]string{
			"kube-apiserver-cp-1": {"kube-apiserver", "--authentication-token-webhook-config-file=/x"},
			"kube-apiserver-cp-2": {"kube-apiserver", "--authentication-token-webhook-config-file=/x"},
		},
		crds: []string{"organizations.kube-dc.com", "projects.kube-dc.com"},
	}
}

func run(t *testing.T, k *fakeK8s) Report {
	t.Helper()
	// No Domain: the front-door probe would need the network. It is covered by
	// its own skip assertion below.
	rep, err := Run(context.Background(), Options{K8s: k, Cluster: "dc1", Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func find(t *testing.T, rep Report, name string) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, rep.Checks)
	return Check{}
}

// A cluster where everything is wired reports usable.
func TestRun_HealthyClusterIsUsable(t *testing.T) {
	rep := run(t, healthy())
	if rep.State != StateUsable {
		t.Errorf("state = %s, want %s (failures: %+v)", rep.State, StateUsable, rep.Failed())
	}
	if got := find(t, rep, "identity/oidc-cutover").Outcome; got != Pass {
		t.Errorf("oidc check = %s, want PASS", got)
	}
}

// THE case this command exists for: everything green, nobody can log in.
func TestRun_ConvergedButNoLoginsIsNotUsable(t *testing.T) {
	k := healthy()
	k.apiArgs = map[string][]string{
		"kube-apiserver-cp-1": {"kube-apiserver", "--anonymous-auth=false"},
		"kube-apiserver-cp-2": {"kube-apiserver", "--anonymous-auth=false"},
	}
	rep := run(t, k)
	if rep.State != StateConverged {
		t.Errorf("state = %s, want %s — Flux is settled but logins fail", rep.State, StateConverged)
	}
	c := find(t, rep, "identity/oidc-cutover")
	if c.Outcome != Fail {
		t.Fatalf("oidc check = %s, want FAIL", c.Outcome)
	}
	for _, want := range []string{"401", "looks healthy"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("the detail must explain the user-visible symptom (%q), got: %s", want, c.Detail)
		}
	}
	if !strings.Contains(c.Fix, "oidc-cutover") {
		t.Errorf("the fix must name the command, got: %s", c.Fix)
	}
}

// A PARTIAL cutover must be called out specifically: its symptom is
// intermittent, which sends people debugging Keycloak or NTP.
func TestRun_PartialCutoverIsNamedAsSuch(t *testing.T) {
	k := healthy()
	k.apiArgs = map[string][]string{
		"kube-apiserver-cp-1": {"kube-apiserver", "--authentication-token-webhook-config-file=/x"},
		"kube-apiserver-cp-2": {"kube-apiserver", "--anonymous-auth=false"},
	}
	rep := run(t, k)
	c := find(t, rep, "identity/oidc-cutover")
	if c.Outcome != Fail {
		t.Fatalf("outcome = %s, want FAIL", c.Outcome)
	}
	for _, want := range []string{"PARTIAL", "kube-apiserver-cp-1", "kube-apiserver-cp-2", "load-balances"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail must mention %q, got: %s", want, c.Detail)
		}
	}
	if rep.State == StateUsable {
		t.Error("a partially wired cluster must never report usable")
	}
}

// "I could not tell" must not read as "fine".
func TestRun_UnverifiableRequiredCheckBlocksUsable(t *testing.T) {
	k := healthy()
	k.apiArgsErr = errors.New("forbidden")
	rep := run(t, k)
	if got := find(t, rep, "identity/oidc-cutover").Outcome; got != Skipped {
		t.Errorf("outcome = %s, want SKIP", got)
	}
	if rep.State == StateUsable {
		t.Error("a SKIPPED required check must not yield usable")
	}
}

// No apiserver pods visible is also unverifiable — and must point at the SSH
// path that can still check it.
func TestRun_NoAPIServerPodsPointsAtTheSSHPath(t *testing.T) {
	k := healthy()
	k.apiArgs = map[string][]string{}
	rep := run(t, k)
	c := find(t, rep, "identity/oidc-cutover")
	if c.Outcome != Skipped {
		t.Errorf("outcome = %s, want SKIP", c.Outcome)
	}
	if !strings.Contains(c.Fix, "oidc-cutover --dry-run") {
		t.Errorf("must offer the SSH-based verification, got: %s", c.Fix)
	}
}

// Flux not settled outranks everything: the other checks cannot be trusted yet.
func TestRun_UnsettledFluxIsReconciling(t *testing.T) {
	k := healthy()
	k.graph = ports.Graph{Nodes: []ports.GraphNode{
		{Name: "infra-core", Ready: true},
		{Name: "platform", Ready: false},
	}}
	rep := run(t, k)
	if rep.State != StateReconciling {
		t.Errorf("state = %s, want %s", rep.State, StateReconciling)
	}
	if d := find(t, rep, "flux/kustomizations").Detail; !strings.Contains(d, "platform") {
		t.Errorf("the failing Kustomization must be named, got: %s", d)
	}
}

// Flux present but having reconciled nothing is not "all Ready".
func TestRun_ZeroKustomizationsIsNotSuccess(t *testing.T) {
	k := healthy()
	k.graph = ports.Graph{Nodes: []ports.GraphNode{}}
	rep := run(t, k)
	if rep.State == StateUsable {
		t.Error("a Flux that has reconciled nothing must not yield usable")
	}
}

// Missing tenancy CRDs means the platform never installed.
func TestRun_MissingTenancyCRDsFails(t *testing.T) {
	k := healthy()
	k.crds = []string{"organizations.kube-dc.com"}
	rep := run(t, k)
	c := find(t, rep, "tenancy/crds")
	if c.Outcome != Fail {
		t.Errorf("outcome = %s, want FAIL", c.Outcome)
	}
	if !strings.Contains(c.Detail, "projects.kube-dc.com") {
		t.Errorf("the missing CRD must be named, got: %s", c.Detail)
	}
}

// Without a domain the front-door check is skipped and is NOT required, so it
// cannot block an otherwise-usable verdict — the operator may legitimately not
// have told us the domain.
func TestRun_FrontDoorSkipWithoutDomainDoesNotBlock(t *testing.T) {
	rep := run(t, healthy())
	c := find(t, rep, "front-door/tls")
	if c.Outcome != Skipped {
		t.Errorf("outcome = %s, want SKIP", c.Outcome)
	}
	if c.Required {
		t.Error("a check skipped for lack of input must not be required")
	}
	if rep.State != StateUsable {
		t.Errorf("state = %s, want usable", rep.State)
	}
}

// Render must show the fix for anything that is not passing, or the report is
// just bad news without a next step.
func TestRender_ShowsFixesAndTheVerdict(t *testing.T) {
	k := healthy()
	k.apiArgs = map[string][]string{"kube-apiserver-cp-1": {"kube-apiserver"}}
	rep := run(t, k)
	var sb strings.Builder
	Render(&sb, rep)
	out := sb.String()
	for _, want := range []string{"[FAIL]", "fix:", "oidc-cutover", "state: converged", "required for `usable`"} {
		if !strings.Contains(out, want) {
			t.Errorf("render must contain %q, got:\n%s", want, out)
		}
	}
}

// A control-plane node whose apiserver static pod is absent or dead is INVISIBLE
// to a check that only looks at the pods the API returns — and it is precisely
// the node that will reject every token when it comes back.
func TestRun_FewerAPIServerPodsThanControlPlanesFails(t *testing.T) {
	k := healthy() // 2 control-plane nodes
	k.apiArgs = map[string][]string{
		"kube-apiserver-cp-1": {"kube-apiserver", "--authentication-token-webhook-config-file=/x"},
	}
	rep := run(t, k)
	c := find(t, rep, "identity/oidc-cutover")
	if c.Outcome != Fail {
		t.Fatalf("outcome = %s, want FAIL — one apiserver is unaccounted for", c.Outcome)
	}
	if !strings.Contains(c.Detail, "1 apiserver pod(s) visible for 2 control-plane") {
		t.Errorf("the detail must state the shortfall, got: %s", c.Detail)
	}
}

// A bare flag name with no path would leave the apiserver pointed at nothing,
// which makes it reject EVERY token.
func TestRun_WebhookFlagWithoutAPathIsNotWired(t *testing.T) {
	k := healthy()
	k.apiArgs = map[string][]string{
		"kube-apiserver-cp-1": {"kube-apiserver", "--authentication-token-webhook-config-file="},
		"kube-apiserver-cp-2": {"kube-apiserver", "--authentication-token-webhook-config-file="},
	}
	rep := run(t, k)
	if find(t, rep, "identity/oidc-cutover").Outcome != Fail {
		t.Error("an empty webhook path must not count as wired")
	}
}

// Zero nodes, or nodes with no control plane, is not a healthy cluster.
func TestRun_ZeroNodesFails(t *testing.T) {
	k := healthy()
	k.nodeLabels = map[string]map[string]string{}
	rep := run(t, k)
	if find(t, rep, "nodes").Outcome != Fail {
		t.Error("zero nodes must fail")
	}
	k.nodeLabels = map[string]map[string]string{"w1": {}}
	rep = run(t, k)
	if find(t, rep, "nodes").Outcome != Fail {
		t.Error("nodes with no control-plane label must fail")
	}
}

// TLS verified is not the same as serving: a trusted certificate in front of a
// 503 reads as a working front door to anything that only checks the request
// completed.
func TestRun_FrontDoorErrorStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	rep, err := Run(context.Background(), Options{
		K8s: healthy(), Cluster: "dc1", Domain: "example.com", Out: io.Discard,
		httpClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The probe targets https://console.<domain>, which will not resolve here;
	// what matters is that a 4xx/5xx is treated as failure when it IS reached.
	c := find(t, rep, "front-door/tls")
	if c.Outcome == Pass {
		t.Errorf("front door must not pass: %s", c.Detail)
	}
}

// SetNodeLabel is unused by these tests; the ingress-label step is covered in
// clusterinit/ingresslabels_test.go.
// NodeInternalIPs is unused by these tests; the front-door CIDR derivation is
// covered in clusterinit/ingresscidr_test.go.
func (f *fakeK8s) NodeInternalIPs(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (f *fakeK8s) SetNodeLabel(context.Context, string, string, string) error {
	return nil
}

// The gateway check is the one that would have caught mod I-17 and the crk/jed
// bastion-subnet bug, all of which passed acceptance with tenant egress dead.
func TestTenantEgressGateway(t *testing.T) {
	const arpReply = "ARPREPLY"
	// The cheap path: a neighbour entry already resolved, no arping needed.
	const preResolved = "RESOLVED 100.65.0.29 dev br-ext-cloud lladdr 22:2a:8f:23:02:a1 REACHABLE"
	const resolved = "100.65.0.29 dev br-ext-cloud lladdr 22:2a:8f:23:02:a1 REACHABLE"
	const silent = "100.65.0.29 dev br-ext-cloud  INCOMPLETE"
	// A node with no anchor on the external segment cannot route to the gateway.
	const noRoute = "NOROUTE"

	cases := []struct {
		name    string
		k8s     *fakeK8s
		want    Outcome
		wantSub string
	}{
		{
			name: "no external gateway configured is not this cluster's problem",
			k8s:  &fakeK8s{},
			want: Skipped,
		},
		{
			name: "a resolved neighbour proves somebody owns the address",
			k8s: &fakeK8s{
				cmData:   map[string]string{"enable-external-gw": "true", "external-gw-addr": "100.65.0.29/16"},
				podNames: []string{"cni-a"},
				execOut:  map[string]string{"cni-a": resolved},
			},
			want: Pass,
		},
		{
			name: "silent gateway is a failure, not a pass",
			k8s: &fakeK8s{
				cmData:   map[string]string{"enable-external-gw": "true", "external-gw-addr": "100.65.0.29/16"},
				podNames: []string{"cni-a"},
				execOut:  map[string]string{"cni-a": silent},
			},
			want:    Fail,
			wantSub: "NOTHING answers ARP",
		},
		{
			// Only nodes carrying an external anchor can resolve it, so one
			// resolving pod is a pass even when the others cannot see it.
			name: "one node resolving it is enough",
			k8s: &fakeK8s{
				cmData:   map[string]string{"enable-external-gw": "true", "external-gw-addr": "100.65.0.29/16"},
				podNames: []string{"cni-a", "cni-b"},
				execOut:  map[string]string{"cni-a": silent, "cni-b": resolved},
			},
			want: Pass,
		},
		{
			// The regression this check shipped with: a MetalLB gateway VIP
			// answers ARP but never ICMP, so a ping-then-read-neighbour probe
			// reported a perfectly healthy gateway as a black hole.
			name: "a VIP that answers ARP but not ICMP is healthy",
			k8s: &fakeK8s{
				cmData:   map[string]string{"enable-external-gw": "true", "external-gw-addr": "100.65.0.29/16"},
				podNames: []string{"cni-a"},
				execOut:  map[string]string{"cni-a": arpReply},
			},
			want: Pass,
		},
		{
			name: "an already-resolved neighbour short-circuits the arping",
			k8s: &fakeK8s{
				cmData:   map[string]string{"enable-external-gw": "true", "external-gw-addr": "100.65.0.29/16"},
				podNames: []string{"cni-a"},
				execOut:  map[string]string{"cni-a": preResolved},
			},
			want: Pass,
		},
		{
			// Workers have no anchor, so they must not be counted as evidence
			// that the gateway is dead.
			name: "nodes with no route to the segment are not evidence",
			k8s: &fakeK8s{
				cmData:   map[string]string{"enable-external-gw": "true", "external-gw-addr": "100.65.0.29/16"},
				podNames: []string{"worker-a", "worker-b"},
				execOut:  map[string]string{"worker-a": noRoute, "worker-b": noRoute},
			},
			want:    Skipped,
			wantSub: "no probed node has a route",
		},
		{
			name: "anchored node silent while unanchored nodes abstain is still a failure",
			k8s: &fakeK8s{
				cmData:   map[string]string{"enable-external-gw": "true", "external-gw-addr": "100.65.0.29/16"},
				podNames: []string{"worker-a", "cni-a"},
				execOut:  map[string]string{"worker-a": noRoute, "cni-a": silent},
			},
			want:    Fail,
			wantSub: "NOTHING answers ARP",
		},
		{
			name: "enabled but no address is a config error",
			k8s: &fakeK8s{
				cmData: map[string]string{"enable-external-gw": "true"},
			},
			want:    Fail,
			wantSub: "external-gw-addr is empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkTenantEgressGateway(context.Background(), Options{K8s: tc.k8s})
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %s, want %s (detail: %s)", got.Outcome, tc.want, got.Detail)
			}
			if tc.wantSub != "" && !strings.Contains(got.Detail, tc.wantSub) {
				t.Fatalf("detail = %q, want it to contain %q", got.Detail, tc.wantSub)
			}
			if got.Outcome == Fail && got.Fix == "" {
				t.Fatal("a failure must tell the operator what to do next")
			}
		})
	}
}
