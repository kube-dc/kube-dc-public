package clusterinit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"gopkg.in/yaml.v3"
)

// PreparedScaffold retains the actual generator output. Execution must publish
// these bytes, including the randomized SOPS ciphertext, without rerendering.
type PreparedScaffold struct {
	Directory   string         `json:"directory"`
	Destination string         `json:"destination"`
	Cluster     string         `json:"cluster"`
	SourceFiles []PreparedFile `json:"sourceFiles"`
	SourceAfter []PreparedFile `json:"sourceAfter"`
	Files       []PreparedFile `json:"files"`
	Hash        string         `json:"hash"`
}

type PreparedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

var reviewSourceEntries = []string{"bootstrap", "infrastructure", "platform", "addons", "scripts", ".starter-version", ".starter-manifest", ".sops.yaml", ".gitignore"}

// SnapshotScaffoldSource includes every generator and shared Fleet input. It
// refuses symlinks and special files instead of following them out of the root.
func SnapshotScaffoldSource(directory string) ([]PreparedFile, error) {
	var files []PreparedFile
	for _, entry := range reviewSourceEntries {
		part, err := snapshotTree(directory, entry, true)
		if err != nil {
			return nil, err
		}
		files = append(files, part...)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func snapshotTree(directory, relative string, optional bool) ([]PreparedFile, error) {
	if !safePreparedPath(relative) {
		return nil, fmt.Errorf("unsafe prepared path")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := root.Lstat(relative); optional && os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var files []PreparedFile
	var total int64
	err = filepath.WalkDir(filepath.Join(directory, relative), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("prepared trees cannot contain symlinks")
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		body, info, err := readPreparedFile(root, rel)
		if err != nil {
			return err
		}
		total += int64(len(body))
		if total > 128<<20 || len(files) >= 10000 {
			return fmt.Errorf("prepared tree exceeds review limits")
		}
		h := sha256.Sum256(body)
		files = append(files, PreparedFile{Path: rel, SHA256: hex.EncodeToString(h[:]), Mode: uint32(info.Mode().Perm()), Size: int64(len(body))})
		return nil
	})
	return files, err
}

func safePreparedPath(path string) bool {
	return path != "" && path != "." && !filepath.IsAbs(path) && filepath.ToSlash(filepath.Clean(path)) == path && path != ".." && !strings.HasPrefix(path, "../") && !strings.ContainsAny(path, "\x00\r\n")
}

func readPreparedFile(root *os.Root, path string) ([]byte, os.FileInfo, error) {
	info, err := root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, nil, fmt.Errorf("prepared file is missing, unsafe, or too large")
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || int64(len(body)) != info.Size() {
		return nil, nil, fmt.Errorf("prepared file changed during read")
	}
	return body, info, nil
}

// PrepareScaffold runs the existing scaffold in a new private directory. The
// fourth script argument prevents ambient Kubernetes discovery. Host-derived
// values must already be explicit in opts. Only local preparation writes occur.
func PrepareScaffold(ctx context.Context, opts ScaffoldOptions, directory string, runner func(string) ports.ScriptRunner, validateSource func(string) error) (PreparedScaffold, error) {
	var prepared PreparedScaffold
	if opts.Plan == nil || runner == nil || validateSource == nil {
		return prepared, fmt.Errorf("prepare requires a plan and runner")
	}
	if !ValidClusterName(opts.Plan.ClusterName) {
		return prepared, fmt.Errorf("invalid prepared cluster name")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return prepared, err
	}
	destination, err := filepath.Abs(opts.FleetRepo)
	if err != nil {
		return prepared, err
	}
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil {
		return prepared, err
	}
	source, err := SnapshotScaffoldSource(opts.FleetRepo)
	if err != nil {
		return prepared, err
	}
	if len(source) == 0 {
		return prepared, fmt.Errorf("prepare requires Fleet source")
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return prepared, fmt.Errorf("prepare directory must not exist: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(directory)
		}
	}()
	root, err := os.OpenRoot(opts.FleetRepo)
	if err != nil {
		return prepared, err
	}
	defer root.Close()
	for _, file := range source {
		body, _, err := readPreparedFile(root, file.Path)
		if err != nil {
			return prepared, err
		}
		h := sha256.Sum256(body)
		if hex.EncodeToString(h[:]) != file.SHA256 {
			return prepared, fmt.Errorf("Fleet source changed during preparation")
		}
		path := filepath.Join(directory, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return prepared, err
		}
		if err := os.WriteFile(path, body, os.FileMode(file.Mode)); err != nil {
			return prepared, err
		}
	}
	if err := validateSource(directory); err != nil {
		return prepared, err
	}
	opts.FleetRepo = directory
	opts.KubeconfigPath = "/dev/null"
	opts.Runner = runner(directory)
	opts.Out = io.Discard
	if err := Scaffold(ctx, opts); err != nil {
		return prepared, err
	}
	after, err := SnapshotScaffoldSource(directory)
	if err != nil {
		return prepared, err
	}
	added, err := reviewedSourceAdditions(source, after)
	if err != nil {
		return prepared, err
	}
	if containsPreparedPath(added, "platform/registry-depot/secret.enc.yaml") {
		if err := verifyRegistryDepotCredential(directory); err != nil {
			return prepared, err
		}
	}
	files, err := snapshotTree(directory, "clusters/"+opts.Plan.ClusterName, false)
	if err != nil {
		return prepared, err
	}
	if len(files) == 0 {
		return prepared, fmt.Errorf("scaffold produced no files")
	}
	files = append(files, added...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	prepared = PreparedScaffold{Directory: directory, Destination: destination, Cluster: opts.Plan.ClusterName, SourceFiles: source, SourceAfter: after, Files: files}
	prepared.Hash = preparedHash(prepared)
	complete = true
	return prepared, nil
}

func preparedHash(p PreparedScaffold) string {
	p.Hash = ""
	body, _ := json.Marshal(p)
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func VerifyPreparedScaffold(p PreparedScaffold) error {
	if p.Hash == "" || p.Hash != preparedHash(p) || !ValidClusterName(p.Cluster) {
		return fmt.Errorf("prepared scaffold binding changed")
	}
	if !filepath.IsAbs(p.Destination) {
		return fmt.Errorf("prepared destination must be explicit")
	}
	info, err := os.Lstat(p.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("prepared directory must remain private")
	}
	source, err := SnapshotScaffoldSource(p.Directory)
	if err != nil || !reflect.DeepEqual(source, p.SourceAfter) {
		return fmt.Errorf("prepared source changed")
	}
	added, err := reviewedSourceAdditions(p.SourceFiles, source)
	if err != nil {
		return err
	}
	if containsPreparedPath(added, "platform/registry-depot/secret.enc.yaml") {
		if err := verifyRegistryDepotCredential(p.Directory); err != nil {
			return err
		}
	}
	files, err := snapshotTree(p.Directory, "clusters/"+p.Cluster, false)
	if err != nil {
		return err
	}
	files = append(files, added...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if !reflect.DeepEqual(files, p.Files) {
		return fmt.Errorf("prepared output changed")
	}
	return nil
}

// The generator can mint one shared encrypted registry credential and record
// the new cluster's suspended catalog revisions. Both changes are reviewed.
func reviewedSourceAdditions(before, after []PreparedFile) ([]PreparedFile, error) {
	prior := map[string]PreparedFile{}
	for _, file := range before {
		prior[file.Path] = file
	}
	var added []PreparedFile
	for _, file := range after {
		old, exists := prior[file.Path]
		if exists {
			if old != file {
				if file.Path != "platform/kube-dc-services-catalog/revisions.lock" {
					return nil, fmt.Errorf("scaffold changed shared Fleet source")
				}
				added = append(added, file)
			}
			delete(prior, file.Path)
			continue
		}
		if file.Path != "platform/registry-depot/secret.enc.yaml" && file.Path != "platform/kube-dc-services-catalog/revisions.lock" {
			return nil, fmt.Errorf("scaffold added an unreviewed shared file")
		}
		added = append(added, file)
	}
	if len(prior) != 0 {
		return nil, fmt.Errorf("scaffold removed shared Fleet source")
	}
	return added, nil
}

func containsPreparedPath(files []PreparedFile, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

// PublishPreparedScaffold is the consumer for the reviewed bytes. A coordinator
// must hold its target claim and supply a fresh read-only guard. It does not
// commit, push, label nodes, or invoke Flux; those stages need their own guard.
func PublishPreparedScaffold(ctx context.Context, p PreparedScaffold, fleet string, guard func(context.Context) error) error {
	if guard == nil {
		return fmt.Errorf("prepared publication requires a review guard")
	}
	if err := VerifyPreparedScaffold(p); err != nil {
		return err
	}
	destination, err := filepath.Abs(fleet)
	if err != nil {
		return err
	}
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil || destination != p.Destination {
		return fmt.Errorf("publication destination differs from review")
	}
	source, err := SnapshotScaffoldSource(fleet)
	if err != nil || !reflect.DeepEqual(source, p.SourceFiles) {
		return fmt.Errorf("Fleet source changed since review")
	}
	root, err := os.OpenRoot(p.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	contents := make([][]byte, len(p.Files))
	for i, file := range p.Files {
		body, _, err := readPreparedFile(root, file.Path)
		if err != nil {
			return err
		}
		h := sha256.Sum256(body)
		if hex.EncodeToString(h[:]) != file.SHA256 {
			return fmt.Errorf("prepared bytes changed before publication")
		}
		contents[i] = body
	}
	if err := guard(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fleetRoot, err := os.OpenRoot(fleet)
	if err != nil {
		return err
	}
	defer fleetRoot.Close()
	relative := "clusters/" + p.Cluster
	if _, err := fleetRoot.Lstat(relative); !os.IsNotExist(err) {
		return fmt.Errorf("prepared target must be absent")
	}
	for _, file := range p.Files {
		if !strings.HasPrefix(file.Path, relative+"/") && file.Path != "platform/registry-depot/secret.enc.yaml" && file.Path != "platform/kube-dc-services-catalog/revisions.lock" {
			return fmt.Errorf("prepared file escapes reviewed write scope")
		}
		if err := checkPreparedParents(fleetRoot, file.Path); err != nil {
			return err
		}
		if file.Path == "platform/kube-dc-services-catalog/revisions.lock" && containsPreparedPath(p.SourceFiles, file.Path) {
			continue // Reviewed replacement; the source snapshot pins its previous bytes.
		}
		if _, err := fleetRoot.Lstat(file.Path); !os.IsNotExist(err) {
			return fmt.Errorf("prepared file destination must be absent")
		}
	}
	// Root confines parent traversal. Claim ownership and cross-process exclusion
	// belong to the coordinator, not this file writer.
	if err := fleetRoot.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
		return err
	}
	if err := fleetRoot.Mkdir(relative, 0o755); err != nil {
		return err
	}
	for i, file := range p.Files {
		if err := fleetRoot.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return err
		}
		if file.Path == "platform/kube-dc-services-catalog/revisions.lock" {
			if err := publishPreparedCatalogLock(fleetRoot, fleet, file, contents[i], p.SourceFiles); err != nil {
				return err
			}
			continue
		}
		f, err := fleetRoot.OpenFile(file.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(file.Mode))
		if err != nil {
			return err
		}
		_, writeErr := f.Write(contents[i])
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func publishPreparedCatalogLock(root *os.Root, fleet string, file PreparedFile, body []byte, before []PreparedFile) error {
	for _, previous := range before {
		if previous.Path != file.Path {
			continue
		}
		current, info, err := readPreparedFile(root, file.Path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("catalog lock changed before publication")
		}
		hash := sha256.Sum256(current)
		if hex.EncodeToString(hash[:]) != previous.SHA256 {
			return fmt.Errorf("catalog lock changed before publication")
		}
		break
	}
	temp, err := os.CreateTemp(filepath.Join(fleet, filepath.Dir(file.Path)), ".revisions.lock.prepared-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(os.FileMode(file.Mode)); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	relTemp, err := filepath.Rel(fleet, temp.Name())
	if err != nil {
		return err
	}
	return root.Rename(relTemp, file.Path)
}

func checkPreparedParents(root *os.Root, path string) error {
	parts := strings.Split(filepath.ToSlash(filepath.Dir(path)), "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("prepared destination has an unsafe parent")
		}
	}
	return nil
}

func verifyRegistryDepotCredential(directory string) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	const path = "platform/registry-depot/secret.enc.yaml"
	if err := checkPreparedParents(root, path); err != nil {
		return err
	}
	body, _, err := readPreparedFile(root, path)
	if err != nil {
		return err
	}
	var secret struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		StringData map[string]string `yaml:"stringData"`
		Data       map[string]string `yaml:"data"`
		SOPS       map[string]any    `yaml:"sops"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&secret); err != nil || secret.APIVersion != "v1" || secret.Kind != "Secret" || secret.Metadata.Name != "registry-depot-auth" || secret.Metadata.Namespace != "kube-dc" || len(secret.StringData)+len(secret.Data) == 0 || len(secret.SOPS) == 0 {
		return fmt.Errorf("shared registry credential is not the generated encrypted Secret")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("shared registry credential must contain exactly one YAML document")
	}
	for _, values := range []map[string]string{secret.StringData, secret.Data} {
		for _, value := range values {
			if !strings.HasPrefix(value, "ENC[AES256_GCM,") || !strings.HasSuffix(value, "]") {
				return fmt.Errorf("shared registry credential contains unencrypted data")
			}
		}
	}
	return nil
}
