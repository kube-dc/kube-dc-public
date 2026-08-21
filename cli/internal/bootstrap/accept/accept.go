// Package accept answers the question the rest of the installer cannot: is this
// cluster USABLE?
//
// Everything else reports CONVERGENCE. `flux get kustomizations` says Ready,
// `kubectl get pods` says Running, `bootstrap status` says Ready — and all three
// say exactly that on a cluster where nobody can log in, the front door serves
// an untrusted certificate, or storage never came up. The install then "finished
// successfully" and the operator discovers the truth from a user.
//
// The distinction is deliberate and is reported as three states:
//
//	reconciling — Flux has not settled yet; ask again later
//	converged   — Flux has settled and the components are up
//	usable      — a real person can log in and get what they came for
//
// Only the last one means the install is done. A check that cannot be performed
// reports SKIPPED with the reason, and a skipped required check never yields
// `usable` — "I could not tell" must not read as "fine".
package accept

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// State is the overall verdict.
type State string

const (
	StateReconciling State = "reconciling"
	StateConverged   State = "converged"
	StateUsable      State = "usable"
)

// Outcome of a single check.
type Outcome string

const (
	Pass    Outcome = "PASS"
	Fail    Outcome = "FAIL"
	Skipped Outcome = "SKIP"
)

// Check is one verified property.
type Check struct {
	Name string
	// Required marks a check that must PASS for `usable`. A non-required check
	// that fails degrades the report but not the verdict.
	Required bool
	Outcome  Outcome
	Detail   string
	// Fix is the concrete next command or edit, present on failure.
	Fix string
}

// Report is the full result.
type Report struct {
	Cluster string
	Domain  string
	State   State
	Checks  []Check
}

// Failed returns the required checks that did not pass.
func (r Report) Failed() []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Required && c.Outcome != Pass {
			out = append(out, c)
		}
	}
	return out
}

// Options drives Run.
type Options struct {
	K8s     ports.K8sClient
	Cluster string
	// Domain enables the front-door TLS check. Empty skips it.
	Domain string
	// HTTPTimeout bounds the front-door probe.
	HTTPTimeout time.Duration
	// Now/dialer seams for tests.
	httpClient *http.Client
	Out        io.Writer
}

