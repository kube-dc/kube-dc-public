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

func TestWriteSingleIPNATPatch_FreshPlatformYAML(t *testing.T) {
	repo := writePlatformFixture(t, platformYAMLBase)
	if err := WriteSingleIPNATPatch(repo, "atlantis", nil); err != nil {
		t.Fatalf("fresh write: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(repo, "clusters", "atlantis", "platform.yaml"))
	got := string(body)
	for _, want := range []string{
		"  patches:",
		natPlatformPatchesMarker,
		"op: test",
		"value: tls-passthrough",
		"op: remove",
		"path: /spec/listeners/12",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in patched platform.yaml:\n%s", want, got)
		}
	}
}

func TestWriteSingleIPNATPatch_Idempotent(t *testing.T) {
	repo := writePlatformFixture(t, platformYAMLBase)
	if err := WriteSingleIPNATPatch(repo, "atlantis", nil); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(repo, "clusters", "atlantis", "platform.yaml"))
	if err := WriteSingleIPNATPatch(repo, "atlantis", nil); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, _ := os.ReadFile(filepath.Join(repo, "clusters", "atlantis", "platform.yaml"))
	if string(first) != string(second) {
		t.Error("second write changed the file — not idempotent")
	}
}

func TestWriteSingleIPNATPatch_ComposesWithOS4DisabledBlock(t *testing.T) {
	// OS-4 disabled writer ran first (scaffold step 7 before step 8).
	repo := writePlatformFixture(t, platformYAMLBase)
	path := filepath.Join(repo, "clusters", "atlantis", "platform.yaml")
	if err := patchFileLines(path, patchPlatformDisabledObjectStorage); err != nil {
		t.Fatalf("OS-4 setup: %v", err)
	}
	if err := WriteSingleIPNATPatch(repo, "atlantis", nil); err != nil {
		t.Fatalf("compose write: %v", err)
	}
	body, _ := os.ReadFile(path)
	got := string(body)
	if strings.Count(got, "patches:") != 1 {
		t.Errorf("want exactly ONE patches: key after composing, got %d:\n%s",
			strings.Count(got, "patches:"), got)
	}
	if !strings.Contains(got, natPlatformPatchesMarker) || !strings.Contains(got, disabledPlatformPatchesMarker) {
		t.Errorf("both markers must be present after composing:\n%s", got)
	}
}

func TestWriteSingleIPNATPatch_RefusesHandEditedPatches(t *testing.T) {
	repo := writePlatformFixture(t, platformYAMLBase+"  patches:\n    - path: hand-crafted.yaml\n")
	err := WriteSingleIPNATPatch(repo, "atlantis", nil)
	if err == nil {
		t.Fatal("want refusal on a hand-edited patches: block")
	}
	if !strings.Contains(err.Error(), natPlatformPatchesMarker) {
		t.Errorf("refusal should name the marker for manual wiring: %v", err)
	}
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

	// 1. WriteAddons creates the patches: block with the VIP entry.
	if err := WriteAddons(repo, cluster, nil); err != nil {
		t.Fatalf("WriteAddons: %v", err)
	}
	// 2. The 6443 removal must compose with it, not reject it.
	if err := WriteSingleIPNATPatch(repo, cluster, nil); err != nil {
		t.Fatalf("WriteSingleIPNATPatch must compose with the Gateway VIP patch: %v", err)
	}

	got := read(t, filepath.Join(repo, "clusters", cluster), "platform.yaml")
	for _, want := range []string{gatewayServiceVIPMarker, gatewayVIPMarker, natPlatformPatchesMarker} {
		if !strings.Contains(got, want) {
			t.Errorf("platform.yaml lost %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "patches:"); n != 1 {
		t.Errorf("want exactly one patches: block, got %d:\n%s", n, got)
	}
}

// TestPatchComposition_RefusesHandEditedBlock — the guard must still protect a
// patches: block we did not write.
func TestPatchComposition_RefusesHandEditedBlock(t *testing.T) {
	repo, cluster := baseOverlay(t, "INGRESS_MODE=hostnetwork\n")
	p := filepath.Join(repo, "clusters", cluster, "platform.yaml")
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	hand := string(body) + "  patches:\n    - target: {kind: Gateway, name: eg}\n      patch: |\n        - op: remove\n          path: /spec/listeners/0\n"
	if err := os.WriteFile(p, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSingleIPNATPatch(repo, cluster, nil); err == nil {
		t.Error("a hand-edited patches: block must still be refused")
	}
}
