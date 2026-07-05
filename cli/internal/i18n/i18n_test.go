package i18n

import (
	"strings"
	"testing"
)

func TestT_KnownKey(t *testing.T) {
	got := T("addnode.v2_notice")
	if !strings.Contains(got, "v2 feature") {
		t.Errorf("known key returned wrong message: %q", got)
	}
}

func TestT_MissingKeyReturnsKeyNeverEmpty(t *testing.T) {
	// A raw key in output is a self-filing bug report; an empty
	// string is silent data loss.
	if got := T("no.such.key"); got != "no.such.key" {
		t.Errorf("missing key must return the key, got %q", got)
	}
	if got := T("no.such.key", "arg1", 2); !strings.Contains(got, "no.such.key") || !strings.Contains(got, "arg1") {
		t.Errorf("missing key with args must keep both visible, got %q", got)
	}
}

func TestT_NoEmptyCatalogEntries(t *testing.T) {
	// Guard the catalog itself: an empty value would defeat the
	// never-empty contract via the front door.
	for k, v := range catalogEN {
		if strings.TrimSpace(v) == "" {
			t.Errorf("catalog key %q has an empty message", k)
		}
	}
}
