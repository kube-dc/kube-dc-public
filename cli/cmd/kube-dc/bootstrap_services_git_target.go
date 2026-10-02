package main

import (
	"context"
	"fmt"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

type managedServicesTargetReader interface {
	Get(context.Context, string, string, string) (map[string]any, error)
}

// Check the existing cluster's actual Flux source before either a new commit
// or a no-change push. go-git's Push sends HEAD to the same-named remote
// branch, regardless of upstream configuration.
func verifyManagedServicesFleetPushTarget(ctx context.Context, path, cluster string, reader managedServicesTargetReader) error {
	config, err := reader.Get(ctx, "flux-system", "configmap", "cluster-config")
	if err != nil {
		return fmt.Errorf("read live cluster identity before rebuild: %w", err)
	}
	if nestedString(config, "data", "CLUSTER_NAME") != cluster {
		return fmt.Errorf("live cluster identity does not match rebuild target %q", cluster)
	}
	flux, err := reader.Get(ctx, "flux-system", "gitrepository", "flux-system")
	if err != nil {
		return fmt.Errorf("read live Flux source before rebuild: %w", err)
	}
	if nestedString(flux, "metadata", "name") != "flux-system" ||
		nestedString(flux, "metadata", "namespace") != "flux-system" {
		return fmt.Errorf("live Flux source has unexpected identity")
	}
	branch := nestedString(flux, "spec", "ref", "branch")
	remoteURL := nestedString(flux, "spec", "url")
	if branch == "" || remoteURL == "" {
		return fmt.Errorf("live Flux source must specify a branch and repository URL")
	}
	for _, selector := range []string{"commit", "tag", "semver", "name"} {
		if nestedString(flux, "spec", "ref", selector) != "" {
			return fmt.Errorf("live Flux source has a revision selector that overrides its branch")
		}
	}
	sync, err := reader.Get(ctx, "flux-system", "kustomization", "flux-system")
	if err != nil {
		return fmt.Errorf("read live Flux sync target before rebuild: %w", err)
	}
	if nestedString(sync, "spec", "path") != "./clusters/"+cluster ||
		nestedString(sync, "spec", "sourceRef", "kind") != "GitRepository" ||
		nestedString(sync, "spec", "sourceRef", "name") != "flux-system" {
		return fmt.Errorf("live Flux sync target does not match cluster %q", cluster)
	}
	sourceNamespace := nestedString(sync, "spec", "sourceRef", "namespace")
	if sourceNamespace != "" && sourceNamespace != "flux-system" {
		return fmt.Errorf("live Flux sync target uses a different source namespace")
	}
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return fmt.Errorf("open Fleet Git checkout: %w", err)
	}
	head, err := repo.Head()
	if err != nil || !head.Name().IsBranch() || head.Name().Short() != branch {
		return fmt.Errorf("Fleet checkout must be on the live Flux branch %q before rebuild", branch)
	}
	origin, err := repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("Fleet checkout has no origin push destination: %w", err)
	}
	// The Git adapter uses go-git, which pushes to the last configured URL.
	// RemoteConfig appends pushurl entries after fetch URLs.
	urls := origin.Config().URLs
	if len(urls) == 0 || !sameManagedServicesGitDestination(urls[len(urls)-1], remoteURL) {
		return fmt.Errorf("Fleet origin push destination differs from the live Flux source")
	}
	return nil
}

func sameManagedServicesGitDestination(a, b string) bool {
	first, firstErr := transport.NewEndpoint(a)
	second, secondErr := transport.NewEndpoint(b)
	if firstErr != nil || secondErr != nil || first.Host == "" || second.Host == "" {
		return false
	}
	if (first.Protocol != "ssh" && first.Protocol != "https") ||
		(second.Protocol != "ssh" && second.Protocol != "https") {
		return false
	}
	canonicalPath := func(path string) string {
		return strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	}
	port := func(protocol string, value int) int {
		if value == 0 || protocol == "ssh" && value == 22 || protocol == "https" && value == 443 {
			return 0
		}
		return value
	}
	return strings.EqualFold(first.Host, second.Host) && port(first.Protocol, first.Port) == port(second.Protocol, second.Port) &&
		canonicalPath(first.Path) != "" && canonicalPath(first.Path) == canonicalPath(second.Path)
}
