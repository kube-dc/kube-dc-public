package anchors

import (
	"strings"
	"testing"
)

func TestParseAnchorMap_Empty(t *testing.T) {
	cases := []string{"", "   ", "\t\n"}
	for _, in := range cases {
		got, err := ParseAnchorMap(in)
		if err != nil {
			t.Errorf("empty %q: unexpected error %v", in, err)
		}
		if got != nil {
			t.Errorf("empty %q: expected nil slice, got %+v", in, got)
		}
	}
}

func TestParseAnchorMap_Valid(t *testing.T) {
	got, err := ParseAnchorMap("host7-a=100.64.0.13/16,host5-a=100.64.0.11/16,host6-a=100.64.0.12/16")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []Entry{
		{Host: "host5-a", CIDR: "100.64.0.11/16"},
		{Host: "host6-a", CIDR: "100.64.0.12/16"},
		{Host: "host7-a", CIDR: "100.64.0.13/16"},
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (got %+v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("entry[%d] = %+v, want %+v (sort by host)", i, got[i], w)
		}
	}
}

func TestParseAnchorMap_ToleratesWhitespace(t *testing.T) {
	got, err := ParseAnchorMap("  host5-a = 100.64.0.11/16 , host6-a=100.64.0.12/16  ")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	if got[0].Host != "host5-a" || got[0].CIDR != "100.64.0.11/16" {
		t.Errorf("entry[0] = %+v", got[0])
	}
}

func TestParseAnchorMap_Errors(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantSub string
	}{
		{"missing equals", "host5-a-100.64.0.11/16", "missing '='"},
		{"empty host", "=100.64.0.11/16", "empty host"},
		{"duplicate host", "host5-a=100.64.0.11/16,host5-a=100.64.0.12/16", "more than once"},
		{"bad CIDR", "host5-a=100.64.0.11", "invalid CIDR"},
		{"bad mask", "host5-a=100.64.0.11/99", "invalid CIDR"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAnchorMap(tc.in)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}
