package clusterinit

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/adapters/script"
)

// Apply with the real script runner and the real Fleet scripts: add-cluster.sh
// must run from the private checkout (not the fleet checkout), and the result
// must reach the fleet checkout only through the fast-forward.
func TestApply_Isolated_RealFleetScripts(t *testing.T) {
	src := os.Getenv("KUBE_DC_TEST_FLEET")
	if src == "" {
		t.Skip("set KUBE_DC_TEST_FLEET to a Fleet checkout to run the real scripts")
	}
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops is not installed")
	}
	repo := filepath.Join(t.TempDir(), "fleet")
	if out, err := exec.Command("cp", "-a", src, repo).CombinedOutput(); err != nil {
		t.Fatalf("copy the fleet: %v: %s", err, out)
	}
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "starter")
	pre := revParse(t, repo, "HEAD")

	var out bytes.Buffer
	opts := east1ApplyOpts(t, repo, script.New(repo, "", nil), plainGit{realGit()})
	opts.NoPush = true
	opts.Out = &out
	opts.Plan.InstallationKind = "kube-dc"
	opts.Plan.ManagedServicesMode = "off"
	opts.Plan.ManagerNodes = 1
	opts.Plan.IngressAddressLayer = "none"
	for k, v := range map[string]string{
		"OVN_DB_IPS": "tcp:192.0.2.10:6641", "KUBE_OVN_MASTER_NODES": "192.0.2.10", "KUBE_OVN_GW_NODES": "server-1",
		"NODE_CIDR": "192.0.2.0/24", "EXT_NET_EXCLUDE_IPS": "100.65.0.1..100.65.0.20", "EXT_PUBLIC_EXCLUDE_IPS_1": "10.0.0.1..10.0.0.12",
	} {
		opts.Sets[k] = v
	}
	if err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), filepath.Join(repo, ".git", "kube-dc-init")) {
		t.Errorf("add-cluster.sh did not run from the private checkout:\n%s", out.String())
	}
	if revParse(t, repo, "HEAD") == pre {
		t.Fatal("no commit")
	}
	if st := status(t, repo); st != "" {
		t.Fatalf("the fleet checkout is not clean after the run:\n%s", st)
	}
	tree := gitOutput(t, repo, "ls-tree", "-r", "--name-only", "HEAD", "--", "clusters/east1")
	for _, want := range []string{"clusters/east1/cluster-config.env", "clusters/east1/secrets.enc.yaml", "clusters/east1/kustomization.yaml"} {
		if !strings.Contains(tree, want) {
			t.Errorf("%s is not in the commit:\n%s", want, tree)
		}
	}
	secret, err := os.ReadFile(filepath.Join(repo, "clusters", "east1", "secrets.enc.yaml"))
	if err != nil || !strings.Contains(string(secret), "ENC[") {
		t.Fatalf("secrets.enc.yaml is missing or not encrypted (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "kube-dc-init")); !os.IsNotExist(err) {
		t.Errorf("the private checkout was left behind (%v)", err)
	}
}
