package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func TestKeycloakFleetConfigTransitionIsNarrowlyValidated(t *testing.T) {
	for _, kind := range []string{"empty", "absent", "unrelated-change", "different-secret"} {
		t.Run(kind, func(t *testing.T) {
			root, prepared := t.TempDir(), t.TempDir()
			path := "clusters/demo/cluster-config.env"
			beforeBody := []byte("CLUSTER_NAME=demo\nDOMAIN=example.test\nKEYCLOAK_ADMIN_CLIENT_SECRET_NAME=\n")
			if kind == "absent" {
				beforeBody = []byte("CLUSTER_NAME=demo\nDOMAIN=example.test\n")
			}
			if kind == "different-secret" {
				beforeBody = []byte("CLUSTER_NAME=demo\nKEYCLOAK_ADMIN_CLIENT_SECRET_NAME=customer-secret\n")
			}
			for _, dir := range []string{root, prepared} {
				if err := os.MkdirAll(filepath.Join(dir, "clusters/demo"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, path), beforeBody, 0600); err != nil {
					t.Fatal(err)
				}
			}
			sum := sha256.Sum256(beforeBody)
			old := clusterinit.PreparedFile{Path: path, SHA256: hex.EncodeToString(sum[:]), Mode: 0600, Size: int64(len(beforeBody))}
			p := clusterinit.PreparedScaffold{Destination: root, Directory: prepared, Cluster: "demo"}
			binding := GitBinding{Directory: root, Branch: "main", Head: "unchanged", Remote: "https://github.com/reviewed/fleet", RemoteHead: "unchanged"}
			before := PlatformSnapshot{Git: binding, Files: []clusterinit.PreparedFile{old}}
			transition, err := keycloakConfigTransition(beforeBody)
			if kind == "different-secret" {
				if err == nil || checkPlatformStageInputs(clusterinit.StepKeycloakOIDC, p, before) == nil {
					t.Fatal("custom secret would be overwritten")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unrelated-change" {
				transition = append(transition, []byte("EXT_NET_GATEWAY=unreviewed\n")...)
			}
			if err := os.WriteFile(filepath.Join(root, path), transition, 0600); err != nil {
				t.Fatal(err)
			}
			sum = sha256.Sum256(transition)
			after := PlatformSnapshot{Git: binding, Files: []clusterinit.PreparedFile{{Path: path, SHA256: hex.EncodeToString(sum[:]), Mode: 0600, Size: int64(len(transition))}}}
			err = checkPlatformDelta(context.Background(), before, after, clusterinit.StepKeycloakOIDC, p)
			if kind == "unrelated-change" {
				if err == nil {
					t.Fatal("unreviewed environment edit accepted")
				}
			} else if err != nil {
				t.Fatalf("Fleet finalizer transition rejected: %v", err)
			}
		})
	}
}

func TestPlatformSnapshotAgainstSelectedFleet(t *testing.T) {
	dir := os.Getenv("KUBE_DC_TEST_FLEET_SNAPSHOT")
	if dir == "" {
		t.Skip("set KUBE_DC_TEST_FLEET_SNAPSHOT for the read-only Fleet inventory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	snapshot, err := SnapshotPlatform(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) == 0 {
		t.Fatal("Fleet inventory is empty")
	}
	t.Logf("Read-only Fleet inventory: %d files", len(snapshot.Files))
}
