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

// Image-acceleration scaffolding — the on-cluster image path that keeps VM
// and container pulls off the WAN. Three pieces, all default-on for new
// clusters (a real install without them re-pulls every containerdisk from
// the internet and leaves tenant OS-image imports at upstream mirror speed):
//
//   - tenant-addons    Sveltos ClusterProfiles (Cilium CNI, CoreDNS, …) for
//     managed/nested tenant clusters. Without the wiring a
//     tenant cluster gets NO CNI: worker nodes stay
//     NotReady, kubelet-csr-approver never schedules and
//     MachineDeployments wedge at ScalingUp 0/1.
//   - cdi-os-mirror    S3 (RGW) mirror of tenant OS images + weekly refresh
//     CronJob, so CDI HTTP-source imports stay on-cluster.
//   - registry-depot   zot: an S3-backed container registry (anonymous read,
//     authenticated push) that the RKE2 embedded registry
//     mirror (spegel, default-on in bootstrap install)
//     P2P-shares across nodes.
//
// The shared manifests ship in the fleet-starter (platform/tenant-addons,
// platform/cdi-os-mirror, platform/registry-depot); this writer adds the
// per-cluster Flux Kustomizations and generates the one secret the starter
// cannot carry: registry-depot's push credential (SOPS blobs are excluded
// from the starter, so a fresh fleet must mint its own).
//
// Graceful with older starters: a piece whose platform/ directory is absent
// is skipped with a warning instead of failing the whole scaffold.
package clusterinit

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// ImageAccelSpec selects the image-acceleration pieces. Zero value = off
// (legacy/API callers); the --image-acceleration flag defaults it to true.
type ImageAccelSpec struct {
	Enabled bool
	// S3 reports whether the install scaffolds object storage; the
	// S3-backed pieces (cdi-os-mirror, registry-depot) depend on
	// infra-object-storage and are skipped without it.
	S3 bool
}

// imageAccelPieces are the per-piece shared-tree directories (relative to
// the fleet root) and the per-cluster Flux Kustomization each one gets.
var imageAccelPieces = []struct {
	name       string
	platform   string // shared tree that must exist in the starter
	yamlFn     func(cluster string) string
	needSecret bool // registry-depot: mint the push credential
	needsS3    bool // depends on infra-object-storage (skipped when absent)
}{
	{"tenant-addons", "platform/tenant-addons", tenantAddonsYAML, false, false},
	{"cdi-os-mirror", "platform/cdi-os-mirror", cdiOSMirrorYAML, false, true},
	{"registry-depot", "platform/registry-depot", registryDepotYAML, true, true},
}

// ImageAccel derives the writer spec from the validated options.
func (o *InitOptions) ImageAccel() ImageAccelSpec {
	return ImageAccelSpec{
		Enabled: o.ImageAcceleration,
		S3:      scaffoldsObjectStorage(o.RookMode),
	}
}