// Run performs every check and returns the report. It does not stop at the first
// failure: an operator fixing an install wants the whole list, not a trickle.
func Run(ctx context.Context, o Options) (Report, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.HTTPTimeout == 0 {
		o.HTTPTimeout = 15 * time.Second
	}
	rep := Report{Cluster: o.Cluster, Domain: o.Domain}

	fluxSettled := true
	controlPlanes := 0

	// --- Convergence -----------------------------------------------------
	if graph, err := o.K8s.DiscoverFluxGraph(ctx); err != nil {
		rep.Checks = append(rep.Checks, Check{
			Name: "flux/kustomizations", Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot read Flux state: %v", err),
			Fix:    "check KUBECONFIG points at the cluster and flux-system exists",
		})
		fluxSettled = false
	} else {
		var notReady []string
		for _, n := range graph.Nodes {
			if !n.Ready {
				notReady = append(notReady, n.Name)
			}
		}
		sort.Strings(notReady)
		switch {
		case len(graph.Nodes) == 0:
			rep.Checks = append(rep.Checks, Check{
				Name: "flux/kustomizations", Required: true, Outcome: Fail,
				Detail: "Flux is installed but has reconciled nothing yet",
				Fix:    "flux get kustomizations -A; flux reconcile source git flux-system",
			})
			fluxSettled = false
		case len(notReady) > 0:
			rep.Checks = append(rep.Checks, Check{
				Name: "flux/kustomizations", Required: true, Outcome: Fail,
				Detail: fmt.Sprintf("%d of %d not Ready: %s",
					len(notReady), len(graph.Nodes), strings.Join(notReady, ", ")),
				Fix: "flux get kustomizations -A  (then describe the failing one)",
			})
			fluxSettled = false
		default:
			rep.Checks = append(rep.Checks, Check{
				Name: "flux/kustomizations", Required: true, Outcome: Pass,
				Detail: fmt.Sprintf("all %d Ready", len(graph.Nodes)),
			})
		}
	}

	// --- Nodes -----------------------------------------------------------
	if labels, err := o.K8s.NodeLabels(ctx); err != nil {
		rep.Checks = append(rep.Checks, Check{
			Name: "nodes", Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list nodes: %v", err),
		})
	} else {
		for _, l := range labels {
			if _, ok := l["node-role.kubernetes.io/control-plane"]; ok {
				controlPlanes++
			}
		}
		switch {
		case len(labels) == 0:
			rep.Checks = append(rep.Checks, Check{
				Name: "nodes", Required: true, Outcome: Fail,
				Detail: "the cluster reports ZERO nodes",
				Fix:    "kubectl get nodes; the API answered but no node is registered",
			})
		case controlPlanes == 0:
			rep.Checks = append(rep.Checks, Check{
				Name: "nodes", Required: true, Outcome: Fail,
				Detail: fmt.Sprintf("%d node(s) but NONE labelled control-plane", len(labels)),
				Fix:    "kubectl get nodes -l node-role.kubernetes.io/control-plane",
			})
		default:
			rep.Checks = append(rep.Checks, Check{
				Name: "nodes", Required: true, Outcome: Pass,
				Detail: fmt.Sprintf("%d node(s), %d control-plane", len(labels), controlPlanes),
			})
		}
	}

	// --- THE check nothing else makes ------------------------------------
	rep.Checks = append(rep.Checks, checkOIDCCutover(ctx, o, controlPlanes))

	// --- Front door ------------------------------------------------------
	rep.Checks = append(rep.Checks, checkFrontDoorTLS(ctx, o))

	// --- Tenancy ---------------------------------------------------------
	rep.Checks = append(rep.Checks, checkTenancyReachable(ctx, o))

	// --- Tenant egress ---------------------------------------------------
	rep.Checks = append(rep.Checks, checkTenantEgressGateway(ctx, o))

	// --- Verdict ---------------------------------------------------------
	switch {
	case !fluxSettled:
		rep.State = StateReconciling
	case len(rep.Failed()) > 0:
		rep.State = StateConverged
	default:
		rep.State = StateUsable
	}
	return rep, nil
}

// checkOIDCCutover verifies that EVERY apiserver is actually calling the OIDC
// webhook — the single most consequential unverified property of an install.
//
// It reads the flags the apiservers are RUNNING with, from the static pods RKE2
// registers per control-plane node. That is stronger than checking the config
// file (RKE2 accepts a malformed config and drops the flag) and stronger than
// checking that the authenticator pods are Ready (which proves the webhook is
// alive, not that anything calls it).
//
// A PARTIAL result is a failure with its own message: on a multi-master cluster
// kubectl load-balances, so tokens are accepted only by the wired subset and the
// symptom is intermittent 401s that look like a Keycloak or clock problem.
func checkOIDCCutover(ctx context.Context, o Options, controlPlanes int) Check {
	const name = "identity/oidc-cutover"
	args, err := o.K8s.PodContainerArgs(ctx, "kube-system", "component=kube-apiserver")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot read apiserver pods: %v", err)}
	}
	if len(args) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "no kube-apiserver static pods visible — cannot verify whether logins work",
			Fix:    "kube-dc bootstrap oidc-cutover --dry-run   (verifies per node over SSH instead)"}
	}
	var wired, missing []string
	for pod, a := range args {
		found := false
		for _, flag := range a {
			// The flag AND a non-empty path. A bare flag name would pass while
			// pointing at nothing, and an apiserver whose webhook file is
			// missing rejects EVERY token.
			if strings.HasPrefix(strings.TrimLeft(flag, "-"), "authentication-token-webhook-config-file=") &&
				len(strings.SplitN(flag, "=", 2)[1]) > 0 {
				found = true
				break
			}
		}
		if found {
			wired = append(wired, pod)
		} else {
			missing = append(missing, pod)
		}
	}
	sort.Strings(wired)
	sort.Strings(missing)
	switch {
	case len(missing) == 0 && controlPlanes > 0 && len(wired) < controlPlanes:
		// Every VISIBLE apiserver is wired, but there are fewer of them than
		// control-plane nodes — a node whose apiserver static pod is absent or
		// dead is invisible here, and it is exactly the node that will reject
		// tokens once it comes back.
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("only %d apiserver pod(s) visible for %d control-plane node(s): %s. "+
				"The unaccounted node(s) cannot be verified and would reject every token",
				len(wired), controlPlanes, strings.Join(wired, ", ")),
			Fix: "kubectl -n kube-system get pods -l component=kube-apiserver -o wide"}
	case len(missing) == 0:
		return Check{Name: name, Required: true, Outcome: Pass,
			Detail: fmt.Sprintf("all %d apiserver(s) call the OIDC webhook", len(wired))}
	case len(wired) == 0:
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: "NO apiserver calls the OIDC webhook — every Keycloak login returns 401, " +
				"so tenant kubectl, the console's organization management and the platform " +
				"operators all fail, while the cluster otherwise looks healthy",
			Fix: "kube-dc bootstrap oidc-cutover"}
	default:
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("PARTIAL: wired %s; NOT wired %s. kubectl load-balances across "+
				"apiservers, so logins succeed only sometimes — an intermittent 401 that reads "+
				"as a Keycloak or clock problem",
				strings.Join(wired, ", "), strings.Join(missing, ", ")),
			Fix: "kube-dc bootstrap oidc-cutover   (finished nodes are skipped)"}
	}
}

