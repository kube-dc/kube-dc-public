// Package secrets owns in-memory storage for plaintext secret material the
// bootstrap engine handles transiently — primarily the 5 OpenBao Shamir
// shares + root token captured from `openbao-init.sh` in M5-T01.
//
// Why a typed wrapper instead of `[]byte` everywhere:
//
//   - Callers MUST `defer buf.Scrub()`. The type's existence makes the
//     scrub discipline reviewable: `grep -RE 'NewBuffer\b' cli/internal/`
//     should show a `defer Scrub()` within a few lines of every match.
//   - Storage is `[]byte`, not `string` — Go strings are immutable and
//     the runtime is free to copy them anywhere, so they can't be
//     zeroed. `[]byte` can.
//   - Forces a single owner per Buffer. There's no `Bytes() []byte`
//     escape hatch — callers ask "give me share N" or "give me the
//     root token" and get a freshly-allocated copy they're responsible
//     for scrubbing too (or never assigning to a long-lived variable).
//   - No `String()` method, deliberately — prevents accidental leakage
//     via `fmt.Printf("%v", buf)`.
//
// See agent rule 7 of `docs/prd/installer-agentic-implementation-plan.md`
// and the broader OpenBao share-custody flow at installer-prd.md §12.3.
package secrets

import (
	"errors"
	"runtime"
)

// ErrScrubbed is returned by Buffer accessors after Scrub() has been
// called. A scrubbed Buffer is permanent — Set* methods become no-ops,
// accessors return this sentinel. Callers MAY use errors.Is(err,
// ErrScrubbed) to distinguish "share not set" from "share was scrubbed".
var ErrScrubbed = errors.New("secrets: buffer has been scrubbed")

// Buffer holds plaintext OpenBao share material strictly in memory.
// The zero value is NOT usable — call NewBuffer().
//
// Lifecycle:
//
//	buf := secrets.NewBuffer()
//	defer buf.Scrub()
//	buf.SetShare(0, shareBytes)
//	buf.SetRootToken(tokBytes)
//	... use share/token ...
//	// buf.Scrub() runs via defer, zeroes the underlying bytes.
//
// Concurrency: a single Buffer is owned by a single goroutine (the
// share-custody flow is sequential by design — operator confirms,
// we encrypt, we scrub). No locking; concurrent use is undefined.
type Buffer struct {
	shares    [5][]byte // 0-indexed; capacity matches Shamir 5/3 fixed shape
	rootToken []byte
	scrubbed  bool
}

// NewBuffer constructs a fresh Buffer. The 5 share slots start as nil
// slices (ShareCount returns 0) until populated via SetShare.
func NewBuffer() *Buffer {
	return &Buffer{}
}

// SetShare stores the i-th share (0-indexed). i in [0,5) — out-of-range
// indices are silently dropped (caller should range over [0,5) and never
// see this). After Scrub() this is a no-op.
//
// Stores a defensive copy — the caller's slice can be scrubbed
// immediately after the call. The buffer's internal slice has its
// underlying array allocated via `make` so Scrub() can zero it cleanly
// (a slice sourced from an append-grown backing might share memory with
// other data we shouldn't touch).
func (b *Buffer) SetShare(i int, share []byte) {
	if b.scrubbed {
		return
	}
	if i < 0 || i >= len(b.shares) {
		return
	}
	b.shares[i] = cloneBytes(share)
}

// SetRootToken stores the OpenBao root token. After Scrub() this is a
// no-op.
func (b *Buffer) SetRootToken(token []byte) {
	if b.scrubbed {
		return
	}
	b.rootToken = cloneBytes(token)
}

// Share returns a freshly-allocated copy of the i-th share. Returns
// (nil, nil) when the slot is unset, (nil, ErrScrubbed) when the
// buffer has been scrubbed.
//
// Caller MUST scrub the returned slice when done, OR pass it directly
// into a consumer that scrubs internally (e.g. SOPSClient.SetStringData
// which the M5-T01 caller invokes per share — the adapter copies via
// `sops --set` and the slice is reusable for the next iteration).
func (b *Buffer) Share(i int) ([]byte, error) {
	if b.scrubbed {
		return nil, ErrScrubbed
	}
	if i < 0 || i >= len(b.shares) || b.shares[i] == nil {
		return nil, nil
	}
	return cloneBytes(b.shares[i]), nil
}

// RootToken returns a freshly-allocated copy of the root token. Returns
// (nil, nil) when unset, (nil, ErrScrubbed) when scrubbed.
func (b *Buffer) RootToken() ([]byte, error) {
	if b.scrubbed {
		return nil, ErrScrubbed
	}
	if b.rootToken == nil {
		return nil, nil
	}
	return cloneBytes(b.rootToken), nil
}

// ShareCount returns how many of the 5 share slots are populated. Useful
// for "did we capture all 5?" assertions in the share-custody flow.
// Returns 0 on a scrubbed buffer.
func (b *Buffer) ShareCount() int {
	if b.scrubbed {
		return 0
	}
	n := 0
	for _, s := range b.shares {
		if s != nil {
			n++
		}
	}
	return n
}

// Scrubbed reports whether Scrub() has been called.
func (b *Buffer) Scrubbed() bool { return b.scrubbed }

// Scrub zeroes the underlying bytes for every share and the root token,
// then marks the Buffer as permanently scrubbed. Idempotent — calling
// twice is fine, second call is a no-op.
//
// **Compiler-fence**: `runtime.KeepAlive(b)` after the zero loop
// prevents the Go compiler from eliminating the zero writes as "dead
// stores" under aggressive escape analysis. Without it, a sufficiently
// smart compiler could observe that the bytes are written and the
// slice headers are about to become unreachable, and elide the
// `for i := range s; s[i] = 0` loop. KeepAlive forces the bytes to be
// considered live until after the zero pass.
func (b *Buffer) Scrub() {
	if b.scrubbed {
		return
	}
	for i := range b.shares {
		zeroBytes(b.shares[i])
		b.shares[i] = nil
	}
	zeroBytes(b.rootToken)
	b.rootToken = nil
	b.scrubbed = true
	runtime.KeepAlive(b)
}

// cloneBytes allocates a fresh slice of the same length as src, copies
// the contents, and returns it. The underlying array is freshly
// allocated via `make` so Scrub() can zero it without touching anyone
// else's memory.
func cloneBytes(src []byte) []byte {
	if src == nil {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

// zeroBytes writes 0 to every byte in b. Used by Scrub() — `KeepAlive`
// in the caller defeats dead-store elimination. Safe to call with nil
// or zero-length slice.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
