package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

type PlatformSnapshot struct {
	Git   GitBinding                 `json:"git"`
	Files []clusterinit.PreparedFile `json:"files"`
}

func platformGitRead(ctx context.Context, directory string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var output boundedGitOutput
	if len(args) > 0 && args[0] == "ls-files" {
		output.limit = 1 << 20
	}
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("platform Git inspection failed")
	}
	return output.String(), nil
}

// SnapshotPlatform includes all tracked and non-ignored untracked files.
// Receipts contain only paths, modes, sizes, and hashes, never file contents.
func SnapshotPlatform(ctx context.Context, directory string) (PlatformSnapshot, error) {
	var result PlatformSnapshot
	git, err := InspectGitWorkingBinding(ctx, directory)
	if err != nil {
		return result, err
	}
	names, err := platformGitRead(ctx, git.Directory, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return result, err
	}
	root, err := os.OpenRoot(git.Directory)
	if err != nil {
		return result, err
	}
	defer root.Close()
	seen := map[string]bool{}
	var total int64
	for _, name := range strings.Split(names, "\x00") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if len(seen) > 10000 || !safePlatformPath(name) {
			return result, fmt.Errorf("platform file inventory exceeds its limits or has an unsafe path")
		}
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			info, err := root.Lstat(strings.Join(parts[:i], "/"))
			if err != nil || !info.IsDir() {
				return result, fmt.Errorf("platform path has an unsafe parent")
			}
		}
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		} // Tracked deletion is represented by absence.
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
			return result, fmt.Errorf("platform file is not a bounded regular file")
		}
		f, err := root.Open(name)
		if err != nil {
			return result, err
		}
		opened, statErr := f.Stat()
		body, readErr := io.ReadAll(io.LimitReader(f, (16<<20)+1))
		closeErr := f.Close()
		if statErr != nil || readErr != nil || closeErr != nil || !os.SameFile(info, opened) || len(body) > 16<<20 {
			return result, fmt.Errorf("platform file changed during inspection")
		}
		total += int64(len(body))
		if total > 128<<20 {
			return result, fmt.Errorf("platform snapshot exceeds its size limit")
		}
		hash := sha256.Sum256(body)
		result.Files = append(result.Files, clusterinit.PreparedFile{Path: name, SHA256: hex.EncodeToString(hash[:]), Mode: uint32(info.Mode().Perm()), Size: int64(len(body))})
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	final, err := InspectGitWorkingBinding(ctx, git.Directory)
	if err != nil || final != git {
		return result, fmt.Errorf("platform Git identity changed during inspection")
	}
	result.Git = git
	return result, ctx.Err()
}

func platformPathAllowed(id clusterinit.StepID, cluster, path string, prepared clusterinit.PreparedScaffold) bool {
	base := "clusters/" + cluster + "/"
	switch id {
	case clusterinit.StepScaffold:
		for _, file := range prepared.Files {
			if file.Path == path {
				return true
			}
		}
	case clusterinit.StepFluxInstall:
		return strings.HasPrefix(path, base+"flux-system/")
	case clusterinit.StepBreakGlass:
		return path == base+"break-glass-kubeconfig.enc.yaml"
	case clusterinit.StepOpenBao:
		return path == base+"secrets.enc.yaml"
	case clusterinit.StepKeycloakOIDC:
		return path == base+"cluster-config.env" || path == base+"secrets.enc.yaml" || path == base+"addons.yaml" || path == base+"kustomization.yaml" || path == base+"addons/kustomization.yaml" || path == base+"addons/keycloak-backend-client-secret.enc.yaml"
	case clusterinit.StepManagedServices:
		return path == base+"services-catalog.yaml" || path == base+"cluster-config.env" || path == base+"escrow/services-cell-signing-key.enc.yaml"
	}
	return false
}

