package log

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// Layer 1 — field-name regex.
//
// Match any field whose name carries a token suggesting it holds secret
// material. The `key$` alternative deliberately matches the literal
// field name "key" (which gets handled before layer 1 in Handle —
// layer 2 inspects the value of a "key" attribute as a marker pointing
// at a sibling "value" attribute). After layer 2 runs, a remaining
// "key" attribute IS itself a secret (`apikey`, `ssh_key`, `age_key`)
// and SHOULD be redacted.
//
// Spec: agent rule 7 of `installer-agentic-implementation-plan.md`,
// M0-T05 contract.
var fieldNameRegex = regexp.MustCompile(`(?i)token|password|share|secret|key$|share_[0-9]+|unseal`)

// Layer 4 — stream-line patterns.
var (
	ageSecretKeyRegex    = regexp.MustCompile(`AGE-SECRET-KEY-1[A-Z0-9]{58}`)
	ageRecipientKeyRegex = regexp.MustCompile(`age1[a-z0-9]{58}`)
	openBaoInitJSONRegex = regexp.MustCompile(`"(unseal_keys_b64|unseal_keys_hex|root_token|keys|keys_base64)"\s*:\s*("(?:[^"\\]|\\.)*"|\[[^\]]*\])`)
	longBase64Regex      = regexp.MustCompile(`[A-Za-z0-9+/]{25,}={0,2}`)
	secretContextWordsRe = regexp.MustCompile(`(?i)token|password|share|secret|unseal|key`)
)

// ---------- exported helpers (layers 3, 4, 5) ----------

// RedactEnv returns a copy of env where any entry whose key matches the
// secret-name regex has its value replaced with the redaction marker.
// Callers in ScriptRunner pass the returned map to slog before exec.
//
// Layer 3 in the M0-T05 contract.
func RedactEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if fieldNameRegex.MatchString(k) {
			out[k] = RedactedMarker
		} else {
			out[k] = v
		}
	}
	return out
}

// RedactStreamLine masks plausible secret material in a single line of
// script output. Applies, in order:
//
//  1. age secret-key pattern (`AGE-SECRET-KEY-1…`) — always redacted.
//  2. age recipient pattern (`age1…`) — always redacted. The recipient
//     is technically public, but it's also a high-signal identifier of
//     a keyholder and we already SOPS-encrypt the secret key; treating
//     both as opaque keeps the regex set small.
//  3. OpenBao-init JSON keys (`unseal_keys_b64` / `root_token` etc.) —
//     value replaced. Matches both string and array shapes.
//  4. If the line contains a secret-context word (`token`, `password`,
//     `share`, `secret`, `unseal`, `key`), any base64-ish run of 25+
//     chars gets masked. The context gate prevents accidentally
//     masking unrelated identifiers (image digests, JWT issuer URIs).
//
// Layer 4 in the M0-T05 contract. Also applied to slog Record.Message
// by redactingHandler.Handle as defense-in-depth — see the WHY note
// there.
func RedactStreamLine(line string) string {
	out := ageSecretKeyRegex.ReplaceAllString(line, RedactedMarker)
	out = ageRecipientKeyRegex.ReplaceAllString(out, RedactedMarker)
	out = openBaoInitJSONRegex.ReplaceAllString(out, `"$1": "`+RedactedMarker+`"`)
	if secretContextWordsRe.MatchString(out) {
		out = longBase64Regex.ReplaceAllStringFunc(out, func(s string) string {
			// Don't re-redact our own marker.
			if s == strings.Trim(RedactedMarker, "[]") {
				return s
			}
			return RedactedMarker
		})
	}
	return out
}

// SentinelPlaceholder is the text emitted by ScriptRunner in place of
// the raw payload when a sentinel-bracketed block was captured. The
// runner uses this exact format so the redaction layer can pass it
// through unchanged.
//
// Layer 5 in the M0-T05 contract.
func SentinelPlaceholder(scriptName, summary string) string {
	return fmt.Sprintf("[%s payload captured — %s]", scriptName, summary)
}

// ---------- redactingHandler (layers 1 + 2 + map-walk + message) ----------

// redactingHandler wraps an inner slog.Handler and rewrites attributes
// (and the record's message) before delegation. It is the only place
// layers 1, 2 and the in-message redaction live; constructing a
// slog.Logger any other way inside `bootstrap` would silently bypass
// redaction (which is why callers MUST use log.New rather than
// building handlers themselves).
//
// **secretKeyContext** carries the adjacency state across
// `Logger.With()` chains. When a `WithAttrs` call sees a `key:` attr
// whose string value matches the secret-name regex, the resulting
// child handler stores that name. Subsequent records OR subsequent
// `WithAttrs` calls that introduce a `value:` attr can then redact it,
// even though the original `key:` attr no longer appears in the
// per-record attr list. Without this propagation, the canonical
// `lg.With("key", "OPENBAO_UNSEAL_KEY_1").Info("write", "value", "…")`
// pattern would silently leak `value`.
//
// Propagation is sticky: once any ancestor handler set context, all
// descendants inherit it. There is intentionally no "clear" path —
// re-using a context-tainted child for non-secret work is the
// caller's bug. Use the root logger (no `With` chain through a secret
// key) for unrelated records.
type redactingHandler struct {
	inner            slog.Handler
	secretKeyContext string // empty == no secret-key adjacency in lineage
}

func newRedactingHandler(inner slog.Handler) *redactingHandler {
	return &redactingHandler{inner: inner}
}

