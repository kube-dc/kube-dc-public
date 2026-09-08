package main

import (
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// The cutover became an automatic finalize step because operators did not know
// to run it: a cluster without it is Ready, green and completely unusable. The
// tests below pin the two ways that automation could silently regress — the
// step disappearing from `init`'s surface, and the opt-out slipping past plan
// review.

func TestInitRegistersOIDCCutoverOptOut(t *testing.T) {
	repo := ""
	cmd := bootstrapInitCmd(&repo)
	f := cmd.Flags().Lookup("no-oidc-cutover")
	if f == nil {
		t.Fatal("init must expose --no-oidc-cutover; without it an operator whose apiserver manifests are owned elsewhere has no way to stop the CLI editing them")
	}
	if f.DefValue != "false" {
		t.Fatalf("--no-oidc-cutover must default to false (the cutover is part of a normal install), got %q", f.DefValue)
	}
}

func TestOIDCCutoverSSHUser(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sshHost string
		want    string
	}{
		// The automatic path must reach the nodes the same way the install
		// did. Defaulting to root on a cluster reached as ubuntu@ would fail
		// every node and turn an automatic step into a deferred one.
		{"user@host", "ubuntu@10.0.0.5", "ubuntu"},
		{"bare host falls back to the resolver default", "10.0.0.5", ""},
		{"unset", "", ""},
		{"empty user is not a user", "@10.0.0.5", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := oidcCutoverSSHUser(&clusterinit.InitOptions{SSHHost: tc.sshHost})
			if got != tc.want {
				t.Fatalf("oidcCutoverSSHUser(%q) = %q, want %q", tc.sshHost, got, tc.want)
			}
		})
	}
	if got := oidcCutoverSSHUser(nil); got != "" {
		t.Fatalf("oidcCutoverSSHUser(nil) = %q, want empty", got)
	}
}

// --no-ssh has no way to reach the control plane, so the cutover must say so
// rather than fail with a confusing dial error.
func TestFinalizeOIDCCutoverRefusesWithoutSSH(t *testing.T) {
	err := runFinalizeOIDCCutover(t.Context(), &strings.Builder{}, &clusterinit.InitOptions{NoSSH: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "--no-ssh") {
		t.Fatalf("want an error naming --no-ssh, got %v", err)
	}
	err = runFinalizeOIDCCutover(t.Context(), &strings.Builder{}, &clusterinit.InitOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "SSH") {
		t.Fatalf("want an error about the missing SSH client, got %v", err)
	}
}

// `bootstrap oidc-cutover` takes NO positional cluster argument — it acts on
// whatever the current kubeconfig points at. Printing a cluster name in the
// re-run hint is worse than omitting it: cobra silently ignores the extra arg,
// so an operator who switched context runs it against the wrong cluster while
// believing the name selected the right one. This command edits apiserver
// configs; "which cluster" is the one thing not to be wrong about.
func TestCutoverRerunCommandCarriesNoClusterName(t *testing.T) {
	got := cutoverRerunCommand(&clusterinit.InitOptions{Name: "webdock", Domain: "webdock.online"})
	if got != "kube-dc bootstrap oidc-cutover" {
		t.Fatalf("re-run hint must not name a cluster, got %q", got)
	}
	// --ssh-user IS load-bearing: without it every node is probed as root.
	got = cutoverRerunCommand(&clusterinit.InitOptions{Name: "webdock", SSHHost: "ubuntu@10.0.0.5"})
	if got != "kube-dc bootstrap oidc-cutover --ssh-user ubuntu" {
		t.Fatalf("re-run hint must carry the SSH user, got %q", got)
	}
}

// The banner is the last thing an install prints when the cutover did not
// happen, and it has to land on someone who has never heard of an OIDC webhook.
func TestCutoverOutstandingBannerStatesTheConsequence(t *testing.T) {
	var b strings.Builder
	writeCutoverOutstandingBanner(&b, &clusterinit.InitOptions{Name: "webdock", Domain: "webdock.online"})
	got := b.String()
	for _, want := range []string{
		"CANNOT BE LOGGED INTO",            // the consequence, not the mechanism
		"401",                              // what the operator will actually see
		"kube-dc bootstrap oidc-cutover",   // the fix
		"kube-dc bootstrap accept webdock", // how to confirm it worked
	} {
		if !strings.Contains(got, want) {
			t.Errorf("banner is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "oidc-cutover webdock") {
		t.Errorf("banner must not name a cluster on the cutover command:\n%s", got)
	}
}
