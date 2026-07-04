// Package anchors implements `kube-dc bootstrap anchors apply` — the
// per-node MetalLB L3 anchor installer.
//
// **Why this exists.** MetalLB in L2 mode announces its
// LoadBalancer-IP via gratuitous ARP from a single speaker pod
// elected over a memberlist. That speaker source-addresses its
// GARPs from an IP bound to the host interface advertised by the
// matching L2Advertisement (here: br-ext-cloud, the kube-ovn-cni
// external bridge). Without ANY host-bound IP on br-ext-cloud,
// kernel arp_announce / arp_filter quietly suppress the GARP — the
// VIP is "announced" but no peer in the broadcast domain ever sees
// it. Speakers on nodes without an anchor IP silently become
// elect-only-other-speakers-can-take-this-VIP placeholders.
//
// The 2026-05-30 cloudacropolis Phase-0 incident proved this:
// removing the hand-bound .11 on srv5 (assumed vestigial) broke
// tenant→VIP traffic in <1s because srv5 was the only speaker
// announcing. Failover recovered after .11 was rebound.
//
// **What this package does.** Each gateway node listed in
// `EXT_NET_ANCHOR_IPS` (cluster-config.env, host=CIDR map)
// receives:
//
//   - `/usr/local/sbin/kube-dc-anchor-bind` — a small POSIX script
//     that waits for the OVS bridge to appear, then runs `ip addr
//     replace`. Idempotent on re-run.
//   - `/etc/systemd/system/kube-dc-anchor.service` — a Type=oneshot
//     unit that calls the script with the node-specific IP at boot,
//     after systemd-networkd + rke2-server + openvswitch-switch.
//
// The unit is enabled + started so the binding is live immediately
// AND survives reboot. Apply is idempotent: re-running over an
// already-installed node is a no-op (ip addr replace + enable --now
// are both idempotent).
//
// **Why not a DaemonSet.** A privileged host-mount DaemonSet
// competing with kube-ovn-cni for `br-ext-cloud` configuration
// would race the CNI's reconcile loop on bridge recreate (kube-ovn
// uses `ovs-vsctl --may-exist add-br` then re-configures attached
// ports — a DaemonSet writing IPs in parallel would alternate
// between bound and unbound depending on whose write was last).
// Host networking config is a node-lifecycle concern, not a
// workload concern; systemd-at-boot is the boring answer.
//
// **Why a sibling subcommand, not folded into `bootstrap init`.**
// The init waterfall (Phases 1-6 in installer-prd §4.2) is already
// dense: form, RKE2, network overlay, GitOps seed, Flux waterfall
// watch, OpenBao bootstrap. Adding a per-node SSH loop couples
// per-node runtime concerns into the cluster-overlay flow. Sibling
// subcommand keeps init clean; operator runs anchors apply
// explicitly post-init (or post-add-node).
//
// See:
//   - docs/prd/internal-platform-endpoints-implementation.md §6.D
//   - docs/internal/internal-platform-endpoints-runbook.md
package anchors
