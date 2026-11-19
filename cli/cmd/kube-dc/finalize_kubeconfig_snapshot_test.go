package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

const twoContextKubeconfig = `apiVersion: v1
kind: Config
current-context: a
clusters:
- name: a
  cluster: {server: 'https://192.0.2.10:6443', certificate-authority: ca-a.crt}
- name: b
  cluster: {server: 'https://192.0.2.20:6443'}
contexts:
- name: a
  context: {cluster: a, user: ua}
- name: b
  context: {cluster: b, user: ub}
users:
- name: ua
  user: {token: token-a}
- name: ub
  user: {token: token-b}
`

// The snapshot keeps the context that was current when it was taken: a
// context switched in the original afterwards (another shell) does not move
// the scripts to another cluster.
func TestSnapshotKubeconfigForScripts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(original, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, discard, err := snapshotKubeconfigForScripts(original)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(snapshot) != filepath.Join(home, ".kube-dc", "run") {
		t.Fatalf("the snapshot is at %s", snapshot)
	}
	if fi, _ := os.Stat(snapshot); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Dir(snapshot)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", fi.Mode())
	}
	// Another shell switches the default context.
	if err := os.WriteFile(original, []byte(strings.Replace(twoContextKubeconfig, "current-context: a", "current-context: b", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := clientcmd.LoadFromFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentContext != "a" {
		t.Fatalf("the snapshot follows the original: current context %q", got.CurrentContext)
	}
	// A relative file reference still leads to the original's directory.
	if ca := got.Clusters["a"].CertificateAuthority; ca != filepath.Join(filepath.Dir(original), "ca-a.crt") {
		t.Fatalf("certificate-authority in the snapshot: %q", ca)
	}
	discard()
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("the snapshot was not removed: %v", err)
	}
}

func TestSnapshotKubeconfigForScripts_Refusals(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "none")
	if _, _, err := snapshotKubeconfigForScripts(missing); err == nil {
		t.Fatal("a kubeconfig that does not exist gave a snapshot")
	}
	noContext := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(noContext, []byte(strings.Replace(twoContextKubeconfig, "current-context: a\n", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := snapshotKubeconfigForScripts(noContext); err == nil || !strings.Contains(err.Error(), "current context") {
		t.Fatalf("want a refusal for a kubeconfig without a current context, got %v", err)
	}
	// A home directory whose path holds the list separator cannot be one
	// KUBECONFIG entry.
	odd := filepath.Join(t.TempDir(), "team"+string(os.PathListSeparator)+"blue")
	if err := os.MkdirAll(odd, 0o700); err != nil {
		t.Skipf("cannot create %s: %v", odd, err)
	}
	t.Setenv("HOME", odd)
	usable := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(usable, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := snapshotKubeconfigForScripts(usable); err == nil || !strings.Contains(err.Error(), "one KUBECONFIG entry") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// A copy whose process is gone is removed; one whose process is alive is kept
// however old it is.
func TestSnapshotKubeconfigForScripts_SweepsOnlyAbandonedCopies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness is not determined on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".kube-dc", "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A process that has exited: its ID is free (or at least not ours).
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip("cannot run a process")
	}
	abandoned := filepath.Join(dir, fmt.Sprintf("%s%d-abc", snapshotPrefix, gone.Process.Pid))
	recent := filepath.Join(dir, fmt.Sprintf("%s%d-new", snapshotPrefix, gone.Process.Pid))
	alive := filepath.Join(dir, fmt.Sprintf("%s%d-old", snapshotPrefix, os.Getppid()))
	unrelated := filepath.Join(dir, "notes.txt")
	for _, f := range []string{abandoned, recent, alive, unrelated} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-72 * time.Hour)
	_ = os.Chtimes(alive, old, old)
	_ = os.Chtimes(abandoned, old, old)
	original := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(original, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, discard, err := snapshotKubeconfigForScripts(original)
	if err != nil {
		t.Fatal(err)
	}
	defer discard()
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("the copy of a process that is gone was kept: %v", err)
	}
	// Kept: a live process's copy however old, a recent copy even though its
	// process is not visible (it may live in another PID namespace), and
	// files that are not copies.
	for _, f := range []string{alive, recent, unrelated} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s was removed: %v", filepath.Base(f), err)
		}
	}
}

// A run directory that other users can write to is not used: someone could
// replace the copy while finalize waits.
func TestSnapshotKubeconfigForScripts_RefusesASharedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not express access on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".kube-dc", "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(original, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := snapshotKubeconfigForScripts(original); err == nil || !strings.Contains(err.Error(), "open to other users") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("a copy was written into the shared directory: %v", left)
	}
}

// The error for an unreadable kubeconfig names the file and nothing from it.
func TestSnapshotKubeconfigForScripts_ErrorsDoNotQuoteTheKubeconfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Valid YAML that the client library refuses while decoding: a field of
	// the wrong type next to the tokens. Its error quotes the document.
	broken := filepath.Join(t.TempDir(), "broken-config")
	body := "apiVersion: v1\nkind: Config\nusers:\n- name: u\n  user: {token: s3cr3t-bearer}\n- name: u\n  user: {token: s3cr3t-other}\nclusters: s3cr3t-not-a-list\n"
	if err := os.WriteFile(broken, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, libErr := clientcmd.LoadFromFile(broken); libErr == nil {
		t.Fatal("the fixture is not refused by the client library; the test would prove nothing")
	}
	_, _, err := snapshotKubeconfigForScripts(broken)
	if err == nil || !strings.Contains(err.Error(), "broken-config") || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("want an error that names the file and does not quote it, got %v", err)
	}
}
