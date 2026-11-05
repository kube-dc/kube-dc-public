package git

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func (c *Client) ForProvider(ctx context.Context, directory, authority string) (ports.GitClient, error) {
	parsed, err := url.Parse("https://" + authority)
	if err != nil || parsed.Host != authority || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("invalid Git provider authority")
	}
	copy := *c
	copy.providerAuthority = strings.ToLower(authority)
	copy.providerDirectory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	urls, err := copy.inspectProviderDestination(ctx, directory)
	if err != nil {
		return nil, err
	}
	copy.providerURLs = make(map[string]bool, len(urls))
	for _, raw := range urls {
		copy.providerURLs[raw] = true
	}
	return &copy, nil
}

func (c *Client) matchProviderRemote(raw string) error {
	if c.providerAuthority == "" {
		return nil
	}
	if c.providerURLs != nil && !c.providerURLs[raw] {
		return fmt.Errorf("Git destination changed after provider review")
	}
	ep, err := transport.NewEndpoint(raw)
	if err != nil || ep.Password != "" {
		return fmt.Errorf("Git destination must use HTTPS or SSH without embedded credentials")
	}
	want, _ := url.Parse("https://" + c.providerAuthority)
	if !strings.EqualFold(ep.Host, want.Hostname()) {
		return fmt.Errorf("Git destination differs from reviewed provider host")
	}
	if ep.Protocol == "ssh" {
		return nil
	}
	port := want.Port()
	if port == "" {
		port = "443"
	}
	actualPort := ep.Port
	if actualPort == 0 {
		actualPort = 443
	}
	if ep.Protocol != "https" || fmt.Sprint(actualPort) != port {
		return fmt.Errorf("Git destination differs from reviewed HTTPS authority")
	}
	return nil
}

type providerOutput struct{ buffer bytes.Buffer }

func (b *providerOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 64<<10 {
		return 0, fmt.Errorf("Git destination output exceeds its limit")
	}
	return b.buffer.Write(p)
}

func (c *Client) checkProviderDestination(ctx context.Context, directory string) error {
	_, err := c.inspectProviderDestination(ctx, directory)
	return err
}

func (c *Client) inspectProviderDestination(ctx context.Context, directory string) ([]string, error) {
	if c.providerAuthority == "" {
		return nil, nil
	}
	resolved, err := filepath.Abs(directory)
	if err != nil || resolved != c.providerDirectory {
		return nil, fmt.Errorf("Git client belongs to another reviewed checkout")
	}
	repo, err := gogit.PlainOpen(directory)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect reviewed Git checkout")
	}
	remote, err := repo.Remote("origin")
	if err != nil || len(remote.Config().URLs) == 0 {
		return nil, fmt.Errorf("reviewed Git origin is missing")
	}
	urls := append([]string{}, remote.Config().URLs...)
	// Check go-git's raw URLs and Git's effective URLs. Flux later uses the Git
	// executable, which also honors insteadOf/pushInsteadOf and pushurl entries.
	for _, raw := range remote.Config().URLs {
		if err := c.matchProviderRemote(raw); err != nil {
			return nil, err
		}
	}
	for _, push := range []bool{false, true} {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		args := []string{"-C", directory, "remote", "get-url", "--all"}
		if push {
			args = append(args, "--push")
		}
		args = append(args, "origin")
		cmd := exec.CommandContext(callCtx, "git", args...)
		var output providerOutput
		cmd.Stdout = &output
		err := cmd.Run()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("cannot inspect effective Git destinations")
		}
		lines := strings.Split(strings.TrimSpace(output.buffer.String()), "\n")
		for _, raw := range lines {
			if err := c.matchProviderRemote(raw); err != nil {
				return nil, err
			}
			urls = append(urls, raw)
		}
	}
	return urls, ctx.Err()
}

var _ ports.GitProviderBinder = (*Client)(nil)
