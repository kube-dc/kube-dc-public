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
