package accept

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Node-level dataplane checks. Everything in this file exists because of one
// class of failure: the control plane (CRs, OVN NB/SB, lflows, even
// ofproto/trace) looks perfect while the wire is dead. All three checks were
// paid for on webdock 2026-08-31 (kube-dc
// docs/internal/issues/ext-public-kernel-vlan-steals-ingress.md):
//
//   - a kernel VLAN subinterface on a provider VLAN steals every tagged frame
//     before the OVS rx_handler runs, so OVN never sees a single packet;
//   - an UNMANAGED flow-restore-wait left true keeps a stock-OVS datapath in
//     its restore state (no new flows) — while the fork's managed true/true
//     state is normal operation and must NOT be flagged;
//   - a retired/refusing public anchor silently removes the host's address
//     and the VIP return route.
//
// Probes exec `ip`/`ovs-vsctl` through the per-node ovs-ovn pods (hostNetwork,
// always present on a kube-ovn cluster), mirroring how the NB checks exec
// through ovn-central.
//
// PROBE CONTRACT (Codex P0, 2026-08-31): legitimately-empty command output is
// indistinguishable from a broken exec transport unless the script proves it
// ran — every probe script must end by emitting the probeOK sentinel on
// success, and a command whose failure would silently fake a healthy value
// must emit PROBE_ERR instead. A node without the sentinel does not count as
// probed, and a check that probed nothing reports a REQUIRED skip so the
// cluster cannot go `usable` on unverified dataplane state.

const probeOK = "PROBE_OK"

// clusterNodeNames returns every node in the cluster — the CLOSED universe
// the dataplane checks must cover. Deriving the universe from Ready ovs pods
// alone is fail-open: a node whose ovs pod is missing or unready simply
// vanishes from the report while its dataplane stays unverified (Codex P0).
func clusterNodeNames(ctx context.Context, o Options) ([]string, error) {
	objs, err := o.K8s.ListResourceObjects(ctx, "", "v1", "nodes", "")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range objs {
		if v := nestedString(n, "metadata", "name"); v != "" {
			names = append(names, v)
		}
	}
	sort.Strings(names)
	return names, nil
}

// ovsPodsByNode maps nodeName -> a Ready kube-ovn OVS pod on that node.
// Ready, not merely Running: mid-rollout pods answer probes with transient
// state (flow-restore-wait is legitimately true for seconds at OVS start).
func ovsPodsByNode(ctx context.Context, o Options) (map[string]string, error) {
	pods, err := o.K8s.ListResourceObjects(ctx, "", "v1", "pods", "kube-system")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range pods {
		if nestedString(p, "metadata", "labels", "app") != "ovs" {
			continue
		}
		if nestedString(p, "status", "phase") != "Running" {
			continue
		}
		ready := false
		for _, c := range nestedSlice(p, "status", "conditions") {
			cm, _ := c.(map[string]any)
			if nestedString(cm, "type") == "Ready" && nestedString(cm, "status") == "True" {
				ready = true
				break
			}
		}
		if !ready {
			continue
		}
		node := nestedString(p, "spec", "nodeName")
		// Bare metadata.name, NOT objName (which prefixes the namespace and
		// would corrupt the exec URL into a list request).
		if node != "" {
			out[node] = nestedString(p, "metadata", "name")
		}
	}
	return out, nil
}

// execNode runs a shell script in the node's OVS pod, with the same
// kubectl fallback discipline as the ovn-central checks. Callers decide
// success by the probeOK sentinel in the output, never by emptiness.
func execNode(ctx context.Context, o Options, pod, script string) (string, error) {
	b, err := o.K8s.PodExec(ctx, "kube-system", pod, []string{"sh", "-c", script}, nil)
	if err != nil || !strings.Contains(string(b), probeOK) {
		alt, altErr := o.K8s.PodExecViaKubectl(ctx, "kube-system", pod, []string{"sh", "-c", script}, nil)
		if altErr == nil && strings.Contains(string(alt), probeOK) {
			return string(alt), nil
		}
		if err == nil {
			err = altErr
		}
		if err == nil {
			err = fmt.Errorf("probe ran but did not confirm success (no %s)", probeOK)
		}
		return "", err
	}
	return string(b), nil
}

