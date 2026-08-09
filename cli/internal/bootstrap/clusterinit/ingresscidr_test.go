package clusterinit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeriveIngressHostCIDR(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  string
	}{
		{
			// The common shape: three consecutive control-plane addresses.
			name:  "three consecutive addresses collapse to a /30",
			addrs: []string{"192.0.2.4", "192.0.2.5", "192.0.2.6"},
			want:  "192.0.2.4/30",
		},
		{
			name:  "single node is a /32",
			addrs: []string{"192.0.2.10"},
			want:  "192.0.2.10/32",
		},
		{
			// A real cluster's nodes sit on PUBLIC address space, spread across the
			// subnet. Nothing may assume 192.168/RFC1918.
			name:  "addresses spread across a public /26",
			addrs: []string{"203.0.113.5", "203.0.113.60"},
			want:  "203.0.113.0/26",
		},
		{
			// Crossing a /24 boundary must widen, not silently pick one side.
			name:  "addresses either side of a /24 boundary widen to /23",
			addrs: []string{"198.51.100.250", "198.51.101.3"},
			want:  "198.51.100.0/23",
		},
		{
			name:  "IPv6 addresses collapse within their family",
			addrs: []string{"2001:db8:1::5", "2001:db8:1::9"},
			want:  "2001:db8:1::/124",
		},
		{
			name:  "identical addresses do not widen",
			addrs: []string{"192.0.2.7", "192.0.2.7"},
			want:  "192.0.2.7/32",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveIngressHostCIDR(tc.addrs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("DeriveIngressHostCIDR(%v) = %q, want %q", tc.addrs, got, tc.want)
			}
		})
	}
}

func TestDeriveIngressHostCIDR_Refusals(t *testing.T) {
	// Each of these must FAIL rather than produce a prefix, because every wrong answer
	// here is a silently denied front door.
	cases := []struct {
		name  string
		addrs []string
		want  string
	}{
		{"no addresses", nil, "no ingress node addresses"},
		{"garbage address", []string{"not-an-ip"}, "not an IP address"},
		{
			// One ipBlock cannot express both families, and quietly admitting one would
			// deny the other.
			name:  "dual-stack refuses rather than guessing a family",
			addrs: []string{"192.0.2.4", "2001:db8:1::5"},
			want:  "dual-stack",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveIngressHostCIDR(tc.addrs)
			if err == nil {
				t.Fatalf("expected an error, got %q", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// fakeAddresser serves a fixed node -> InternalIP map.
type fakeAddresser struct {
	ips map[string]string
	err error
}

func (f fakeAddresser) NodeInternalIPs(context.Context) (map[string]string, error) {
	return f.ips, f.err
}

func TestIngressHostCIDRFor(t *testing.T) {
	ctx := context.Background()

	t.Run("derives from the planned nodes only", func(t *testing.T) {
		// The cluster has a fourth node that is NOT an ingress node; its address must
		// not widen the prefix, or the policy would admit a node that never serves.
		f := fakeAddresser{ips: map[string]string{
			"cp-0": "192.0.2.4", "cp-1": "192.0.2.5", "cp-2": "192.0.2.6",
			"worker-9": "198.51.100.200",
		}}
		cidr, addrs, err := IngressHostCIDRFor(ctx, f, []string{"cp-0", "cp-1", "cp-2"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cidr != "192.0.2.4/30" {
			t.Errorf("cidr = %q, want 192.0.2.4/30", cidr)
		}
		if len(addrs) != 3 {
			t.Errorf("addrs = %v, want 3 entries", addrs)
		}
	})

	t.Run("names the node that has no InternalIP", func(t *testing.T) {
		f := fakeAddresser{ips: map[string]string{"cp-0": "192.0.2.4"}}
		_, _, err := IngressHostCIDRFor(ctx, f, []string{"cp-0", "cp-1"})
		if err == nil {
			t.Fatal("expected an error naming the missing node")
		}
		if !strings.Contains(err.Error(), "cp-1") {
			t.Errorf("error %q does not name cp-1", err.Error())
		}
	})

	t.Run("no nodes planned is an error, not an empty CIDR", func(t *testing.T) {
		_, _, err := IngressHostCIDRFor(ctx, fakeAddresser{}, nil)
		if err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("nil client is an error, not a silent skip", func(t *testing.T) {
		_, _, err := IngressHostCIDRFor(ctx, nil, []string{"cp-0"})
		if err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("propagates the lookup failure", func(t *testing.T) {
		sentinel := errors.New("apiserver unreachable")
		_, _, err := IngressHostCIDRFor(ctx, fakeAddresser{err: sentinel}, []string{"cp-0"})
		if !errors.Is(err, sentinel) {
			t.Errorf("error %v does not wrap the lookup failure", err)
		}
	})
}

func TestCIDRIsWiderThanNeeded(t *testing.T) {
	// A /30 for three nodes is tight; a /26 for two is not, and the operator should be
	// told rather than have the allow-list quietly widened for them.
	if cidrIsWiderThanNeeded("192.0.2.4/30", 3) {
		t.Error("a /30 covering 3 nodes should not be reported as too wide")
	}
	if !cidrIsWiderThanNeeded("203.0.113.0/26", 2) {
		t.Error("a /26 covering 2 nodes should be reported as wider than needed")
	}
	if cidrIsWiderThanNeeded("not-a-cidr", 3) {
		t.Error("an unparseable CIDR must not be reported as too wide")
	}
}
