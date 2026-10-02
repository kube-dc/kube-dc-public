package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// Starter-ref digest pinning (review P1 2026-07-20): a TAG is mutable —
// v0.5.1 (and especially :latest) can resolve to different content
// between plan review and apply. The plan must therefore carry the
// concrete digest (…:tag@sha256:…). flux pull artifact accepts
// digest-qualified refs, so the pinned string flows straight through to
// extraction.
//
// Resolution is a registry manifest HEAD (Docker-Content-Digest header)
// with the anonymous bearer-token dance ghcr uses. Plain-HTTP is only
// attempted for loopback registries (the local test harness). On
// resolution failure the tag ref is kept with a warning. The greenfield
// plan gate below then refuses the mutable ref before review. Existing Fleet
// paths can continue because they do not pull a starter.

const starterDigestTimeout = 10 * time.Second

// pinStarterDigest resolves ref's tag to a digest-qualified ref.
// Already-pinned refs pass through untouched.
func pinStarterDigest(out io.Writer, ref string) string {
	if strings.Contains(ref, "@sha256:") {
		return ref
	}
	digest, err := resolveStarterDigest(ref)
	if err != nil {
		fmt.Fprintf(out, "WARNING: could not resolve %s to a digest (%v); greenfield plans require an immutable starter ref\n", ref, err)
		return ref
	}
	fmt.Fprintf(out, "starter ref pinned: %s@%s\n", ref, digest)
	return ref + "@" + digest
}

func starterRefForPlan(out io.Writer, o *clusterinit.InitOptions) string {
	ref := resolveStarterRef(o.StarterRef)
	if o.Repo != "" && clusterinit.StarterShapePresent(o.Repo) {
		return ref // no registry call when the starter is already on disk
	}
	return pinStarterDigest(out, ref)
}

// An apply-plan retry may find the starter that the first attempt extracted.
// Keep the reviewed digest when the caller supplied the same tag (or relied
// on the default), so local progress cannot invalidate a saved plan.
func starterRefForReplay(out io.Writer, o *clusterinit.InitOptions, reviewed string) string {
	ref := resolveStarterRef(o.StarterRef)
	if o.Repo != "" && clusterinit.StarterShapePresent(o.Repo) {
		reviewedTag, _, _ := strings.Cut(reviewed, "@sha256:")
		if o.StarterRef == "" || reviewedTag == ref {
			return reviewed
		}
	}
	return pinStarterDigest(out, ref)
}

// A greenfield plan must identify the exact artifact it will extract. An
// existing Fleet checkout or a repo with the starter shape does not pull an
// artifact, so a historical tag remains valid for those paths.
func requireGreenfieldStarterDigest(o *clusterinit.InitOptions) error {
	if o.FleetMode == clusterinit.FleetExistingFleet || o.Repo != "" && clusterinit.StarterShapePresent(o.Repo) {
		return nil
	}
	if err := clusterinit.ValidateStarterOCIRef(o.StarterRef); err != nil {
		return fmt.Errorf("fleet starter %q requires a valid immutable OCI ref before extraction; retry when the registry is available or provide a digest-pinned --starter-ref: %w", o.StarterRef, err)
	}
	return nil
}

// resolveStarterDigest HEADs the manifest for an oci://host/path:tag
// ref and returns its Docker-Content-Digest.
func resolveStarterDigest(ref string) (string, error) {
	rest, ok := strings.CutPrefix(ref, "oci://")
	if !ok {
		return "", fmt.Errorf("not an oci:// ref")
	}
	hostPath, tag := rest, "latest"
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		hostPath, tag = rest[:i], rest[i+1:]
	}
	host, repoPath, ok := strings.Cut(hostPath, "/")
	if !ok {
		return "", fmt.Errorf("ref has no repository path")
	}
	scheme := "https"
	if clusterinit.IsLoopbackRegistryHost(host) {
		scheme = "http" // loopback test registries are plain HTTP
	}
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repoPath, tag)

	client := &http.Client{Timeout: starterDigestTimeout}
	head := func(token string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodHead, url, nil)
		if err != nil {
			return nil, err
		}
		// Both OCI and Docker manifest types — flux pushes OCI manifests.
		req.Header.Set("Accept",
			"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}

	resp, err := head("")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		token, tokenErr := anonymousBearerToken(client, resp.Header.Get("WWW-Authenticate"))
		if tokenErr != nil {
			return "", fmt.Errorf("anonymous token: %w", tokenErr)
		}
		resp2, err := head(token)
		if err != nil {
			return "", err
		}
		defer resp2.Body.Close()
		resp = resp2
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest HEAD returned %s", resp.Status)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("registry returned no usable Docker-Content-Digest (%q)", digest)
	}
	return digest, nil
}

// anonymousBearerToken performs the registry token dance from a
// WWW-Authenticate: Bearer realm=…,service=…,scope=… challenge with no
// credentials (public artifacts only — the starter is public by
// contract).
func anonymousBearerToken(client *http.Client, challenge string) (string, error) {
	params := map[string]string{}
	challenge = strings.TrimPrefix(challenge, "Bearer ")
	for _, part := range strings.Split(challenge, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			params[k] = strings.Trim(v, `"`)
		}
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("challenge has no realm: %q", challenge)
	}
	url := realm
	sep := "?"
	for _, k := range []string{"service", "scope"} {
		if v := params[k]; v != "" {
			url += sep + k + "=" + v
			sep = "&"
		}
	}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	// tiny extraction instead of a JSON struct: {"token":"…"}
	_, after, ok := strings.Cut(string(body), `"token":"`)
	if !ok {
		return "", fmt.Errorf("token endpoint returned no token")
	}
	token, _, ok := strings.Cut(after, `"`)
	if !ok || token == "" {
		return "", fmt.Errorf("malformed token response")
	}
	return token, nil
}