var kernelVLANLine = regexp.MustCompile(`^\d+:\s+(\S+?)@(\S+?):\s.*vlan(?: protocol \S+)? id (\d+)`)

// parseKernelVLANs extracts (device, parent, vlanID) triples from
// `ip -d -o link show type vlan` output.
func parseKernelVLANs(out string) [][3]string {
	var res [][3]string
	for _, line := range strings.Split(out, "\n") {
		if m := kernelVLANLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			res = append(res, [3]string{m[1], m[2], m[3]})
		}
	}
	return res
}

// ovnVLAN models one Vlan CR: which interfaces its provider actually rides,
// with per-node scoping for customInterfaces. VLAN ids are scoped to a trunk,
// not a host — a kernel eth2.100 is harmless when OVN's VLAN 100 lives on
// bond0 — and one id may legitimately be used by several providers, so the
// map keeps ALL candidates per id (a single-entry map let one provider hide
// another's overlap; Codex P1 both rounds).
type ovnVLAN struct {
	crName  string
	known   bool                           // provider resolved; false = match any parent (fail closed)
	parents map[string]map[string]struct{} // iface -> node set (empty set = all nodes)
}

func buildOVNVLANs(vlans, pns []map[string]any) map[string][]ovnVLAN {
	type pnModel struct{ ifaces map[string]map[string]struct{} }
	pnByName := map[string]pnModel{}
	for _, pn := range pns {
		name := nestedString(pn, "metadata", "name")
		m := pnModel{ifaces: map[string]map[string]struct{}{}}
		if v := nestedString(pn, "spec", "defaultInterface"); v != "" {
			m.ifaces[v] = map[string]struct{}{}
		}
		for _, ci := range nestedSlice(pn, "spec", "customInterfaces") {
			cim, _ := ci.(map[string]any)
			iface := nestedString(cim, "interface")
			if iface == "" {
				continue
			}
			nodes := map[string]struct{}{}
			for _, n := range nestedSlice(cim, "nodes") {
				if ns, ok := n.(string); ok && ns != "" {
					nodes[ns] = struct{}{}
				}
			}
			m.ifaces[iface] = nodes
		}
		m.ifaces["br-"+name] = map[string]struct{}{}
		pnByName[name] = m
	}
	out := map[string][]ovnVLAN{}
	for _, v := range vlans {
		id, ok := nestedInt(v, "spec", "id")
		if !ok || id <= 0 {
			continue
		}
		entry := ovnVLAN{crName: objName(v)}
		if prov := nestedString(v, "spec", "provider"); prov != "" {
			if m, resolved := pnByName[prov]; resolved {
				entry.known, entry.parents = true, m.ifaces
			}
		}
		key := fmt.Sprintf("%d", id)
		out[key] = append(out[key], entry)
	}
	return out
}

func (v ovnVLAN) parentMatches(parent, node string) bool {
	if !v.known {
		return true // provider unknown — conservative
	}
	nodes, ok := v.parents[parent]
	if !ok {
		return false
	}
	if len(nodes) == 0 {
		return true // interface not node-scoped
	}
	_, ok = nodes[node]
	return ok
}