// WriteImageAccel scaffolds the per-cluster wiring for the image
// acceleration stack. No-op when disabled.
func WriteImageAccel(fleetRepo, clusterName string, spec ImageAccelSpec, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if !spec.Enabled {
		return nil
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)

	wired := []string{}
	for _, p := range imageAccelPieces {
		if p.needsS3 && !spec.S3 {
			fmt.Fprintf(out, "[scaffold] image-accel: %s skipped (no object-storage mode; it depends on infra-object-storage)\n", p.name)
			continue
		}
		fi, err := os.Stat(filepath.Join(fleetRepo, p.platform))
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("image-accel: stat %s: %w", p.platform, err)
			}
			fmt.Fprintf(out, "[scaffold] image-accel: %s skipped (%s not in this starter)\n", p.name, p.platform)
			continue
		}
		if !fi.IsDir() {
			return fmt.Errorf("image-accel: %s exists but is not a directory", p.platform)
		}
		if p.needSecret {
			if err := ensureRegistryDepotSecret(fleetRepo, out); err != nil {
				return err
			}
		}
		file := filepath.Join(clusterDir, p.name+".yaml")
		if err := os.WriteFile(file, []byte(p.yamlFn(clusterName)), 0o644); err != nil {
			return fmt.Errorf("image-accel: write %s: %w", file, err)
		}
		if err := patchFileLines(filepath.Join(clusterDir, "kustomization.yaml"),
			patchKustomizationResource(p.name+".yaml")); err != nil {
			return fmt.Errorf("image-accel: patch kustomization.yaml for %s: %w", p.name, err)
		}
		wired = append(wired, p.name)
	}
	if len(wired) > 0 {
		fmt.Fprintf(out, "[scaffold] image-acceleration wired (%s; spegel is enabled by bootstrap install)\n",
			joinComma(wired))
	}
	return nil
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// ensureRegistryDepotSecret mints platform/registry-depot/secret.enc.yaml:
// a random ci-pusher credential, bcrypt htpasswd (the only algo zot
// supports), SOPS-encrypted in place with the fleet's own .sops.yaml rules.
// Skips silently when the file already exists (existing-repo installs).
// The plaintext never persists: on any encrypt failure the file is removed
// and the scaffold fails.
func ensureRegistryDepotSecret(fleetRepo string, out io.Writer) error {
	secretPath := filepath.Join(fleetRepo, "platform", "registry-depot", "secret.enc.yaml")
	if fi, err := os.Lstat(secretPath); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("image-accel: %s exists but is not a regular file", secretPath)
		}
		b, err := os.ReadFile(secretPath)
		if err != nil {
			return fmt.Errorf("image-accel: read existing registry secret: %w", err)
		}
		// A leftover from an interrupted earlier run must never be trusted:
		// only accept the file when it carries SOPS metadata.
		if !strings.Contains(string(b), "sops") || !strings.Contains(string(b), "ENC[") && !strings.Contains(string(b), "encrypted_regex") && !strings.Contains(string(b), "age") {
			return fmt.Errorf("image-accel: %s exists but does not look SOPS-encrypted — refusing to keep or overwrite it; inspect and remove it manually", secretPath)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("image-accel: stat %s: %w", secretPath, err)
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("image-accel: generate registry credential: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return fmt.Errorf("image-accel: bcrypt registry credential: %w", err)
	}

	plain := fmt.Sprintf(`# Push credential for the registry depot (zot). htpasswd = bcrypt (the only
# algo zot supports). REGISTRY_PUSH_USER/_PASSWORD are consumed by CI push
# steps. Generated by kube-dc bootstrap init; SOPS-encrypted with this
# fleet's age recipients.
apiVersion: v1
kind: Secret
metadata:
  name: registry-depot-auth
  namespace: kube-dc
  labels:
    kube-dc.com/system: "true"
    kube-dc.com/system-component: registry-depot
type: Opaque
stringData:
  htpasswd: "ci-pusher:%s"
  REGISTRY_PUSH_USER: "ci-pusher"
  REGISTRY_PUSH_PASSWORD: "%s"
`, string(hash), password)

	// Write plaintext to a temp file (same dir, 0600), encrypt in place,
	// VERIFY the result is actually encrypted, then atomically rename to the
	// final tracked path — a crash mid-way leaves only the temp file, which
	// the .enc.yaml SOPS creation rule never matches and git never sees as
	// the real secret. The temp is removed on every failure path.
	// NOTE: the temp name must still match .sops.yaml's path_regex
	// (\.enc\.yaml$) or sops refuses to encrypt it.
	tmp := secretPath + ".tmp.enc.yaml"
	if err := os.WriteFile(tmp, []byte(plain), 0o600); err != nil {
		return fmt.Errorf("image-accel: write registry secret: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmp) }
	cmd := exec.Command("sops", "--encrypt", "--in-place", tmp)
	cmd.Dir = fleetRepo
	if outB, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return fmt.Errorf("image-accel: sops-encrypt registry secret: %w (%s)", err, string(outB))
	}
	enc, err := os.ReadFile(tmp)
	if err != nil {
		cleanup()
		return fmt.Errorf("image-accel: read encrypted secret: %w", err)
	}
	if !strings.Contains(string(enc), "sops") || strings.Contains(string(enc), password) {
		cleanup()
		return fmt.Errorf("image-accel: sops output does not look encrypted — refusing to keep it")
	}
	if err := os.Rename(tmp, secretPath); err != nil {
		cleanup()
		return fmt.Errorf("image-accel: finalize registry secret: %w", err)
	}
	fmt.Fprintf(out, "[scaffold] registry-depot push credential minted (ci-pusher, SOPS-encrypted)\n")
	return nil
}

// patchKustomizationResource returns a patchFileLines patch that appends
// `  - <resource>` after the last entry of the resources: list (idempotent).
func patchKustomizationResource(resource string) func([]string) ([]string, bool, error) {
	entry := "  - " + resource
	return func(lines []string) ([]string, bool, error) {
		for _, l := range lines {
			if strings.TrimSpace(l) == strings.TrimSpace(entry) {
				return lines, false, nil // already wired
			}
		}
		resIdx := -1
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if t == "resources:" || strings.HasPrefix(t, "resources: #") {
				resIdx = i
				break
			}
		}
		if resIdx == -1 {
			return nil, false, fmt.Errorf("kustomization has no resources: list")
		}
		// insertion point: after the last consecutive "  - …" entry
		insert := resIdx + 1
		for insert < len(lines) && strings.HasPrefix(lines[insert], "  - ") {
			insert++
		}
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:insert]...)
		out = append(out, entry)
		out = append(out, lines[insert:]...)
		return out, true, nil
	}
}

