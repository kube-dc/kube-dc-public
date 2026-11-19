package clusterinit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// This file binds an SSH push destination to the project Flux is installed
// for.
//
// WHY: flux-install is told a provider, a host and an owner/repo, and Flux
// then reaches that project over HTTPS at that host. An HTTPS push URL is
// bound to the same authority by the provider check. An SSH push URL is not:
// its host name can be the same while its port (or an SSH alias) reaches
// another instance, typically the old one during a migration. The push and
// its confirmation would then both succeed there, and Flux would be started
// against a project whose branch never received the commit.
//
// The provider's API is the one source that says which SSH address belongs to
// that project. So, for an SSH push URL, the project is looked up through the
// API at the provider host, and the push URL must be the SSH clone address the
// API reports for it: same host, same port, same path.

// providerAPIClient performs the lookups. Tests replace it.
var providerAPIClient = &http.Client{Timeout: 20 * time.Second}

// providerAPIURL is the API address of the project owner/repo at host (an
// HTTPS authority). Tests replace it to point at a local server.
var providerAPIURL = func(provider Provider, host, owner, repo string) string {
	if provider == ProviderGitLab {
		// The project is addressed by its URL-encoded full path.
		return "https://" + host + "/api/v4/projects/" + url.PathEscape(owner+"/"+repo)
	}
	base := "https://" + host + "/api/v3" // GitHub Enterprise Server
	if strings.EqualFold(host, "github.com") {
		base = "https://api.github.com"
	}
	return base + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// providerSSHCloneURL asks the provider which SSH address the project has.
func providerSSHCloneURL(ctx context.Context, provider Provider, host, owner, repo, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, providerAPIURL(provider, host, owner, repo), nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := providerAPIClient.Do(req)
	if err != nil {
		// The error of an HTTP client names the URL, never the token.
		return "", fmt.Errorf("ask %s for the project %s/%s: %w", host, owner, repo, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read the answer of %s about %s/%s: %w", host, owner, repo, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s for the project %s/%s", host, resp.Status, owner, repo)
	}
	var project struct {
		GitLab string `json:"ssh_url_to_repo"`
		GitHub string `json:"ssh_url"`
	}
	if err := json.Unmarshal(body, &project); err != nil {
		return "", fmt.Errorf("%s gave an answer about %s/%s that is not a project description: %w", host, owner, repo, err)
	}
	address := project.GitHub
	if provider == ProviderGitLab {
		address = project.GitLab
	}
	if address == "" {
		return "", fmt.Errorf("%s does not report an SSH address for the project %s/%s", host, owner, repo)
	}
	return address, nil
}

// sshAddress is what an SSH remote names: the login, the host and port that
// are dialled, and the repository path given to that login. The login is part
// of it: on one server, `ops@host:acme/fleet` and `git@host:acme/fleet` are
// different repositories.
type sshAddress struct{ user, host, port, path string }

func (a sshAddress) String() string {
	user := a.user
	if user != "" {
		user += "@"
	}
	return user + a.host + ":" + a.port + "/" + a.path
}

// parseSSHRemote reads an SSH remote with the parser the Git transport itself
// uses (ssh://, SSH://, and the scp-like [user@]host:path), so what is
// classified here as SSH is exactly what will be sent over SSH. ok is false for
// anything else, and says nothing about the path: an SSH remote with an empty
// or odd path is still an SSH remote, and is then refused for not being the
// project's address.
//
// The address is taken as written. The push that follows dials exactly this
// host and port: the adapter makes the transport ignore SSH configuration for
// it (PushCommit), so there is one answer to "where does this connect", and no
// second resolver that could disagree with the first.
func parseSSHRemote(remote string) (a sshAddress, ok bool) {
	ep, err := transport.NewEndpoint(remote)
	if err != nil || ep.Protocol != "ssh" || ep.Host == "" {
		return a, false
	}
	a.user = ep.User
	a.host = strings.ToLower(ep.Host)
	a.port = "22"
	if ep.Port != 0 {
		a.port = strconv.Itoa(ep.Port)
	}
	a.path = strings.Trim(strings.TrimSuffix(strings.Trim(ep.Path, "/"), ".git"), "/")
	return a, true
}

// sshDestinationIsProviderProject refuses an SSH push URL that is not the SSH
// address the provider reports for owner/repo. Anything that is not an SSH
// URL is not judged here. A lookup that fails is a refusal: without it,
// nothing says the push reaches the project Flux will read.
func sshDestinationIsProviderProject(ctx context.Context, provider Provider, host, owner, repo, token, pushURL string) error {
	if owner == "" || repo == "" {
		return nil
	}
	pushing, isSSH := parseSSHRemote(pushURL)
	if !isSSH {
		return nil
	}
	reported, err := providerSSHCloneURL(ctx, provider, host, owner, repo, token)
	if err != nil {
		return fmt.Errorf("pushes from this checkout go over SSH (%s), and that this is the project Flux is installed for could not be confirmed: %w. Fix the access, or push over the project's HTTPS address", pushing, err)
	}
	project, ok := parseSSHRemote(reported)
	if !ok || project.path == "" {
		return fmt.Errorf("%s reports an SSH address for %s/%s that cannot be read", host, owner, repo)
	}
	if pushing.user != project.user || pushing.host != project.host || pushing.port != project.port || !strings.EqualFold(pushing.path, project.path) {
		return fmt.Errorf("pushes from this checkout go over SSH to %s, but the project Flux is installed for (%s/%s at %s) is at %s: the push would reach another server, login or repository. Set the push URL to exactly the project's own SSH address (init dials the address in the URL; SSH configuration aliases are not applied)", pushing, owner, repo, host, project)
	}
	return nil
}
