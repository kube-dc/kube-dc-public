package setup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func starterBindingFixture(t *testing.T) (ArtifactProof, string, string) {
	t.Helper()
	source := t.TempDir()
	cache := t.TempDir()
	var lines []string
	for _, path := range []string{"bootstrap/add-cluster.sh", "infrastructure/kube-ovn-network-public/kustomization.yaml", "infrastructure/ext-net-bridge-tag/kustomization.yaml", "platform/kustomization.yaml", "addons/metallb/kustomization.yaml", "addons/metallb-config/kustomization.yaml", "addons/metallb-config-bgp/kustomization.yaml", "scripts/install-prerequisites.sh"} {
		body := []byte("fixture source\n")
		sum := sha256.Sum256(body)
		lines = append(lines, fmt.Sprintf("644 %x %s", sum, path))
		file := filepath.Join(source, path)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	baseline := []byte(strings.Join(lines, "\n") + "\n")
	sum := sha256.Sum256(baseline)
	if err := os.WriteFile(filepath.Join(source, ".starter-manifest"), baseline, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".starter-version"), []byte(fmt.Sprintf("schemaVersion: 2\nmanifestSha256: %x\n", sum)), 0644); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: ".starter-manifest", Mode: 0644, Size: int64(len(baseline))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(baseline); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	layer := cachedBlob(t, cache, archive.Bytes())
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "layers": []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": "sha256:" + layer, "size": archive.Len()}}})
	digest := cachedBlob(t, cache, manifest)
	proof := ArtifactProof{CacheDirectory: cache, Objects: []ArtifactObject{{digest, int64(len(manifest))}, {layer, int64(archive.Len())}}}
	proof.ProofHash = artifactProofHash(proof)
	return proof, "oci://example.test/starter@sha256:" + digest, source
}

func TestStarterBindingChecksConsumedBytesAndLocalBaseline(t *testing.T) {
	for _, variant := range []string{"clean", "manifest-cache", "layer-cache", "local-source", "local-baseline"} {
		t.Run(variant, func(t *testing.T) {
			proof, ref, source := starterBindingFixture(t)
			switch variant {
			case "manifest-cache":
				_ = os.WriteFile(filepath.Join(proof.CacheDirectory, "blobs", "sha256", proof.Objects[0].SHA256), []byte(`{"layers":[]}`), 0600)
			case "layer-cache":
				_ = os.WriteFile(filepath.Join(proof.CacheDirectory, "blobs", "sha256", proof.Objects[1].SHA256), []byte("replaced tar"), 0600)
			case "local-source":
				_ = os.WriteFile(filepath.Join(source, "bootstrap", "add-cluster.sh"), []byte("replaced source"), 0644)
			case "local-baseline":
				body := []byte("forged baseline\n")
				sum := sha256.Sum256(body)
				_ = os.WriteFile(filepath.Join(source, ".starter-manifest"), body, 0644)
				_ = os.WriteFile(filepath.Join(source, ".starter-version"), []byte("schemaVersion: 2\nmanifestSha256: "+hex.EncodeToString(sum[:])+"\n"), 0644)
			}
			err := BindStarterSource(proof, ref, source)
			if variant == "clean" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("starter source drift accepted")
			}
		})
	}
}