// checkProviderVLANKernelDevices fails when any node carries a kernel VLAN
// subinterface that shadows an OVN provider VLAN on the same trunk. The
// kernel delivers tagged frames to a matching VLAN device BEFORE the OVS
// rx_handler (vlan_do_receive precedes it in __netif_receive_skb_core), so
// such a device makes OVN mute on that VLAN on that node: tenant EIP/FIP ARP
// goes unanswered and the MetalLB anchor announcer is deaf — while every
// control-plane object still reports healthy.
func checkProviderVLANKernelDevices(ctx context.Context, o Options) Check {
	const name = "network/kernel-vlan-overlap"
	vlans, err := o.K8s.ListResourceObjects(ctx, "kubeovn.io", "v1", "vlans", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list kube-ovn Vlans: %v", err)}
	}
	pns, err := o.K8s.ListResourceObjects(ctx, "kubeovn.io", "v1", "provider-networks", "")
	if err != nil {
		pns = nil // fall back to id-only matching, noted below
	}
	ovnIDs := buildOVNVLANs(vlans, pns)
	if len(ovnIDs) == 0 {
		return Check{Name: name, Outcome: Skipped, Detail: "no VLAN-tagged provider networks — nothing to overlap"}
	}
	pods, err := ovsPodsByNode(ctx, o)
	if err != nil || len(pods) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "cannot find Ready per-node ovs-ovn pods to probe host links",
			Fix:    "kubectl -n kube-system get pods -l app=ovs -o wide"}
	}
	nodes, err := clusterNodeNames(ctx, o)
	if err != nil || len(nodes) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list cluster nodes to bound the probe universe: %v", err)}
	}
	var offenders, unprobed []string
	var lastErr error
	probed := 0
	for _, node := range nodes {
		pod, ok := pods[node]
		if !ok {
			unprobed = append(unprobed, node+" (no Ready ovs-ovn pod)")
			continue
		}
		out, execErr := execNode(ctx, o, pod,
			"ip -d -o link show type vlan 2>/dev/null && echo "+probeOK)
		if execErr != nil {
			lastErr = execErr
			unprobed = append(unprobed, node)
			continue
		}
		probed++
		for _, kv := range parseKernelVLANs(out) {
			for _, v := range ovnIDs[kv[2]] {
				if v.parentMatches(kv[1], node) {
					offenders = append(offenders,
						fmt.Sprintf("%s: %s (vlan %s = OVN vlan %q, parent %s)", node, kv[0], kv[2], v.crName, kv[1]))
					break
				}
			}
		}
	}
	if probed == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("could not read host links from any ovs-ovn pod (last error: %v)", lastErr)}
	}
	if len(offenders) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: "kernel VLAN device on an OVN provider VLAN: " + strings.Join(offenders, "; "),
			Fix: "the kernel steals every tagged frame before OVS sees it — OVN cannot answer ARP for tenant " +
				"EIPs/FIPs and anchor-based MetalLB announcements are deaf on these nodes, while all CRs look " +
				"healthy. Move the host's address + default route onto the OVS anchor (ext-net-bridge-tag " +
				"EXT_NET_PUBLIC_ANCHOR_IPS + EXT_NET_PUBLIC_ANCHOR_DEFAULT_ROUTE=true), update netplan, then " +
				"delete the kernel device. Runbook: kube-dc docs/internal/issues/ext-public-kernel-vlan-steals-ingress.md",
		}
	}
	if len(unprobed) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("no overlap on %d probed node(s), but %s could not be probed (last error: %v)",
				probed, strings.Join(unprobed, ", "), lastErr),
			Fix: "every node must be verified — a single unprobed gateway node can hide the overlap"}
	}
	ids := make([]string, 0, len(ovnIDs))
	for id := range ovnIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	scope := ""
	if pns == nil {
		scope = " (provider interfaces unreadable — matched by VLAN id only)"
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("%d node(s) probed, no kernel VLAN device on OVN VLAN(s) %s%s", probed, strings.Join(ids, ","), scope)}
}

// frwProbe reads flow-restore-wait state from one node. Each ovs-vsctl call
// confirms its own success — a failed read must not masquerade as "clear".
const frwScript = `frw=$(ovs-vsctl --timeout=5 --if-exists get open_vswitch . other_config:flow-restore-wait 2>/dev/null) && echo "FRW=$frw" || echo "PROBE_ERR=frw"; ` +
	`mng=$(ovs-vsctl --timeout=5 --if-exists get open_vswitch . external_ids:ovn-managed-flow-restore-wait 2>/dev/null) && echo "MANAGED=$mng" || echo "PROBE_ERR=managed"; ` +
	`echo ` + probeOK

