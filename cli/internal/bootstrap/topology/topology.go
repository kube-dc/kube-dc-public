// Package topology classifies a Kube-DC cluster's external-networking
// topology into one of three classes and recommends whether the
// Internal Platform Endpoints feature should be enabled.
//
// The classification is a heuristic: probes look at cluster-side
// configuration (cloud-provider integration, EnvoyProxy CR shape,
// Fork E Service presence) and combine the per-probe hints into a
// single verdict + confidence level. It is NOT authoritative — a
// laptop operator running this against a live cluster will get the
// right answer in the common cases (A/B/C with clean signals) and
// "ambiguous" + a pointer to the docs in the edge cases.
//
// The probes deliberately shell out to `kubectl` rather than going
// through the K8sClient port: the port surface is intentionally
// minimal (NodeLabels, DeploymentImages, …) and topology
// classification needs ad-hoc reads (EnvoyProxy CR jsonpath, arbitrary
// Service existence) that don't justify per-probe Port methods. The
// command's UX guarantee is "if `kubectl get` against the target
// cluster works, topology classification works".
//
// See docs/platform/internal-platform-endpoints.md §"Do you need
// this?" for the canonical decision rule the classify() heuristic
// approximates.
package topology

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Class is one of the three topology classes described in the
// operator-facing docs.
type Class string

const (
	// ClassA — 1:1-NAT hairpin. Single public IP NAT'd at upstream
	// router; tenant pods can't hairpin to it. Requires Fork E.
	ClassA Class = "A"

	// ClassB — Flat-L2 with per-node externalIPs. Each CP node holds
	// a public IP in a shared broadcast domain. Tenant traffic
	// reaches platform hostnames L2-locally; Fork E adds overhead
	// for no observable benefit.
	ClassB Class = "B"

	// ClassC — Cloud-provider LoadBalancer (AWS/GCP/Azure). Provider
	// LB handles hairpin natively; Fork E is redundant.
	ClassC Class = "C"

	// ClassUnknown — no probe produced a strong-enough signal.
	// Operator should classify manually per the docs.
	ClassUnknown Class = "unknown"
)

// Verdict is the recommended action for the classified topology.
type Verdict string

const (
	// VerdictRequired — feature must be enabled for tenant pods to
	// reach platform hostnames.
	VerdictRequired Verdict = "required"

	// VerdictNotNeeded — feature would add overhead with no benefit.
	VerdictNotNeeded Verdict = "not-needed"

	// VerdictAlreadyEnabled — Fork E Services are already deployed;
	// the operator has already made the decision.
	VerdictAlreadyEnabled Verdict = "already-enabled"

	// VerdictAmbiguous — probes couldn't classify; operator should
	// pick one manually.
	VerdictAmbiguous Verdict = "ambiguous"
)

// Signal is one row in the per-probe evidence table.
//
// Probe is a short label ("cloud-provider", "Envoy externalIPs",
// "Fork E Services"). Detail is the human-readable finding from
// running the probe. Hint, when non-empty, marks which Class this
// signal points at — the classifier tallies hints to pick the final
// Class. Confidence captures how strong this individual signal is,
// independent of the aggregate verdict.
type Signal struct {
	Probe      string
	Detail     string
	Hint       Class  // empty when the signal is purely informational
	Confidence string // "high" | "medium" | "low"
}

// Result is the full topology classification for one cluster.
type Result struct {
	Class          Class
	Verdict        Verdict
	Confidence     string // "high" | "medium" | "low" — aggregate
	Signals        []Signal
	Reasoning      string // 1–3 sentence summary of how Class was decided
	Recommendation string // next-step text for the operator
}

