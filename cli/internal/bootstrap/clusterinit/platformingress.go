package clusterinit

import (
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// KeyPlatformIngressVIP names the address the platform HTTPS front door
// (the Envoy Gateway LoadBalancer — bao.<domain>, s3.<domain>) answers on
// from inside the cluster. The manager turns it into ONE exact TCP/443 egress
// rule on the managed control-plane security group, so the kms-plugin
// sidecar can reach OpenBao Transit and the etcd-backup Job can reach S3 over
// the locked infra NIC (kube-dc internal/infraattachment/config.go). It is
// only meaningful when the address sits inside INFRA_ATTACHMENT_ROUTES; outside
// them the pod takes the tenant route and the manager rejects the key as a
// dead rule.
//
// Who seeds it, and why it is split in two:
//
//   - postProcessClusterConfig, from platformIngressVIPCandidate: the one case
//     an env file can ESTABLISH on its own — a MetalLB layer whose announced
//     VIP is the arrival address;
//   - Scaffold, through reconcilePlatformIngressVIP: the none layer, where the
//     file cannot tell a NAT-free node from a 1:1-NAT one (add-cluster.sh
//     writes NODE_EXTERNAL_IP and KUBE_API_ARRIVAL_IP from the same argument,
//     which under NAT is already the post-NAT address) and only the scaffold's
//     SingleIPNAT detection and its own resolved node address know.
//
// An explicit value — operator --set, a saved config, a clone — is never
// discarded: it is written as given and, when it disagrees with the address
// this cluster establishes, called out loudly (a clone's sibling VIP is
// exactly that case). Validation demands the key only for the establishable
// case; everything else is the scaffold's to seed or warn about, and the
// encryption runbook's step D proves it live.
const KeyPlatformIngressVIP = "INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP"

// PlatformIngressVIPNone is the persistable spelling of "deliberately no
// front-door rule": --set INFRA_ATTACHMENT_PLATFORM_INGRESS_VIP=none. A bare
// empty --set means the same thing and is normalised to it, non-mutatingly,
// at every boundary an explicit input crosses: EnvMapFor (the map preflight
// validation and the scaffold read), inputsForHash (the reviewed-plan hash,
// so --save-config → --apply-plan does not drift) and ExportMap (so
// --save-config / --config keep the decision; empty values are dropped
// there). A reload that lost it would re-seed what the operator declined
// (codex review 2026-09-08, passes 3-5). An ABSENT key is a different input
// — "let the scaffold decide" — and stays absent everywhere.
//
// The manager's contract stays IP-or-empty: reconcilePlatformIngressVIP
// rewrites the sentinel to an EMPTY value in cluster-config.env before
// anything else, and the chart treats a literal "none" as empty for the
// paths that bypass the scaffold (`kube-dc bootstrap config set KEY=none`
// writes it verbatim; so does a hand edit).
const PlatformIngressVIPNone = "none"

// platformIngressVIPPersisted is the spelling an explicit input keeps on
// disk: an empty explicit value becomes the sentinel, anything else is kept.
func platformIngressVIPPersisted(v string) string {
	if strings.TrimSpace(v) == "" {
		return PlatformIngressVIPNone
	}
	return strings.TrimSpace(v)
}

// normalizeSetsForHash returns sets with the front-door key spelled the way
// it is persisted, so an explicit empty --set and its saved form hash
// identically and a reviewed plan survives --save-config → --apply-plan.
// Non-mutating; sets is returned as-is when nothing needs to change, and an
// absent key stays absent.
func normalizeSetsForHash(sets map[string]string) map[string]string {
	v, ok := sets[KeyPlatformIngressVIP]
	if !ok || platformIngressVIPPersisted(v) == v {
		return sets
	}
	out := make(map[string]string, len(sets))
	for k, val := range sets {
		out[k] = val
	}
	out[KeyPlatformIngressVIP] = platformIngressVIPPersisted(v)
	return out
}

// platformIngressVIPCandidate returns the address bao./s3.<domain> resolve to
// for a pod inside this cluster, or "" when the env cannot ESTABLISH one.
//
// KUBE_API_ARRIVAL_IP is not that address in general: the off-Envoy contract
// (fleet platform/front-door/components/kube-api-off-envoy) defines it as the
// destination seen at node PREROUTING — on 1:1-NAT the post-NAT address,
// while DNS names the public one (codex review 2026-09-08, MEDIUM #5). Only
// one topology lets the file prove they coincide: a MetalLB layer whose
// announced VIP IS the arrival address — the front door and kube-api share
// that VIP by construction.
//
// The platform-endpoint overlay (PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED) is
// deliberately NOT treated as proof. Enabling it creates the internal VIP's
// pool and Service; pointing bao.<domain> at that VIP is a separate, manual
// vpc-dns rewrite the CLI cannot see, and a clone carries the flag and the VIP
// without the rewrite (codex review 2026-09-08, pass 2). With the overlay on,
// nothing is establishable here: the operator sets the key as the overlay
// flow's last step, and the scaffold says so.
//
// Anything else — the none layer (see KeyPlatformIngressVIP), an arrival
// address imported from a sibling that no longer matches this cluster's VIP,
// a placeholder — yields "". METALLB_FLOATING_IP is never read off a MetalLB
// layer, where it is an unannounced leftover (kubeapi_offenvoy.go).
func platformIngressVIPCandidate(get func(string) string) string {
	if platformEndpointOverlayEnabled(get) {
		return ""
	}
	return metalLBFrontDoor(get)
}

// metalLBFrontDoor is the one address an env file can prove on its own: on a
// MetalLB layer, the announced VIP when it is also the arrival address. "" on
// any other layer, on a placeholder, or when a clone's inherited arrival
// address no longer matches this cluster's VIP.
func metalLBFrontDoor(get func(string) string) string {
	if !addressLayerRequiresVIP(get("INGRESS_ADDRESS_LAYER")) {
		return ""
	}
	arrival := canonicalIPv4(get("KUBE_API_ARRIVAL_IP"))
	if arrival != "" && canonicalIPv4(get("METALLB_FLOATING_IP")) == arrival {
		return arrival
	}
	return ""
}

func platformEndpointOverlayEnabled(get func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(get("PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED")), "true")
}

