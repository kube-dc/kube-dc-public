package git

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func configureProviderOrigin(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := append([]string{"-C", directory, "config"}, args...)
	if out, err := exec.Command("git", command...).CombinedOutput(); err != nil {
		t.Fatalf("config: %v: %s", err, out)
	}
}

func TestProviderBindingChecksFetchPushPortsAndRewrites(t *testing.T) {
	for _, variant := range []string{"matching", "fetch-host", "push-host", "push-port", "rewrite", "plaintext"} {
		t.Run(variant, func(t *testing.T) {
			directory, _ := initLocalRepo(t)
			configureProviderOrigin(t, directory, "remote.origin.url", "https://git.example.test/acme/fleet.git")
			switch variant {
			case "fetch-host":
				configureProviderOrigin(t, directory, "remote.origin.url", "https://other.example.test/acme/fleet.git")
			case "push-host":
				configureProviderOrigin(t, directory, "remote.origin.pushurl", "https://other.example.test/acme/fleet.git")
			case "push-port":
				configureProviderOrigin(t, directory, "remote.origin.pushurl", "https://git.example.test:8443/acme/fleet.git")
			case "rewrite":
				configureProviderOrigin(t, directory, "url.https://other.example.test/.pushInsteadOf", "https://git.example.test/")
			case "plaintext":
				configureProviderOrigin(t, directory, "remote.origin.url", "http://git.example.test/acme/fleet.git")
			}
			_, err := New().ForProvider(context.Background(), directory, "git.example.test")
			if (err == nil) != (variant == "matching") {
				t.Fatalf("variant %s: %v", variant, err)
			}
		})
	}
}

func TestBoundProviderRejectsDriftBeforeCommit(t *testing.T) {
	directory, _ := initLocalRepo(t)
	configureProviderOrigin(t, directory, "remote.origin.url", "https://git.example.test/acme/fleet.git")
	client, err := New().ForProvider(context.Background(), directory, "git.example.test")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := client.Head(context.Background(), directory)
	configureProviderOrigin(t, directory, "remote.origin.pushurl", "https://other.example.test/acme/fleet.git")
	if err := os.WriteFile(filepath.Join(directory, "pending.txt"), []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CommitAndPush(context.Background(), directory, "must not commit", "synthetic-token"); err == nil {
		t.Fatal("drifted push admitted")
	}
	after, _ := client.Head(context.Background(), directory)
	if before != after {
		t.Fatal("created a commit before rejecting changed destination")
	}
}

func TestBoundProviderRejectsAnotherRepositoryOnSameHost(t *testing.T) {
	directory, _ := initLocalRepo(t)
	configureProviderOrigin(t, directory, "remote.origin.url", "https://git.example.test/acme/fleet.git")
	client, err := New().ForProvider(context.Background(), directory, "git.example.test")
	if err != nil {
		t.Fatal(err)
	}
	configureProviderOrigin(t, directory, "remote.origin.url", "https://git.example.test/other/fleet.git")
	if _, err := client.(*Client).ForProvider(context.Background(), directory, "git.example.test"); err == nil {
		t.Fatal("rebinding admitted a different repository")
	}
	if err := client.Push(context.Background(), directory, "synthetic-token"); err == nil {
		t.Fatal("same-host repository drift admitted")
	}
}

func TestAuthenticatedRequestRetainsSelectedURLAfterConfigChanges(t *testing.T) {
	for _, direction := range []string{"pull", "push"} {
		t.Run(direction, func(t *testing.T) {
			directory, _ := initLocalRepo(t)
			var selectedCalls, otherCalls atomic.Int32
			selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				selectedCalls.Add(1)
				http.NotFound(w, r)
			}))
			defer selected.Close()
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				otherCalls.Add(1)
				http.NotFound(w, r)
			}))
			defer other.Close()
			configureProviderOrigin(t, directory, "remote.origin.url", selected.URL+"/acme/fleet.git")
			client := New() // Local HTTP fixtures exercise selection without a TLS bypass.
			client.afterRemoteSelection = func() {
				configureProviderOrigin(t, directory, "remote.origin.url", other.URL+"/other/fleet.git")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if direction == "pull" {
				_ = client.Pull(ctx, directory, "synthetic-token")
			} else {
				_ = client.Push(ctx, directory, "synthetic-token")
			}
			if selectedCalls.Load() == 0 || otherCalls.Load() != 0 {
				t.Fatalf("authenticated destination changed: selected=%d other=%d", selectedCalls.Load(), otherCalls.Load())
			}
		})
	}
}