// Probe runs the topology probes against the cluster reachable via
// the given kubeconfig. An empty kubeconfig falls back to KUBECONFIG
// env var or ~/.kube/config (standard kubectl semantics).
//
// All probes run sequentially — there are only four, each completes
// in well under a second on a healthy cluster, and parallel kubectl
// invocations would just contend on the same apiserver round-trip
// budget. If a single probe fails (CR missing, permissions denied,
// connection error), the failure is captured into a Signal with an
// empty Hint and the classification proceeds with the remaining
// signals.
func Probe(ctx context.Context, kubeconfig string) (Result, error) {
	k := &kubectl{kubeconfig: kubeconfig}

	var r Result
	r.Signals = append(r.Signals, probeForkEServices(ctx, k))
	r.Signals = append(r.Signals, probeCloudProvider(ctx, k))
	r.Signals = append(r.Signals, probeEnvoyExternalIPs(ctx, k))
	r.Signals = append(r.Signals, probeEnvoyHostNetwork(ctx, k))

	classify(&r)
	return r, nil
}

// Classify is exposed for tests — drive a Result.Signals list through
// the classification heuristic without running real kubectl.
func Classify(r *Result) { classify(r) }

// classify combines the per-probe Signals into Class + Verdict +
// Reasoning + Recommendation. Precedence:
//
//  1. Fork E Services already deployed → ClassA / AlreadyEnabled
//     (the operator made the call; reflect that)
//  2. Cloud-provider providerID → ClassC / NotNeeded
//  3. Envoy externalIPs binding → ClassB / NotNeeded
//  4. Envoy hostNetwork or ClusterIP-with-MetalLB → ClassA / Required
//  5. otherwise → Unknown / Ambiguous
//
// The order matters: a cluster can show both a cloud-provider ID
// (residual from a cluster-api install) AND externalIPs — in that
// case the Cloud LB takes precedence because it's the more
// load-bearing piece of plumbing.
func classify(r *Result) {
	foundEnabled := false
	for _, s := range r.Signals {
		if s.Probe == probeNameForkE && (strings.Contains(s.Detail, "kube-api-platform") || strings.Contains(s.Detail, "envoy-gateway-platform")) {
			foundEnabled = true
			break
		}
	}

	hints := map[Class]int{}
	for _, s := range r.Signals {
		if s.Hint != "" {
			hints[s.Hint]++
		}
	}

	switch {
	case foundEnabled:
		r.Class = ClassA
		r.Verdict = VerdictAlreadyEnabled
		r.Confidence = "high"
		r.Reasoning = "Fork E Services are deployed — internal platform endpoints are currently enabled on this cluster."
		r.Recommendation = "Already configured. Verify health: see docs/platform/internal-platform-endpoints.md §Verifying."

	case hints[ClassC] > 0:
		r.Class = ClassC
		r.Verdict = VerdictNotNeeded
		r.Confidence = "medium"
		r.Reasoning = "Cloud-provider integration detected — the provider LoadBalancer typically handles hairpin natively, making internal platform endpoints redundant."
		r.Recommendation = "Leave the feature off. Confirm by checking that tenant pods reach platform hostnames today (`curl -sk https://login.<DOMAIN>/...`)."

	case hints[ClassB] > 0 && hints[ClassB] >= hints[ClassA]:
		r.Class = ClassB
		r.Verdict = VerdictNotNeeded
		r.Confidence = "high"
		r.Reasoning = "Envoy is configured with externalIPs (per-node public IPs), which indicates a flat-L2 topology. Tenant pods reach platform hostnames via the L2 broadcast domain — no NAT hairpin involved."
		r.Recommendation = "Leave the feature off. Review only if topology changes (L3-routed transit, VLAN-isolated CP nodes, etc.). See docs/platform/internal-platform-endpoints.md §\"Do you need this?\"."

	case hints[ClassA] > 0:
		r.Class = ClassA
		r.Verdict = VerdictRequired
		r.Confidence = "medium"
		r.Reasoning = "Envoy configuration suggests 1:1-NAT hairpin topology — likely needs internal platform endpoints to avoid black-holed tenant traffic to platform hostnames."
		r.Recommendation = "Enable the feature. See docs/platform/internal-platform-endpoints.md §\"Enabling on a cluster\" for the 7-step workflow."

	default:
		r.Class = ClassUnknown
		r.Verdict = VerdictAmbiguous
		r.Confidence = "low"
		r.Reasoning = "No probe produced a strong enough signal to classify this cluster. Possible causes: EnvoyProxy CR uses non-standard naming, cluster is mid-bootstrap, or the topology doesn't match any of the three known classes."
		r.Recommendation = "Classify manually per docs/platform/internal-platform-endpoints.md §\"Do you need this?\" — the 5-line tenant-pod smoke test is the authoritative check."
	}
}

