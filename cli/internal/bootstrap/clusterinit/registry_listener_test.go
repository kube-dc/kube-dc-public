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

	"gopkg.in/yaml.v3"
)

func TestWriteImageAccel_RegistryWiresGatewayListener(t *testing.T) {
	fleet := tempFleet(t, "c1", "platform/registry-depot")
	secret := filepath.Join(fleet, "platform", "registry-depot", "secret.enc.yaml")
	if err := os.WriteFile(secret, []byte("stringData:\n  htpasswd: ENC[AES256_GCM,data:x]\nsops:\n  age: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // existing encrypted secret needs no sops

	if err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: true, S3: true}, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: true, S3: true}, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}

	platform, err := os.ReadFile(filepath.Join(fleet, "clusters", "c1", "platform.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(platform)
	var parsed map[string]any
	if err := yaml.Unmarshal(platform, &parsed); err != nil {
		t.Fatalf("generated platform.yaml is invalid: %v\n%s", err, got)
	}
	for _, want := range []string{
		registryListenerMarker,
		"name: https-registry",
		"${REGISTRY_HOSTNAME:=registry.${DOMAIN}}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("platform listener patch missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, registryListenerMarker) != 1 {
		t.Fatalf("listener patch duplicated:\n%s", got)
	}

	kust, err := os.ReadFile(filepath.Join(fleet, "clusters", "c1", "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(kust), "  - registry-depot.yaml") != 1 {
		t.Fatalf("registry resource duplicated:\n%s", kust)
	}
}

func TestPatchPlatformRegistryListener_ComposesBeforeNextSpecKey(t *testing.T) {
	input := strings.Split(genPlatformYAML+"  patches:\n    - patch: |\n        existing\n  postBuild:\n    substitute: {}\n", "\n")
	got, changed, err := patchPlatformRegistryListener(input)
	if err != nil || !changed {
		t.Fatalf("patch: changed=%v err=%v", changed, err)
	}
	joined := strings.Join(got, "\n")
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(joined), &parsed); err != nil {
		t.Fatalf("composed platform.yaml is invalid: %v\n%s", err, joined)
	}
	if strings.Index(joined, registryListenerMarker) > strings.Index(joined, "  postBuild:") {
		t.Fatalf("listener must remain inside patches list, not after the next spec key:\n%s", joined)
	}
}

func TestPatchPlatformRegistryListener_NoAddonMeansNoPatch(t *testing.T) {
	fleet := tempFleet(t, "c1", "platform/tenant-addons")
	if err := WriteImageAccel(fleet, "c1", ImageAccelSpec{Enabled: true, S3: false}, nil); err != nil {
		t.Fatal(err)
	}
	platform, err := os.ReadFile(filepath.Join(fleet, "clusters", "c1", "platform.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(platform), registryListenerMarker) {
		t.Fatal("cluster without registry-depot must not carry its Gateway listener")
	}
}