func (h *redactingHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.inner.Enabled(ctx, lvl)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	// Collect attrs into a slice so we can do a layer-2 scan that needs
	// access to sibling values, then a layer-1 pass.
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	// Layer 2: in-record adjacency. Either a same-record `key:` attr
	// names a secret OR our lineage already set a secret-key context.
	keyName, valueIdx := scanKeyValue(attrs)
	redactedValueByAdjacency := false
	if valueIdx >= 0 {
		sameRecordSecret := keyName != "" && fieldNameRegex.MatchString(keyName)
		if sameRecordSecret || h.secretKeyContext != "" {
			attrs[valueIdx] = slog.String("value", RedactedMarker)
			redactedValueByAdjacency = true
		}
	}

	// Layer 1 + map-walk: rewrite each attr based on its own name and,
	// for map-valued attrs, walk the map.
	for i, a := range attrs {
		if i == valueIdx && redactedValueByAdjacency {
			continue
		}
		attrs[i] = redactAttr(a)
	}

	// Defense-in-depth: redact the message itself. The shape contract
	// (constant msg + structured attrs) makes this a no-op for
	// well-behaved callers, but `lg.Error(err.Error())` or
	// `lg.Info(rawScriptLine)` would otherwise bypass every other
	// layer. RedactStreamLine is conservative (context-gated base64)
	// so legitimate messages pass through unchanged.
	msg := RedactStreamLine(r.Message)

	newRec := slog.NewRecord(r.Time, r.Level, msg, r.PC)
	newRec.AddAttrs(attrs...)
	return h.inner.Handle(ctx, newRec)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Layer 2 within THIS call's own attrs. The canonical paired-attrs
	// shape `With("key", "OPENBAO_UNSEAL_KEY_1", "value", "...")` is
	// handled here — by the time the inner handler stores anything,
	// `value` has already been redacted, so the original cannot leak
	// even if a downstream sink scans stored attrs.
	keyName, valueIdx := scanKeyValue(attrs)
	out := make([]slog.Attr, len(attrs))
	redactedValueByAdjacency := false
	if valueIdx >= 0 {
		sameCallSecret := keyName != "" && fieldNameRegex.MatchString(keyName)
		if sameCallSecret || h.secretKeyContext != "" {
			out[valueIdx] = slog.String("value", RedactedMarker)
			redactedValueByAdjacency = true
		}
	}
	for i, a := range attrs {
		if i == valueIdx && redactedValueByAdjacency {
			continue
		}
		out[i] = redactAttr(a)
	}

	// Context propagation. A same-call secret `key:` name takes
	// precedence; otherwise inherit the lineage's context. There's no
	// path to clear it — see type doc.
	ctx := h.secretKeyContext
	if keyName != "" && fieldNameRegex.MatchString(keyName) {
		ctx = keyName
	}

	return &redactingHandler{
		inner:            h.inner.WithAttrs(out),
		secretKeyContext: ctx,
	}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{
		inner:            h.inner.WithGroup(name),
		secretKeyContext: h.secretKeyContext, // preserve lineage
	}
}

// scanKeyValue locates the `key:` and `value:` siblings in an attr
// slice. Returns the string value of `key` (or empty if absent / not a
// string) and the index of `value` (or -1 if absent). Used by both
// Handle and WithAttrs so the two paths share semantics exactly.
func scanKeyValue(attrs []slog.Attr) (keyName string, valueIdx int) {
	valueIdx = -1
	for i, a := range attrs {
		switch a.Key {
		case "key":
			if a.Value.Kind() == slog.KindString {
				keyName = a.Value.String()
			}
		case "value":
			valueIdx = i
		}
	}
	return keyName, valueIdx
}

// redactAttr applies layer-1 name-match redaction and walks map-valued
// attributes for layer-3 in-record redaction. Recurses into slog.Group
// values and nested maps / slices so deeply-structured payloads can't
// hide a secret behind one more layer of nesting.
func redactAttr(a slog.Attr) slog.Attr {
	if fieldNameRegex.MatchString(a.Key) {
		return slog.String(a.Key, RedactedMarker)
	}
	switch a.Value.Kind() {
	case slog.KindAny:
		v := a.Value.Any()
		if rv, ok := redactValue(v); ok {
			return slog.Any(a.Key, rv)
		}
	case slog.KindGroup:
		group := a.Value.Group()
		out := make([]slog.Attr, len(group))
		for i, ga := range group {
			out[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	return a
}

// redactValue walks structured values (maps, slices) and returns a new
// copy with secret-keyed entries replaced. ok=false means v is a
// scalar (caller leaves it alone) — keep this signature so callers
// can fall through cleanly without an extra type switch.
//
// Supports: map[string]string, map[string]any, []any, []map[string]any.
// Nested combinations are walked recursively (a slice of maps inside
// an `any` map is fully covered).
func redactValue(v any) (any, bool) {
	switch m := v.(type) {
	case map[string]string:
		return RedactEnv(m), true
	case map[string]any:
		return redactAnyMap(m), true
	case []any:
		out := make([]any, len(m))
		for i, item := range m {
			if rv, ok := redactValue(item); ok {
				out[i] = rv
			} else {
				out[i] = item
			}
		}
		return out, true
	case []map[string]any:
		out := make([]map[string]any, len(m))
		for i, item := range m {
			out[i] = redactAnyMap(item)
		}
		return out, true
	}
	return nil, false
}

// redactAnyMap copies m, redacting entries whose key matches the
// secret-name regex AND recursing into values that themselves are
// structured (maps, slices). Without recursion, a payload like
// `{"meta": {"token": "abc"}}` would leak `abc` because the top-level
// key "meta" doesn't match the regex.
func redactAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if fieldNameRegex.MatchString(k) {
			out[k] = RedactedMarker
			continue
		}
		if rv, ok := redactValue(v); ok {
			out[k] = rv
		} else {
			out[k] = v
		}
	}
	return out
}
