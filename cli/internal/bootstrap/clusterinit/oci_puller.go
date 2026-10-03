package clusterinit

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const (
	fluxContentType  = "application/vnd.cncf.flux.content.v1.tar+gzip"
	fluxConfigType   = "application/vnd.cncf.flux.config.v1+json"
	maxStarterBlob   = 256 << 20
	maxStarterFile   = 64 << 20
	maxStarterTotal  = 1 << 30
	maxStarterFiles  = 20000
	maxStarterTail   = 1 << 20
	maxStarterStream = maxStarterTotal + 32<<20
)

var digestOCIRef = regexp.MustCompile(`^oci://([^/]+)/(.+)@sha256:([a-f0-9]{64})$`)
var starterOCITag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// OCIPuller fetches a digest-pinned public Flux artifact without a Flux binary,
// Fleet checkout, or kubeconfig. EnsureStarter supplies the scratch directory
// and verifies the extracted starter manifest before promotion.
type OCIPuller struct{}

func (OCIPuller) PullArtifact(ctx context.Context, ref, dir string) error {
	repo, digest, err := starterOCIRepository(ref)
	if err != nil {
		return err
	}
	repo.Client = auth.DefaultClient
	return pullFluxStarter(ctx, repo, digest, dir)
}

// ValidateStarterOCIRef uses the same local checks as OCIPuller. It does not
// contact a registry and can be used before a plan is shown to the operator.
func ValidateStarterOCIRef(ref string) error {
	_, _, err := starterOCIRepository(ref)
	return err
}

func starterOCIRepository(ref string) (*remote.Repository, string, error) {
	repository, digest, err := parseStarterOCIRef(ref)
	if err != nil {
		return nil, "", err
	}
	repo, err := remote.NewRepository(repository)
	if err != nil {
		return nil, "", fmt.Errorf("starter OCI repository: %w", err)
	}
	host, _, _ := strings.Cut(repository, "/")
	repo.PlainHTTP = IsLoopbackRegistryHost(host)
	return repo, digest, nil
}

// IsLoopbackRegistryHost permits plain HTTP only for literal loopback
// endpoints. Registry hostnames that merely start with localhost or 127 are
// remote names and still require HTTPS.
func IsLoopbackRegistryHost(host string) bool {
	parsed, err := url.Parse("http://" + host)
	if err != nil || parsed.Host != host || parsed.User != nil {
		return false
	}
	name := parsed.Hostname()
	ip := net.ParseIP(name)
	return name == "localhost" || ip != nil && ip.IsLoopback()
}

func parseStarterOCIRef(ref string) (repository, digest string, err error) {
	match := digestOCIRef.FindStringSubmatch(ref)
	if match == nil {
		return "", "", fmt.Errorf("starter OCI ref must use oci://registry/repository@sha256:<64 lowercase hex>")
	}
	path := match[2]
	if strings.ContainsAny(path, "@?#") || strings.ContainsAny(match[1], "@?#") {
		return "", "", fmt.Errorf("starter OCI ref has an invalid repository path")
	}
	if colon := strings.LastIndex(path, ":"); colon > strings.LastIndex(path, "/") {
		if !starterOCITag.MatchString(path[colon+1:]) {
			return "", "", fmt.Errorf("starter OCI ref has an invalid tag")
		}
		path = path[:colon] // the reviewed tag is advisory; fetch by digest
	}
	if path == "" || strings.Contains(path, ":") {
		return "", "", fmt.Errorf("starter OCI ref has an invalid repository path")
	}
	return match[1] + "/" + path, "sha256:" + match[3], nil
}

type starterRepository interface {
	FetchReference(ctx context.Context, reference string) (ocispec.Descriptor, io.ReadCloser, error)
	Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error)
}