func tenantAddonsYAML(cluster string) string {
	return `# Sveltos-delivered addons (Cilium CNI, CoreDNS, …) for managed/nested
# tenant clusters. The ClusterProfiles in platform/tenant-addons select CAPI
# clusters labelled kube-dc.com/tenant-addons=enabled (set by k8-manager on
# every cluster it creates). Without this Kustomization a tenant cluster gets
# NO CNI: nodes NotReady -> csr-approver Pending -> MachineDeployment stuck.
# Scaffolded by kube-dc bootstrap init (image-acceleration stack).
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: tenant-addons
  namespace: flux-system
spec:
  dependsOn:
    - name: platform
  interval: 10m
  retryInterval: 2m
  timeout: 5m
  path: ./platform/tenant-addons
  prune: true
  sourceRef:
    kind: GitRepository
    name: flux-system
`
}

func cdiOSMirrorYAML(cluster string) string {
	return `# CDI OS-image mirror — S3 (RGW) copy of tenant OS images + weekly refresh
# CronJob, so CDI HTTP-source imports stay on-cluster instead of pulling from
# upstream mirrors over the WAN. Pair with the kube-dc HelmRelease value
# osImages.mirrorBaseURL (https://<S3_HOSTNAME>/cdi-os-images) so the console
# hands tenants mirror-backed URLs.
# Scaffolded by kube-dc bootstrap init (image-acceleration stack).
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: cdi-os-mirror
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-object-storage
    - name: platform
  interval: 10m
  retryInterval: 2m
  timeout: 10m
  path: ./platform/cdi-os-mirror
  prune: false
  force: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
      - kind: Secret
        name: cluster-secrets
        optional: true
`
}

func registryDepotYAML(cluster string) string {
	return `# zot container-registry depot — S3-backed (RGW) local registry (anonymous
# read, authenticated push) that the RKE2 embedded registry mirror (spegel,
# enabled by kube-dc bootstrap install) P2P-shares across nodes, keeping
# containerdisk/image pulls off the WAN.
# Scaffolded by kube-dc bootstrap init (image-acceleration stack).
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: registry-depot
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-object-storage
    - name: platform
  interval: 10m
  retryInterval: 2m
  timeout: 10m
  path: ./platform/registry-depot
  prune: false
  force: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  decryption:
    provider: sops
    secretRef:
      name: sops-age
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
      - kind: Secret
        name: cluster-secrets
        optional: true
`
}
