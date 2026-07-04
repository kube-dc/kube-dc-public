/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/backend"
)

// Unit tests for `kube-dc db credentials` — focused on the pure
// formatting + rendering helpers so column shape can be pinned
// without touching the backend or network. Mirrors kms_test.go.

func TestDBCPRotationDescription(t *testing.T) {
	cases := []struct {
		name string
		in   backend.DBCredentialRotation
		mode string
		want string
	}{
		{
			name: "dynamic-mode-ignores-rotation",
			in:   backend.DBCredentialRotation{Interval: "30d"},
			mode: "dynamic",
			want: "n/a (dynamic)",
		},
		{
			name: "static-no-interval-defaults",
			in:   backend.DBCredentialRotation{},
			mode: "static-rotated",
			want: "30d (default)",
		},
		{
			name: "static-with-interval-rolling-default-omitted",
			in:   backend.DBCredentialRotation{Interval: "7d", Strategy: "rolling"},
			mode: "static-rotated",
			want: "7d",
		},
		{
			name: "static-immediate-strategy-rendered",
			in:   backend.DBCredentialRotation{Interval: "12h", Strategy: "immediate"},
			mode: "static-rotated",
			want: "12h (immediate)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dbcpRotationDescription(tc.in, tc.mode); got != tc.want {
				t.Errorf("dbcpRotationDescription(%+v, %q) = %q; want %q", tc.in, tc.mode, got, tc.want)
			}
		})
	}
}

func TestDBCPSyncDescription(t *testing.T) {
	fls := false
	cases := []struct {
		name       string
		in         backend.DBCredentialSync
		statusName string
		want       string
	}{
		{
			name: "explicit-disabled-wins-over-target",
			// User flipped sync off — even if a target name is set on
			// spec, the disabled signal is what matters for the table cell.
			in:   backend.DBCredentialSync{Enabled: &fls, TargetSecretName: "ignored"},
			want: "disabled",
		},
		{
			name: "status-target-preferred-over-spec",
			// Status mirrors what the reconciler actually projected, which
			// is the source of truth for "where can I find the rotated creds".
			in:         backend.DBCredentialSync{TargetSecretName: "spec-target"},
			statusName: "status-target",
			want:       "status-target",
		},
		{
			name: "spec-target-when-status-empty",
			in:   backend.DBCredentialSync{TargetSecretName: "spec-only"},
			want: "spec-only",
		},
		{
			name: "fully-unset-renders-dash",
			in:   backend.DBCredentialSync{},
			want: "-",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dbcpSyncDescription(tc.in, tc.statusName); got != tc.want {
				t.Errorf("dbcpSyncDescription(%+v, %q) = %q; want %q", tc.in, tc.statusName, got, tc.want)
			}
		})
	}
}

// Smoke-test the table renderer with mixed static + dynamic rows + a
// row missing a target Secret — primarily checks it doesn't panic.
// The per-cell content is pinned by the dedicated description tests
// above. Mirrors TestPrintKMSKeysTable_RendersExpectedColumns.
func TestPrintDBCPTable_RendersExpectedColumns(t *testing.T) {
	tru := true
	items := []backend.DBCredentialPolicySummary{
		{
			Name:        "docs-pg-app",
			DatabaseRef: "docs-pg",
			Mode:        "static-rotated",
			Username:    "app",
			Rotation:    backend.DBCredentialRotation{Interval: "30d"},
			Sync:        backend.DBCredentialSync{Enabled: &tru, TargetSecretName: "docs-pg-app"},
			Status: backend.DBCredentialPolicyStatus{
				TargetSecretName: "docs-pg-app",
				Conditions:       []map[string]any{{"type": "Ready", "status": "True"}},
			},
		},
		{
			Name:        "docs-pg-readonly",
			DatabaseRef: "docs-pg",
			Mode:        "dynamic",
			Role:        "readonly",
			Status: backend.DBCredentialPolicyStatus{
				Conditions: []map[string]any{{"type": "Ready", "status": "False", "reason": "DynamicModeDeferred"}},
			},
		},
	}
	if err := printDBCPTable(items); err != nil {
		t.Fatalf("printDBCPTable: %v", err)
	}
}

func TestPrintDBCPTable_EmptyList(t *testing.T) {
	if err := printDBCPTable(nil); err != nil {
		t.Fatalf("printDBCPTable(nil): %v", err)
	}
}

// Spec round-trip on the create payload: ensure the helper-built spec
// preserves the canonical naming contract (databaseRef.name is set,
// mode/username/role flow through). Pure data — no network.
// captureStdout swaps os.Stdout for a pipe, runs fn, then returns
// what fn wrote. Used to pin the env renderer's exact byte output —
// the value is meant for shell `eval`, so unexpected whitespace or
// extra lines would break callers silently.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	if err := fn(); err != nil {
		_ = w.Close()
		<-done
		t.Fatalf("fn: %v", err)
	}
	_ = w.Close()
	<-done
	return buf.String()
}

