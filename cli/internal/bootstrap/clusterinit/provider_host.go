package clusterinit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ResolveRepositoryProviderHost keeps existing private origins authoritative
// when no host was supplied. An explicit host must match an existing origin.
// Missing origins use the selected host or the provider default for new repos.
func ResolveRepositoryProviderHost(ctx context.Context, provider Provider, selected, directory string) (string, error) {
	resolved, err := ResolveProviderHost(provider, selected)
	if err != nil || directory == "" {
		return resolved, err
	}
	if _, err := os.Lstat(filepath.Join(directory, ".git")); os.IsNotExist(err) {
		return resolved, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", directory, "remote", "get-url", "--push", "--all", "origin")
	var out providerHostOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 2 {
			return resolved, nil
		}
		return "", fmt.Errorf("Git provider destination could not be inspected")
	}
	raw := strings.TrimSpace(out.buffer.String())
	if strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("review one Git push destination before choosing credentials")
	}
	ep, err := transport.NewEndpoint(raw)
	if err != nil || ep.Password != "" {
		return "", fmt.Errorf("invalid Git provider destination")
	}
	if ep.Protocol == "file" && selected == "" {
		return resolved, nil
	}
	if ep.Protocol != "https" && ep.Protocol != "ssh" {
		return "", fmt.Errorf("Git provider destination must use HTTPS or SSH")
	}
	authority := ep.Host
	if strings.Contains(authority, ":") {
		authority = "[" + authority + "]"
	}
	if ep.Protocol == "https" && ep.Port != 0 && ep.Port != 443 {
		authority = net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port))
	}
	inferred, err := ResolveProviderHost(provider, authority)
	if err != nil {
		return "", err
	}
	if selected != "" {
		want, _ := url.Parse("https://" + resolved)
		if !strings.EqualFold(want.Hostname(), ep.Host) || (ep.Protocol == "https" && resolved != inferred) {
			return "", fmt.Errorf("Git origin differs from the selected provider authority")
		}
		return resolved, nil
	}
	return inferred, nil
}

type providerHostOutput struct{ buffer bytes.Buffer }

func (b *providerHostOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 8192 {
		return 0, fmt.Errorf("Git provider output exceeds its limit")
	}
	return b.buffer.Write(p)
}

func applyProviderHost(ctx context.Context, opts ApplyOptions) (string, error) {
	selected := opts.ProviderHost
	if selected == "" {
		selected = opts.GitHost
	}
	return ResolveRepositoryProviderHost(ctx, opts.Provider, selected, opts.FleetRepo)
}

// ResolveProviderHost accepts an HTTPS authority, never a URL, credential,
// path, or TLS bypass. Empty selects the provider's public service.
func ResolveProviderHost(provider Provider, host string) (string, error) {
	if _, err := resolveProvider(provider); err != nil {
		return "", err
	}
	if host == "" {
		if provider == ProviderGitLab {
			return "gitlab.com", nil
		}
		return "github.com", nil
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.User != nil || u.Host != host || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(host, "\\ \t\r\n") {
		return "", fmt.Errorf("--provider-host must be a hostname with an optional HTTPS port")
	}
	name := u.Hostname()
	if net.ParseIP(name) == nil {
		if len(name) > 253 || name == "" {
			return "", fmt.Errorf("invalid Git provider hostname")
		}
		for _, label := range strings.Split(name, ".") {
			if !dnsSubdomainLabel.MatchString(strings.ToLower(label)) || len(label) > 63 {
				return "", fmt.Errorf("invalid Git provider hostname")
			}
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid Git provider HTTPS port")
		}
	} else if strings.HasSuffix(host, ":") {
		return "", fmt.Errorf("invalid Git provider HTTPS port")
	}
	if u.Port() == "443" {
		if strings.Contains(name, ":") {
			return "[" + strings.ToLower(name) + "]", nil
		}
		return strings.ToLower(name), nil
	}
	return strings.ToLower(host), nil
}

func providerHostForHash(provider Provider, host string) string {
	resolved, err := ResolveProviderHost(provider, host)
	if err != nil {
		return host
	}
	defaultHost, _ := ResolveProviderHost(provider, "")
	if resolved == defaultHost {
		return ""
	}
	return resolved
}

func providerHostEnv(provider Provider) string {
	if provider == ProviderGitLab {
		return "GITLAB_HOST"
	}
	return "GH_HOST"
}

// CheckProviderHostSupport runs before any platform or Git writes. Older
// starters can use their public defaults, but cannot silently ignore a private
// host selected in the reviewed input.
func CheckProviderHostSupport(directory string, provider Provider, authority string) error {
	if _, err := ResolveProviderHost(provider, authority); err != nil {
		return err
	}
	if providerHostForHash(provider, authority) == "" {
		return nil
	}
	script, err := os.ReadFile(filepath.Join(directory, "bootstrap", "flux-install.sh"))
	compatible := strings.Contains(string(script), "scaffold-capability: provider-host-v1") || (provider == ProviderGitLab && strings.Contains(string(script), "GITLAB_HOST") && strings.Contains(string(script), "--hostname="))
	if err != nil || !compatible {
		return fmt.Errorf("selected Fleet does not support --provider-host; use a compatible starter")
	}
	return nil
}
