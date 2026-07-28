package clusterinit

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSops plants an executable `sops` in PATH that simulates
// `sops --encrypt --in-place <file>`: it rewrites the file to a
// SOPS-metadata-carrying ciphertext shape WITHOUT the original content, so the
// writer's "output must look encrypted and must not leak the key" checks are
// exercised for real. Success-path tests must not require age keys or the real
// binary in CI.
func fakeSops(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
# fake sops: --encrypt --in-place <file>
f="$3"
[ -n "$f" ] || exit 2
printf 'data: ENC[AES256_GCM,data:fake]\nsops:\n    age: []\n    lastmodified: "2026-01-01T00:00:00Z"\n' > "$f"
`
	if err := os.WriteFile(filepath.Join(dir, "sops"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func wildcardMaterial(t *testing.T) *WildcardTLSMaterial {
	t.Helper()
	now := time.Now()
	cert, key := writePair(t, []string{"*.example.com", "example.com"}, now.Add(-time.Hour), now.Add(240*time.Hour))
	m, err := LoadWildcardTLS(cert, key, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func tlsFleet(t *testing.T) string {
	t.Helper()
	fleet := tempFleet(t, "c1")
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	if err := os.WriteFile(filepath.Join(clusterDir, "cluster-config.env"),
		[]byte("DOMAIN=example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "registry-depot.yaml"),
		[]byte(registryDepotYAML("c1")), 0o644); err != nil {
		t.Fatal(err)
	}
	return fleet
}

func TestWriteWildcardTLS_NilMaterialIsNoOp(t *testing.T) {
	fleet := tlsFleet(t)
	if err := WriteWildcardTLS(fleet, "c1", "example.com", nil, nil); err != nil {
		t.Fatalf("acme mode must be a no-op: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fleet, "clusters", "c1", wildcardSecretsFileName)); !os.IsNotExist(err) {
		t.Fatal("acme mode must not write the secret file")
	}
}

func TestWriteWildcardTLS_FullShape(t *testing.T) {
	fleet := tlsFleet(t)
	m := wildcardMaterial(t)
	fakeSops(t)

	if err := WriteWildcardTLS(fleet, "c1", "example.com", m, nil); err != nil {
		t.Fatal(err)
	}
	clusterDir := filepath.Join(fleet, "clusters", "c1")

	// The committed artifact is ciphertext, never the material.
	enc, err := os.ReadFile(filepath.Join(clusterDir, wildcardSecretsFileName))
	if err != nil {
		t.Fatal(err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(m.KeyPEM)
	if !strings.Contains(string(enc), "sops") || strings.Contains(string(enc), keyB64) {
		t.Fatalf("committed secret is not encrypted:\n%.200s", enc)
	}
	// No plaintext temp survives.
	entries, _ := os.ReadDir(clusterDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}

	kust, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if !strings.Contains(string(kust), "  - "+wildcardSecretsFileName) {
		t.Fatalf("secret file not in kustomization resources:\n%s", kust)
	}

	// platform.yaml gains the 12 platform suppressions; registry-depot its 1.
	plat, _ := os.ReadFile(filepath.Join(clusterDir, "platform.yaml"))
	if !strings.Contains(string(plat), byoWildcardTLSMarker) ||
		!strings.Contains(string(plat), "name: wildcard-tls") ||
		!strings.Contains(string(plat), "$patch: delete") {
		t.Fatalf("platform.yaml missing suppression patches:\n%s", plat)
	}
	if got := strings.Count(string(plat), "$patch: delete"); got != 12 {
		t.Fatalf("platform.yaml must suppress exactly the 12 platform Certificates, got %d", got)
	}
	depot, _ := os.ReadFile(filepath.Join(clusterDir, "registry-depot.yaml"))
	if !strings.Contains(string(depot), "name: registry-tls") ||
		strings.Count(string(depot), "$patch: delete") != 1 {
		t.Fatalf("registry-depot.yaml missing its suppression patch:\n%s", depot)
	}

	env, _ := os.ReadFile(filepath.Join(clusterDir, "cluster-config.env"))
	if !strings.Contains(string(env), "TLS_MODE="+TLSModeBYOWildcard) {
		t.Fatalf("TLS_MODE not recorded:\n%s", env)
	}
}

// Absent optional layers must be skipped (their Certificates are absent too),
// while a missing platform.yaml is a hard error.
func TestWriteWildcardTLS_OptionalLayersSkipped(t *testing.T) {
	fleet := tlsFleet(t)
	if err := os.Remove(filepath.Join(fleet, "clusters", "c1", "registry-depot.yaml")); err != nil {
		t.Fatal(err)
	}
	m := wildcardMaterial(t)
	fakeSops(t)
	var log strings.Builder
	if err := WriteWildcardTLS(fleet, "c1", "example.com", m, &log); err != nil {
		t.Fatalf("absent optional layer must be skipped, not fatal: %v", err)
	}
	if !strings.Contains(log.String(), "registry-depot.yaml absent") {
		t.Fatalf("skip must be REPORTED so the operator knows to re-run later:\n%s", log.String())
	}

	if err := os.Remove(filepath.Join(fleet, "clusters", "c1", "platform.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := WriteWildcardTLS(fleet, "c1", "example.com", m, nil); err == nil {
		t.Fatal("a missing platform.yaml must be a hard error — the 12 platform Certificates would go unsuppressed")
	}
}

// Re-running (certificate rotation, resumed init) must not duplicate patches
// or resource entries, and must rewrite the secret material.
func TestWriteWildcardTLS_Idempotent(t *testing.T) {
	fleet := tlsFleet(t)
	m := wildcardMaterial(t)
	fakeSops(t)
	for i := 0; i < 2; i++ {
		if err := WriteWildcardTLS(fleet, "c1", "example.com", m, nil); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	plat, _ := os.ReadFile(filepath.Join(clusterDir, "platform.yaml"))
	if got := strings.Count(string(plat), byoWildcardTLSMarker); got != 1 {
		t.Fatalf("marker duplicated %d times in platform.yaml", got)
	}
	kust, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if got := strings.Count(string(kust), wildcardSecretsFileName); got != 1 {
		t.Fatalf("kustomization resource duplicated %d times", got)
	}
}

// A failed encrypt must leave NO plaintext and NO final file.
func TestWriteWildcardTLS_NoSopsLeavesNothing(t *testing.T) {
	fleet := tlsFleet(t)
	m := wildcardMaterial(t)
	t.Setenv("PATH", t.TempDir()) // sops absent
	if err := WriteWildcardTLS(fleet, "c1", "example.com", m, nil); err == nil {
		t.Fatal("expected a sops failure")
	}
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	entries, _ := os.ReadDir(clusterDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "wildcard-tls") {
			t.Fatalf("no wildcard artifact may survive a failed encrypt: %s", e.Name())
		}
	}
}

// A hand-edited patches: block (no kube-dc marker) must abort with guidance,
// not silently append into a structure the operator owns.
func TestWriteWildcardTLS_HandEditedPatchesRefused(t *testing.T) {
	fleet := tlsFleet(t)
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	plat, _ := os.ReadFile(filepath.Join(clusterDir, "platform.yaml"))
	edited := string(plat) + "  patches:\n    - target:\n        kind: HelmRelease\n        name: hand-edit\n      patch: |\n        {}\n"
	if err := os.WriteFile(filepath.Join(clusterDir, "platform.yaml"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	m := wildcardMaterial(t)
	fakeSops(t)
	err := WriteWildcardTLS(fleet, "c1", "example.com", m, nil)
	if err == nil || !strings.Contains(err.Error(), "hand-edited") {
		t.Fatalf("hand-edited patches block must be refused with guidance, got: %v", err)
	}
}

// A validation failure on the LAST patch target must leave the overlay
// UNTOUCHED — no secret written, no platform patch, no kustomization entry.
// Mutating first and validating later would strand a mixed overlay (platform
// suppressed but registry ACME still armed) behind an init that cannot be
// re-run (codex pass-4, MEDIUM).
func TestWriteWildcardTLS_LateTargetFailureIsAllOrNothing(t *testing.T) {
	fleet := tlsFleet(t)
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	depot, _ := os.ReadFile(filepath.Join(clusterDir, "registry-depot.yaml"))
	edited := string(depot) + "  patches:\n    - target:\n        kind: HelmRelease\n        name: hand-edit\n      patch: |\n        {}\n"
	if err := os.WriteFile(filepath.Join(clusterDir, "registry-depot.yaml"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	platBefore, _ := os.ReadFile(filepath.Join(clusterDir, "platform.yaml"))
	kustBefore, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))

	m := wildcardMaterial(t)
	fakeSops(t)
	if err := WriteWildcardTLS(fleet, "c1", "example.com", m, nil); err == nil {
		t.Fatal("hand-edited registry-depot.yaml must fail the whole step")
	}
	if _, err := os.Stat(filepath.Join(clusterDir, wildcardSecretsFileName)); !os.IsNotExist(err) {
		t.Fatal("secret must not be written when a later patch target fails validation")
	}
	platAfter, _ := os.ReadFile(filepath.Join(clusterDir, "platform.yaml"))
	kustAfter, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if string(platBefore) != string(platAfter) || string(kustBefore) != string(kustAfter) {
		t.Fatal("no file may be mutated when a later patch target fails validation")
	}
}

// The fake sops used above never emits the plaintext, so the leak check is
// only proven by a sops that MISBEHAVES: metadata present but values kept.
func TestSopsEncryptToFile_RejectsLeakyOutput(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
f="$3"
printf 'data: SECRETVALUE\nsops:\n    age: []\n' > "$f"
`
	if err := os.WriteFile(filepath.Join(dir, "sops"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	fleet := t.TempDir()
	final := filepath.Join(fleet, "leaky.enc.yaml")
	err := sopsEncryptToFile(fleet, final, "data: SECRETVALUE\n", []string{"SECRETVALUE"})
	if err == nil {
		t.Fatal("output containing the secret in cleartext must be refused")
	}
	if _, statErr := os.Stat(final); !os.IsNotExist(statErr) {
		t.Fatal("leaky output must not reach the final path")
	}
}
