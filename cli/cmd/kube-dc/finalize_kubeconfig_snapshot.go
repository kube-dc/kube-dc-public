package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

// snapshotKubeconfigForScripts writes a private copy of the kubeconfig at path,
// as it is right now (its current context included), and returns the copy's
// path and a function that removes it.
//
// It is for scripts that must act on the cluster the caller's own API clients
// were just built for. Those clients keep their cluster for as long as they
// live; a script that is handed the kubeconfig's path reads it again when it
// starts, minutes later, and follows whatever context is current by then.
//
// The copy is mode 0600 in ~/.kube-dc/run, which must be a directory of this
// user that nobody else can write to (a directory someone else can write to
// would let them replace the copy). Relative file references of the original
// are resolved, so the copy works from where it is. A copy left by a run that
// was killed is removed by a later run once the process that made it is gone.
//
// Errors do not quote the kubeconfig: a parse error from the client library
// can print user entries.
func snapshotKubeconfigForScripts(path string) (snapshot string, discard func(), err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, errors.New("the home directory is not known")
	}
	dir := filepath.Join(home, ".kube-dc", "run")
	// KUBECONFIG is a list: a path with the list separator in it cannot be
	// given to a script as one entry.
	if strings.ContainsRune(dir, os.PathListSeparator) {
		return "", nil, fmt.Errorf("%s cannot be written as one KUBECONFIG entry", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("%s cannot be created", dir)
	}
	if err := privateDirectory(dir); err != nil {
		return "", nil, err
	}
	sweepAbandonedSnapshots(dir)
	config, err := (&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}).Load()
	if err != nil {
		return "", nil, fmt.Errorf("the kubeconfig %s cannot be read", path)
	}
	if config.CurrentContext == "" {
		return "", nil, fmt.Errorf("the kubeconfig %s has no current context", path)
	}
	// The name carries the process ID: that is how a later run tells a copy
	// in use from an abandoned one.
	file, err := os.CreateTemp(dir, fmt.Sprintf("%s%d-*", snapshotPrefix, os.Getpid()))
	if err != nil {
		return "", nil, fmt.Errorf("no file can be created in %s", dir)
	}
	snapshot = file.Name()
	_ = file.Close()
	discard = func() { _ = os.Remove(snapshot) }
	if err := clientcmd.WriteToFile(*config, snapshot); err != nil {
		discard()
		return "", nil, errors.New("the kubeconfig snapshot cannot be written")
	}
	if err := os.Chmod(snapshot, 0o600); err != nil {
		discard()
		return "", nil, errors.New("the kubeconfig snapshot cannot be made private")
	}
	return snapshot, discard, nil
}

const snapshotPrefix = "finalize-kubeconfig-"

// sweepAbandonedSnapshots removes the copies that are more than a day old AND
// whose process is gone. Either test alone can be wrong: a run can be
// suspended for days, and a process can be invisible from here (another PID
// namespace sharing this home directory). A copy whose name does not tell is
// left alone.
func sweepAbandonedSnapshots(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, snapshotPrefix) || !entry.Type().IsRegular() {
			continue
		}
		pidText, _, ok := strings.Cut(strings.TrimPrefix(name, snapshotPrefix), "-")
		pid, convErr := strconv.Atoi(pidText)
		if !ok || convErr != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || time.Since(info.ModTime()) < 24*time.Hour {
			continue
		}
		if processGone(pid) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
