package clusterinit

import "testing"

// The node LAN prefix is the one value Tenant Networking v2 cannot obtain from
// the installer's existing inputs during a greenfield bootstrap, and getting it
// wrong does not fail loudly: a plausible-but-wrong CIDR installs cleanly and
// then misroutes kubelet probe replies, so pods never reach Ready while nothing
// is red. Every case here therefore pins either an exact answer or an error.

const addrFixture = `1: lo    inet 127.0.0.1/8 scope host lo\ valid_lft forever
2: eth0    inet 192.168.110.11/24 brd 192.168.110.255 scope global eth0\ valid_lft forever
2: eth0    inet6 fe80::5054:ff:fe12:3456/64 scope link \ valid_lft forever
3: eth1    inet 10.8.0.3/16 brd 10.8.255.255 scope global eth1\ valid_lft forever
`

func TestNodeCIDRFromAddrOutput_MasksToTheNetwork(t *testing.T) {
	got, err := NodeCIDRFromAddrOutput([]byte(addrFixture), "192.168.110.11")
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.110.0/24" {
		t.Fatalf("got %q, want 192.168.110.0/24", got)
	}
}

// The prefix must come from the interface, never be assumed to be /24 — a /16
// node network is legitimate and assuming /24 would drop three quarters of it
// out of the injected routes.
func TestNodeCIDRFromAddrOutput_HonoursNonSlash24Prefix(t *testing.T) {
	got, err := NodeCIDRFromAddrOutput([]byte(addrFixture), "10.8.0.3")
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.8.0.0/16" {
		t.Fatalf("got %q, want 10.8.0.0/16 — the prefix must come from the interface", got)
	}
}

func TestNodeCIDRFromAddrOutput_FailsClosed(t *testing.T) {
	cases := map[string]struct{ out, ip string }{
		"address not present on the host": {addrFixture, "203.0.113.9"},
		"not an IP":                       {addrFixture, "not-an-ip"},
		"empty output":                    {"", "192.168.110.11"},
		"garbage output":                  {"nonsense\n", "192.168.110.11"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := NodeCIDRFromAddrOutput([]byte(tc.out), tc.ip)
			if err == nil {
				t.Fatalf("expected an error; got %q — a guessed CIDR misroutes silently", got)
			}
		})
	}
}

// IPv6 must not be picked up: the injected routes are IPv4 and a v6 prefix here
// would produce an unusable route.
func TestNodeCIDRFromAddrOutput_IgnoresIPv6(t *testing.T) {
	if got, err := NodeCIDRFromAddrOutput([]byte(addrFixture), "fe80::5054:ff:fe12:3456"); err == nil {
		t.Fatalf("matched an IPv6 address: %q", got)
	}
}
