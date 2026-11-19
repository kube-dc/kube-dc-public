package clusterinit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"k8s.io/client-go/tools/clientcmd"
)

func TestParseSSHRemote(t *testing.T) {
	for remote, want := range map[string]string{
		"git@gitlab.example.net:acme/fleet.git":          "git@gitlab.example.net:22/acme/fleet",
		"ssh://git@gitlab.example.net/acme/fleet.git":    "git@gitlab.example.net:22/acme/fleet",
		"ssh://git@GitLab.Example.net:2222/acme/fleet":   "git@gitlab.example.net:2222/acme/fleet",
		"SSH://git@gitlab.example.net:2222/acme/fleet":   "git@gitlab.example.net:2222/acme/fleet", // git keeps the case; it is still SSH
		"ssh://gitlab.example.net:2222/group/sub/fleet/": "gitlab.example.net:2222/group/sub/fleet",
		"gitlab.example.net:acme/fleet.git":              "gitlab.example.net:22/acme/fleet",
		"ops@gitlab.example.net:acme/fleet.git":          "ops@gitlab.example.net:22/acme/fleet",
		"https://gitlab.example.net/acme/fleet.git":      "",
		"HTTPS://gitlab.example.net/acme/fleet.git":      "",
		"file:///srv/git/fleet.git":                      "",
		"/srv/git/fleet.git":                             "",
	} {
		got, ok := parseSSHRemote(remote)
		if want == "" {
			if ok {
				t.Errorf("%s: read as SSH (%s)", remote, got)
			}
			continue
		}
		if !ok || got.String() != want {
			t.Errorf("%s: got %q ok=%v, want %q", remote, got, ok, want)
		}
	}
}

// providerStub answers the project lookup like the provider's API.
func providerStub(t *testing.T, status int, body string) (calls *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization")+" "+r.URL.EscapedPath())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	oldURL, oldClient := providerAPIURL, providerAPIClient
	real := providerAPIURL
	providerAPIURL = func(p Provider, host, owner, repo string) string {
		// Same path as the real one, served by the stub.
		u := real(p, host, owner, repo)
		return srv.URL + u[strings.Index(u[len("https://"):], "/")+len("https://"):]
	}
	providerAPIClient = srv.Client()
	t.Cleanup(func() { providerAPIURL, providerAPIClient = oldURL, oldClient })
	return &seen
}

// The migration case: the checkout still pushes to the old instance over SSH
// on another port, while Flux is installed for the project at the HTTPS host.
func TestSSHDestinationIsProviderProject(t *testing.T) {
	ctx := context.Background()
	const host = "git.example.net"
	t.Run("GitLab", func(t *testing.T) {
		calls := providerStub(t, 200, `{"ssh_url_to_repo":"git@git.example.net:acme/fleet.git","http_url_to_repo":"https://git.example.net/acme/fleet.git"}`)
		for pushURL, ok := range map[string]bool{
			"git@git.example.net:acme/fleet.git":            true,
			"ssh://git@git.example.net/acme/fleet.git":      true,
			"ssh://git@git.example.net:22/acme/fleet":       true,
			"ssh://git@git.example.net:2222/acme/fleet.git": false, // the old instance
			"SSH://git@git.example.net:2222/acme/fleet.git": false, // the same, scheme in upper case
			"git@old.example.net:acme/fleet.git":            false,
			"git@git.example.net:acme/other.git":            false,
			"ops@git.example.net:acme/fleet.git":            false, // another login: a mirror under the operator's account
			"deploy@git.example.net:.git":                   false, // SSH, with no project path at all
			"git@git.example.net:.git":                      false,
			"git.example.net:acme/fleet.git":                false, // no login: the OS user's, not the project's
			"ssh://git.example.net/acme/fleet.git":          false,
			"https://git.example.net/acme/fleet.git":        true, // not SSH: bound by the provider check
		} {
			err := sshDestinationIsProviderProject(ctx, ProviderGitLab, host, "acme", "fleet", "tok", pushURL)
			if (err == nil) != ok {
				t.Errorf("%s: ok=%v, got %v", pushURL, ok, err)
			}
		}
		if len(*calls) == 0 || (*calls)[0] != "Bearer tok /api/v4/projects/acme%2Ffleet" {
			t.Fatalf("the project lookup: %v", *calls)
		}
	})
	t.Run("GitLab on a non-standard SSH port", func(t *testing.T) {
		providerStub(t, 200, `{"ssh_url_to_repo":"ssh://git@git.example.net:2222/group/sub/fleet.git"}`)
		if err := sshDestinationIsProviderProject(ctx, ProviderGitLab, host, "group/sub", "fleet", "tok", "ssh://git@git.example.net:2222/group/sub/fleet.git"); err != nil {
			t.Fatalf("the project's own address on port 2222 was refused: %v", err)
		}
		if err := sshDestinationIsProviderProject(ctx, ProviderGitLab, host, "group/sub", "fleet", "tok", "git@git.example.net:group/sub/fleet.git"); err == nil {
			t.Fatal("port 22 accepted for a project that is on port 2222")
		}
	})
	t.Run("GitHub", func(t *testing.T) {
		calls := providerStub(t, 200, `{"ssh_url":"git@github.com:acme/fleet.git","clone_url":"https://github.com/acme/fleet.git"}`)
		if err := sshDestinationIsProviderProject(ctx, ProviderGitHub, "github.com", "acme", "fleet", "tok", "git@github.com:acme/fleet.git"); err != nil {
			t.Fatal(err)
		}
		if err := sshDestinationIsProviderProject(ctx, "", "github.com", "acme", "fleet", "tok", "ssh://git@github.com:2222/acme/fleet.git"); err == nil {
			t.Fatal("another port accepted")
		}
		if (*calls)[0] != "Bearer tok /repos/acme/fleet" {
			t.Fatalf("the project lookup: %v", *calls)
		}
	})
	t.Run("a lookup that fails refuses, without leaking the token", func(t *testing.T) {
		providerStub(t, 404, `{"message":"404 Project Not Found"}`)
		err := sshDestinationIsProviderProject(ctx, ProviderGitLab, host, "acme", "fleet", "s3cr3t-token", "git@git.example.net:acme/fleet.git")
		if err == nil || strings.Contains(err.Error(), "s3cr3t-token") || !strings.Contains(err.Error(), "could not be confirmed") {
			t.Fatalf("want a refusal that does not print the token, got %v", err)
		}
	})
	t.Run("no SSH address reported", func(t *testing.T) {
		providerStub(t, 200, `{"http_url_to_repo":"https://git.example.net/acme/fleet.git"}`)
		if err := sshDestinationIsProviderProject(ctx, ProviderGitLab, host, "acme", "fleet", "tok", "git@git.example.net:acme/fleet.git"); err == nil {
			t.Fatal("want a refusal")
		}
	})
}

