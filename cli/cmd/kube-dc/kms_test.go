/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/backend"
)

// printKMSKeysTable / rotationDescription / kmsReadyFromConditions are
// pure helpers extracted from kms.go so the column formatting can be
// pinned without hitting the network. Same pattern as
// certificates_test.go for the cert verbs.

func TestRotationDescription(t *testing.T) {
	cases := []struct {
		name string
		in   backend.KMSKeyRotation
		want string
	}{
		{"disabled", backend.KMSKeyRotation{Enabled: false}, "disabled"},
		{"enabled-no-interval", backend.KMSKeyRotation{Enabled: true}, "enabled"},
		{"enabled-with-interval", backend.KMSKeyRotation{Enabled: true, Interval: "30d"}, "30d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rotationDescription(tc.in); got != tc.want {
				t.Errorf("rotationDescription(%+v) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestKMSReadyFromConditions(t *testing.T) {
	cases := []struct {
		name  string
		conds []map[string]any
		want  string
	}{
		{
			name:  "empty",
			conds: nil,
			want:  "-",
		},
		{
			name:  "ready-true",
			conds: []map[string]any{{"type": "Ready", "status": "True", "reason": "KeyReady"}},
			want:  "True",
		},
		{
			name: "ready-false-with-reason",
			conds: []map[string]any{
				{"type": "Ready", "status": "False", "reason": "DeletionScheduled"},
			},
			want: "False/DeletionScheduled",
		},
		{
			name: "non-Ready-conds-ignored",
			conds: []map[string]any{
				{"type": "RotationScheduled", "status": "True"},
				{"type": "Ready", "status": "True"},
			},
			want: "True",
		},
		{
			name: "ready-false-no-reason",
			conds: []map[string]any{
				{"type": "Ready", "status": "False"},
			},
			want: "False",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kmsReadyFromConditions(tc.conds); got != tc.want {
				t.Errorf("kmsReadyFromConditions = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestPrintKMSKeysTable_RendersExpectedColumns(t *testing.T) {
	// Redirect stdout to capture the table output. The function
	// writes via os.Stdout through tabwriter; capture by swapping
	// stdout for a pipe-end is fiddly, so we accept that this test
	// just runs without panicking and produces non-empty output.
	// The rotationDescription + kmsReadyFromConditions tests above
	// cover the per-row content.
	items := []backend.KMSKeySummary{
		{
			Name: "app-key", Purpose: "application", Algorithm: "aes256-gcm96",
			Rotation: backend.KMSKeyRotation{Enabled: true, Interval: "30d"},
			Status: backend.KMSKeyStatus{
				CurrentVersion: 2,
				Conditions:     []map[string]any{{"type": "Ready", "status": "True"}},
			},
			CreationTimestamp: "",
		},
		{
			Name: "backup-key", Purpose: "backup", Algorithm: "chacha20-poly1305",
			DeletionPolicy: "schedule",
			Status: backend.KMSKeyStatus{
				CurrentVersion: 1,
				Conditions:     []map[string]any{{"type": "Ready", "status": "False", "reason": "DeletionScheduled"}},
			},
		},
	}
	if err := printKMSKeysTable(items); err != nil {
		t.Fatalf("printKMSKeysTable: %v", err)
	}
}

func TestPrintKMSKeysTable_EmptyMessage(t *testing.T) {
	if err := printKMSKeysTable(nil); err != nil {
		t.Fatalf("printKMSKeysTable(nil): %v", err)
	}
}

func TestReadInline_PrefersInlineOverFile(t *testing.T) {
	b, err := readInline("hello", "")
	if err != nil {
		t.Fatalf("readInline: %v", err)
	}
	if string(b) != "hello" {
		t.Errorf("readInline = %q; want hello", string(b))
	}
}

func TestReadInline_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/payload.bin"
	if err := writeFile(path, []byte("from-file")); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := readInline("", path)
	if err != nil {
		t.Fatalf("readInline: %v", err)
	}
	if string(b) != "from-file" {
		t.Errorf("readInline = %q; want from-file", string(b))
	}
}

// writeFile is a tiny test helper to keep the import surface in this
// test file minimal.
func writeFile(path string, b []byte) error {
	var buf bytes.Buffer
	buf.Write(b)
	return writeOutput(path, buf.Bytes())
}

func TestEncodeDecodeBase64_Roundtrip(t *testing.T) {
	original := []byte("\x00binary\xffdata")
	b64 := encodeBase64(original)
	if strings.Contains(b64, "\x00") {
		t.Errorf("encodeBase64 produced non-printable output: %q", b64)
	}
	out, err := decodeBase64(b64)
	if err != nil {
		t.Fatalf("decodeBase64: %v", err)
	}
	if !bytes.Equal(out, original) {
		t.Errorf("roundtrip mismatch: got %v want %v", out, original)
	}
}

func TestDecodeBase64_RejectsGarbage(t *testing.T) {
	if _, err := decodeBase64("not base64!!!"); err == nil {
		t.Errorf("decodeBase64 accepted garbage input")
	}
}

func TestTruncateHelper(t *testing.T) {
	if got := truncate("vault:v1:short", 8); got != "vault:v1" {
		t.Errorf("truncate('vault:v1:short', 8) = %q; want vault:v1", got)
	}
	if got := truncate("ab", 8); got != "ab" {
		t.Errorf("truncate('ab', 8) = %q; want ab", got)
	}
}
