package rke2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func artifactFixture(t *testing.T) ArtifactSet {
	t.Helper()
	body := []byte("#!/bin/sh\nexit 0\n")
	hash := sha256.Sum256(body)
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return ArtifactSet{ArchiveSize: 123, Version: defaultRKE2Version, Architecture: "amd64", InstallerPath: path, InstallerSHA256: hex.EncodeToString(hash[:]), ArchiveURL: "https://example.test/rke2.tar.gz", ArchiveSHA256: strings.Repeat("a", 64)}
}
func TestChangedArtifactCannotReachHostWrites(t *testing.T) {
	for _, variant := range []string{"bytes", "version", "url", "resolver"} {
		t.Run(variant, func(t *testing.T) {
			a := artifactFixture(t)
			switch variant {
			case "bytes":
				_ = os.WriteFile(a.InstallerPath, []byte("changed"), 0600)
			case "version":
				a.Version = "other"
			case "url":
				a.ArchiveURL = "https://user:secret@example.test/archive"
			case "resolver":
				t.Setenv("RKE2_DNS_PUBLIC_FALLBACK", "true")
			}
			ssh := &fakeSSH{}
			o := baseOpts(ssh)
			o.Artifacts = &a
			if err := Install(context.Background(), o); err == nil {
				t.Fatal("changed artifacts accepted")
			}
			if ssh.putCalls != 0 || len(ssh.ranCmds) != 0 {
				t.Fatal("invalid bytes reached SSH")
			}
		})
	}
}
func TestArtifactMismatchStopsBeforeHostConfiguration(t *testing.T) {
	a := artifactFixture(t)
	ssh := &fakeSSH{runs: map[string]string{"mktemp -d": "/var/lib/kube-dc-rke2.test123", "curl --fail": ""}, runErr: map[string]error{"curl --fail": fmt.Errorf("checksum mismatch")}}
	o := baseOpts(ssh)
	o.NodeIP = "192.0.2.10"
	o.Artifacts = &a
	if err := Install(context.Background(), o); err == nil {
		t.Fatal("bad host download accepted")
	}
	if ssh.ranAny("install-server.sh") || !ssh.ranAny("rm -rf --") {
		t.Fatalf("host configuration ran or private staging was not cleaned: %v", ssh.ranCmds)
	}
	if ssh.putCalls != 2 {
		t.Fatalf("expected only installer and checksum staging, got %d", ssh.putCalls)
	}
}

func TestReviewedHostDriftAfterStagingCannotConfigure(t *testing.T) {
	a := artifactFixture(t)
	ssh := &fakeSSH{runs: map[string]string{"mktemp -d": "/var/lib/kube-dc-rke2.test123"}}
	o := baseOpts(ssh)
	o.NodeIP = "192.0.2.10"
	o.Artifacts = &a
	o.BeforeConfigure = func(context.Context) error {
		if !ssh.ranAny("curl --fail") {
			t.Fatal("fresh guard ran before the download")
		}
		return fmt.Errorf("host or review changed during download")
	}
	if err := Install(context.Background(), o); err == nil {
		t.Fatal("post-download drift accepted")
	}
	if ssh.ranAny("install-server.sh") || !ssh.ranAny("rm -rf --") || ssh.putCalls != 2 {
		t.Fatal("expired host review reached durable configuration")
	}
}

func TestArtifactTransferHasHardByteAndExactSizeLimits(t *testing.T) {
	a := artifactFixture(t)
	ssh := &fakeSSH{runs: map[string]string{"mktemp -d": "/var/lib/kube-dc-rke2.test123"}}
	_, cleanup, err := stageArtifacts(context.Background(), ssh, ports.SSHHost{Alias: "server-1"}, a, a.Version, nil)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
	var transfer string
	for _, command := range ssh.ranCmds {
		if strings.Contains(command, "curl --fail") {
			transfer = command
		}
	}
	for _, limit := range []string{"--max-filesize 123", "head -c 124", "stat -c %s", "sha256sum --check --status"} {
		if !strings.Contains(transfer, limit) {
			t.Fatalf("missing transfer guard %q", limit)
		}
	}
}

// Execute the real command builder through local sudo/env shims. In particular
// join args must reach the inner script, not become bash -c's $0/$1.
func TestReviewedServerCommandPreservesJoinArguments(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "server.sh")
	body := []byte("#!/bin/bash\nprintf '%s\\n' \"$NODE_NAME\" \"$1\" \"$2\"\n")
	if err := os.WriteFile(scriptPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	sudo := filepath.Join(dir, "sudo")
	if err := os.WriteFile(sudo, []byte("#!/bin/sh\n[ \"$1\" = -n ] && shift\nexec \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command := reviewedScriptCommand(map[string]string{"NODE_NAME": "server-2"}, scriptPath, body, true, "token with ' quote", "192.0.2.10")
	cmd := exec.Command("bash", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "server-2\ntoken with ' quote\n192.0.2.10\n" {
		t.Fatalf("join argv was changed: %s %v", output, err)
	}
	if err := os.WriteFile(scriptPath, []byte("echo REPLACED\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err = cmd.CombinedOutput()
	if err == nil || strings.Contains(string(output), "REPLACED") {
		t.Fatal("replaced embedded consumer executed")
	}
}

var _ ports.SSHClient = (*fakeSSH)(nil)
