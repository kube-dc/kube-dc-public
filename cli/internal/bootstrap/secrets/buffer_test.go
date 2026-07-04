package secrets

import (
	"errors"
	"reflect"
	"testing"
)

func TestBuffer_SetAndGetRoundTrip(t *testing.T) {
	b := NewBuffer()
	defer b.Scrub()

	b.SetShare(0, []byte("share-zero"))
	b.SetShare(2, []byte("share-two"))
	b.SetRootToken([]byte("root-tok"))

	if n := b.ShareCount(); n != 2 {
		t.Errorf("ShareCount=%d, want 2", n)
	}

	got0, err := b.Share(0)
	if err != nil {
		t.Fatalf("Share(0) err: %v", err)
	}
	if string(got0) != "share-zero" {
		t.Errorf("Share(0)=%q, want share-zero", got0)
	}

	got2, err := b.Share(2)
	if err != nil {
		t.Fatalf("Share(2) err: %v", err)
	}
	if string(got2) != "share-two" {
		t.Errorf("Share(2)=%q, want share-two", got2)
	}

	// Unset slot returns (nil, nil) — distinct from scrubbed.
	got1, err := b.Share(1)
	if err != nil {
		t.Errorf("Share(1) on unset slot returned err=%v, want nil", err)
	}
	if got1 != nil {
		t.Errorf("Share(1) on unset slot=%v, want nil", got1)
	}

	tok, err := b.RootToken()
	if err != nil {
		t.Fatalf("RootToken err: %v", err)
	}
	if string(tok) != "root-tok" {
		t.Errorf("RootToken=%q, want root-tok", tok)
	}
}

func TestBuffer_DefensiveCopy_OnSet(t *testing.T) {
	// Caller scrubs their own slice immediately after SetShare; the
	// buffer's copy must remain intact.
	b := NewBuffer()
	defer b.Scrub()

	src := []byte("important")
	b.SetShare(0, src)

	// Caller scrubs their own copy
	for i := range src {
		src[i] = 0
	}

	got, _ := b.Share(0)
	if string(got) != "important" {
		t.Errorf("after caller scrubbed source, Share(0)=%q, want important — defensive copy not made", got)
	}
}

func TestBuffer_DefensiveCopy_OnGet(t *testing.T) {
	// Caller scrubs the slice they received from Share(); the buffer's
	// internal copy must remain intact.
	b := NewBuffer()
	defer b.Scrub()

	b.SetShare(0, []byte("important"))

	first, _ := b.Share(0)
	for i := range first {
		first[i] = 0
	}

	second, _ := b.Share(0)
	if string(second) != "important" {
		t.Errorf("after caller scrubbed receive-copy, second Share(0)=%q, want important — defensive copy on read missing", second)
	}
}

func TestBuffer_Scrub_ZeroesAndLocks(t *testing.T) {
	b := NewBuffer()

	b.SetShare(0, []byte("share-zero"))
	b.SetRootToken([]byte("root-tok"))

	if b.Scrubbed() {
		t.Fatal("Scrubbed() true before Scrub()")
	}

	b.Scrub()

	if !b.Scrubbed() {
		t.Fatal("Scrubbed() false after Scrub()")
	}
	if n := b.ShareCount(); n != 0 {
		t.Errorf("ShareCount after Scrub=%d, want 0", n)
	}

	// Accessors return ErrScrubbed.
	_, err := b.Share(0)
	if !errors.Is(err, ErrScrubbed) {
		t.Errorf("Share after Scrub: err=%v, want ErrScrubbed", err)
	}
	_, err = b.RootToken()
	if !errors.Is(err, ErrScrubbed) {
		t.Errorf("RootToken after Scrub: err=%v, want ErrScrubbed", err)
	}

	// Mutators after Scrub are no-ops.
	b.SetShare(1, []byte("post-scrub"))
	b.SetRootToken([]byte("post-scrub"))
	if n := b.ShareCount(); n != 0 {
		t.Errorf("SetShare after Scrub mutated state — ShareCount=%d", n)
	}
}

func TestBuffer_Scrub_Idempotent(t *testing.T) {
	b := NewBuffer()
	b.SetShare(0, []byte("x"))
	b.Scrub()
	// Second scrub must not panic.
	b.Scrub()
	if !b.Scrubbed() {
		t.Error("Scrubbed() false after double Scrub()")
	}
}

func TestBuffer_OutOfRangeShareIndex(t *testing.T) {
	b := NewBuffer()
	defer b.Scrub()

	// Out-of-range Set: silently dropped.
	b.SetShare(-1, []byte("nope"))
	b.SetShare(5, []byte("nope"))
	b.SetShare(99, []byte("nope"))
	if n := b.ShareCount(); n != 0 {
		t.Errorf("ShareCount after out-of-range Set=%d, want 0", n)
	}

	// Out-of-range Get: (nil, nil).
	got, err := b.Share(-1)
	if got != nil || err != nil {
		t.Errorf("Share(-1)=%v, %v; want nil, nil", got, err)
	}
	got, err = b.Share(99)
	if got != nil || err != nil {
		t.Errorf("Share(99)=%v, %v; want nil, nil", got, err)
	}
}

func TestBuffer_NilInputsHandled(t *testing.T) {
	b := NewBuffer()
	defer b.Scrub()

	b.SetShare(0, nil)
	b.SetRootToken(nil)
	if n := b.ShareCount(); n != 0 {
		t.Errorf("SetShare(nil) populated slot — ShareCount=%d", n)
	}
	tok, err := b.RootToken()
	if tok != nil || err != nil {
		t.Errorf("RootToken after SetRootToken(nil)=%v, %v; want nil, nil", tok, err)
	}
}

func TestBuffer_AllFiveShares(t *testing.T) {
	// The canonical OpenBao 5/3 share-custody flow populates all 5 slots.
	b := NewBuffer()
	defer b.Scrub()

	want := [5]string{"s0", "s1", "s2", "s3", "s4"}
	for i, s := range want {
		b.SetShare(i, []byte(s))
	}

	if n := b.ShareCount(); n != 5 {
		t.Fatalf("ShareCount=%d, want 5", n)
	}

	for i, expect := range want {
		got, err := b.Share(i)
		if err != nil {
			t.Errorf("Share(%d) err: %v", i, err)
			continue
		}
		if string(got) != expect {
			t.Errorf("Share(%d)=%q, want %q", i, got, expect)
		}
	}
}

func TestZeroBytes_NilSafe(t *testing.T) {
	// Internal helper sanity check — Scrub() calls this on nil slots.
	zeroBytes(nil)
	zeroBytes([]byte{})
	// No assertion; just confirming no panic.
}

func TestCloneBytes_ProducesIndependentSlice(t *testing.T) {
	src := []byte{1, 2, 3}
	dst := cloneBytes(src)
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("cloneBytes content mismatch: %v vs %v", src, dst)
	}
	src[0] = 99
	if dst[0] == 99 {
		t.Error("cloneBytes shared backing array — modifying src changed dst")
	}
}
