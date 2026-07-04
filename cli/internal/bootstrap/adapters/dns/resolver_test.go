package dns

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func TestResolve_HappyPath(t *testing.T) {
	r := New()
	r.testLookup = func(ctx context.Context, res *net.Resolver, name string) ([]net.IP, error) {
		if name != "kube-dc.cloud" {
			t.Fatalf("unexpected name %q", name)
		}
		return []net.IP{net.IPv4(213, 111, 154, 233)}, nil
	}

	got, err := r.Resolve(context.Background(), "kube-dc.cloud", ports.DNSRecordTypeA)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0] != "213.111.154.233" {
		t.Errorf("got %v, want [213.111.154.233]", got)
	}
}

func TestResolve_NXDOMAIN_ReturnsEmptyNoError(t *testing.T) {
	r := New()
	r.testLookup = func(ctx context.Context, res *net.Resolver, name string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}

	got, err := r.Resolve(context.Background(), "kdc-dns-test-abc.example.com", ports.DNSRecordTypeA)
	if err != nil {
		t.Fatalf("NXDOMAIN should be (nil, nil), got err=%v", err)
	}
	if len(got) != 0 {
		t.Errorf("NXDOMAIN should be empty slice, got %v", got)
	}
}

func TestResolve_NXDOMAIN_StringMatch_Compat(t *testing.T) {
	// Some resolver chains don't set IsNotFound; ensure we still treat
	// the string as NXDOMAIN.
	r := New()
	r.testLookup = func(ctx context.Context, res *net.Resolver, name string) ([]net.IP, error) {
		return nil, errors.New("lookup foo.invalid: no such host")
	}

	got, err := r.Resolve(context.Background(), "foo.invalid", ports.DNSRecordTypeA)
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}
}

func TestResolve_SystemFails_FallbackSucceeds(t *testing.T) {
	r := New()
	calls := 0
	r.testLookup = func(ctx context.Context, res *net.Resolver, name string) ([]net.IP, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("read udp 10.0.0.1:53: i/o timeout")
		}
		return []net.IP{net.IPv4(1, 2, 3, 4)}, nil
	}

	got, err := r.Resolve(context.Background(), "kube-dc.cloud", ports.DNSRecordTypeA)
	if err != nil {
		t.Fatalf("expected fallback to recover, got %v", err)
	}
	if got[0] != "1.2.3.4" {
		t.Errorf("got %v want [1.2.3.4]", got)
	}
}

func TestResolve_SystemFailsTransport_FallbackAlsoFails_OriginalSurfaced(t *testing.T) {
	r := New()
	r.testLookup = func(ctx context.Context, res *net.Resolver, name string) ([]net.IP, error) {
		if res == r.systemResolver {
			return nil, errors.New("read udp 10.0.0.1:53: corp proxy block")
		}
		return nil, errors.New("read udp 1.1.1.1:53: also blocked")
	}

	_, err := r.Resolve(context.Background(), "kube-dc.cloud", ports.DNSRecordTypeA)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "corp proxy block") {
		t.Errorf("error should surface ORIGINAL system-resolver error, got %v", err)
	}
}

func TestResolve_RejectsAAAA(t *testing.T) {
	r := New()
	_, err := r.Resolve(context.Background(), "kube-dc.cloud", "AAAA")
	if err == nil {
		t.Fatal("want error for AAAA")
	}
	if !strings.Contains(err.Error(), "not supported in v1") {
		t.Errorf("error message should call out v1 scope: %v", err)
	}
}

func TestResolve_EmptyName(t *testing.T) {
	r := New()
	_, err := r.Resolve(context.Background(), "", ports.DNSRecordTypeA)
	if err == nil {
		t.Fatal("want error for empty name")
	}
}

func TestIsNotFound_Variants(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"net.DNSError IsNotFound", &net.DNSError{IsNotFound: true}, true},
		{"net.DNSError other", &net.DNSError{Err: "timeout", IsTimeout: true}, false},
		{"string match no such host", errors.New("lookup foo: no such host"), true},
		{"string match NXDOMAIN", errors.New("server returned NXDOMAIN"), true},
		{"unrelated error", errors.New("connection refused"), false},
		// Review-pass case: a *net.DNSError that carries the
		// "no such host" message but leaves IsNotFound unset
		// (older resolver libraries do this). The fix is to
		// fall through to string-match instead of returning
		// dnsErr.IsNotFound as-is.
		{"DNSError IsNotFound unset, message NXDOMAIN-like", &net.DNSError{Err: "no such host", Name: "foo.invalid"}, true},
	}
	for _, c := range cases {
		if got := isNotFound(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
