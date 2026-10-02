package setup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type GitBinding struct {
	Directory  string `json:"directory"`
	Branch     string `json:"branch"`
	Head       string `json:"head"`
	Remote     string `json:"remote"`
	RemoteHead string `json:"remoteHead"`
}

var gitObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type boundedGitOutput struct{ bytes.Buffer }

func (b *boundedGitOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, fmt.Errorf("Git output exceeds its limit")
	}
	return b.Buffer.Write(p)
}

// InspectGitBinding performs no fetch, credential enrollment, or index writes.
// The first batch supports a clean existing branch with one matching upstream.
// New repositories and divergent branches remain explicit coordinator gates.
func InspectGitBinding(ctx context.Context, directory string) (GitBinding, error) {
	var binding GitBinding
	directory, err := filepath.Abs(directory)
	if err != nil {
		return binding, err
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return binding, fmt.Errorf("Git checkout is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
		var out boundedGitOutput
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("Git identity read failed; check local access and the upstream branch")
		}
		return strings.TrimSpace(out.String()), nil
	}
	root, err := run("rev-parse", "--show-toplevel")
	if err != nil || root != directory {
		return binding, fmt.Errorf("select the Git checkout root")
	}
	branch, err := run("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return binding, fmt.Errorf("review requires a named Git branch")
	}
	if _, err := run("check-ref-format", "refs/heads/"+branch); err != nil {
		return binding, err
	}
	head, err := run("rev-parse", "HEAD")
	if err != nil || !gitObjectID.MatchString(head) {
		return binding, fmt.Errorf("invalid local Git identity")
	}
	status, err := run("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || status != "" {
		return binding, fmt.Errorf("review requires a clean tracked and untracked working tree")
	}
	remote, err := run("remote", "get-url", "--all", "origin")
	if err != nil {
		return binding, err
	}
	push, err := run("remote", "get-url", "--push", "--all", "origin")
	if err != nil || push != remote {
		return binding, fmt.Errorf("review requires one matching fetch and push destination")
	}
	if err := validateReviewRemote(remote); err != nil {
		return binding, err
	}
	upstream, err := run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil || upstream != "origin/"+branch {
		return binding, fmt.Errorf("review requires origin to track the selected branch")
	}
	ref, err := run("ls-remote", "--exit-code", "origin", "refs/heads/"+branch)
	if err != nil {
		return binding, err
	}
	fields := strings.Fields(ref)
	if len(fields) != 2 || !gitObjectID.MatchString(fields[0]) || fields[1] != "refs/heads/"+branch || fields[0] != head {
		return binding, fmt.Errorf("local and remote Git branch differ; synchronize before review")
	}
	// Local state can change while the remote is queried.
	finalHead, e1 := run("rev-parse", "HEAD")
	finalBranch, e2 := run("symbolic-ref", "--quiet", "--short", "HEAD")
	finalStatus, e3 := run("status", "--porcelain=v1", "--untracked-files=all")
	if e1 != nil || e2 != nil || e3 != nil || finalHead != head || finalBranch != branch || finalStatus != "" {
		return binding, fmt.Errorf("Git checkout changed during review")
	}
	return GitBinding{Directory: directory, Branch: branch, Head: head, Remote: remote, RemoteHead: fields[0]}, nil
}

func validateReviewRemote(remote string) error {
	if remote == "" || strings.ContainsAny(remote, "\x00\r\n\t ") {
		return fmt.Errorf("Git destination is missing or ambiguous")
	}
	if filepath.IsAbs(remote) {
		return nil
	} // Local bare fixtures and offline Fleet workflows.
	if strings.HasPrefix(remote, "git@") && strings.Contains(remote, ":") && !strings.Contains(remote, "?") {
		return nil
	}
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "https" && u.Scheme != "ssh" && u.Scheme != "file") || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "file" && u.Host == "") {
		return fmt.Errorf("Git destination must be explicit and contain no embedded credentials")
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword || u.Scheme != "ssh" {
			return fmt.Errorf("Git destination contains embedded credentials")
		}
	}
	return nil
}

func RecheckGitBinding(ctx context.Context, binding GitBinding) error {
	current, err := InspectGitBinding(ctx, binding.Directory)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, binding) {
		return fmt.Errorf("Git destination, branch, or revision changed since review")
	}
	return nil
}
