// Package config reads and writes the per-cluster files in kube-dc-fleet:
// cluster-config.env, secrets.enc.yaml, and inline patches.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Env is a parsed cluster-config.env. Order is preserved so a round-trip
// write produces a diff that touches only changed lines (no churn).
type Env struct {
	// Path to the file on disk; empty when constructed in memory.
	Path string

	// lines is the original file in order. Each entry is either a key/value
	// pair, a blank line, or a comment.
	lines []envLine

	// index maps KEY → position in lines for O(1) lookup/replace.
	index map[string]int
}

type envLineKind int

const (
	lineKV envLineKind = iota
	lineBlank
	lineComment
)

type envLine struct {
	kind  envLineKind
	key   string // populated when kind == lineKV
	value string // populated when kind == lineKV
	raw   string // verbatim original (blank/comment) or "KEY=value" reconstructed
}

// LoadEnv reads cluster-config.env at path.
func LoadEnv(path string) (*Env, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	e := &Env{Path: path, index: map[string]int{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			e.lines = append(e.lines, envLine{kind: lineBlank, raw: line})
		case strings.HasPrefix(trimmed, "#"):
			e.lines = append(e.lines, envLine{kind: lineComment, raw: line})
		default:
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				e.lines = append(e.lines, envLine{kind: lineComment, raw: line})
				continue
			}
			key := strings.TrimSpace(line[:eq])
			val := strings.TrimSpace(line[eq+1:])
			val = unquote(val)
			e.lines = append(e.lines, envLine{kind: lineKV, key: key, value: val, raw: line})
			e.index[key] = len(e.lines) - 1
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return e, nil
}

// Get returns the value for key and whether it was set.
func (e *Env) Get(key string) (string, bool) {
	i, ok := e.index[key]
	if !ok {
		return "", false
	}
	return e.lines[i].value, true
}

// GetOr returns the value for key or fallback if missing.
func (e *Env) GetOr(key, fallback string) string {
	v, ok := e.Get(key)
	if !ok {
		return fallback
	}
	return v
}

// AsMap returns a snapshot of every KEY=value pair as a plain map.
// Used by callers that need to feed the env into helpers expecting
// `map[string]string` (e.g. clusterinit.ValidateAnchorConfig).
func (e *Env) AsMap() map[string]string {
	out := make(map[string]string, len(e.index))
	for _, l := range e.lines {
		if l.kind == lineKV {
			out[l.key] = l.value
		}
	}
	return out
}

// Keys returns every KEY in the file in their original order.
func (e *Env) Keys() []string {
	out := make([]string, 0, len(e.index))
	for _, l := range e.lines {
		if l.kind == lineKV {
			out = append(out, l.key)
		}
	}
	return out
}

// Set inserts or replaces a KEY=value entry in the env.
//
//   - When `key` already exists: the value is updated in place. The
//     original line's comments, leading whitespace, and any inline
//     suffix after the value are NOT preserved — the line is
//     rewritten as `KEY=value`. (The fleet's existing
//     `add-cluster.sh` doesn't emit inline comments on the keys we
//     mutate, so this is safe for the M4-T10 post-process.)
//   - When `key` is absent: appends a new `KEY=value` line at the
//     end of the file. Callers that want a particular section
//     placement should add a comment header first then call Set.
//
// Set never panics; empty keys are accepted but generate a line
// that LoadEnv's eq-position parser handles as a comment on
// round-trip — callers should reject empty keys upstream.
func (e *Env) Set(key, value string) {
	if i, ok := e.index[key]; ok {
		e.lines[i].value = value
		e.lines[i].raw = key + "=" + value
		return
	}
	e.lines = append(e.lines, envLine{
		kind:  lineKV,
		key:   key,
		value: value,
		raw:   key + "=" + value,
	})
	e.index[key] = len(e.lines) - 1
}

// AppendComment appends a comment line. Used by M4-T10 to mark
// inherited-version-pin sections in the post-processed env file
// (e.g. `# --- inherited from sibling: cloud ---`).
func (e *Env) AppendComment(text string) {
	e.lines = append(e.lines, envLine{kind: lineComment, raw: text})
}

// AppendBlank appends a blank separator line. Pairs with
// AppendComment for clean section breaks in the rewritten file.
func (e *Env) AppendBlank() {
	e.lines = append(e.lines, envLine{kind: lineBlank, raw: ""})
}

// Write atomically writes the env to `path` (or to e.Path when
// empty). Format: one line per envLine, joined with `\n`, trailing
// newline. Atomicity: writes to `<path>.tmp.<random>` then renames.
// Mode 0644 — cluster-config.env contains no secret material.
//
// M4-T10's post-process step calls this after Set'ting the
// preset's defaults + --set deltas + inherited version pins onto
// the env that `bootstrap/add-cluster.sh` generated.
func (e *Env) Write(path string) error {
	if path == "" {
		path = e.Path
	}
	if path == "" {
		return fmt.Errorf("env: Write needs a path (none on the env, none supplied)")
	}
	dir := dirOf(path)
	tmp, err := os.CreateTemp(dir, filepathBase(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("env: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	var b strings.Builder
	for i, l := range e.lines {
		switch l.kind {
		case lineKV:
			b.WriteString(l.key)
			b.WriteByte('=')
			b.WriteString(l.value)
		case lineComment, lineBlank:
			b.WriteString(l.raw)
		}
		if i < len(e.lines)-1 || true {
			// Always write a trailing newline so the file is
			// well-formed (most parsers, including ours, tolerate
			// either shape; explicit terminator is cleaner).
			b.WriteByte('\n')
		}
	}

	if _, werr := tmp.WriteString(b.String()); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("env: write %s: %w", tmpPath, werr)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("env: chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("env: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("env: rename %s -> %s: %w", tmpPath, path, err)
	}
	// Update e.Path so a subsequent Write() without an argument
	// targets the same file.
	e.Path = path
	return nil
}

// dirOf returns the directory portion of `path`. Lives here (vs
// filepath.Dir) so config/env.go's import set stays minimal — this
// package is read by many slices and avoiding cascading imports
// keeps the dep tree tight.
func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		if i == 0 {
			return "/"
		}
		return path[:i]
	}
	return "."
}

// filepathBase mirrors filepath.Base for `path` — see dirOf rationale.
func filepathBase(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// unquote strips matching surrounding single or double quotes, if present.
// Values without quotes are returned unchanged.
func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