// checkFrontDoorTLS proves the console is reachable AND presents a certificate
// the operating system trusts — deliberately without the equivalent of curl -k,
// because "works with -k" is what makes an ACME failure invisible until a user
// sees a browser warning.
func checkFrontDoorTLS(ctx context.Context, o Options) Check {
	const name = "front-door/tls"
	if strings.TrimSpace(o.Domain) == "" {
		return Check{Name: name, Required: false, Outcome: Skipped,
			Detail: "no domain known; pass --domain to check the front door"}
	}
	url := "https://console." + o.Domain
	client := o.httpClient
	if client == nil {
		client = &http.Client{
			Timeout: o.HTTPTimeout,
			Transport: &http.Transport{
				// Explicitly NOT InsecureSkipVerify: the point is to verify.
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
				DialContext:     (&net.Dialer{Timeout: o.HTTPTimeout}).DialContext,
			},
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return Check{Name: name, Required: false, Outcome: Skipped, Detail: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		detail := err.Error()
		fix := "check wildcard DNS points at the front door and that :443 is reachable"
		if strings.Contains(detail, "x509") || strings.Contains(detail, "certificate") {
			fix = "the certificate is not trusted yet — kubectl get certificate -A, and check " +
				"the ACME order; DNS must resolve publicly before issuance can succeed"
		}
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%s: %v", url, err), Fix: fix}
	}
	defer resp.Body.Close()
	// TLS verified is not the same as serving. A 404/502/503 means the
	// certificate is fine and the console is not actually there — which reads as
	// a working front door to anyone only checking that the request completed.
	if resp.StatusCode >= 400 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%s presented a trusted certificate but answered %s", url, resp.Status),
			Fix:    "kubectl -n kube-dc get pods,httproutes; the front door terminates TLS but nothing serves the console"}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("%s answered %s with a trusted certificate", url, resp.Status)}
}

// checkTenancyReachable confirms the tenancy API surface exists, and warns about
// the specific shape that makes a tenant login succeed while producing no
// kubectl access at all: an Organization with no Project.
func checkTenancyReachable(ctx context.Context, o Options) Check {
	const name = "tenancy/crds"
	crds, err := o.K8s.ListCRDs(ctx)
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list CRDs: %v", err)}
	}
	need := map[string]bool{
		"organizations.kube-dc.com": false,
		"projects.kube-dc.com":      false,
	}
	for _, c := range crds {
		if _, ok := need[c]; ok {
			need[c] = true
		}
	}
	var missing []string
	for c, present := range need {
		if !present {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: "missing tenancy CRDs: " + strings.Join(missing, ", "),
			Fix:    "the kube-dc platform has not installed; flux get kustomizations -A"}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: "Organization and Project CRDs present"}
}

