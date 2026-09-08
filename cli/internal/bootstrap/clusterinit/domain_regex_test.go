package clusterinit

import "testing"

// Private enterprise zones may carry hyphens and digits in the top-level
// label (kubedc.diia-dcir); the validator must accept them while still
// rejecting URLs, dotted IPs and malformed labels.
func TestDomainRegexPrivateZones(t *testing.T) {
	accept := []string{
		"kdc.example.com",
		"kubedc.diia-dcir",
		"main.kubedc.diia-dcir",
		"corp.site-01",
		"a.b2c",
		"x.internal",
	}
	reject := []string{
		"",
		"example",             // no dot
		"https://foo.example", // scheme
		"foo.example/path",    // path
		"1.2.3.4",             // dotted IP: numeric last label
		"foo.7up",             // last label must start with a letter
		"foo.-bad",            // leading hyphen
		"foo.bad-",            // trailing hyphen
		"foo.c",               // single-character last label
		"-foo.example.com",    // leading hyphen in a label
		"foo..example.com",    // empty label
	}
	for _, d := range accept {
		if err := ValidateDomainField(d); err != nil {
			t.Errorf("ValidateDomainField(%q) = %v, want nil", d, err)
		}
		if errs := validateDomain(d); len(errs) != 0 {
			t.Errorf("validateDomain(%q) = %v, want none", d, errs)
		}
	}
	for _, d := range reject {
		if err := ValidateDomainField(d); err == nil {
			t.Errorf("ValidateDomainField(%q) = nil, want error", d)
		}
	}
}