// Probe-name constants — used as the `Probe` field on each Signal and
// also for foundEnabled matching in classify(). Kept as exported
// constants so tests and printers can reference them stably.
const (
	probeNameForkE         = "Fork E Services"
	probeNameCloudProvider = "cloud-provider"
	probeNameEnvoyExtIPs   = "Envoy externalIPs"
	probeNameHostNetwork   = "Envoy hostNetwork"
)

// probeForkEServices checks whether the manager-owned platform-endpoint
// Services already exist. Their presence is the strongest possible
// signal — the operator has already decided this cluster is Class A
// and the feature is on.
func probeForkEServices(ctx context.Context, k *kubectl) Signal {
	_, errAPI := k.run(ctx, "-n", "kube-system", "get", "svc", "kube-api-platform", "-o", "name")
	_, errEnvoy := k.run(ctx, "-n", "envoy-gateway-system", "get", "svc", "envoy-gateway-platform", "-o", "name")

	switch {
	case errAPI == nil && errEnvoy == nil:
		return Signal{Probe: probeNameForkE, Detail: "both kube-api-platform + envoy-gateway-platform deployed", Hint: ClassA, Confidence: "high"}
	case errAPI == nil:
		return Signal{Probe: probeNameForkE, Detail: "kube-api-platform deployed (envoy-gateway-platform not yet)", Hint: ClassA, Confidence: "high"}
	case errEnvoy == nil:
		return Signal{Probe: probeNameForkE, Detail: "envoy-gateway-platform deployed (kube-api-platform not yet)", Hint: ClassA, Confidence: "high"}
	default:
		return Signal{Probe: probeNameForkE, Detail: "neither platform-endpoint Service present", Confidence: "high"}
	}
}

// probeCloudProvider looks for spec.providerID on any node — non-empty
// means a cloud-provider integration is wired up.
func probeCloudProvider(ctx context.Context, k *kubectl) Signal {
	out, err := k.run(ctx, "get", "nodes", "-o", "jsonpath={range .items[*]}{.spec.providerID}{\"\\n\"}{end}")
	if err != nil {
		return Signal{Probe: probeNameCloudProvider, Detail: "probe error: " + shortenErr(err)}
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		scheme := id
		if i := strings.Index(id, "://"); i > 0 {
			scheme = id[:i]
		}
		return Signal{Probe: probeNameCloudProvider, Detail: "providerID scheme: " + scheme, Hint: ClassC, Confidence: "medium"}
	}
	return Signal{Probe: probeNameCloudProvider, Detail: "no providerID on any node", Confidence: "high"}
}