// checkTenantEgressGateway verifies that SOMETHING actually answers for the
// address every tenant VPC uses as its default next hop.
//
// This is the check whose absence has now cost several clusters. Kube-DC treats
// the external gateway as a fact about the environment — the address of a router
// somebody else operates. On a site backed by real network gear that is true: the
// address belongs to a redundant router pair and there is nothing for us to own.
// On a provider that sells pure layer-2 segments with no SVI it is false, and
// whatever the installer happens to ship becomes the tenant router by accident.
//
// Both halves of that have now failed in the field:
//
//   - the upstream network never brought the agreed gateway up, so tenant egress
//     was dead from day one and the install still reported healthy;
//   - the installer's own jump host WAS the router, but its address had been
//     hardcoded to a different location's subnet, so it answered on an address
//     nobody used.
//
// In both cases the cluster passed acceptance as `usable` with no tenant egress
// at all, and the failure surfaced days later, several layers away, as managed
// clusters that could not provision storage. Ingress was covered by the
// front-door check; egress was covered by nothing.
//
// The probe is ARP, not ping, and that distinction is the whole check. ICMP
// proves nothing here twice over: a ping can be answered via some OTHER route
// and say nothing about the next hop, and a gateway that is a load-balancer VIP
// answers ARP while nothing answers ICMP at all. Probing such a gateway with
// ping and then reading the neighbour table reports a healthy gateway as dead,
// because the failed ICMP drives the entry straight to FAILED.
//
// So it arpings the gateway on whichever device routes to it and looks for a
// reply — that is exactly "does a device on this segment own this address",
// which is the property in question. The neighbour table is only a fallback for
// an image without arping. A silent gateway matches what the OVN side shows as
// an empty mac_binding.
//
// It runs from the kube-ovn CNI pods because they are hostNetwork and therefore
// see the node's real neighbour table. Only nodes carrying an external-network
// anchor can resolve the gateway, and this cannot map pods to nodes, so it asks
// every CNI pod and passes if ANY of them resolves it. That is the correct
// quantifier anyway: the gateway needs one working path, not N.
func checkTenantEgressGateway(ctx context.Context, o Options) Check {
	const name = "network/tenant-egress-gw"

	enabled, err := o.K8s.GetConfigMapData(ctx, "kube-system", "ovn-external-gw-config", "enable-external-gw")
	if err != nil || strings.TrimSpace(enabled) != "true" {
		return Check{Name: name, Outcome: Skipped,
			Detail: "no kube-ovn external gateway configured on this cluster"}
	}

	addr, err := o.K8s.GetConfigMapData(ctx, "kube-system", "ovn-external-gw-config", "external-gw-addr")
	if err != nil || strings.TrimSpace(addr) == "" {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: "external gateway is enabled but external-gw-addr is empty",
			Fix:    "set EXT_NET_GATEWAY in the cluster's cluster-config.env and reconcile"}
	}
	gw := strings.TrimSpace(addr)
	if i := strings.IndexByte(gw, '/'); i >= 0 {
		gw = gw[:i] // the CM carries it as CIDR, the neighbour table wants the address
	}

	pods, err := o.K8s.ListPodNames(ctx, "kube-system", "app=kube-ovn-cni")
	if err != nil || len(pods) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot reach the kube-ovn CNI pods to probe %s", gw),
			Fix:    "kubectl -n kube-system get pods -l app=kube-ovn-cni"}
	}

	// Bounded: a large cluster should not turn acceptance into an exec storm.
	const maxProbes = 6
	if len(pods) > maxProbes {
		pods = pods[:maxProbes]
	}

	// Nodes without an external-network anchor have no route to the gateway at
	// all; they report NOROUTE and are not counted as evidence either way.
	probe := fmt.Sprintf(`
dev=$(ip route get %[1]s 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p' | head -1)
[ -n "$dev" ] || { echo NOROUTE; exit 0; }
n=$(ip neigh show %[1]s 2>/dev/null)
case "$n" in
  *lladdr*)
    echo "$n" | grep -qE 'FAILED|INCOMPLETE' || { echo "RESOLVED $n"; exit 0; }
    ;;
esac
if command -v arping >/dev/null 2>&1; then
  # Exit status, not output text: arping implementations disagree on wording
  # (iputils prints "60 bytes from <mac>", others "Unicast reply from"), but
  # all exit 0 on a reply. Kept to one probe with a short deadline so the whole
  # exec stays sub-second-ish -- a slow exec loses its output to the apiserver
  # WebSocket stream far more often than it returns a useful answer.
  arping -c 1 -w 2 -I "$dev" %[1]s >/dev/null 2>&1 && { echo ARPREPLY; exit 0; }
fi
ip neigh show %[1]s
`, gw)
	var probed int
	for _, pod := range pods {
		// The in-process WebSocket exec drops output from a slow command often
		// enough to turn a healthy gateway into a reported black hole, so fall
		// back to kubectl exec (a separate implementation of the same protocol)
		// whenever it errors or comes back empty.
		out, err := o.K8s.PodExec(ctx, "kube-system", pod,
			[]string{"sh", "-c", probe}, nil)
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			if alt, altErr := o.K8s.PodExecViaKubectl(ctx, "kube-system", pod,
				[]string{"sh", "-c", probe}, nil); altErr == nil {
				out, err = alt, nil
			}
		}
		if err != nil {
			continue // pod may be starting, or lack the tools; try the next
		}
		line := strings.TrimSpace(string(out))
		if strings.Contains(line, "NOROUTE") {
			continue // node is not on the external segment; it cannot answer this
		}
		probed++
		if strings.Contains(line, "ARPREPLY") || strings.Contains(line, "RESOLVED") {
			return Check{Name: name, Required: true, Outcome: Pass,
				Detail: fmt.Sprintf("%s answers ARP on the external segment", gw)}
		}
		if strings.Contains(line, "lladdr") &&
			!strings.Contains(line, "FAILED") &&
			!strings.Contains(line, "INCOMPLETE") {
			return Check{Name: name, Required: true, Outcome: Pass,
				Detail: fmt.Sprintf("%s is owned on the external segment (%s)", gw, line)}
		}
	}

	if probed == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("no probed node has a route to %s, so none could test it", gw),
			Fix:    "check EXT_NET_ANCHOR_IPS — at least one gateway node must hold an address on the external segment"}
	}
	return Check{Name: name, Required: true, Outcome: Fail,
		Detail: fmt.Sprintf("NOTHING answers ARP for the tenant gateway %s on %d probed node(s) — every tenant VPC's default route is a black hole", gw, probed),
		Fix:    "confirm who is meant to own this address. Provider-routed site: the upstream router/SVI is down or the VLAN is not trunked. Provider L2 with no SVI (e.g. CloudSigma): something WE ship must own it — check the bastion holds it in the right subnet, or enable EXT_NET_NODE_EGRESS_ENABLED with a gateway VIP"}
}

// Render writes the report as an operator-facing summary.
func Render(w io.Writer, rep Report) {
	fmt.Fprintf(w, "\n=== acceptance: %s ===\n", rep.Cluster)
	for _, c := range rep.Checks {
		req := " "
		if c.Required {
			req = "*"
		}
		fmt.Fprintf(w, " [%s]%s %-26s %s\n", c.Outcome, req, c.Name, c.Detail)
		if c.Outcome != Pass && c.Fix != "" {
			fmt.Fprintf(w, "          fix: %s\n", c.Fix)
		}
	}
	fmt.Fprintf(w, "\nstate: %s\n", rep.State)
	switch rep.State {
	case StateUsable:
		fmt.Fprintln(w, "The cluster is usable: identity works, the front door is trusted, tenancy is installed.")
	case StateConverged:
		fmt.Fprintln(w, "Flux has settled but the cluster is NOT yet usable — the starred failures above")
		fmt.Fprintln(w, "are things a user would hit. Fix them, then re-run.")
	case StateReconciling:
		fmt.Fprintln(w, "Still reconciling. Wait for Flux to settle, then re-run — most checks below")
		fmt.Fprintln(w, "cannot be trusted until it has.")
	}
	fmt.Fprintln(w, "(* = required for `usable`)")
}
