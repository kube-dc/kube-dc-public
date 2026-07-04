// Unit tests for `kube-dc certificates` rendering helpers. The
// network-bound paths (resolveScope + backend.Client) are exercised
// by manual + e2e flows; here we pin the pure formatters so a
// future refactor can't quietly regress the column shape.

package main

import (
	"strings"
	"testing"
	"time"
)

func TestCertificateReadyFromConditions(t *testing.T) {
	cases := []struct {
		name  string
		conds []map[string]any
		want  string
	}{
		{
			name:  "missing conditions returns dash",
			conds: nil,
			want:  "-",
		},
		{
			name: "Ready=True wins over other condition types",
			conds: []map[string]any{
				{"type": "DomainValidated", "status": "True"},
				{"type": "Ready", "status": "True"},
			},
			want: "True",
		},
		{
			name: "Ready=False mirrors verbatim",
			conds: []map[string]any{
				{"type": "Ready", "status": "False"},
			},
			want: "False",
		},
		{
			name: "Ready with non-string status falls through to dash",
			conds: []map[string]any{
				{"type": "Ready", "status": 42},
			},
			want: "-",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := certificateReadyFromConditions(c.conds)
			if got != c.want {
				t.Errorf("certificateReadyFromConditions = %q; want %q", got, c.want)
			}
		})
	}
}

func TestFormatExpiry(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty input", in: "", want: ""},
		{name: "bad RFC3339 falls through to raw", in: "not-a-date", want: "not-a-date"},
		{
			name: "expired returns 'expired'",
			in:   now.Add(-1 * time.Hour).Format(time.RFC3339),
			want: "expired",
		},
		{
			// 50h, not 48h — formatExpiry truncates time.Until() to whole days,
			// so 48h that the runtime has already eaten a few µs of would
			// floor to 1d, not 2d. 50h keeps a comfortable margin.
			name: "future days renders 'in Nd'",
			in:   now.Add(50 * time.Hour).Format(time.RFC3339),
			want: "in 2d",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatExpiry(c.in)
			if !strings.HasPrefix(got, c.want) && got != c.want {
				t.Errorf("formatExpiry(%q) = %q; want prefix %q", c.in, got, c.want)
			}
		})
	}
}

func TestFormatAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty returns dash", in: "", want: "-"},
		{name: "30 seconds ago → just now", in: now.Add(-30 * time.Second).Format(time.RFC3339), want: "just now"},
		{name: "2 hours ago → 2h", in: now.Add(-2 * time.Hour).Format(time.RFC3339), want: "2h"},
		{name: "3 days ago → 3d", in: now.Add(-72 * time.Hour).Format(time.RFC3339), want: "3d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatAge(c.in)
			if got != c.want {
				t.Errorf("formatAge(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

func TestJoinTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		max  int
		want string
	}{
		{name: "empty returns dash", in: nil, max: 2, want: "-"},
		{name: "under max joins all", in: []string{"a", "b"}, max: 3, want: "a,b"},
		{name: "over max appends overflow count", in: []string{"a", "b", "c", "d"}, max: 2, want: "a,b,+2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := joinTruncate(c.in, c.max)
			if got != c.want {
				t.Errorf("joinTruncate(%v, %d) = %q; want %q", c.in, c.max, got, c.want)
			}
		})
	}
}