func frwProbe(ctx context.Context, o Options, pod string) (frw, managed string, err error) {
	out, err := execNode(ctx, o, pod, frwScript)
	if err != nil {
		return "", "", err
	}
	if strings.Contains(out, "PROBE_ERR=") {
		return "", "", fmt.Errorf("ovs-vsctl read failed inside the pod")
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, "FRW="); ok {
			frw = strings.Trim(v, `"`)
		}
		if v, ok := strings.CutPrefix(l, "MANAGED="); ok {
			managed = strings.Trim(v, `"`)
		}
	}
	return frw, managed, nil
}

// checkFlowRestoreWait fails when a node's OVS has
// other_config:flow-restore-wait=true WITHOUT the fork's management marker.
// In stock OVS that state means ovs-vswitchd serves only the restored flow
// table and installs no new flows — normally cleared seconds after a restart,
// fatal if a crashed restore leaves it behind. The kube-ovn fork's
// ovn-controller, however, actively MANAGES the flag around flow-update
// batches and stamps external_ids:ovn-managed-flow-restore-wait=true; on a
// healthy busy cluster the pair reads true/true most of the time (verified on
// stage, 390 days up, all nodes true/true) and is NOT a defect — only an
// UNMANAGED true is. The flag is also legitimately true for seconds during an
// OVS restart, so a suspect node is re-probed after a pause and only
// persistent state fails.
func checkFlowRestoreWait(ctx context.Context, o Options) Check {
	const name = "network/flow-restore-wait"
	pods, err := ovsPodsByNode(ctx, o)
	if err != nil || len(pods) == 0 {
		// Unlike the kernel-vlan check (gated on Vlan CRs, which prove
		// kube-ovn is present), this runs unconditionally — a cluster with no
		// ovs pods at all has no kube-ovn dataplane to be stuck, and its real
		// problems surface in the flux/nodes checks. Pods present but
		// unprobeable IS required-skip (probed==0 below).
		return Check{Name: name, Outcome: Skipped,
			Detail: "no Ready per-node ovs-ovn pods to probe OVS state",
			Fix:    "kubectl -n kube-system get pods -l app=ovs -o wide"}
	}
	nodes, err := clusterNodeNames(ctx, o)
	if err != nil || len(nodes) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list cluster nodes to bound the probe universe: %v", err)}
	}
	var stuck, managed, unprobed []string
	var lastErr error
	probed := 0
	for _, node := range nodes {
		pod, ok := pods[node]
		if !ok {
			unprobed = append(unprobed, node+" (no Ready ovs-ovn pod)")
			continue
		}
		frw, mng, probeErr := frwProbe(ctx, o, pod)
		if probeErr != nil {
			lastErr = probeErr
			unprobed = append(unprobed, node)
			continue
		}
		probed++
		if frw == "true" && mng != "true" {
			stuck = append(stuck, node)
		}
		if mng == "true" {
			managed = append(managed, node)
		}
	}
	if probed == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("could not read OVS state from any ovs-ovn pod (last error: %v)", lastErr)}
	}
	if len(stuck) > 0 {
		// Transient-restart grace: re-probe only the stuck nodes once.
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
		var persistent []string
		for _, node := range stuck {
			if frw, mng, probeErr := frwProbe(ctx, o, pods[node]); probeErr != nil || (frw == "true" && mng != "true") {
				persistent = append(persistent, node)
			}
		}
		stuck = persistent
	}
	if len(stuck) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: "UNMANAGED flow-restore-wait=true (persistent) on: " + strings.Join(stuck, ", "),
			Fix: "stock-OVS stuck restore state: the datapath installs no new flows on these nodes. Clear it " +
				"(ovs-vsctl remove open_vswitch . other_config flow-restore-wait) and restart the node's ovs-ovn " +
				"pod so ovn-controller re-establishes its managed sequence; re-check afterwards. Do NOT pin " +
				"ovn-managed-flow-restore-wait=false — the managed true/true state is the fork's normal operation",
		}
	}
	if len(unprobed) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("clear on %d probed node(s), but %s could not be probed (last error: %v)",
				probed, strings.Join(unprobed, ", "), lastErr),
			Fix: "every node must be verified — an unprobed node can hide a stuck datapath"}
	}
	detail := fmt.Sprintf("%d node(s) probed, no unmanaged flow-restore-wait", probed)
	if len(managed) > 0 {
		detail += fmt.Sprintf(" (%d under ovn-controller management — normal)", len(managed))
	}
	return Check{Name: name, Required: true, Outcome: Pass, Detail: detail}
}

