package clusterinit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// scriptedSSH answers Run per-command; RFC 5737 addresses throughout
// (192.0.2.0/24 = "public", 198.51.100.0/24 = "internal") per the
// no-real-infra lint contract.
type scriptedSSH struct {
	responses map[string]string
	errs      map[string]error
	calls     []string
}

func (s *scriptedSSH) Run(_ context.Context, _ ports.SSHHost, cmd string) ([]byte, error) {
	s.calls = append(s.calls, cmd)
	if err, ok := s.errs[cmd]; ok {
		return nil, err
	}
	return []byte(s.responses[cmd]), nil
}
func (s *scriptedSSH) Fetch(_ context.Context, _ ports.SSHHost, _ string) ([]byte, error) {
	return nil, nil
}
func (s *scriptedSSH) Put(_ context.Context, _ ports.SSHHost, _ string, _ []byte, _ uint32) error {
	return nil
}

const ipAddrShowBareMetal = `1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
2: eth0    inet 192.0.2.10/24 brd 192.0.2.255 scope global eth0\       valid_lft forever preferred_lft forever
2: eth0    inet6 fe80::1/64 scope link\       valid_lft forever preferred_lft forever`

const ipAddrShowNAT = `1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
2: enp1s0    inet 198.51.100.5/24 brd 198.51.100.255 scope global enp1s0\       valid_lft forever preferred_lft forever`

const ipRouteGetNAT = `192.0.2.1 via 198.51.100.1 dev enp1s0 src 198.51.100.5 uid 1000
    cache`

