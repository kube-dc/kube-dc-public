package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cachedBlob(t *testing.T, cache string, body []byte) string {
	t.Helper()
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	directory := filepath.Join(cache, "blobs", "sha256")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, hash), body, 0600); err != nil {
		t.Fatal(err)
	}
	return hash
}

func artifactCacheFixture(t *testing.T) (Compiled, string, string, string) {
	t.Helper()
	spec, _ := fixture(t)
	cache := t.TempDir()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	configHash := cachedBlob(t, cache, config)
	layer := []byte("fixture image layer\n")
	layerHash := cachedBlob(t, cache, layer)
	manifest := map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + configHash, "size": len(config)}, "layers": []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + layerHash, "size": len(layer)}}}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := cachedBlob(t, cache, body)
	spec.Release.StarterRef = "oci://example.test/starter@sha256:" + manifestHash
	record := completeReleaseRecord(spec)
	cli := []byte("synthetic CLI artifact\n")
	cliHash := cachedBlob(t, cache, cli)
	cliPath := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(cliPath, cli, 0600); err != nil {
		t.Fatal(err)
	}
	record.Artifacts.CLI.SHA256 = cliHash
	record.Artifacts.BackendImage = "example.test/backend@sha256:" + manifestHash
	record.Artifacts.FrontendImage = "example.test/frontend@sha256:" + manifestHash
	record.Artifacts.AdminImage = "example.test/admin@sha256:" + manifestHash
	fileHash := cachedBlob(t, cache, []byte("fixture chart, theme, and qualification bytes\n"))
	record.Artifacts.PlatformChart.SHA256 = fileHash
	record.Artifacts.ThemeArchives = map[string]string{"kube-dc-theme.jar": fileHash, "kube-dc-theme-provider.jar": fileHash}
	record.Profiles[0].Qualification.EvidenceSHA256 = fileHash
	installerHash := cachedBlob(t, cache, []byte("#!/bin/sh\nexit 0\n"))
	archiveHash := cachedBlob(t, cache, []byte("fixture RKE2 archive\n"))
	record.Artifacts.RKE2Installer = ReleaseDownload{URL: "https://example.test/install.sh", SHA256: installerHash}
	record.Artifacts.RKE2Archives = map[string]ReleaseDownload{"amd64": {URL: "https://example.test/rke2.tar.gz", SHA256: archiveHash}}
	writeReleaseTestRecord(t, spec, record)
	compiled, err := Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	return compiled, cache, cliPath, layerHash
}

func TestArtifactCacheVerifiesReachableBytesAndRunningCLI(t *testing.T) {
	c, cache, cli, layer := artifactCacheFixture(t)
	proof, err := VerifyArtifactCache(context.Background(), c, cache, cli)
	if err != nil {
		t.Fatal(err)
	}
	if proof.ReleaseSHA256 != c.ReleaseSHA256 || proof.ProofHash == "" || len(proof.Objects) != 6 || proof.RKE2["amd64"].ArchiveSize != 21 {
		t.Fatalf("incomplete byte proof: %+v", proof)
	}
	for _, variant := range []string{"layer", "cli", "release", "missing", "symlink"} {
		t.Run(variant, func(t *testing.T) {
			c, cache, cli, layer := artifactCacheFixture(t)
			switch variant {
			case "layer":
				_ = os.WriteFile(filepath.Join(cache, "blobs", "sha256", layer), []byte("changed"), 0600)
			case "cli":
				_ = os.WriteFile(cli, []byte("replaced running artifact"), 0600)
			case "release":
				_ = os.WriteFile(c.Spec.Release.RecordFile, []byte(`{"changed":true}`), 0600)
			case "missing":
				_ = os.Remove(filepath.Join(cache, "blobs", "sha256", layer))
			case "symlink":
				_ = os.Remove(filepath.Join(cache, "blobs", "sha256", layer))
				_ = os.Symlink(cli, filepath.Join(cache, "blobs", "sha256", layer))
			}
			if _, err := VerifyArtifactCache(context.Background(), c, cache, cli); err == nil {
				t.Fatal("changed artifacts accepted")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(cache, "blobs", "sha256", layer)); err != nil {
		t.Fatal("verification changed cache bytes")
	}
}

func TestArtifactCacheRejectsDescriptorSizeMismatch(t *testing.T) {
	c, cache, cli, _ := artifactCacheFixture(t)
	_, digest, _ := strings.Cut(c.Spec.Release.StarterRef, "@sha256:")
	body, err := os.ReadFile(filepath.Join(cache, "blobs", "sha256", digest))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["config"].(map[string]any)["size"] = 999
	body, _ = json.Marshal(manifest)
	hash := cachedBlob(t, cache, body)
	var record InstallerReleaseRecord
	body, _ = os.ReadFile(c.Spec.Release.RecordFile)
	_ = json.Unmarshal(body, &record)
	spec := c.Spec
	spec.Release.StarterRef = "oci://example.test/starter@sha256:" + hash
	record.Artifacts.StarterRef = spec.Release.StarterRef
	writeReleaseTestRecord(t, spec, record)
	c, err = Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArtifactCache(context.Background(), c, cache, cli); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("descriptor mismatch accepted: %v", err)
	}
}
