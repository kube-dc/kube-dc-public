/*
Copyright Kube-DC 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package clusterinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempFleet builds a minimal fleet layout: the given platform/ piece dirs
// plus clusters/<name>/kustomization.yaml with a resources list.
func tempFleet(t *testing.T, cluster string, platformDirs ...string) string {
	t.Helper()
	fleet := t.TempDir()
	for _, d := range platformDirs {
		if err := os.MkdirAll(filepath.Join(fleet, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	clusterDir := filepath.Join(fleet, "clusters", cluster)
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	kust := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - infrastructure.yaml\n  - platform.yaml\n"
	if err := os.WriteFile(filepath.Join(clusterDir, "kustomization.yaml"), []byte(kust), 0o644); err != nil {
		t.Fatal(err)
	}
	return fleet
}

func TestWriteImageAccel_Disabled_NoOp(t *testing.T) {
	fleet := tempFleet(t, "c1", "platform/tenant-addons")
	if err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: false}, nil); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fleet, "clusters", "c1", "tenant-addons.yaml")); !os.IsNotExist(err) {
		t.Fatalf("disabled spec must write nothing")
	}
}

func TestWriteImageAccel_WiresPresentPieces_SkipsAbsent(t *testing.T) {
	// registry-depot dir deliberately absent (older starter) — must be
	// skipped without failing, and without requiring sops in the test env.
	fleet := tempFleet(t, "c1", "platform/tenant-addons", "platform/cdi-os-mirror")
	var log strings.Builder
	if err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: true, S3: true}, &log); err != nil {
		t.Fatalf("write: %v", err)
	}
	clusterDir := filepath.Join(fleet, "clusters", "c1")

	for _, f := range []string{"tenant-addons.yaml", "cdi-os-mirror.yaml"} {
		b, err := os.ReadFile(filepath.Join(clusterDir, f))
		if err != nil {
			t.Fatalf("%s not written: %v", f, err)
		}
		if !strings.Contains(string(b), "kind: Kustomization") {
			t.Fatalf("%s is not a Flux Kustomization", f)
		}
	}
	if _, err := os.Stat(filepath.Join(clusterDir, "registry-depot.yaml")); !os.IsNotExist(err) {
		t.Fatalf("absent platform dir must skip its per-cluster file")
	}

	kust, err := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  - tenant-addons.yaml", "  - cdi-os-mirror.yaml"} {
		if !strings.Contains(string(kust), want) {
			t.Fatalf("kustomization missing %q:\n%s", want, kust)
		}
	}
	if strings.Contains(string(kust), "registry-depot.yaml") {
		t.Fatalf("kustomization must not reference the skipped piece:\n%s", kust)
	}
	if !strings.Contains(log.String(), "registry-depot skipped") {
		t.Fatalf("skip must be logged, got: %s", log.String())
	}

	// Idempotency: second run must not duplicate kustomization entries.
	if err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: true, S3: true}, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}
	kust2, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	if strings.Count(string(kust2), "  - tenant-addons.yaml") != 1 {
		t.Fatalf("kustomization entry duplicated:\n%s", kust2)
	}
}

func TestWriteImageAccel_RegistryDepotSecretRequiresSops(t *testing.T) {
	// With the registry-depot dir present and no pre-existing secret, the
	// writer mints + sops-encrypts the push credential. Without a usable
	// sops (empty PATH), it must FAIL and must not leave plaintext behind.
	fleet := tempFleet(t, "c1", "platform/registry-depot")
	t.Setenv("PATH", t.TempDir()) // no sops reachable
	err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: true, S3: true}, nil)
	if err == nil {
		t.Fatalf("expected sops failure")
	}
	if _, statErr := os.Stat(filepath.Join(fleet, "platform", "registry-depot", "secret.enc.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("plaintext secret must not survive a failed encrypt")
	}
}

func TestWriteImageAccel_ExistingSecretPreserved(t *testing.T) {
	fleet := tempFleet(t, "c1", "platform/registry-depot")
	secret := filepath.Join(fleet, "platform", "registry-depot", "secret.enc.yaml")
	if err := os.WriteFile(secret, []byte("stringData:\n  htpasswd: ENC[AES256_GCM,data:x]\nsops:\n  age: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // sops absent — must not be needed
	if err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: true, S3: true}, nil); err != nil {
		t.Fatalf("existing secret must be preserved without sops: %v", err)
	}
	b, _ := os.ReadFile(secret)
	if !strings.Contains(string(b), "ENC[AES256_GCM,data:x]") {
		t.Fatalf("existing secret was rewritten")
	}
}

func TestWriteImageAccel_NoObjectStorage_SkipsS3Pieces(t *testing.T) {
	fleet := tempFleet(t, "c1", "platform/tenant-addons", "platform/cdi-os-mirror", "platform/registry-depot")
	t.Setenv("PATH", t.TempDir()) // sops must not even be needed
	var log strings.Builder
	if err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: true, S3: false}, &log); err != nil {
		t.Fatalf("write: %v", err)
	}
	clusterDir := filepath.Join(fleet, "clusters", "c1")
	if _, err := os.Stat(filepath.Join(clusterDir, "tenant-addons.yaml")); err != nil {
		t.Fatalf("tenant-addons must not depend on object storage: %v", err)
	}
	for _, f := range []string{"cdi-os-mirror.yaml", "registry-depot.yaml"} {
		if _, err := os.Stat(filepath.Join(clusterDir, f)); !os.IsNotExist(err) {
			t.Fatalf("%s must be skipped without object storage", f)
		}
	}
}