// parseAnchorMap parses "node=ip/prefix,node=ip/prefix" into node -> full
// CIDR. Malformed or duplicate entries are returned as errors — a silently
// dropped entry would exempt that node from verification (Codex P2).
func parseAnchorMap(raw string) (map[string]string, []string) {
	out := map[string]string{}
	var bad []string
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		node, cidr, ok := strings.Cut(pair, "=")
		node, cidr = strings.TrimSpace(node), strings.TrimSpace(cidr)
		if !ok || node == "" || !strings.Contains(cidr, "/") {
			bad = append(bad, pair)
			continue
		}
		if _, dup := out[node]; dup {
			bad = append(bad, pair+" (duplicate node)")
			continue
		}
		out[node] = cidr
	}
	return out, bad
}

// checkPublicAnchors verifies the declared public anchors are actually LIVE on
// their nodes: OVS port present with the right tag, exact address bound, the
// VIP return-path policy route in place, and — when the anchor owns the
// node's default route — that route pointing at it via the public gateway.
// A retired anchor (daemonset REFUSING loop, fabric ARP-suppression ghost,
// manual surgery) takes down the host's public leg and the VIP return path
// with no Kubernetes-visible signal.
func checkPublicAnchors(ctx context.Context, o Options) Check {
	const name = "network/public-anchors"
	cms, err := o.K8s.ListResourceObjects(ctx, "", "v1", "configmaps", "flux-system")
	if err != nil {
		return Check{Name: name, Outcome: Skipped, Detail: fmt.Sprintf("cannot list flux-system ConfigMaps: %v", err)}
	}
	var cfg map[string]any
	for _, cm := range cms {
		if nestedString(cm, "metadata", "name") == "cluster-config" {
			data, _ := nested(cm, "data")
			cfg, _ = data.(map[string]any)
			break
		}
	}
	str := func(k string) string {
		v, _ := cfg[k].(string)
		return strings.TrimSpace(v)
	}
	raw := str("EXT_NET_PUBLIC_ANCHOR_IPS")
	iface := str("EXT_NET_PUBLIC_ANCHOR_INTERFACE")
	vlan := str("EXT_NET_PUBLIC_ANCHOR_VLAN")
	if cfg == nil || raw == "" || iface == "" {
		return Check{Name: name, Outcome: Skipped, Detail: "no public anchors declared — nothing to verify"}
	}
	// Mirror the fleet worker's activation predicate exactly: it retires the
	// anchors when the mode is not l2 OR the VLAN tag is empty, so accept must
	// not demand state the worker intentionally removed.
	if mode := str("METALLB_MODE"); mode != "" && mode != "l2" {
		return Check{Name: name, Outcome: Skipped, Detail: "public anchors inactive (METALLB_MODE=" + mode + ")"}
	}
	if vlan == "" {
		return Check{Name: name, Outcome: Skipped, Detail: "public anchors inactive (EXT_NET_PUBLIC_ANCHOR_VLAN empty)"}
	}
	wantRoute := str("EXT_NET_PUBLIC_ANCHOR_DEFAULT_ROUTE") == "true"
	gw := str("EXT_PUBLIC_GATEWAY")
	vip := str("METALLB_FLOATING_IP")
	anchors, malformed := parseAnchorMap(raw)
	if len(malformed) > 0 || len(anchors) == 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("EXT_NET_PUBLIC_ANCHOR_IPS has unusable entries: %s", strings.Join(malformed, ", ")),
			Fix:    "expected node=ip/prefix[,node=ip/prefix...] in clusters/<name>/cluster-config.env"}
	}
	pods, err := ovsPodsByNode(ctx, o)
	if err != nil || len(pods) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "cannot find Ready per-node ovs-ovn pods to probe anchors",
			Fix:    "kubectl -n kube-system get pods -l app=ovs -o wide"}
	}
	var problems []string
	nodes := make([]string, 0, len(anchors))
	for n := range anchors {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	probed := 0
	for _, node := range nodes {
		pod, ok := pods[node]
		if !ok {
			problems = append(problems, node+": no Ready ovs-ovn pod (node gone or not Ready?)")
			continue
		}
		script := fmt.Sprintf(
			`echo "ADDR=$(ip -4 -o addr show dev %[1]s 2>/dev/null | tr '\n' ' ')"; `+
				`echo "ROUTE=$(ip route show default 2>/dev/null | head -1)"; `+
				`echo "RULE=$(ip rule show pref 100 2>/dev/null | head -1)"; `+
				`echo "T129=$(ip route show table 129 2>/dev/null | tr '\n' ' ')"; `+
				`echo "TAG=$(ovs-vsctl --timeout=5 --if-exists get port %[1]s tag 2>/dev/null)"; `+
				`echo %[2]s`, iface, probeOK)
		out, execErr := execNode(ctx, o, pod, script)
		if execErr != nil {
			problems = append(problems, fmt.Sprintf("%s: probe failed (%v)", node, execErr))
			continue
		}
		probed++
		fields := map[string]string{}
		for _, l := range strings.Split(out, "\n") {
			if k, v, found := strings.Cut(strings.TrimSpace(l), "="); found {
				fields[k] = v
			}
		}
		switch {
		case strings.TrimSpace(fields["ADDR"]) == "":
			problems = append(problems, fmt.Sprintf("%s: %s carries no IPv4 address (anchor retired or REFUSING)", node, iface))
		case !strings.Contains(fields["ADDR"], "inet "+anchors[node]+" "):
			problems = append(problems, fmt.Sprintf("%s: %s does not hold declared anchor %s (has: %s)",
				node, iface, anchors[node], strings.TrimSpace(fields["ADDR"])))
		}
		if tag := strings.TrimSpace(fields["TAG"]); tag != vlan {
			problems = append(problems, fmt.Sprintf("%s: OVS port %s tag is %q, want %s (port missing or mistagged)", node, iface, tag, vlan))
		}
		if wantRoute {
			route := fields["ROUTE"]
			if !strings.Contains(route, "dev "+iface) || (gw != "" && !strings.Contains(route, "via "+gw)) {
				problems = append(problems, fmt.Sprintf("%s: default route is %q, want via %s dev %s (EXT_NET_PUBLIC_ANCHOR_DEFAULT_ROUTE=true)",
					node, route, gw, iface))
			}
		}
		if vip != "" && vip != "CHANGEME" {
			// Exactly what the fleet worker programs: `from <VIP> lookup 129`
			// and, in table 129, a default route via the public gateway on the
			// anchor. A rule for a different source, or only the connected
			// public-CIDR route, must not pass (Codex P1 round 2).
			if !strings.Contains(fields["RULE"], "from "+vip+" ") || !strings.Contains(fields["RULE"], "lookup 129") {
				problems = append(problems, fmt.Sprintf("%s: VIP return rule (pref 100, from %s -> table 129) missing (has: %q)", node, vip, fields["RULE"]))
			}
			if gw != "" && !strings.Contains(fields["T129"], "default via "+gw+" dev "+iface) {
				problems = append(problems, fmt.Sprintf("%s: table 129 lacks 'default via %s dev %s' — VIP replies will exit asymmetrically (has: %q)", node, gw, iface, strings.TrimSpace(fields["T129"])))
			}
		}
	}
	if len(problems) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: strings.Join(problems, "; "),
			Fix: "check the ext-net-bridge-tag public-anchor container logs on the failing node " +
				"(kubectl -n kube-system logs -l app=ext-net-bridge-tagger -c public-anchor --tail=5); a REFUSING " +
				"loop means something else answers ARP for the anchor address — a leftover kernel VLAN device, a " +
				"real conflict, or the fabric's ARP-suppression cache echoing a dead MAC (ages out in ~10min)",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("%d/%d anchor(s) live on %s (tag %s, VIP return path%s)", probed, len(anchors), iface, vlan,
			map[bool]string{true: ", default route", false: ""}[wantRoute])}
}
