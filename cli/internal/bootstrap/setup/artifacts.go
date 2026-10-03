package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
)

type ArtifactProof struct {
	ReleaseSHA256  string                      `json:"releaseSHA256"`
	CacheDirectory string                      `json:"cacheDirectory"`
	Objects        []ArtifactObject            `json:"objects"`
	RKE2           map[string]rke2.ArtifactSet `json:"rke2"`
	ProofHash      string                      `json:"proofHash"`
}
type ArtifactObject struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// VerifyArtifactCache checks all referenced OCI manifests, configuration and
// layers, plus local release files. Cache objects are content-addressed at
// blobs/sha256/DIGEST. It does not execute them or certify qualification evidence.
func VerifyArtifactCache(ctx context.Context, c Compiled, cacheDirectory, cliPath string) (ArtifactProof, error) {
	var proof ArtifactProof
	if err := checkCompiled(c); err != nil {
		return proof, err
	}
	report, err := InspectRelease(c)
	if err != nil || !report.RecordComplete {
		return proof, fmt.Errorf("artifact verification requires a complete bound release record")
	}
	body, err := readReleaseRecord(c.Spec.Release.RecordFile)
	if err != nil {
		return proof, err
	}
	bound := sha256.Sum256(body)
	if hex.EncodeToString(bound[:]) != c.ReleaseSHA256 {
		return proof, fmt.Errorf("release changed during artifact verification")
	}
	var record InstallerReleaseRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return proof, err
	}
	directory, err := filepath.Abs(cacheDirectory)
	if err != nil || cacheDirectory == "" {
		return proof, fmt.Errorf("artifact cache directory is required")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return proof, fmt.Errorf("open artifact cache")
	}
	defer root.Close()
	proof = ArtifactProof{ReleaseSHA256: c.ReleaseSHA256, CacheDirectory: directory, RKE2: map[string]rke2.ArtifactSet{}}
	seen := map[string]bool{}
	var total int64
	readObject := func(hash string, metadata bool) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !hexSHA256.MatchString(hash) {
			return nil, fmt.Errorf("artifact lacks a SHA-256 pin")
		}
		path := "blobs/sha256/" + hash
		info, err := root.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 4<<30 {
			return nil, fmt.Errorf("artifact %s is missing, nonregular, or too large", hash)
		}
		if seen[hash] && !metadata {
			return nil, nil
		}
		if !seen[hash] {
			total += info.Size()
			if total > 32<<30 || len(seen) >= 4096 {
				return nil, fmt.Errorf("artifact set exceeds verification limits")
			}
		}
		file, err := root.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open pinned artifact")
		}
		defer file.Close()
		h := sha256.New()
		var content []byte
		var bytesRead int64
		if metadata {
			if info.Size() > 4<<20 {
				return nil, fmt.Errorf("artifact metadata exceeds verification limit")
			}
			content, err = io.ReadAll(io.TeeReader(io.LimitReader(file, (4<<20)+1), h))
			bytesRead = int64(len(content))
		} else {
			bytesRead, err = io.Copy(h, io.LimitReader(file, (4<<30)+1))
		}
		if err != nil || bytesRead != info.Size() || hex.EncodeToString(h.Sum(nil)) != hash {
			return nil, fmt.Errorf("artifact bytes differ from release pin %s", hash)
		}
		if !seen[hash] {
			proof.Objects = append(proof.Objects, ArtifactObject{hash, info.Size()})
			seen[hash] = true
		}
		return content, nil
	}
	type descriptor struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	}
	var walk func(string, int) error
	manifestSeen := map[string]bool{}
	walk = func(hash string, depth int) error {
		if depth > 8 {
			return fmt.Errorf("OCI descriptor graph exceeds maximum depth")
		}
		if manifestSeen[hash] {
			return nil
		}
		manifestSeen[hash] = true
		body, err := readObject(hash, true)
		if err != nil {
			return err
		}
		var manifest struct {
			SchemaVersion int          `json:"schemaVersion"`
			MediaType     string       `json:"mediaType"`
			Config        descriptor   `json:"config"`
			Layers        []descriptor `json:"layers"`
			Manifests     []descriptor `json:"manifests"`
		}
		if err := json.Unmarshal(body, &manifest); err != nil || manifest.SchemaVersion != 2 {
			return fmt.Errorf("invalid OCI artifact manifest")
		}
		var children []descriptor
		switch manifest.MediaType {
		case "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json":
			if manifest.Config.Digest == "" || len(manifest.Layers) == 0 {
				return fmt.Errorf("incomplete OCI artifact manifest")
			}
			children = append(children, manifest.Config)
			children = append(children, manifest.Layers...)
		case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json":
			if len(manifest.Manifests) == 0 {
				return fmt.Errorf("empty OCI artifact index")
			}
			children = manifest.Manifests
		default:
			return fmt.Errorf("unsupported OCI artifact manifest media type")
		}
		for _, child := range children {
			if !strings.HasPrefix(child.Digest, "sha256:") || !hexSHA256.MatchString(strings.TrimPrefix(child.Digest, "sha256:")) || child.Size < 1 {
				return fmt.Errorf("invalid OCI descriptor")
			}
			childHash := strings.TrimPrefix(child.Digest, "sha256:")
			info, err := root.Stat("blobs/sha256/" + childHash)
			if err != nil || info.Size() != child.Size {
				return fmt.Errorf("OCI descriptor size differs from cached bytes")
			}
			if strings.Contains(child.MediaType, "manifest") || strings.Contains(child.MediaType, "index") {
				if err := walk(childHash, depth+1); err != nil {
					return err
				}
			} else {
				if _, err := readObject(childHash, false); err != nil {
					return err
				}
			}
		}
		return nil
	}
	refs := []string{strings.TrimPrefix(record.Artifacts.StarterRef, "oci://"), record.Artifacts.BackendImage, record.Artifacts.FrontendImage, record.Artifacts.AdminImage}
	for _, ref := range record.Artifacts.Images {
		if !validDigestImage(ref) {
			return proof, fmt.Errorf("component image lacks an immutable reference")
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		_, digest, ok := strings.Cut(ref, "@sha256:")
		if !ok {
			return proof, fmt.Errorf("artifact reference has no digest")
		}
		if err := walk(digest, 0); err != nil {
			return proof, err
		}
	}
	files := []string{record.Artifacts.PlatformChart.SHA256}
	for _, hash := range record.Artifacts.ThemeArchives {
		files = append(files, hash)
	}
	for _, file := range record.Artifacts.Files {
		files = append(files, file.SHA256)
	}
	for _, profile := range record.Profiles {
		if profile.ID == c.Spec.Profile {
			files = append(files, profile.Qualification.EvidenceSHA256)
		}
	}
	for _, hash := range files {
		if _, err := readObject(hash, false); err != nil {
			return proof, err
		}
	}
	cli, err := os.Open(cliPath)
	if err != nil {
		return proof, fmt.Errorf("running CLI bytes are unavailable")
	}
	h := sha256.New()
	_, err = io.Copy(h, cli)
	cli.Close()
	if err != nil || hex.EncodeToString(h.Sum(nil)) != record.Artifacts.CLI.SHA256 {
		return proof, fmt.Errorf("running CLI differs from release pin")
	}
	installer, err := readObject(record.Artifacts.RKE2Installer.SHA256, true)
	if err != nil || len(installer) == 0 || len(record.Artifacts.RKE2Archives) == 0 {
		return proof, fmt.Errorf("release must pin RKE2 installer and architecture archive bytes")
	}
	for arch, archive := range record.Artifacts.RKE2Archives {
		if _, err := readObject(archive.SHA256, false); err != nil {
			return proof, err
		}
		archiveInfo, err := root.Stat("blobs/sha256/" + archive.SHA256)
		if err != nil {
			return proof, err
		}
		set := rke2.ArtifactSet{ArchiveSize: archiveInfo.Size(), Version: record.Artifacts.RKE2Version, Architecture: arch, InstallerPath: filepath.Join(directory, "blobs", "sha256", record.Artifacts.RKE2Installer.SHA256), InstallerSHA256: record.Artifacts.RKE2Installer.SHA256, ArchiveURL: archive.URL, ArchiveSHA256: archive.SHA256}
		if err := set.Validate(record.Artifacts.RKE2Version); err != nil {
			return proof, err
		}
		proof.RKE2[arch] = set
	}
	sort.Slice(proof.Objects, func(i, j int) bool { return proof.Objects[i].SHA256 < proof.Objects[j].SHA256 })
	proof.ProofHash = artifactProofHash(proof)
	return proof, nil
}

func artifactProofHash(proof ArtifactProof) string {
	proof.ProofHash = ""
	body, _ := json.Marshal(proof)
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}
