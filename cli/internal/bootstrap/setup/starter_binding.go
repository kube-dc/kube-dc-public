package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// BindStarterSource checks the source baseline against the manifest inside
// the already byte-verified OCI starter layer. Self-declared local metadata
// alone cannot establish that the reviewed release owns these generators.
func BindStarterSource(proof ArtifactProof, ref, directory string) error {
	if proof.ProofHash == "" || proof.ProofHash != artifactProofHash(proof) {
		return fmt.Errorf("starter binding requires verified artifacts")
	}
	if err := clusterinit.VerifyReviewedStarter(directory); err != nil {
		return err
	}
	_, digest, ok := strings.Cut(ref, "@sha256:")
	if !ok || !hexSHA256.MatchString(digest) {
		return fmt.Errorf("starter requires a digest reference")
	}
	root, err := os.OpenRoot(proof.CacheDirectory)
	if err != nil {
		return err
	}
	defer root.Close()
	readPinned := func(hash string, limit int64) ([]byte, error) {
		info, err := root.Lstat("blobs/sha256/" + hash)
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
			return nil, fmt.Errorf("invalid cached starter file")
		}
		f, err := root.Open("blobs/sha256/" + hash)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, limit+1))
		sum := sha256.Sum256(body)
		if err != nil || int64(len(body)) > limit || hex.EncodeToString(sum[:]) != hash {
			return nil, fmt.Errorf("starter bytes changed before use")
		}
		return body, nil
	}
	body, err := readPinned(digest, 4<<20)
	if err != nil {
		return fmt.Errorf("starter manifest is unavailable")
	}
	var manifest struct {
		Layers []struct {
			Digest    string `json:"digest"`
			MediaType string `json:"mediaType"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil || len(manifest.Layers) != 1 {
		return fmt.Errorf("review requires a starter with one tar layer")
	}
	layer := manifest.Layers[0]
	hash := strings.TrimPrefix(layer.Digest, "sha256:")
	if !hexSHA256.MatchString(hash) {
		return fmt.Errorf("invalid starter layer pin")
	}
	layerBody, err := readPinned(hash, 128<<20)
	if err != nil {
		return err
	}
	var reader io.Reader = bytes.NewReader(layerBody)
	if strings.HasSuffix(layer.MediaType, "+gzip") || strings.HasSuffix(layer.MediaType, ".gzip") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return fmt.Errorf("invalid starter gzip")
		}
		defer gz.Close()
		reader = gz
	} else if !strings.HasSuffix(layer.MediaType, ".tar") {
		return fmt.Errorf("unsupported starter layer media type")
	}
	tr := tar.NewReader(io.LimitReader(reader, 128<<20))
	var baseline []byte
	for count := 0; count < 10000; count++ {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid starter archive")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name != ".starter-manifest" {
			continue
		}
		if baseline != nil || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size < 1 || header.Size > 4<<20 {
			return fmt.Errorf("invalid starter baseline")
		}
		baseline, err = io.ReadAll(io.LimitReader(tr, (4<<20)+1))
		if err != nil {
			return err
		}
	}
	local, err := os.ReadFile(filepath.Join(directory, ".starter-manifest"))
	if err != nil || len(baseline) == 0 || !bytes.Equal(local, baseline) {
		return fmt.Errorf("Fleet baseline differs from the reviewed starter release")
	}
	return nil
}