func pullFluxStarter(ctx context.Context, repo starterRepository, digest, dir string) error {
	manifestDesc, manifestReader, err := repo.FetchReference(ctx, digest)
	if err != nil {
		return fmt.Errorf("fetch starter manifest: %w", err)
	}
	defer manifestReader.Close()
	if manifestDesc.Digest.String() != digest {
		return fmt.Errorf("starter manifest digest changed: expected %s, got %s", digest, manifestDesc.Digest)
	}
	manifestBytes, err := readVerified(manifestReader, manifestDesc, 2<<20)
	if err != nil {
		return fmt.Errorf("verify starter manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode starter manifest: %w", err)
	}
	if manifest.MediaType != ocispec.MediaTypeImageManifest || manifest.Config.MediaType != fluxConfigType || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != fluxContentType {
		return fmt.Errorf("starter OCI artifact has an unsupported Flux manifest or content layer")
	}
	layer := manifest.Layers[0]
	if layer.Size <= 0 || layer.Size > maxStarterBlob {
		return fmt.Errorf("starter OCI layer size %d is outside the supported limit", layer.Size)
	}
	reader, err := repo.Fetch(ctx, layer)
	if err != nil {
		return fmt.Errorf("fetch starter content: %w", err)
	}
	defer reader.Close()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".starter-content-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := copyVerified(tmp, reader, layer, maxStarterBlob); err != nil {
		return fmt.Errorf("verify starter content: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return extractStarterTar(ctx, tmp, dir)
}

func readVerified(reader io.Reader, desc ocispec.Descriptor, max int64) ([]byte, error) {
	if desc.Size < 0 || desc.Size > max {
		return nil, fmt.Errorf("size %d exceeds limit", desc.Size)
	}
	data, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != desc.Size {
		return nil, fmt.Errorf("size mismatch: expected %d, got %d", desc.Size, len(data))
	}
	digest := sha256.Sum256(data)
	if desc.Digest.String() != "sha256:"+hex.EncodeToString(digest[:]) {
		return nil, fmt.Errorf("digest mismatch")
	}
	return data, nil
}

func copyVerified(dst io.Writer, reader io.Reader, desc ocispec.Descriptor, max int64) error {
	if desc.Size < 0 || desc.Size > max {
		return fmt.Errorf("size %d exceeds limit", desc.Size)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(reader, max+1))
	if err != nil {
		return err
	}
	if n != desc.Size {
		return fmt.Errorf("size mismatch: expected %d, got %d", desc.Size, n)
	}
	if desc.Digest.String() != "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
		return fmt.Errorf("digest mismatch")
	}
	return nil
}

func extractStarterTar(ctx context.Context, src io.Reader, dir string) error {
	gzipReader, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("open starter gzip: %w", err)
	}
	defer gzipReader.Close()
	bounded := &starterStreamReader{ctx: ctx, source: gzipReader, limit: maxStarterStream}
	archive := tar.NewReader(bounded)
	var total int64
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read starter tar: %w", err)
		}
		entries++
		if entries > maxStarterFiles {
			return fmt.Errorf("starter tar has too many entries")
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		// OCI archives use slash-separated paths on every host, including Windows.
		clean := path.Clean(name)
		if clean == "." {
			continue
		}
		if path.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(name, "\\:") || clean != name {
			return fmt.Errorf("starter tar contains unsafe path %q", hdr.Name)
		}
		path := filepath.Join(dir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 || hdr.Size > maxStarterFile || total+hdr.Size > maxStarterTotal {
				return fmt.Errorf("starter tar file %q exceeds size limit", name)
			}
			total += hdr.Size
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if hdr.Mode&0111 != 0 {
				mode = 0755
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return fmt.Errorf("starter tar file %q: %w", name, err)
			}
			_, copyErr := io.CopyN(f, archive, hdr.Size)
			closeErr := f.Close()
			if copyErr != nil {
				return fmt.Errorf("starter tar file %q: %w", name, copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("starter tar entry %q has unsupported type %d", name, hdr.Typeflag)
		}
	}
	// tar.Reader stops at the tar end marker. Read a bounded tail so the
	// gzip checksum is verified without decompressing an unbounded payload.
	tail := int64(0)
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := bounded.Read(buf)
		tail += int64(n)
		if tail > maxStarterTail {
			return fmt.Errorf("starter gzip has excess data after tar end")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("starter gzip checksum: %w", err)
		}
	}
	if err := gzipReader.Close(); err != nil {
		return fmt.Errorf("starter gzip checksum: %w", err)
	}
	return nil
}

// starterStreamReader also counts tar metadata that archive/tar consumes
// internally, including PAX and GNU extension headers.
type starterStreamReader struct {
	ctx    context.Context
	source io.Reader
	limit  int64
	read   int64
}

func (r *starterStreamReader) Read(buf []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	remaining := r.limit - r.read
	if remaining <= 0 {
		return 0, fmt.Errorf("starter decompressed stream exceeds size limit")
	}
	if int64(len(buf)) > remaining+1 {
		buf = buf[:remaining+1]
	}
	n, err := r.source.Read(buf)
	r.read += int64(n)
	if r.read > r.limit {
		return n, fmt.Errorf("starter decompressed stream exceeds size limit")
	}
	return n, err
}
