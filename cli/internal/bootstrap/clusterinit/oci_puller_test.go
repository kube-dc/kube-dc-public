package clusterinit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeStarterRepository struct {
	manifest []byte
	layer    []byte
	badLayer bool
}

func (r fakeStarterRepository) FetchReference(_ context.Context, _ string) (ocispec.Descriptor, io.ReadCloser, error) {
	return descriptor(r.manifest, ocispec.MediaTypeImageManifest), io.NopCloser(bytes.NewReader(r.manifest)), nil
}

func (r fakeStarterRepository) Fetch(_ context.Context, _ ocispec.Descriptor) (io.ReadCloser, error) {
	data := r.layer
	if r.badLayer {
		data = []byte("modified")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func descriptor(data []byte, mediaType string) ocispec.Descriptor {
	return ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
}

func tarGzip(t *testing.T, name string, kind byte, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	h := &tar.Header{Name: name, Typeflag: kind, Mode: 0755, Size: int64(len(body))}
	if kind != tar.TypeReg {
		h.Size = 0
	}
	if err := tw.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if kind == tar.TypeReg {
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPullFluxStarterVerifiesBeforeExtraction(t *testing.T) {
	layer := tarGzip(t, "scripts/install-prerequisites.sh", tar.TypeReg, "#!/bin/sh\nexit 0\n")
	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    ocispec.Descriptor{MediaType: fluxConfigType},
		Layers:    []ocispec.Descriptor{descriptor(layer, fluxContentType)},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	repo := fakeStarterRepository{manifest: manifestBytes, layer: layer}
	dir := t.TempDir()
	if err := pullFluxStarter(context.Background(), repo, descriptor(manifestBytes, "").Digest.String(), dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scripts", "install-prerequisites.sh")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("extracted script mode: %v, %v", info, err)
	}
	badDir := t.TempDir()
	repo.badLayer = true
	if err := pullFluxStarter(context.Background(), repo, descriptor(manifestBytes, "").Digest.String(), badDir); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("expected layer rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(badDir, "scripts")); !os.IsNotExist(err) {
		t.Fatalf("unverified blob created files: %v", err)
	}
}

func TestExtractStarterTarRejectsUnsafeEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind byte
	}{
		{"../escape", tar.TypeReg},
		{"/absolute", tar.TypeReg},
		{"dir/../../escape", tar.TypeReg},
		{"C:/escape", tar.TypeReg},
		{"dir\\escape", tar.TypeReg},
		{"link", tar.TypeSymlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := tarGzip(t, tc.name, tc.kind, "payload")
			if err := extractStarterTar(context.Background(), bytes.NewReader(archive), dir); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestExtractStarterTarAcceptsDirectoryHeaders(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "./scripts/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\n")
	if err := tw.WriteHeader(&tar.Header{Name: "./scripts/install-prerequisites.sh", Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := extractStarterTar(context.Background(), bytes.NewReader(buf.Bytes()), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "scripts", "install-prerequisites.sh")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractStarterTarRejectsBadGzipChecksum(t *testing.T) {
	archive := tarGzip(t, "scripts/install-prerequisites.sh", tar.TypeReg, "content")
	archive[len(archive)-8] ^= 0xff
	if err := extractStarterTar(context.Background(), bytes.NewReader(archive), t.TempDir()); err == nil || !strings.Contains(err.Error(), "gzip checksum") {
		t.Fatalf("expected checksum error, got %v", err)
	}
}

func TestExtractStarterTarBoundsTrailingData(t *testing.T) {
	var tarBytes bytes.Buffer
	tw := tar.NewWriter(&tarBytes)
	if err := tw.WriteHeader(&tar.Header{Name: "ok", Typeflag: tar.TypeReg, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(tarBytes.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := gz.Write(make([]byte, maxStarterTail+1)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractStarterTar(context.Background(), bytes.NewReader(compressed.Bytes()), t.TempDir()); err == nil || !strings.Contains(err.Error(), "excess data") {
		t.Fatalf("expected tail limit error, got %v", err)
	}
}

func TestStarterStreamReaderBoundsMetadataAndHonorsCancellation(t *testing.T) {
	r := &starterStreamReader{ctx: context.Background(), source: bytes.NewReader([]byte("metadata")), limit: 3}
	if _, err := r.Read(make([]byte, 16)); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected decompressed stream limit, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = &starterStreamReader{ctx: ctx, source: bytes.NewReader([]byte("x")), limit: 3}
	if _, err := r.Read(make([]byte, 1)); err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestOCIPullerRequiresPinnedRef(t *testing.T) {
	for _, ref := range []string{"oci://ghcr.io/kube-dc/fleet-starter:latest", "oci://ghcr.io/kube-dc/fleet-starter@sha256:bad", "http://example.test/starter"} {
		if err := (OCIPuller{}).PullArtifact(context.Background(), ref, t.TempDir()); err == nil {
			t.Fatalf("accepted %q", ref)
		}
	}
}

func TestParseStarterOCIRefDiscardsAdvisoryTag(t *testing.T) {
	wantDigest := "sha256:" + strings.Repeat("a", 64)
	repository, gotDigest, err := parseStarterOCIRef("oci://ghcr.io/kube-dc/fleet-starter:v1.2.3@" + wantDigest)
	if err != nil || repository != "ghcr.io/kube-dc/fleet-starter" || gotDigest != wantDigest {
		t.Fatalf("repository=%q digest=%q err=%v", repository, gotDigest, err)
	}
}

func TestValidateStarterOCIRefMatchesPuller(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, ref := range []string{
		"garbage@sha256:" + digest,
		"oci://ghcr.io/x@sha256:" + strings.Repeat("A", 64),
		"oci://ghcr.io/x?query@sha256:" + digest,
		"oci://ghcr.io/x:tag@evil@sha256:" + digest,
		"oci://ghcr.io/x:@sha256:" + digest,
		"oci://ghcr.io/x:old:new@sha256:" + digest,
		"oci://ghcr.io/x:tag@sha256:bad",
	} {
		if err := ValidateStarterOCIRef(ref); err == nil {
			t.Errorf("accepted invalid ref %q", ref)
		}
	}
	ref := "oci://127.0.0.1:12345/kube-dc/fleet-starter:v1@sha256:" + digest
	if err := ValidateStarterOCIRef(ref); err != nil {
		t.Fatal(err)
	}
	repo, gotDigest, err := starterOCIRepository(ref)
	if err != nil || !repo.PlainHTTP || gotDigest != "sha256:"+digest {
		t.Fatalf("loopback repo=%v digest=%q err=%v", repo, gotDigest, err)
	}
	repo, _, err = starterOCIRepository("oci://ghcr.io/kube-dc/fleet-starter@sha256:" + digest)
	if err != nil || repo.PlainHTTP {
		t.Fatalf("public repo=%v err=%v", repo, err)
	}
}

func TestLoopbackRegistryHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"localhost:5000", true},
		{"127.0.0.1:5000", true},
		{"[::1]:5000", true},
		{"localhost.example.com", false},
		{"127.attacker.example", false},
		{"127.0.0.1.attacker.example:5000", false},
		{"example.com", false},
	} {
		if got := IsLoopbackRegistryHost(tc.host); got != tc.want {
			t.Errorf("host %q: loopback=%v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestOCIPullerPublicArtifactSmoke(t *testing.T) {
	ref := os.Getenv("KDC_STARTER_OCI_SMOKE_REF")
	if ref == "" {
		t.Skip("set KDC_STARTER_OCI_SMOKE_REF to a published digest-pinned starter for the registry smoke")
	}
	dir := t.TempDir()
	if err := (OCIPuller{}).PullArtifact(context.Background(), ref, dir); err != nil {
		t.Fatal(err)
	}
	if !StarterShapePresent(dir) {
		t.Fatal("published artifact lacks starter shape")
	}
	if err := verifyStarterManifest(dir); err != nil {
		t.Fatal(err)
	}
}