func TestDetectArrivingIP_PublicBoundOnNode(t *testing.T) {
	ssh := &scriptedSSH{responses: map[string]string{
		"ip -o addr show": ipAddrShowBareMetal,
	}}
	ip, nat, err := DetectArrivingIP(context.Background(), ArrivingIPOptions{
		SSH: ssh, Host: ports.SSHHost{Hostname: "192.0.2.10"}, PublicIP: "192.0.2.10",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nat || ip != "192.0.2.10" {
		t.Errorf("bare-metal shape: got ip=%s nat=%t, want public/false", ip, nat)
	}
	// Route lookup must NOT run when the public IP is local.
	for _, c := range ssh.calls {
		if strings.Contains(c, "route get") {
			t.Errorf("route lookup ran despite public IP being bound: %v", ssh.calls)
		}
	}
}

func TestDetectArrivingIP_NATSubstitutesArrivingIP(t *testing.T) {
	ssh := &scriptedSSH{responses: map[string]string{
		"ip -o addr show":           ipAddrShowNAT,
		"ip -4 route get 192.0.2.1": ipRouteGetNAT,
	}}
	ip, nat, err := DetectArrivingIP(context.Background(), ArrivingIPOptions{
		SSH: ssh, Host: ports.SSHHost{Hostname: "192.0.2.10"}, PublicIP: "192.0.2.10",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !nat || ip != "198.51.100.5" {
		t.Errorf("NAT shape: got ip=%s nat=%t, want 198.51.100.5/true", ip, nat)
	}
}

func TestDetectArrivingIP_ErrorsFailClosedToCaller(t *testing.T) {
	boom := errors.New("connect refused")
	ssh := &scriptedSSH{errs: map[string]error{"ip -o addr show": boom}}
	_, _, err := DetectArrivingIP(context.Background(), ArrivingIPOptions{
		SSH: ssh, Host: ports.SSHHost{Hostname: "192.0.2.10"}, PublicIP: "192.0.2.10",
	})
	if err == nil {
		t.Fatal("want error when the probe can't run")
	}
}

func TestDetectArrivingIP_RejectsNonIP(t *testing.T) {
	_, _, err := DetectArrivingIP(context.Background(), ArrivingIPOptions{
		SSH: &scriptedSSH{}, PublicIP: "not-an-ip",
	})
	if err == nil {
		t.Fatal("want error for invalid PublicIP")
	}
}

func TestParseRouteSrc_NoSrcToken(t *testing.T) {
	if _, err := parseRouteSrc([]byte("192.0.2.1 dev tun0 scope link")); err == nil {
		t.Error("want error when route output has no src")
	}
}

func TestHostHasIP_NoSubstringFalsePositive(t *testing.T) {
	// 198.51.100.5 present; probing for 198.51.100.50 must not match.
	if hostHasIP([]byte(ipAddrShowNAT), "198.51.100.50") {
		t.Error("substring false-positive: .5 matched probe for .50")
	}
}

// --- platform.yaml patch writer ---

// platformYAMLBase mirrors the add-cluster.sh emission shape (tail =
// postBuild block, no patches key).
const platformYAMLBase = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
  namespace: flux-system
spec:
  path: ./platform
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
`

func writePlatformFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	clusterDir := filepath.Join(dir, "clusters", "atlantis")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "platform.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestGatewayCollidesWithAPIServer — the 6443 listener is unsafe whenever the
// Envoy Service's externalIP is also an apiserver address. Single-IP NAT is
// only one route there; pointing NODE_EXTERNAL_IP at a control-plane node's
// own IP is the other, involves no NAT, and so was invisible to the SSH probe.
func TestGatewayCollidesWithAPIServer(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want bool
	}{
		{
			// The on-prem case: an internal cluster fronted by master01.
			name: "node external IP is a control-plane node",
			env: "NODE_EXTERNAL_IP=203.0.113.11\n" +
				"KUBE_OVN_MASTER_NODES=203.0.113.11,203.0.113.12,203.0.113.13\n",
			want: true,
		},
		{
			name: "only OVN_DB_IPS lists it",
			env:  "NODE_EXTERNAL_IP=192.0.2.13\nOVN_DB_IPS=tcp:192.0.2.11:6641,tcp:192.0.2.12:6641,tcp:192.0.2.13:6641\n",
			want: true,
		},
		{
			// A real public ingress address — the listener is safe, and
			// removing it would needlessly kill tenant kube-api SNI routing.
			name: "distinct public ingress IP",
			env: "NODE_EXTERNAL_IP=203.0.113.10\n" +
				"KUBE_OVN_MASTER_NODES=203.0.113.11,203.0.113.12,203.0.113.13\n",
			want: false,
		},
		{
			name: "whitespace around list entries still matches",
			env:  "NODE_EXTERNAL_IP=203.0.113.12\nKUBE_OVN_MASTER_NODES=203.0.113.11, 203.0.113.12 ,203.0.113.13\n",
			want: true,
		},
		{
			name: "no node external IP",
			env:  "KUBE_OVN_MASTER_NODES=203.0.113.11\n",
			want: false,
		},
		{
			// A commented-out assignment is not a definition.
			name: "commented key does not count",
			env:  "NODE_EXTERNAL_IP=203.0.113.11\n#KUBE_OVN_MASTER_NODES=203.0.113.11\n",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GatewayCollidesWithAPIServer(tc.env); got != tc.want {
				t.Errorf("GatewayCollidesWithAPIServer = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPatchComposition_GatewayVIPThenSingleIPNAT — the two Gateway patches must
// coexist. Scaffold writes the VIP patch (WriteAddons) BEFORE the 6443-listener
// removal, so by the time the latter runs a `patches:` block already exists.
// Accepting only the object-storage marker made a normal combination abort the
// whole install:
//
//	METALLB_FLOATING_IP != NODE_EXTERNAL_IP
//	NODE_EXTERNAL_IP is a control-plane address (or single-IP NAT)
//	object storage ENABLED, so the OS-4 marker is absent
//
// which is exactly the shape of the cluster this was found on.
func TestPatchComposition_GatewayVIPThenSingleIPNAT(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=metallb-lb\n"+
		"NODE_EXTERNAL_IP=203.0.113.11\nMETALLB_FLOATING_IP=198.51.100.2\n"+
		"KUBE_OVN_MASTER_NODES=203.0.113.11,203.0.113.12,203.0.113.13\n")

	// WriteAddons creates the patches: block with the Gateway VIP entry. (The
	// single-IP-NAT :6443 listener-removal writer that used to compose here was
	// retired 2026-08-11 — off-Envoy kube-api is the base default, so there is no
	// listener to remove.)
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}

	got := read(t, filepath.Join(repo, "clusters", cluster), "platform.yaml")
	for _, want := range []string{gatewayServiceVIPMarker, gatewayVIPMarker} {
		if !strings.Contains(got, want) {
			t.Errorf("platform.yaml lost %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "patches:"); n != 1 {
		t.Errorf("want exactly one patches: block, got %d:\n%s", n, got)
	}
}

// NOTE: the hand-edited-patches-block refusal that WriteSingleIPNATPatch used to
// assert here is now covered by dns01_test.go's
// TestPatchPlatformDNS01Solver_ComposesWithOwnedBlock — the refusal semantics are
// shared across every owned-patch writer via ownedPlatformPatchMarkers, and the
// single-IP-NAT writer was retired with the off-Envoy inversion (2026-08-11).

// egressSSH answers by command intent: the route lookup vs the arping/ping
// wrapper. It lets ProbeEgressGateway tests script both hops without pinning the
// exact (long) wrapper string. RFC 5737 addresses throughout.
type egressSSH struct {
	route, probe       string
	routeErr, probeErr error
}

func (s *egressSSH) Run(_ context.Context, _ ports.SSHHost, cmd string) ([]byte, error) {
	if strings.Contains(cmd, "route get") {
		return []byte(s.route), s.routeErr
	}
	return []byte(s.probe), s.probeErr
}
func (s *egressSSH) Fetch(context.Context, ports.SSHHost, string) ([]byte, error) { return nil, nil }
func (s *egressSSH) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	return nil
}

func TestProbeEgressGateway(t *testing.T) {
	host := ports.SSHHost{Hostname: "192.0.2.10"}
	cases := []struct {
		name                            string
		ssh                             *egressSSH
		gw, extIface, anchors           string
		wantProbed, wantReach, wantWarn bool
		noteHas                         string
	}{
		{
			name: "direct + arping reply → reachable",
			ssh:  &egressSSH{route: "192.0.2.1 dev bond0 src 192.0.2.5 uid 0", probe: "REPLIED"},
			gw:   "192.0.2.1", extIface: "bond0",
			wantProbed: true, wantReach: true,
		},
		{
			name: "VLAN sub-interface still matches the ext parent base",
			ssh:  &egressSSH{route: "192.0.2.1 dev bond0.163 src 192.0.2.5 uid 0", probe: "REPLIED"},
			gw:   "192.0.2.1", extIface: "bond0",
			wantProbed: true, wantReach: true,
		},
		{
			name: "direct + definitive NOREPLY → probed, unreachable, no warn",
			ssh:  &egressSSH{route: "192.0.2.1 dev bond0 src 192.0.2.5 uid 0", probe: "NOREPLY"},
			gw:   "192.0.2.1", extIface: "bond0",
			wantProbed: true, wantReach: false,
		},
		{
			name: "INCONCLUSIVE (no arping / ICMP filtered) is NOT a false negative",
			ssh:  &egressSSH{route: "192.0.2.1 dev bond0 src 192.0.2.5 uid 0", probe: "INCONCLUSIVE"},
			gw:   "192.0.2.1", extIface: "bond0",
			wantProbed: false, noteHas: "inconclusive",
		},
		{
			name: "route via a different interface than configured ext → inconclusive",
			ssh:  &egressSSH{route: "192.0.2.1 dev eth0 src 198.51.100.5 uid 0", probe: "REPLIED"},
			gw:   "192.0.2.1", extIface: "bond0",
			wantProbed: false, noteHas: "not the configured ext interface",
		},
		{
			name: "local gateway that is NOT a configured anchor → warn",
			ssh:  &egressSSH{route: "local 192.0.2.1 dev lo src 192.0.2.1 uid 0"},
			gw:   "192.0.2.1", anchors: "192.0.2.3,192.0.2.4",
			wantProbed: false, wantWarn: true, noteHas: "NOT a configured ext anchor",
		},
		{
			name: "local gateway that IS a configured anchor → node-egress, no warn",
			ssh:  &egressSSH{route: "local 192.0.2.3 dev lo src 192.0.2.3 uid 0"},
			gw:   "192.0.2.3", anchors: "192.0.2.3, 192.0.2.4",
			wantProbed: false, wantWarn: false, noteHas: "node-egress anchor",
		},
		{
			name:       "reached via a next hop → ext not directly configured yet",
			ssh:        &egressSSH{route: "192.0.2.1 via 198.51.100.1 dev eth0 src 198.51.100.5 uid 0"},
			gw:         "192.0.2.1",
			wantProbed: false, noteHas: "not directly configured",
		},
		{
			name:       "interface name with a shell metacharacter is rejected, never run",
			ssh:        &egressSSH{route: "192.0.2.1 dev x;reboot src 192.0.2.5 uid 0", probe: "REPLIED"},
			gw:         "192.0.2.1", // extIface empty → iface-match skipped, safe-name guard fires
			wantProbed: false, noteHas: "failed validation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ProbeEgressGateway(context.Background(), EgressGatewayProbeOptions{
				SSH: tc.ssh, Host: host, Gateway: tc.gw, ExtIface: tc.extIface, AnchorIPs: tc.anchors,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Probed != tc.wantProbed || res.Reachable != tc.wantReach || res.Warn != tc.wantWarn {
				t.Errorf("got probed=%t reach=%t warn=%t, want %t/%t/%t (note=%q)",
					res.Probed, res.Reachable, res.Warn, tc.wantProbed, tc.wantReach, tc.wantWarn, res.Note)
			}
			if tc.noteHas != "" && !strings.Contains(res.Note, tc.noteHas) {
				t.Errorf("note %q does not contain %q", res.Note, tc.noteHas)
			}
		})
	}
}

func TestProbeEgressGateway_RouteErrorFailsToCaller(t *testing.T) {
	ssh := &egressSSH{routeErr: errors.New("connect refused")}
	if _, err := ProbeEgressGateway(context.Background(), EgressGatewayProbeOptions{
		SSH: ssh, Gateway: "192.0.2.1",
	}); err == nil {
		t.Fatal("want error when the route lookup itself fails")
	}
}

func TestIsSafeIfaceName(t *testing.T) {
	for _, ok := range []string{"eth0", "bond0.163", "br-ext-cloud", "enp1s0", "eth0:1", "vlan.100@bond0"} {
		if !isSafeIfaceName(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"", "x;reboot", "eth0 && rm", "a$b", "`id`", "toolongforanifname"} {
		if isSafeIfaceName(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestParseEgressRoute(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		iface   string
		direct  bool
		isLocal bool
	}{
		// Directly connected on the ext interface → arpable.
		{"direct", "100.65.0.1 dev bond0.163 src 100.65.0.5 uid 0", "bond0.163", true, false},
		// Gateway == this node's own anchor (node-egress) → ARP N/A.
		{"local anchor", "local 100.65.0.1 dev lo src 100.65.0.1 uid 0", "lo", false, true},
		// Reached via a next hop → ext network not directly configured yet.
		{"via next hop", "100.65.0.1 via 10.0.0.1 dev eth0 src 10.0.0.5 uid 0", "eth0", false, false},
		{"empty", "", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iface, direct, isLocal := parseEgressRoute(tc.out)
			if iface != tc.iface || direct != tc.direct || isLocal != tc.isLocal {
				t.Errorf("parseEgressRoute(%q) = (%q,%v,%v), want (%q,%v,%v)",
					tc.out, iface, direct, isLocal, tc.iface, tc.direct, tc.isLocal)
			}
		})
	}
}
