package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/oidccutover"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

type failingManagedPublicationGit struct {
	ports.GitClient
	resetTo string
	head    string
	prior   string
	newSHA  string
	dirty   bool
}

func TestManagedServicesFinalizeAcceptsReachableServerTargets(t *testing.T) {
	repo := ""
	cmd := bootstrapServicesFinalizeCmd(&repo)
	flag := cmd.Flags().Lookup("ssh-host")
	if flag == nil || flag.Value.Type() != "stringArray" {
		t.Fatal("services finalize must accept one reachable SSH target per control-plane node")
	}
}

func TestManagedServicesSSHHostsKeepLiveNodeIdentity(t *testing.T) {
	nodes := []oidccutover.Node{
		{Name: "master-1", InternalIP: "10.77.0.110", Host: ports.SSHHost{Hostname: "10.77.0.110"}},
		{Name: "master-2", InternalIP: "10.77.0.111", Host: ports.SSHHost{Hostname: "10.77.0.111"}},
	}
	got, err := mapManagedServicesSSHHosts(nodes, []string{"master-2=ubuntu@192.0.2.2", "master-1=primary-alias"}, "")
	if err != nil || got[0].Name != "master-1" || got[0].Host.Alias != "primary-alias" ||
		got[1].Name != "master-2" || got[1].InternalIP != "10.77.0.111" ||
		got[1].Host.Alias != "192.0.2.2" || got[1].Host.User != "ubuntu" {
		t.Fatalf("SSH target mapping lost live Node identity: nodes=%+v err=%v", got, err)
	}
	if _, err := mapManagedServicesSSHHosts(nodes, []string{"primary-alias"}, ""); err == nil {
		t.Fatal("accepted an incomplete multi-server SSH target set")
	}
	if _, err := mapManagedServicesSSHHosts(nodes, []string{"master-1=primary-alias", "missing=other"}, ""); err == nil {
		t.Fatal("accepted an SSH target for a non-member")
	}
	sole, err := mapManagedServicesSSHHosts(nodes[:1], []string{"primary-alias"}, "")
	if err != nil || sole[0].Name != "master-1" || sole[0].Host.Alias != "primary-alias" {
		t.Fatalf("single-server shorthand failed: nodes=%+v err=%v", sole, err)
	}
}

func (g *failingManagedPublicationGit) Head(context.Context, string) (string, error) {
	if g.head == "" {
		return g.prior, nil
	}
	return g.head, nil
}
func (g *failingManagedPublicationGit) CommitAndPush(context.Context, string, string, string) (string, error) {
	g.head = g.newSHA
	return g.newSHA, errors.New("push unavailable")
}
func (g *failingManagedPublicationGit) Diff(context.Context, string) (ports.Diff, error) {
	if g.dirty {
		return ports.Diff{Files: []ports.FileDiff{{Path: "foreign.txt", Status: "M"}}}, nil
	}
	return ports.Diff{}, nil
}
func (g *failingManagedPublicationGit) ResetHard(_ context.Context, _, ref string) error {
	g.resetTo = ref
	return nil
}

func TestManagedServicesPublicationRollsBackFailedPush(t *testing.T) {
	repo := initManagedServicesRollbackRepo(t)
	prior := managedServicesTestCommit(t, repo, "prior")
	newSHA := managedServicesTestCommit(t, repo, "ours")
	git := &failingManagedPublicationGit{prior: prior, newSHA: newSHA}
	if err := commitManagedServicesPublication(context.Background(), git, repo, "sample", "token"); err == nil || git.resetTo != prior {
		t.Fatalf("failed push left a publication commit for generic bootstrap retry: reset=%q err=%v", git.resetTo, err)
	}
}

func initManagedServicesRollbackRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "test"}, {"config", "user.email", "test@example.invalid"}} {
		if output, err := execGit(t, repo, args...); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	return repo
}

