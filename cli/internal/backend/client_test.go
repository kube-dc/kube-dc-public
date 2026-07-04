package backend

import "testing"

func TestNormaliseDomain(t *testing.T) {
	cases := map[string]string{
		"kube-dc.cloud":             "kube-dc.cloud",
		"https://kube-dc.cloud":     "kube-dc.cloud",
		"http://kube-dc.cloud":      "kube-dc.cloud",
		"https://kube-dc.cloud/":    "kube-dc.cloud",
		"  kube-dc.cloud  ":         "kube-dc.cloud",
		"https://kube-dc.cloud///":  "kube-dc.cloud",
		"":                              "",
		" ":                             "",
	}
	for in, want := range cases {
		if got := normaliseDomain(in); got != want {
			t.Errorf("normaliseDomain(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestNew_RejectsPathLikeInput(t *testing.T) {
	for _, bad := range []string{
		"kube-dc.cloud/foo",
		"https://kube-dc.cloud/api/secrets",
		"kube-dc.cloud?x=1",
		"kube-dc.cloud#frag",
	} {
		if _, err := New(bad, "", "", true); err == nil {
			t.Errorf("New(%q) should have returned an error for path-like input", bad)
		}
	}
}

func TestNew_BaseURLPrependsBackendSubdomain(t *testing.T) {
	c, err := New("kube-dc.cloud", "tok", "", true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.BaseURL != "https://backend.kube-dc.cloud" {
		t.Errorf("BaseURL = %q; want https://backend.kube-dc.cloud", c.BaseURL)
	}
}

func TestNew_NormalisesScheme(t *testing.T) {
	c, err := New("https://kube-dc.cloud/", "tok", "", true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.BaseURL != "https://backend.kube-dc.cloud" {
		t.Errorf("BaseURL = %q; want https://backend.kube-dc.cloud", c.BaseURL)
	}
}
