package main

import "testing"

// TestHasAdminGroup covers every shape Keycloak might emit the "admin"
// group in. Failing this would gatekeep a legitimate admin out of the
// CLI over a stylistic detail (leading slash, claim transform prefix).
//
// adminGroup is "admin" (master realm), reused by flux-web, grafana,
// AND kubectl admin. The "platform:" prefix is what the apiserver's
// claim mapping prepends — the CLI sees the *raw* token, but accepts
// both pre- and post-mapping shapes for resilience.
func TestHasAdminGroup(t *testing.T) {
	cases := []struct {
		name   string
		groups []string
		want   bool
	}{
		{name: "exact match", groups: []string{"admin"}, want: true},
		{name: "leading slash", groups: []string{"/admin"}, want: true},
		{name: "platform prefix", groups: []string{"platform:admin"}, want: true},
		{name: "platform + slash", groups: []string{"platform:/admin"}, want: true},
		{name: "mixed with other groups", groups: []string{"viewer", "/admin", "ops"}, want: true},
		{name: "empty", groups: nil, want: false},
		{name: "wrong group", groups: []string{"viewer"}, want: false},
		{name: "substring not enough", groups: []string{"prefix-admin-suffix"}, want: false},
		// Common confusion: a tenant org named "admin" would emit the
		// claim as "<org>:admin" (e.g. "shalb:admin"), not "admin".
		// We must NOT accept that — only the master-realm token's
		// admin group counts.
		{name: "tenant admin not enough", groups: []string{"shalb:admin"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAdminGroup(tc.groups, adminGroup); got != tc.want {
				t.Errorf("hasAdminGroup(%v, %q) = %v, want %v", tc.groups, adminGroup, got, tc.want)
			}
		})
	}
}