func managedServicesTestCommit(t *testing.T, repo, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "marker"), []byte(message), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "marker"}, {"commit", "-m", message}} {
		if output, err := execGit(t, repo, args...); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	sha, err := execGit(t, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(sha)
}

func TestManagedServicesPublicationPreservesConcurrentWork(t *testing.T) {
	for _, tc := range []struct {
		name        string
		foreignHead bool
		foreignBase bool
		dirty       bool
	}{
		{name: "new commit", foreignHead: true},
		{name: "commit between captured head and ours", foreignBase: true},
		{name: "worktree edit", dirty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initManagedServicesRollbackRepo(t)
			prior := managedServicesTestCommit(t, repo, "prior")
			if tc.foreignBase {
				managedServicesTestCommit(t, repo, "other before ours")
			}
			ours := managedServicesTestCommit(t, repo, "ours")
			git := &failingManagedPublicationGit{prior: prior, head: ours, dirty: tc.dirty}
			if tc.foreignHead {
				git.head = managedServicesTestCommit(t, repo, "other after ours")
			}
			err := rollbackOwnManagedServicesCommit(context.Background(), git, repo, prior, ours, errors.New("push unavailable"))
			if err == nil || git.resetTo != "" {
				t.Fatalf("concurrent work was reset: reset=%q err=%v", git.resetTo, err)
			}
		})
	}
}

func TestManagedServicesDay2CommandsAndServerSetGuard(t *testing.T) {
	repo := ""
	cmd := bootstrapServicesCmd(&repo)
	if cmd.CommandPath() == "" || cmd.Commands()[0] == nil || cmd.Commands()[1] == nil {
		t.Fatal("services day-2 commands missing")
	}
	servers, err := parseManagedServicesServerArgs([]string{"node-a=192.0.2.1", "node-b=192.0.2.2"})
	if err != nil || len(servers) != 2 {
		t.Fatalf("server inventory: %v %v", servers, err)
	}
	plane := map[string]any{"spec": map[string]any{"backupAdmission": map[string]any{"servers": []any{
		map[string]any{"address": "192.0.2.1:6443"}, map[string]any{"address": "192.0.2.2:6443"},
	}}}}
	if err := sameManagedServicesServerAddresses(plane, servers); err != nil {
		t.Fatal(err)
	}
	if err := sameManagedServicesInventory(servers, servers); err != nil {
		t.Fatal(err)
	}
	if err := sameManagedServicesInventory(servers, []managedServicesServer{{Name: "node-a", IP: "192.0.2.1"}, {Name: "node-a", IP: "192.0.2.1"}}); err == nil {
		t.Fatal("duplicate node omitted another live server")
	}
	servers[1].IP = "192.0.2.3"
	if err := sameManagedServicesServerAddresses(plane, servers); err == nil || !strings.Contains(err.Error(), "IP set changed") {
		t.Fatalf("changed membership accepted: %v", err)
	}
	if _, err := parseManagedServicesServerArgs([]string{"192.0.2.1"}); err == nil {
		t.Fatal("unmapped API server accepted")
	}
}

func TestManagedServicesPublicationDeltaRejectsConcurrentConfigChange(t *testing.T) {
	base := t.TempDir()
	beforeCatalog := []byte("spec:\n  suspend: true\n")
	beforeConfig := []byte("CLUSTER_NAME=sample\nKUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=false\n")
	if err := os.WriteFile(filepath.Join(base, "services-catalog.yaml"), []byte("spec:\n  suspend: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(base, "cluster-config.env")
	if err := os.WriteFile(configPath, []byte("CLUSTER_NAME=sample\nKUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyManagedServicesPublicationDelta(base, beforeCatalog, beforeConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("CLUSTER_NAME=sample\nSERVICES_HUB_DIGEST=changed\nKUBE_DC_UI_MANAGED_SERVICES_ALL_ORGANIZATIONS=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyManagedServicesPublicationDelta(base, beforeCatalog, beforeConfig); err == nil {
		t.Fatal("unrelated pin change was accepted")
	}
}