func checkPlatformDelta(ctx context.Context, before, after PlatformSnapshot, id clusterinit.StepID, p clusterinit.PreparedScaffold) error {
	if before.Git.RemoteHead != after.Git.RemoteHead {
		canPush := id == clusterinit.StepCommitPush || id == clusterinit.StepFluxInstall || id == clusterinit.StepBreakGlass || id == clusterinit.StepOpenBao || id == clusterinit.StepKeycloakOIDC || id == clusterinit.StepManagedServices
		if !canPush || after.Git.RemoteHead != after.Git.Head {
			return fmt.Errorf("remote Git revision changed outside a confirmed stage publication")
		}
	}
	if before.Git.Directory != after.Git.Directory || before.Git.Branch != after.Git.Branch || before.Git.Remote != after.Git.Remote {
		return fmt.Errorf("platform Git destination changed")
	}
	files := map[string]clusterinit.PreparedFile{}
	for _, f := range before.Files {
		files[f.Path] = f
	}
	for _, f := range after.Files {
		old, exists := files[f.Path]
		if id == clusterinit.StepKeycloakOIDC && f.Path == "clusters/"+p.Cluster+"/cluster-config.env" && old != f {
			if !exists {
				return fmt.Errorf("OIDC config has no reviewed baseline")
			}
			beforeBody, err := readSnapshotFile(p.Directory, old)
			if err != nil {
				return err
			}
			expected, err := keycloakConfigTransition(beforeBody)
			if err != nil {
				return err
			}
			actual, err := readSnapshotFile(p.Destination, f)
			if err != nil || !bytes.Equal(actual, expected) {
				return fmt.Errorf("OIDC config changed beyond the reviewed backend-secret wiring")
			}
		}
		if (!exists || old != f) && !platformPathAllowed(id, p.Cluster, f.Path, p) {
			return fmt.Errorf("stage changed a file outside its reviewed scope")
		}
		delete(files, f.Path)
	}
	if len(files) != 0 {
		return fmt.Errorf("stage removed Fleet files; inspect before recovery")
	}
	if before.Git.Head != after.Git.Head {
		if _, err := platformGitRead(ctx, p.Destination, "merge-base", "--is-ancestor", before.Git.Head, after.Git.Head); err != nil {
			return fmt.Errorf("platform Git history was rewritten")
		}
		names, err := platformGitRead(ctx, p.Destination, "diff", "--name-only", "-z", before.Git.Head, after.Git.Head)
		if err != nil {
			return err
		}
		for _, name := range strings.Split(names, "\x00") {
			if name != "" && !platformPathAllowed(id, p.Cluster, name, p) && id != clusterinit.StepCommitPush {
				return fmt.Errorf("stage committed files outside its reviewed scope")
			}
			if name != "" && id == clusterinit.StepCommitPush && !platformPathAllowed(clusterinit.StepScaffold, p.Cluster, name, p) {
				return fmt.Errorf("publication commit contains unreviewed files")
			}
		}
	}
	return nil
}

func readSnapshotFile(directory string, file clusterinit.PreparedFile) ([]byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(file.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, fmt.Errorf("reviewed config is unavailable")
	}
	f, err := root.Open(file.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	hash := sha256.Sum256(body)
	if err != nil || len(body) > 16<<20 || hex.EncodeToString(hash[:]) != file.SHA256 {
		return nil, fmt.Errorf("reviewed config differs from its snapshot")
	}
	return body, nil
}

func keycloakConfigTransition(before []byte) ([]byte, error) {
	const key = "KEYCLOAK_ADMIN_CLIENT_SECRET_NAME="
	const value = "keycloak-backend-client-secret"
	lines := strings.Split(string(before), "\n")
	count := 0
	for i, line := range lines {
		if strings.HasPrefix(line, key) {
			count++
			old := strings.TrimPrefix(line, key)
			if old != "" && old != value {
				return nil, fmt.Errorf("OIDC backend secret name conflicts with the reviewed finalizer")
			}
			lines[i] = key + value
		}
	}
	if count > 1 {
		return nil, fmt.Errorf("duplicate OIDC backend secret setting")
	}
	if count == 0 {
		return append(append([]byte{}, before...), []byte("\n"+key+value+"\n")...), nil
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func checkPlatformStageInputs(id clusterinit.StepID, p clusterinit.PreparedScaffold, snapshot PlatformSnapshot) error {
	if id != clusterinit.StepKeycloakOIDC {
		return nil
	}
	for _, file := range snapshot.Files {
		if file.Path == "clusters/"+p.Cluster+"/cluster-config.env" {
			body, err := readSnapshotFile(p.Directory, file)
			if err != nil {
				return err
			}
			_, err = keycloakConfigTransition(body)
			return err
		}
	}
	return fmt.Errorf("OIDC stage has no reviewed cluster configuration")
}