// probeEnvoyExternalIPs reads the EnvoyProxy CR and looks at the
// envoyService configuration. externalIPs set → Class B (per-node
// public IPs). ClusterIP type → Class A (hostNetwork bypass).
// LoadBalancer with metallb class → Class A indicator (the typical
// Class A shape).
func probeEnvoyExternalIPs(ctx context.Context, k *kubectl) Signal {
	out, err := k.run(ctx, "-n", "envoy-gateway-system", "get", "envoyproxy", "custom-proxy-config", "-o", "json")
	if err != nil {
		return Signal{Probe: probeNameEnvoyExtIPs, Detail: "EnvoyProxy CR not found (cluster may be mid-bootstrap)"}
	}
	var cr struct {
		Spec struct {
			Provider struct {
				Kubernetes struct {
					EnvoyService struct {
						Type  string `json:"type"`
						Patch struct {
							Value struct {
								Spec struct {
									ExternalIPs       []string `json:"externalIPs"`
									LoadBalancerClass *string  `json:"loadBalancerClass"`
								} `json:"spec"`
							} `json:"value"`
						} `json:"patch"`
					} `json:"envoyService"`
				} `json:"kubernetes"`
			} `json:"provider"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &cr); err != nil {
		return Signal{Probe: probeNameEnvoyExtIPs, Detail: "decode EnvoyProxy CR: " + shortenErr(err)}
	}

	svcType := cr.Spec.Provider.Kubernetes.EnvoyService.Type
	extIPs := cr.Spec.Provider.Kubernetes.EnvoyService.Patch.Value.Spec.ExternalIPs
	lbClass := ""
	if c := cr.Spec.Provider.Kubernetes.EnvoyService.Patch.Value.Spec.LoadBalancerClass; c != nil {
		lbClass = *c
	}

	switch {
	case len(extIPs) > 0:
		return Signal{
			Probe:      probeNameEnvoyExtIPs,
			Detail:     fmt.Sprintf("externalIPs=%v (svc type=%s lb-class=%s)", extIPs, ifBlank(svcType, "default"), ifBlank(lbClass, "default")),
			Hint:       ClassB,
			Confidence: "high",
		}
	case strings.EqualFold(svcType, "ClusterIP"):
		return Signal{
			Probe:      probeNameEnvoyExtIPs,
			Detail:     "type=ClusterIP (probable hostNetwork bypass — Class A shape)",
			Hint:       ClassA,
			Confidence: "medium",
		}
	case strings.EqualFold(svcType, "LoadBalancer") && lbClass == "metallb":
		return Signal{
			Probe:      probeNameEnvoyExtIPs,
			Detail:     "type=LoadBalancer, lb-class=metallb (MetalLB exposure — Class A shape)",
			Hint:       ClassA,
			Confidence: "medium",
		}
	default:
		return Signal{
			Probe:  probeNameEnvoyExtIPs,
			Detail: fmt.Sprintf("type=%s lb-class=%s, no externalIPs", ifBlank(svcType, "default"), ifBlank(lbClass, "default")),
		}
	}
}

// probeEnvoyHostNetwork checks the strategic-merge patch for
// hostNetwork: true. Independent signal that often correlates with
// Class A on bare-metal installs that use the chart's hostNetwork
// override.
func probeEnvoyHostNetwork(ctx context.Context, k *kubectl) Signal {
	out, err := k.run(ctx, "-n", "envoy-gateway-system", "get", "envoyproxy", "custom-proxy-config",
		"-o", "jsonpath={.spec.provider.kubernetes.envoyDeployment.patch.value.spec.template.spec.hostNetwork}")
	if err != nil {
		return Signal{Probe: probeNameHostNetwork, Detail: "probe error: " + shortenErr(err)}
	}
	val := strings.TrimSpace(string(out))
	if val == "true" {
		return Signal{Probe: probeNameHostNetwork, Detail: "true (Envoy on host network — Class A shape)", Hint: ClassA, Confidence: "medium"}
	}
	return Signal{Probe: probeNameHostNetwork, Detail: "false or unset (standard pod networking)"}
}

// kubectl wraps `kubectl` invocation with optional --kubeconfig.
type kubectl struct {
	kubeconfig string
}

func (k *kubectl) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	if k.kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+k.kubeconfig)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %s", err.Error(), strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// shortenErr trims kubectl/decoder error strings to one tight line.
func shortenErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if len(s) > 100 {
		s = s[:97] + "…"
	}
	return s
}

func ifBlank(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