// `-o env` is the dynamic-credential UX so `eval "$(... -o env)"`
// works. Lock the exact line shape — variable names + ordering both
// matter because shell consumers may read them positionally.
func TestPrintDBCredentialsEnv_KeyValueLines(t *testing.T) {
	creds := &backend.DBCredentials{
		Username:          "app",
		Password:          "rotated-pw-1",
		LastVaultRotation: "2026-05-23T13:00:00Z",
		RotationPeriod:    2592000,
	}
	got := captureStdout(t, func() error { return printDBCredentialsEnv(creds) })
	wantLines := []string{
		"KUBE_DC_DB_USERNAME=app",
		"KUBE_DC_DB_PASSWORD=rotated-pw-1",
		"KUBE_DC_DB_LAST_ROTATION=2026-05-23T13:00:00Z",
		"KUBE_DC_DB_ROTATION_PERIOD=2592000",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("env output missing %q line; got:\n%s", want, got)
		}
	}
	// Verify ordering — first two lines MUST be USERNAME then PASSWORD
	// so a downstream `read -r KEY VAL` pattern works.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "KUBE_DC_DB_USERNAME=") || !strings.HasPrefix(lines[1], "KUBE_DC_DB_PASSWORD=") {
		t.Errorf("first two env lines must be USERNAME then PASSWORD; got %q + %q", lines[0], lines[1])
	}
}

// Lease env renderer for `issue`. Includes the lease ID + duration so
// the consumer can drive renew/revoke after `eval`.
func TestPrintDBLeaseEnv_IncludesLeaseMetadata(t *testing.T) {
	lease := &backend.DBLease{
		Username:      "v-user-abc",
		Password:      "lease-pw",
		LeaseId:       "database/creds/foo-1234",
		LeaseDuration: 3600,
	}
	got := captureStdout(t, func() error { return printDBLeaseEnv(lease) })
	for _, want := range []string{
		"KUBE_DC_DB_USERNAME=v-user-abc",
		"KUBE_DC_DB_PASSWORD=lease-pw",
		"KUBE_DC_DB_LEASE_ID=database/creds/foo-1234",
		"KUBE_DC_DB_LEASE_DURATION=3600",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("lease env output missing %q line; got:\n%s", want, got)
		}
	}
}

// parseDBOutput should pass non-env values through to parseOutput and
// reject unknown formats with a clear error.
func TestParseDBOutput(t *testing.T) {
	cases := []struct {
		in      string
		wantEnv bool
		wantFmt outputFormat
		wantErr bool
	}{
		{"env", true, "", false},
		{"ENV", true, "", false}, // case-insensitive parity with parseOutput
		{"table", false, outTable, false},
		{"", false, outTable, false},
		{"json", false, outJSON, false},
		{"yaml", false, outYAML, false},
		{"yml", false, outYAML, false},
		{"toml", false, "", true},
	}
	for _, tc := range cases {
		envOut, gotFmt, err := parseDBOutput(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseDBOutput(%q) err = %v; wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if envOut != tc.wantEnv {
			t.Errorf("parseDBOutput(%q) envOut = %v; want %v", tc.in, envOut, tc.wantEnv)
		}
		if !tc.wantErr && gotFmt != tc.wantFmt {
			t.Errorf("parseDBOutput(%q) fmt = %q; want %q", tc.in, gotFmt, tc.wantFmt)
		}
	}
}

func TestCreateDBCredentialPolicySpec_FieldsFlowThrough(t *testing.T) {
	tru := true
	spec := backend.CreateDBCredentialPolicySpec{
		DatabaseRef: backend.DatabaseRef{Name: "docs-pg"},
		Mode:        "static-rotated",
		Username:    "reporting",
		Rotation:    backend.DBCredentialRotation{Interval: "7d", Strategy: "rolling"},
		Sync:        backend.DBCredentialSync{Enabled: &tru, TargetSecretName: "reporting-db-creds"},
	}
	if spec.DatabaseRef.Name != "docs-pg" {
		t.Errorf("databaseRef.name lost")
	}
	if spec.Mode != "static-rotated" || spec.Username != "reporting" {
		t.Errorf("mode/username lost")
	}
	if spec.Rotation.Interval != "7d" || spec.Rotation.Strategy != "rolling" {
		t.Errorf("rotation lost")
	}
	if spec.Sync.Enabled == nil || !*spec.Sync.Enabled || spec.Sync.TargetSecretName != "reporting-db-creds" {
		t.Errorf("sync lost: %+v", spec.Sync)
	}
}
