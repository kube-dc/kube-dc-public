package main

import (
	"strings"
	"testing"
)

// sanitizeAPIServerHost feeds the "read from <server>" evidence line that
// --mode=auto prints, so an operator can see WHICH cluster the verdict came
// from. A kubeconfig server field can legitimately carry userinfo, a path, a
// query or a fragment; none of that identifies the cluster, and userinfo can
// be a credential — so the display form is reduced to scheme://host[:port],
// and unparseable input is dropped rather than echoed back.
func TestSanitizeAPIServerHost(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "https://10.0.0.1:6443", "https://10.0.0.1:6443"},
		{"strips userinfo", "https://user:secret@10.0.0.1:6443", "https://10.0.0.1:6443"},
		{"strips path and query", "https://api.example.test:6443/apis?token=abc", "https://api.example.test:6443"},
		{"strips fragment", "https://api.example.test:6443#frag", "https://api.example.test:6443"},
		{"empty stays empty", "", ""},
		{"garbage is dropped, not echoed", "://not a url", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeAPIServerHost(tc.in); got != tc.want {
				t.Fatalf("sanitizeAPIServerHost(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The leak this exists to prevent: whatever the input, the printed form must
// never carry credentials.
func TestSanitizeAPIServerHost_NeverLeaksCredentials(t *testing.T) {
	for _, in := range []string{
		"https://admin:hunter2@10.0.0.1:6443",
		"https://token@10.0.0.1:6443/path?x=y#z",
	} {
		got := sanitizeAPIServerHost(in)
		for _, leak := range []string{"hunter2", "admin:", "token@"} {
			if strings.Contains(got, leak) {
				t.Fatalf("sanitizeAPIServerHost(%q) leaked %q: %q", in, leak, got)
			}
		}
	}
}
