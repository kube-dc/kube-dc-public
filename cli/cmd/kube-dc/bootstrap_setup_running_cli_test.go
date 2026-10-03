package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunningCLIPathSurvivesExecutableReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux executable inode test")
	}
	if os.Getenv("KDC_RUNNING_CLI_CHILD") == "1" {
		ready, release := os.Getenv("KDC_RUNNING_CLI_READY"), os.Getenv("KDC_RUNNING_CLI_RELEASE")
		if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		waitForTestFile(t, release)
		file, err := os.Open(runningCLIPath())
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(hash.Sum(nil)); got != os.Getenv("KDC_RUNNING_CLI_EXPECTED") {
			t.Fatalf("running executable changed after pathname replacement: %s", got)
		}
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "running-cli")
	source, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	copyFile, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(copyFile, hash), source); err != nil {
		copyFile.Close()
		t.Fatal(err)
	}
	if err := copyFile.Close(); err != nil {
		t.Fatal(err)
	}
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, copyPath, "-test.run=^TestRunningCLIPathSurvivesExecutableReplacement$")
	cmd.Env = append(os.Environ(), "KDC_RUNNING_CLI_CHILD=1", "KDC_RUNNING_CLI_READY="+ready,
		"KDC_RUNNING_CLI_RELEASE="+release, "KDC_RUNNING_CLI_EXPECTED="+hex.EncodeToString(hash.Sum(nil)))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	waitForTestFile(t, ready)
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("different binary at the original pathname"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, copyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("running binary check failed: %v: %s", err, output.String())
	}
}

func waitForTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
