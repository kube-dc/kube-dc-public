package clusterinit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderHostValidationHashAndRestore(t *testing.T) {
	for _, host := range []string{"https://git.example.test", "user@git.example.test", "git.example.test/path", "git.example.test?x", "git.example.test:0", "git.example.test:65536", "git.example.test\nother", "git.example.test:"} {
		if _, err := ResolveProviderHost(ProviderGitLab, host); err == nil {
			t.Fatalf("accepted %q", host)
		}
	}
	o := validBase()
	o.Provider = ProviderGitLab
	before, _ := ComputeInputHash(&o)
	o.ProviderHost = "GITLAB.COM:443"
	equivalent, _ := ComputeInputHash(&o)
	if before != equivalent {
		t.Fatal("equivalent public authority changed the hash")
	}
	o.ProviderHost = "git.example.test:8443"
	after, _ := ComputeInputHash(&o)
	if before == after {
		t.Fatal("private authority not hashed")
	}
	saved := ExportMap(&o)
	var restored InitOptions
	ImportMap(&restored, saved, func(string) bool { return false })
	if restored.ProviderHost != o.ProviderHost {
		t.Fatal("private authority lost on restore")
	}
}

func TestEnterpriseCreationScopesExplicitTokenAndHost(t *testing.T) {
	swapEnviron(t, []string{"PATH=/usr/bin", "GH_HOST=other.example.test", "GH_TOKEN=stale-public", "GH_ENTERPRISE_TOKEN=stale-enterprise", "GITHUB_ENTERPRISE_TOKEN=stale-alias", "GH_REPO=other.example.test/acme/other"})
	runner := &fakeGhRunner{}
	err := CreateRemoteRepo(context.Background(), CreateRepoOptions{Provider: ProviderGitHub, ProviderHost: "git.example.test", Owner: "acme", Name: "fleet", Token: "explicit-test-token", runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(runner.lastEnv, "\n")
	if !strings.Contains(env, "GH_ENTERPRISE_TOKEN=explicit-test-token") || !strings.Contains(env, "GH_HOST=git.example.test") || strings.Contains(env, "stale") || strings.Contains(env, "GH_REPO=") {
		t.Fatal("provider environment was not scoped")
	}
}

func TestPrivateProviderNeedsCompatibleStarter(t *testing.T) {
	directory := t.TempDir()
	if err := CheckProviderHostSupport(directory, ProviderGitLab, "git.example.test"); err == nil {
		t.Fatal("old starter accepted private host")
	}
	if err := CheckProviderHostSupport(directory, ProviderGitLab, ""); err != nil {
		t.Fatal("public legacy default blocked")
	}
	if err := os.MkdirAll(filepath.Join(directory, "bootstrap"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "bootstrap/flux-install.sh"), []byte("# scaffold-capability: provider-host-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckProviderHostSupport(directory, ProviderGitLab, "git.example.test:8443"); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryProviderHostInferenceAndExplicitMismatch(t *testing.T) {
	for _, tc := range []struct {
		remote, selected, want string
		fail                   bool
	}{
		{"https://git.example.test:8443/group/fleet.git", "", "git.example.test:8443", false},
		{"git@git.example.test:group/fleet.git", "", "git.example.test", false},
		{"https://git.example.test/group/fleet.git", "other.example.test", "", true},
		{"https://git.example.test:8443/group/fleet.git", "git.example.test", "", true},
	} {
		t.Run(tc.remote+tc.selected, func(t *testing.T) {
			dir := t.TempDir()
			for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", tc.remote}} {
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				if body, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git fixture: %v %s", err, body)
				}
			}
			got, err := ResolveRepositoryProviderHost(context.Background(), ProviderGitLab, tc.selected, dir)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("authority=%q error=%v", got, err)
			}
		})
	}
}