// canonicalIPv4 returns text when it is a canonical IPv4 literal, else "".
func canonicalIPv4(text string) string {
	text = strings.TrimSpace(text)
	ip := net.ParseIP(text)
	if ip == nil || ip.To4() == nil || ip.String() != text {
		return ""
	}
	return text
}

// routeCoversIP reports whether one of the comma-separated CIDRs contains
// ipText. Malformed entries are skipped; validateInfraAttachment reports those.
func routeCoversIP(routes, ipText string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipText))
	if ip == nil {
		return false
	}
	for _, route := range strings.Split(routes, ",") {
		if _, network, err := net.ParseCIDR(strings.TrimSpace(route)); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// reconcilePlatformIngressVIP is Scaffold's last word on the front door, run
// once the processed cluster-config.env is on disk. nodeIP is the address the
// scaffold itself resolved for THIS cluster (opts.NodeExternalIP: the
// operator-declared address, or the arriving one when SingleIPNAT detection
// substituted it) — never the file's KUBE_API_ARRIVAL_IP, which a clone can
// carry over from a sibling unchanged (codex review 2026-09-08, pass 2 HIGH).
//
// It establishes this cluster's front door where it can:
//   - a MetalLB layer: the announced VIP, when it is the arrival address
//     (postProcessClusterConfig has already seeded it);
//   - a none layer with no 1:1 NAT in front: nodeIP, the node's own address,
//     which is what DNS names;
//   - never under the platform-endpoint overlay (see platformIngressVIPCandidate).
//
// Then, in order: a deliberate "no rule" (the none sentinel; an empty --set
// has been normalised to it) is written as empty — before the dual-homing
// check, so the sentinel never survives in the file — and called out when it
// costs reach; an explicit
// value is kept as given and announced only when it IS the established
// address — otherwise the operator hears why it could not be checked, or that
// it disagrees; an established address inside the routes is seeded and
// announced; one outside the routes is left alone silently (tenant-route
// reach, nothing to grant); and when nothing could be established while the
// relevant address sits inside the routes, a WARNING names the key to set.
func reconcilePlatformIngressVIP(path, nodeIP string, singleIPNAT bool, sets map[string]string, clusterName string, out io.Writer) error {
	env, err := config.LoadEnv(path)
	if err != nil {
		return err
	}
	get := func(k string) string { return envGet(env, k) }
	current := strings.TrimSpace(get(KeyPlatformIngressVIP))
	_, explicit := sets[KeyPlatformIngressVIP]
	// The sentinel (or a bare explicit empty, should a caller skip
	// postProcessClusterConfig) is a decision, never a value: the file the
	// manager reads carries EMPTY — regardless of whether dual-homing is on.
	declined := current == PlatformIngressVIPNone || (current == "" && explicit)
	if declined && current != "" {
		env.Set(KeyPlatformIngressVIP, "")
		if err := env.Write(""); err != nil {
			return err
		}
	}
	if strings.TrimSpace(get("INFRA_ATTACHMENT_ENABLED")) != "true" {
		return nil
	}
	routes := get("INFRA_ATTACHMENT_ROUTES")
	overlay := platformEndpointOverlayEnabled(get)

	established := ""
	switch {
	case overlay:
		// Cannot be proven from here; see platformIngressVIPCandidate.
	case addressLayerRequiresVIP(get("INGRESS_ADDRESS_LAYER")):
		established = platformIngressVIPCandidate(get)
	case !singleIPNAT:
		established = canonicalIPv4(nodeIP)
	}

	announce := func(vip string) {
		fmt.Fprintf(out, "[scaffold] managed control plane: front door %s is inside the infra routes; %s=%s (exact TCP/443 egress for OpenBao Transit + S3)\n", vip, KeyPlatformIngressVIP, vip)
	}
	// A deliberate "no rule": already written as EMPTY above; called out
	// when this cluster's front door does sit inside the routes, because then
	// the decision costs OpenBao/S3 reach for managed control planes.
	if declined {
		if established != "" && routeCoversIP(routes, established) {
			fmt.Fprintf(out, "[scaffold] WARNING: %s=%s: no front-door egress rule, although this cluster's front door %s sits inside the infra routes — managed control planes will not reach OpenBao/S3 until the key is set\n", KeyPlatformIngressVIP, PlatformIngressVIPNone, established)
		}
		return nil
	}

	// An explicit value (--set, a saved config, a clone) is always kept as
	// given. It is announced only when it IS this cluster's established front
	// door; otherwise the operator hears exactly why it could not be checked —
	// a value inherited from a sibling looks identical to a deliberate one
	// (codex review 2026-09-08, pass 3 HIGH).
	if current != "" {
		switch {
		case established == "":
			fmt.Fprintf(out, "[scaffold] WARNING: explicit %s=%s cannot be verified from here (platform-endpoint overlay, 1:1 NAT, or KUBE_API_ARRIVAL_IP does not match this cluster's announced VIP): confirm bao.<domain> resolves to %s inside the cluster — a value cloned from a sibling looks identical, and a wrong one leaves managed control planes unable to reach OpenBao/S3\n", KeyPlatformIngressVIP, current, current)
		case established != current:
			fmt.Fprintf(out, "[scaffold] WARNING: explicit %s=%s differs from this cluster's established front door %s — keeping the explicit value; if this file was cloned from a sibling, clear the key so the scaffold re-derives it\n", KeyPlatformIngressVIP, current, established)
		default:
			announce(current)
		}
		return nil
	}

	if established != "" {
		if !routeCoversIP(routes, established) {
			return nil // reached over the tenant route; nothing to grant
		}
		env.Set(KeyPlatformIngressVIP, established)
		if err := env.Write(""); err != nil {
			return err
		}
		announce(established)
		return nil
	}

	// Nothing established. Say so only where it matters: the overlay VIP is
	// inside the routes (or unknown), or the address the cluster arrives on is.
	switch {
	case overlay:
		internal := canonicalIPv4(get("ENVOY_GATEWAY_INTERNAL_VIP"))
		switch {
		case internal == "":
			fmt.Fprintf(out, "[scaffold] WARNING: the platform-endpoint overlay is on but ENVOY_GATEWAY_INTERNAL_VIP is not a canonical IPv4 address; once the vpc-dns rewrite points bao.<domain> at the internal VIP and that VIP sits inside INFRA_ATTACHMENT_ROUTES, set %s to it in clusters/%s/cluster-config.env (overlay flow step 6) or managed-cluster control planes cannot reach OpenBao/S3\n", KeyPlatformIngressVIP, clusterName)
		case routeCoversIP(routes, internal):
			fmt.Fprintf(out, "[scaffold] WARNING: the platform-endpoint overlay is on and ENVOY_GATEWAY_INTERNAL_VIP=%s sits inside INFRA_ATTACHMENT_ROUTES, but the CLI cannot see whether the vpc-dns rewrite already points bao.<domain> at it; once it does, set %s=%s in clusters/%s/cluster-config.env (overlay flow step 6) or managed-cluster control planes cannot reach OpenBao/S3\n", internal, KeyPlatformIngressVIP, internal, clusterName)
		default:
			// Internal VIP outside the routes: reached over the tenant route,
			// nothing to grant, and the manager would refuse it anyway.
		}
	case addressLayerRequiresVIP(get("INGRESS_ADDRESS_LAYER")) || routeCoversIP(routes, canonicalIPv4(get("KUBE_API_ARRIVAL_IP"))):
		fmt.Fprintf(out, "[scaffold] WARNING: could not establish the platform front-door address (1:1-NAT without the platform-endpoint overlay, or KUBE_API_ARRIVAL_IP does not match this cluster's announced VIP); if bao.<domain> resolves to an address inside INFRA_ATTACHMENT_ROUTES, set %s in clusters/%s/cluster-config.env or managed-cluster control planes cannot reach OpenBao/S3\n", KeyPlatformIngressVIP, clusterName)
	}
	return nil
}

// validatePlatformIngressVIP guards the one egress rule the manager adds for
// managed control planes. Two mistakes are caught, both silent at install time:
//
//   - a value outside INFRA_ATTACHMENT_ROUTES: the manager refuses it at
//     startup, leaving kube-dc-manager in CrashLoopBackOff;
//   - no value while an ESTABLISHED front door sits inside a route (a MetalLB
//     VIP that is the arrival address): the dial rides the locked infra NIC
//     and times out, the tenant apiserver never becomes Ready and CAPI never
//     creates a worker (found live on a lab cluster, 2026-09-08). The scaffold
//     seeds the key in that case; an env written before the key existed, or an
//     operator who cleared it, lands here.
//
// The none layer and the platform-endpoint overlay are deliberately NOT
// demanded: an env file cannot tell a NAT-free node from a 1:1-NAT one, nor
// whether the overlay's DNS rewrite is in place. Scaffold seeds or warns
// (reconcilePlatformIngressVIP) and the encryption runbook's step D proves it
// live. With dual-homing off the chart never renders the key, so only its
// shape is checked.
func validatePlatformIngressVIP(envMap map[string]string, dualHomed bool, routes string, errs *[]string) {
	vip := strings.TrimSpace(envMap[KeyPlatformIngressVIP])
	if vip == PlatformIngressVIPNone {
		return // a deliberate decline; the scaffold rewrites it to empty and warns if it costs reach
	}
	if vip == "" {
		if !dualHomed {
			return
		}
		candidate := platformIngressVIPCandidate(func(k string) string { return envMap[k] })
		if candidate != "" && routeCoversIP(routes, candidate) {
			*errs = append(*errs, fmt.Sprintf(
				"%s must be set when INFRA_ATTACHMENT_ENABLED=true and the front door %s sits inside INFRA_ATTACHMENT_ROUTES "+
					"(managed control-plane pods dial bao./s3.<domain> over the locked infra NIC; without the exact TCP/443 egress rule "+
					"the kms-plugin never logs in, the tenant apiserver never becomes Ready and no worker is created) — set %s=%s",
				KeyPlatformIngressVIP, candidate, KeyPlatformIngressVIP, candidate))
		}
		return
	}
	if canonicalIPv4(vip) == "" {
		*errs = append(*errs, fmt.Sprintf("%s=%q must be a canonical IPv4 address", KeyPlatformIngressVIP, vip))
		return
	}
	if dualHomed && routes != "" && !routeCoversIP(routes, vip) {
		*errs = append(*errs, fmt.Sprintf(
			"%s=%s is outside every INFRA_ATTACHMENT_ROUTES entry (%s); the manager rejects it at startup because control-plane pods "+
				"would reach that address over the tenant route, where an infra egress rule never applies — add its subnet to the routes or clear the key",
			KeyPlatformIngressVIP, vip, routes))
	}
}