const kubeconfigBody = "apiVersion: v1\nkind: Config\ncurrent-context: c\nclusters:\n- name: c\n  cluster:\n    server: https://192.0.2.10:6443\n    certificate-authority: ca.crt\ncontexts:\n- name: c\n  context: {cluster: c, user: u}\nusers:\n- name: u\n  user:\n    token: tok\n"

// kubeconfigFleet is a fleet checkout holding a kubeconfig (and the CA file it
// names by relative path).
func kubeconfigFleet(t *testing.T) (fleet, inside, runTemp string) {
	t.Helper()
	fleet = t.TempDir()
	runTemp = filepath.Join(t.TempDir(), "run.tmp")
	if err := os.WriteFile(filepath.Join(fleet, "ca.crt"), []byte("CA-PEM"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside = filepath.Join(fleet, "kubeconfig")
	if err := os.WriteFile(inside, []byte(kubeconfigBody), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir()) // no ~/.kube/<cluster>_config unless a test makes one
	return fleet, inside, runTemp
}

// isPrivate reports whether path is the run-owned copy called name.
func isPrivate(runTemp, name, path string) bool { return path == filepath.Join(runTemp, name) }

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The script's kubeconfig is replaced by a private copy when it is inside the
// fleet checkout and passed on as it is otherwise; $KUBECONFIG is always a
// private snapshot.
func TestIsolateKubeconfigs(t *testing.T) {
	fleet, inside, runTemp := kubeconfigFleet(t)
	discovery, env, err := isolateKubeconfigs(fleet, runTemp, "east1", inside, "")
	if err != nil || !isPrivate(runTemp, "kubeconfig-discovery", discovery) || !isPrivate(runTemp, "kubeconfig-env", env) {
		t.Fatalf("want private copies in the run directory, got %q %q %v", discovery, env, err)
	}
	copied := mustRead(t, discovery)
	if !strings.Contains(copied, "token: tok") || !strings.Contains(copied, "certificate-authority: "+string(filepath.Separator)) {
		t.Fatalf("the copy must hold the kubeconfig, its CA file by absolute path:\n%s", copied)
	}
	if fi, _ := os.Stat(discovery); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if after := mustRead(t, inside); after != kubeconfigBody {
		t.Fatal("the operator's kubeconfig was changed")
	}
	outside := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(outside, []byte(kubeconfigBody), 0o600); err != nil {
		t.Fatal(err)
	}
	// Outside the checkout the explicit path is passed on exactly as given
	// (a verified session pins it), and $KUBECONFIG is still a snapshot.
	discovery, env, err = isolateKubeconfigs(fleet, runTemp, "east1", outside, outside)
	if err != nil || discovery != outside || !isPrivate(runTemp, "kubeconfig-env", env) {
		t.Fatalf("a kubeconfig outside the checkout: %q %q %v", discovery, env, err)
	}
	if !strings.Contains(mustRead(t, env), "token: tok") {
		t.Fatal("the snapshot of $KUBECONFIG lacks its content")
	}
	if discovery, _, err := isolateKubeconfigs(fleet, runTemp, "east1", "", ""); err != nil || discovery != "" {
		t.Fatalf("no kubeconfig: %q %v", discovery, err)
	}
	// A path that opens nothing is nothing kubectl could rewrite.
	if discovery, _, err := isolateKubeconfigs(fleet, runTemp, "east1", "/no/such/kubeconfig", ""); err != nil || discovery != "/no/such/kubeconfig" {
		t.Fatalf("a missing kubeconfig: %q %v", discovery, err)
	}
}

// `--repo .` from the fleet root: the fleet path is relative, the kubeconfig
// path absolute. The kubeconfig is inside all the same.
func TestIsolateKubeconfigs_RelativeFleetPath(t *testing.T) {
	fleet, inside, runTemp := kubeconfigFleet(t)
	t.Chdir(fleet)
	for _, rel := range []string{".", "./", "../" + filepath.Base(fleet)} {
		discovery, _, err := isolateKubeconfigs(rel, runTemp, "east1", inside, "")
		if err != nil || !isPrivate(runTemp, "kubeconfig-discovery", discovery) {
			t.Fatalf("fleet %q: the kubeconfig in the checkout was not isolated: %q %v", rel, discovery, err)
		}
	}
	// And a relative kubeconfig path.
	if discovery, _, err := isolateKubeconfigs(".", runTemp, "east1", "kubeconfig", ""); err != nil || !isPrivate(runTemp, "kubeconfig-discovery", discovery) {
		t.Fatalf("a relative kubeconfig path: %q %v", discovery, err)
	}
}

// A relative spelling means one file here and another in the directory the
// script runs in. Run from <work>/clients/acme/fleet with
// ../../../configs/east1: that is <work>/configs/east1, outside the checkout.
// From the private checkout the same spelling would open a file of the fleet.
// The script gets a spelling that opens the operator's file from anywhere.
func TestIsolateKubeconfigs_RelativeSpellingIsMadeAbsolute(t *testing.T) {
	work := t.TempDir()
	fleet := filepath.Join(work, "clients", "acme", "fleet")
	meant := filepath.Join(work, "configs", "east1")
	for _, dir := range []string{fleet, filepath.Dir(meant)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(meant, []byte(strings.Replace(kubeconfigBody, "    certificate-authority: ca.crt\n", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Chdir(fleet)
	runTemp := filepath.Join(t.TempDir(), "run.tmp")
	rel := filepath.Join("..", "..", "..", "configs", "east1")
	discovery, env, err := isolateKubeconfigs(".", runTemp, "east1", rel, rel)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(discovery) {
		t.Fatalf("the script, which runs in another directory, was given the relative spelling %q", discovery)
	}
	got, _ := os.Stat(discovery)
	want, _ := os.Stat(meant)
	if got == nil || !os.SameFile(got, want) {
		t.Fatalf("%q is not the file the operator named (%s)", discovery, meant)
	}
	// $KUBECONFIG was read here, where the operator wrote it.
	if !strings.Contains(mustRead(t, env), "token: tok") {
		t.Fatalf("the snapshot of the relative $KUBECONFIG lacks its content:\n%s", mustRead(t, env))
	}
}

// /ops/current is a link to <fleet>/clusters, so /ops/current/../east1_config
// is <fleet>/east1_config, although the spelling, cleaned as text, says
// /ops/east1_config. The file is in the checkout: it is copied.
func TestIsolateKubeconfigs_LinkThenDotDot(t *testing.T) {
	fleet, inside, runTemp := kubeconfigFleet(t)
	if err := os.Mkdir(filepath.Join(fleet, "clusters"), 0o755); err != nil {
		t.Fatal(err)
	}
	ops := t.TempDir()
	if err := os.Symlink(filepath.Join(fleet, "clusters"), filepath.Join(ops, "current")); err != nil {
		t.Fatal(err)
	}
	spelled := ops + "/current/../" + filepath.Base(inside)
	if _, err := os.Stat(spelled); err != nil {
		t.Fatalf("the spelling does not open the checkout's kubeconfig: %v", err)
	}
	discovery, _, err := isolateKubeconfigs(fleet, runTemp, "east1", spelled, "")
	if err != nil || !isPrivate(runTemp, "kubeconfig-discovery", discovery) {
		t.Fatalf("a kubeconfig of the checkout, reached through a link and .., was not isolated: %q %v", discovery, err)
	}
	// The same, relative to the working directory.
	t.Chdir(ops)
	discovery, _, err = isolateKubeconfigs(fleet, runTemp, "east1", "current/../"+filepath.Base(inside), "")
	if err != nil || !isPrivate(runTemp, "kubeconfig-discovery", discovery) {
		t.Fatalf("the relative form: %q %v", discovery, err)
	}
}

// A second name (hard link) outside the checkout for a kubeconfig inside it:
// kubectl rewrites in place, so writing through either name changes both.
func TestIsolateKubeconfigs_HardLinkedIsCopied(t *testing.T) {
	fleet, inside, runTemp := kubeconfigFleet(t)
	outside := filepath.Join(filepath.Dir(runTemp), "second-name")
	if err := os.Link(inside, outside); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	discovery, _, err := isolateKubeconfigs(fleet, runTemp, "east1", outside, "")
	if err != nil || !isPrivate(runTemp, "kubeconfig-discovery", discovery) {
		t.Fatalf("a kubeconfig with a second name was handed to the script: %q %v", discovery, err)
	}
}

// A fleet checkout that cannot be resolved is an error, never "outside".
func TestIsolateKubeconfigs_UnresolvableFleetIsAnError(t *testing.T) {
	_, inside, runTemp := kubeconfigFleet(t)
	if _, _, err := isolateKubeconfigs(filepath.Join(t.TempDir(), "gone"), runTemp, "east1", inside, ""); err == nil {
		t.Fatal("want an error")
	}
}

// With no explicit path the script reads ~/.kube/<cluster>_config. When that
// is a link into the checkout, the script is given a private copy instead.
func TestIsolateKubeconfigs_ScriptDefaultLinkedIntoTheCheckout(t *testing.T) {
	fleet, inside, runTemp := kubeconfigFleet(t)
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(home, ".kube", "east1_config")); err != nil {
		t.Fatal(err)
	}
	discovery, _, err := isolateKubeconfigs(fleet, runTemp, "east1", "", "")
	if err != nil || !isPrivate(runTemp, "kubeconfig-discovery", discovery) {
		t.Fatalf("the script's default kubeconfig leads into the checkout and was not isolated: %q %v", discovery, err)
	}
	// Another cluster's default is not this run's input.
	if discovery, _, err := isolateKubeconfigs(fleet, runTemp, "west2", "", ""); err != nil || discovery != "" {
		t.Fatalf("west2: %q %v", discovery, err)
	}
	// Outside the checkout, the default stays the script's own business.
	if err := os.Remove(filepath.Join(home, ".kube", "east1_config")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kube", "east1_config"), []byte(kubeconfigBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if discovery, _, err := isolateKubeconfigs(fleet, runTemp, "east1", "", ""); err != nil || discovery != "" {
		t.Fatalf("a default outside the checkout: %q %v", discovery, err)
	}
}

// With $KUBECONFIG unset kubectl reads ~/.kube/config. The scripts get a
// snapshot of that too, so it does not matter where it really is.
func TestIsolateKubeconfigs_UnsetReadsTheHomeDefault(t *testing.T) {
	fleet, inside, runTemp := kubeconfigFleet(t)
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(home, ".kube", "config")); err != nil {
		t.Fatal(err)
	}
	// client-go computed the default's location when the test binary
	// started; point it at this test's home.
	old := clientcmd.RecommendedHomeFile
	clientcmd.RecommendedHomeFile = filepath.Join(home, ".kube", "config")
	t.Cleanup(func() { clientcmd.RecommendedHomeFile = old })
	_, env, err := isolateKubeconfigs(fleet, runTemp, "east1", "", "")
	if err != nil || !isPrivate(runTemp, "kubeconfig-env", env) || !strings.Contains(mustRead(t, env), "token: tok") {
		t.Fatalf("want a snapshot of ~/.kube/config, got %q %v", env, err)
	}
}

// A KUBECONFIG list becomes one private file holding the merged view; the
// explicit path gets its own copy.
func TestIsolateKubeconfigs_ListAndDistinctCopies(t *testing.T) {
	fleet, inside, runTemp := kubeconfigFleet(t)
	other := filepath.Join(t.TempDir(), "other")
	otherBody := "apiVersion: v1\nkind: Config\nclusters:\n- name: o\n  cluster: {server: 'https://192.0.2.20:6443'}\ncontexts:\n- name: o\n  context: {cluster: o, user: ou}\nusers:\n- name: ou\n  user: {token: other-tok}\n"
	if err := os.WriteFile(other, []byte(otherBody), 0o600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(fleet, "second")
	if err := os.WriteFile(second, []byte(strings.ReplaceAll(otherBody, "other-tok", "second-tok")), 0o600); err != nil {
		t.Fatal(err)
	}
	list := other + string(os.PathListSeparator) + inside + string(os.PathListSeparator) + "/no/such"
	discovery, env, err := isolateKubeconfigs(fleet, runTemp, "east1", second, list)
	if err != nil || !isPrivate(runTemp, "kubeconfig-discovery", discovery) || !isPrivate(runTemp, "kubeconfig-env", env) {
		t.Fatalf("got %q %q %v", discovery, env, err)
	}
	if discovery == env {
		t.Fatal("the explicit kubeconfig and $KUBECONFIG share one copy: one overwrote the other")
	}
	if d := mustRead(t, discovery); !strings.Contains(d, "second-tok") || strings.Contains(d, "other-tok") {
		t.Fatalf("the discovery copy is not the explicit kubeconfig:\n%s", d)
	}
	e := mustRead(t, env)
	for _, want := range []string{"other-tok", "token: tok", "name: o", "name: c"} {
		if !strings.Contains(e, want) {
			t.Fatalf("the merged copy lacks %q:\n%s", want, e)
		}
	}
}

// A context that names a certificate file that is gone (an old local cluster)
// does not stop the run: kubectl does not mind it either.
func TestIsolateKubeconfigs_StaleFileReferenceIsNotAnError(t *testing.T) {
	fleet, inside, runTemp := kubeconfigFleet(t)
	if err := os.Remove(filepath.Join(fleet, "ca.crt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := isolateKubeconfigs(fleet, runTemp, "east1", inside, inside); err != nil {
		t.Fatalf("a kubeconfig naming a missing CA file was refused: %v", err)
	}
}

// The copy is in another directory: what the original names by relative path
// (a CA file, a token file, an exec plugin) must still be found.
func TestIsolateKubeconfigs_CopyKeepsRelativeReferencesWorking(t *testing.T) {
	fleet, _, runTemp := kubeconfigFleet(t)
	body := "apiVersion: v1\nkind: Config\ncurrent-context: c\nclusters:\n- name: c\n  cluster: {server: 'https://192.0.2.10:6443', certificate-authority: ca.crt}\ncontexts:\n- name: c\n  context: {cluster: c, user: u}\nusers:\n- name: u\n  user:\n    tokenFile: creds/token\n- name: x\n  user:\n    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: ./bin/login\n      interactiveMode: Never\n- name: p\n  user:\n    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: kubelogin\n      interactiveMode: Never\n"
	path := filepath.Join(fleet, "relative")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	discovery, _, err := isolateKubeconfigs(fleet, runTemp, "east1", path, "")
	if err != nil {
		t.Fatal(err)
	}
	copied := mustRead(t, discovery)
	root, _ := filepath.EvalSymlinks(fleet)
	for _, want := range []string{
		"certificate-authority: " + filepath.Join(fleet, "ca.crt"),
		"tokenFile: " + filepath.Join(fleet, "creds", "token"),
		"command: " + filepath.Join(fleet, "bin", "login"),
		"command: kubelogin", // found on PATH: left alone
	} {
		if !strings.Contains(copied, want) && !strings.Contains(copied, strings.Replace(want, fleet, root, 1)) {
			t.Fatalf("the copy lacks %q:\n%s", want, copied)
		}
	}
}

// A kubeconfig that cannot be read stops the run, and the run cleans up after
// itself: no private checkout stays registered, TMPDIR and KUBECONFIG are
// what they were.
func TestApply_Isolated_KubeconfigCopyFailureCleansUp(t *testing.T) {
	repo := committedFleet(t)
	t.Setenv("HOME", t.TempDir())
	operatorTemp := t.TempDir()
	t.Setenv("TMPDIR", operatorTemp)
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("/kubeconfig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(repo, "kubeconfig")
	if err := os.WriteFile(inside, []byte("clusters: [not, a, kubeconfig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", inside)
	runner := applyRunner(t, repo)
	opts := east1ApplyOpts(t, repo, runner, plainGit{realGit()})
	opts.KubeconfigPath = inside
	err := Apply(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "kubeconfig") {
		t.Fatalf("want the kubeconfig error, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("a script ran: %v", runner.calls)
	}
	if got := os.Getenv("TMPDIR"); got != operatorTemp {
		t.Fatalf("TMPDIR was left at %s", got)
	}
	if got := os.Getenv("KUBECONFIG"); got != inside {
		t.Fatalf("KUBECONFIG was left at %s", got)
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output(); err != nil || strings.Count(string(out), "worktree ") != 1 {
		t.Fatalf("a private checkout stayed registered (%v):\n%s", err, out)
	}
	if left, _ := filepath.Glob(filepath.Join(repo, ".git", "kube-dc-init", "run-*")); len(left) != 0 {
		t.Fatalf("the run left %v", left)
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the checkout changed:\n%s", st)
	}
}

// Through Apply: the scripts of the private checkout see the snapshot of
// $KUBECONFIG, and when the run is over the operator's value is back.
func TestApply_Isolated_ScriptsGetASnapshotOfKUBECONFIG(t *testing.T) {
	repo := committedFleet(t)
	t.Setenv("HOME", t.TempDir())
	outside := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(outside, []byte(strings.Replace(kubeconfigBody, "    certificate-authority: ca.crt\n", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", outside)
	runner := applyRunner(t, repo)
	var during string
	opts := east1ApplyOpts(t, repo, runner, plainGit{realGit()})
	opts.NoPush = true
	opts.Reporter = stepHook{after: StepScaffold, fn: func() { during = os.Getenv("KUBECONFIG") }}
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if during == outside || !strings.Contains(during, "kube-dc-init") || filepath.Base(during) != "kubeconfig-env" {
		t.Fatalf("while the private checkout existed KUBECONFIG was %q", during)
	}
	if got := os.Getenv("KUBECONFIG"); got != outside {
		t.Fatalf("KUBECONFIG was left at %s", got)
	}
}

// The age key Apply exported for the fleet is taken back when Apply returns.
func TestExportFleetAgeKey_IsTakenBack(t *testing.T) {
	fleet := t.TempDir()
	if err := os.WriteFile(filepath.Join(fleet, "age.key"), []byte("AGE-SECRET-KEY-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY", "")
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	restore := exportFleetAgeKey(fleet)
	if os.Getenv("SOPS_AGE_KEY_FILE") == "" {
		t.Fatal("the fleet's key was not exported")
	}
	restore()
	if got, had := os.LookupEnv("SOPS_AGE_KEY_FILE"); got != "" || !had {
		t.Fatalf("want the variable as it was (set, empty), got %q set=%v", got, had)
	}
	// Not set at all before: not set after.
	os.Unsetenv("SOPS_AGE_KEY_FILE")
	exportFleetAgeKey(fleet)()
	if got, had := os.LookupEnv("SOPS_AGE_KEY_FILE"); had {
		t.Fatalf("the fleet's key stayed exported: %q", got)
	}
	// A key the operator selected is not touched, either way.
	t.Setenv("SOPS_AGE_KEY_FILE", "/operator/key")
	exportFleetAgeKey(fleet)()
	if got := os.Getenv("SOPS_AGE_KEY_FILE"); got != "/operator/key" {
		t.Fatalf("the operator's key selection was changed to %q", got)
	}
}

// The fleet path is spelled through a link and "..": <fleet>/current is a link
// into another worktree (on another branch), so <fleet>/current/.. IS that
// other worktree, although the spelling, cleaned as text, says <fleet>. Every
// part of the run must work in the same one: the directory the path opens.
func TestApply_Isolated_FleetPathThroughALinkAndDotDot(t *testing.T) {
	fleet := committedFleet(t)
	if err := os.WriteFile(filepath.Join(fleet, ".git", "info", "exclude"), []byte("/current\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "stage")
	runGit(t, fleet, "worktree", "add", "-q", "-b", "stage", stage)
	sub := filepath.Join(stage, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sub, filepath.Join(fleet, "current")); err != nil {
		t.Fatal(err)
	}
	pre := revParse(t, fleet, "main")
	spelled := fleet + "/current/.."
	runner := applyRunner(t, stage)
	opts := east1ApplyOpts(t, spelled, runner, plainGit{realGit()})
	opts.NoPush = true
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := revParse(t, fleet, "main"); got != pre {
		t.Fatalf("main moved to %s: the run captured the branch of a directory the path does not open", got)
	}
	if got := revParse(t, fleet, "stage"); got == pre || got == "" {
		t.Fatalf("stage, the branch of the worktree the path opens, did not receive the commit (%s)", got)
	}
	if _, err := os.Stat(filepath.Join(stage, "clusters", "east1", "cluster-config.env")); err != nil {
		t.Fatalf("the overlay is not in the worktree the path opens: %v", err)
	}
	if st := status(t, fleet); st != "" {
		t.Fatalf("the other checkout changed:\n%s", st)
	}
	if st := status(t, stage); st != "" {
		t.Fatalf("the worktree is not clean after the run:\n%s", st)
	}
}

// A resume whose tip is already on the remote: the push has nothing new to
// scan, and the managed secret files of that tip are checked all the same.
func TestApply_Isolated_ResumeChecksEveryManagedFileOfAPublishedTip(t *testing.T) {
	repo := committedFleet(t)
	first := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	first.NoPush = true
	if err := Apply(context.Background(), first); err != nil {
		t.Fatalf("run 1 (--no-push): %v", err)
	}
	const enc = "ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]"
	body := "kind: Secret\nmetadata:\n  name: wildcard-tls\n  annotations:\n    kubectl.kubernetes.io/last-applied-configuration: '{\"stringData\":{\"tls.key\":\"-----BEGIN PRIVATE KEY-----\"}}'\nstringData:\n  tls.key: " + enc + "\nsops:\n  mac: " + enc + "\n"
	if err := os.WriteFile(filepath.Join(repo, "clusters", "east1", "wildcard-tls-secrets.enc.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "clusters/east1/wildcard-tls-secrets.enc.yaml")
	runGit(t, repo, "commit", "-q", "-m", "a Secret exported with kubectl --save-config")
	bareOrigin(t, repo)
	runGit(t, repo, "push", "-q", "origin", "main") // the tip is on the remote already
	runner := applyRunner(t, repo)
	err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "wildcard-tls-secrets.enc.yaml") || !strings.Contains(err.Error(), "last-applied-configuration") {
		t.Fatalf("want a refusal naming the file and the annotation, got %v", err)
	}
	if ranFluxInstall(runner) {
		t.Fatal("flux-install ran")
	}
}

// flux-install.sh runs in the fleet repository, not where the operator
// started: a relative kubeconfig is handed over as an absolute path, or it
// would name a file of the fleet.
func TestApply_FluxInstallGetsAnAbsoluteKubeconfig(t *testing.T) {
	start := t.TempDir()
	t.Chdir(start)
	cwd, _ := os.Getwd()
	fluxEnv := func(t *testing.T, explicit string) string {
		repo := applyFleet(t)
		runner := applyRunner(t, repo)
		opts := east1ApplyOpts(t, repo, runner, &fakeGit{preSHA: "abc123"})
		opts.KubeconfigPath = explicit
		if err := Apply(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		for _, call := range runner.calls {
			if call.Kind == ports.ScriptFluxInstall {
				return call.Env["KUBECONFIG"]
			}
		}
		t.Fatal("flux-install did not run")
		return ""
	}
	t.Setenv("KUBECONFIG", "")
	if got, want := fluxEnv(t, filepath.Join("config", "east1")), cwd+string(os.PathSeparator)+filepath.Join("config", "east1"); got != want {
		t.Fatalf("the explicit kubeconfig: flux-install got %q, want %q", got, want)
	}
	t.Setenv("KUBECONFIG", filepath.Join("config", "a")+string(os.PathListSeparator)+"/abs/b")
	if got, want := fluxEnv(t, ""), cwd+string(os.PathSeparator)+filepath.Join("config", "a")+string(os.PathListSeparator)+"/abs/b"; got != want {
		t.Fatalf("a relative entry of $KUBECONFIG: flux-install got %q, want %q", got, want)
	}
	// Nothing relative: the environment is left to the runner, as before.
	t.Setenv("KUBECONFIG", "/abs/only")
	if got := fluxEnv(t, ""); got != "" {
		t.Fatalf("an absolute $KUBECONFIG was rewritten to %q", got)
	}
}

// providerFleet is a committed fleet whose starter supports a provider host
// other than the default, with an HTTPS origin at git.example.net.
func providerFleet(t *testing.T) string {
	t.Helper()
	repo := committedFleet(t)
	script := filepath.Join(repo, "bootstrap", "flux-install.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(script, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n# scaffold-capability: provider-host-v1\n")
	f.Close()
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "provider-host capable starter")
	runGit(t, repo, "remote", "add", "origin", "https://git.example.net/acme/fleet.git")
	return repo
}

// The fleet is given through a link (~/fleet -> /srv/fleet), with the real
// adapter and its provider binding, as in production. The binding reviews the
// checkout once and later refuses any other directory: it must have reviewed
// the same directory the run then publishes from. Here the run gets as far as
// the destination check, which refuses the old instance's SSH address.
func TestApply_Isolated_FleetThroughALinkWithTheProviderBinding(t *testing.T) {
	providerStub(t, 200, `{"ssh_url_to_repo":"git@git.example.net:acme/fleet.git"}`)
	repo := providerFleet(t)
	runGit(t, repo, "config", "remote.origin.pushurl", "ssh://git@git.example.net:2222/acme/fleet.git")
	link := filepath.Join(t.TempDir(), "fleet")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	runner := applyRunner(t, repo)
	opts := east1ApplyOpts(t, link, runner, realGit()) // not plainGit: the binding takes part
	opts.Provider, opts.ProviderHost = ProviderGitLab, "git.example.net"
	opts.GitHubOwner, opts.GitHubRepo = "acme", "fleet"
	err := Apply(context.Background(), opts)
	if err == nil || strings.Contains(err.Error(), "another reviewed checkout") {
		t.Fatalf("the provider binding reviewed another spelling of the fleet than the run uses: %v", err)
	}
	if !strings.Contains(err.Error(), "another server, login or repository") || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("want the run to reach the destination check and be refused there, got %v", err)
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the checkout changed:\n%s", st)
	}
}

// A link where init writes for the cluster, even one that stays inside the
// repository: clusters/east1/tenant-addons.yaml -> ../west1/tenant-addons.yaml
// (to reuse west1's baseline). Generating east1 would replace west1's file
// and publish that.
func TestApply_Isolated_RefusesALinkInTheClusterOverlay(t *testing.T) {
	repo := committedFleet(t)
	west := filepath.Join(repo, "clusters", "west1")
	east := filepath.Join(repo, "clusters", "east1")
	for _, dir := range []string{west, east} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const westBody = "# west1's own tenant baseline\n"
	if err := os.WriteFile(filepath.Join(west, "tenant-addons.yaml"), []byte(westBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "west1", "tenant-addons.yaml"), filepath.Join(east, "tenant-addons.yaml")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "east1 reuses west1's tenant baseline")
	pre := revParse(t, repo, "HEAD")
	runner := applyRunner(t, repo)
	opts := east1ApplyOpts(t, repo, runner, plainGit{realGit()})
	opts.NoPush = true
	err := Apply(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "clusters/east1/tenant-addons.yaml") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a refusal naming the link, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("a script ran: %v", runner.calls)
	}
	if got := revParse(t, repo, "HEAD"); got != pre {
		t.Fatalf("a commit was made: %s", got)
	}
	if got := mustRead(t, filepath.Join(west, "tenant-addons.yaml")); got != westBody {
		t.Fatalf("west1's file was replaced:\n%s", got)
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the checkout changed:\n%s", st)
	}
}

// platform/registry-depot is a link to ../shared/depot: Git stores the
// credential under shared/depot, where the check of
// platform/registry-depot/secret.enc.yaml does not look. A decrypted
// credential committed there must not be published by a resume.
func TestApply_Isolated_RefusesALinkedRegistryDepot(t *testing.T) {
	repo := committedFleet(t)
	first := east1ApplyOpts(t, repo, applyRunner(t, repo), plainGit{realGit()})
	first.NoPush = true
	if err := Apply(context.Background(), first); err != nil {
		t.Fatalf("run 1 (--no-push): %v", err)
	}
	depot := filepath.Join(repo, "platform", "registry-depot")
	shared := filepath.Join(repo, "shared", "depot")
	if err := os.MkdirAll(filepath.Dir(shared), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(depot); err != nil {
		if err := os.MkdirAll(depot, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(depot, shared); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "shared", "depot"), depot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "secret.enc.yaml"), []byte("kind: Secret\nstringData:\n  password: hunter2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "depot moved; credential decrypted for maintenance")
	bare := bareOrigin(t, repo)
	runner := applyRunner(t, repo)
	err := Apply(context.Background(), east1ApplyOpts(t, repo, runner, plainGit{realGit()}))
	if err == nil || !strings.Contains(err.Error(), "platform/registry-depot") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a refusal naming the link, got %v", err)
	}
	if revParse(t, bare, "main") != "" {
		t.Fatal("the decrypted credential was pushed")
	}
	if ranFluxInstall(runner) {
		t.Fatal("flux-install ran")
	}
}

// Every place a link is refused: the directories leading to what init
// manages, and anything below them. Links elsewhere are not its business.
func TestRefuseManagedLinks(t *testing.T) {
	for _, tc := range []struct {
		link string
		ok   bool
	}{
		{"clusters", false},
		{"clusters/east1", false},
		{"clusters/east1/patches", false},
		{"clusters/east1/patches/x.yaml", false},
		{"platform", false},
		{"platform/registry-depot", false},
		{"clusters/eu", false}, // on the way to the nested cluster eu/east1
		{"clusters/eu/east1", false},
		{"clusters/eu/west1", true},
		{"clusters/west1", true}, // another cluster
		{"docs/current", true},
	} {
		work := t.TempDir()
		name := "east1"
		if strings.HasPrefix(tc.link, "clusters/eu") {
			name = "eu/east1" // a nested cluster name
		}
		target := filepath.Join(work, "elsewhere")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{"clusters/" + name + "/patches", "platform/registry-depot", "docs"} {
			full := filepath.Join(work, dir)
			if strings.HasPrefix(dir, tc.link) && (len(dir) == len(tc.link) || dir[len(tc.link)] == '/') {
				full = filepath.Dir(filepath.Join(work, tc.link)) // stop above the link
			}
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, filepath.Join(work, tc.link)); err != nil {
			t.Fatal(err)
		}
		err := refuseManagedLinks(work, name)
		if (err == nil) != tc.ok {
			t.Errorf("a link at %s: ok=%v, got %v", tc.link, tc.ok, err)
		}
		if err != nil && !strings.Contains(err.Error(), tc.link) {
			t.Errorf("the refusal does not name %s: %v", tc.link, err)
		}
	}
	// No overlay yet, no platform directory: nothing to refuse.
	if err := refuseManagedLinks(t.TempDir(), "east1"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderAPIURL(t *testing.T) {
	for _, tc := range []struct {
		provider          Provider
		host, owner, repo string
		want              string
	}{
		{ProviderGitLab, "gitlab.com", "acme", "fleet", "https://gitlab.com/api/v4/projects/acme%2Ffleet"},
		{ProviderGitLab, "git.example.net:8443", "group/sub", "fleet", "https://git.example.net:8443/api/v4/projects/group%2Fsub%2Ffleet"},
		{ProviderGitHub, "github.com", "acme", "fleet", "https://api.github.com/repos/acme/fleet"},
		{"", "GitHub.com", "acme", "fleet", "https://api.github.com/repos/acme/fleet"},
		{ProviderGitHub, "ghe.example.net", "acme", "fleet", "https://ghe.example.net/api/v3/repos/acme/fleet"},
	} {
		if got := providerAPIURL(tc.provider, tc.host, tc.owner, tc.repo); got != tc.want {
			t.Errorf("%v %s %s/%s: got %s want %s", tc.provider, tc.host, tc.owner, tc.repo, got, tc.want)
		}
	}
}

// End to end through Apply with the real Git adapter: the checkout's push URL
// is the old instance's SSH address (another port); the provider says the
// project is on port 22. Nothing is sent and flux-install does not run. With
// the project's own SSH address the check passes (the push then fails only
// because no SSH server is there to receive it).
func TestApply_Isolated_SSHPushToAnotherInstanceRefused(t *testing.T) {
	providerStub(t, 200, `{"ssh_url_to_repo":"git@git.example.net:acme/fleet.git"}`)
	for _, tc := range []struct {
		name, pushURL, want string
	}{
		{"the old instance, on another port", "ssh://git@git.example.net:2222/acme/fleet.git", "another server, login or repository"},
		{"another repository on the right server", "git@git.example.net:acme/other.git", "they must be the same repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := committedFleet(t)
			// A starter that supports a provider host other than the default.
			script := filepath.Join(repo, "bootstrap", "flux-install.sh")
			if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(script, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o755)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.WriteString("\n# scaffold-capability: provider-host-v1\n")
			f.Close()
			runGit(t, repo, "add", "-A")
			runGit(t, repo, "commit", "-q", "-m", "provider-host capable starter")
			runGit(t, repo, "remote", "add", "origin", "https://git.example.net/acme/fleet.git")
			runGit(t, repo, "config", "remote.origin.pushurl", tc.pushURL)
			runner := applyRunner(t, repo)
			opts := east1ApplyOpts(t, repo, runner, plainGit{realGit()})
			opts.Provider, opts.ProviderHost = ProviderGitLab, "git.example.net"
			opts.GitHubOwner, opts.GitHubRepo = "acme", "fleet"
			err = Apply(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "nothing was pushed") {
				t.Fatalf("want a refusal (%q) before anything is sent, got %v", tc.want, err)
			}
			if ranFluxInstall(runner) {
				t.Fatal("flux-install ran")
			}
			if st := status(t, repo); st != "" {
				t.Fatalf("the checkout changed:\n%s", st)
			}
		})
	}
}
