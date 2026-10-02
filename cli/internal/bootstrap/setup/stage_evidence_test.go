package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
)

func verifiedStageFixture(t *testing.T) (*SessionWriter, string, string) {
	t.Helper()
	w, c, _ := sessionFixture(t)
	h := primaryServer(c.Spec.Hosts)
	attempt, err := w.Begin("rke2-first-server")
	if err != nil {
		t.Fatal(err)
	}
	node := rke2.VerifiedNode{Name: h.ID, UID: "verified-node-uid", Role: h.Role, InternalIP: h.ManagementAddress}
	hash, err := w.saveStageEvidence("rke2-first-server", attempt, h, node, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.completeVerified("rke2-first-server", attempt, hash); err != nil {
		t.Fatal(err)
	}
	return w, filepath.Join(w.directory, "evidence-"+hash+".json"), hash
}
func TestCoordinatorStageProofIsPrivateAndBoundToCompletion(t *testing.T) {
	w, path, hash := verifiedStageFixture(t)
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("proof is not private")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != hash {
		t.Fatal("completion has no persisted proof")
	}
	var proof sessionStageEvidence
	if err := json.Unmarshal(body, &proof); err != nil {
		t.Fatal(err)
	}
	if proof.Node.UID != "verified-node-uid" || proof.Stage != "rke2-first-server" || proof.Attempt != 1 {
		t.Fatal(proof)
	}
	if _, err := ReadSession(w.directory); err != nil {
		t.Fatal(err)
	}
}
func TestCoordinatorProofLossBlocksReadReopenAndFurtherEffects(t *testing.T) {
	for _, kind := range []string{"missing", "changed", "symlink", "public"} {
		t.Run(kind, func(t *testing.T) {
			w, path, _ := verifiedStageFixture(t)
			directory := w.directory
			switch kind {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "changed":
				if err := os.WriteFile(path, []byte("altered proof"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-old", path); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadSession(directory); err == nil {
				t.Fatal("unverified completion presented as evidence")
			}
			if _, err := w.Begin("fetch-kubeconfig"); err == nil {
				t.Fatal("continued after lost proof")
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := OpenSession(directory); err == nil {
				reopened.Close()
				t.Fatal("reopened with invalid proof")
			}
		})
	}
}

func TestChildProofRejectsChangedProtectedConfig(t *testing.T) {
	w, c, _ := sessionFixture(t)
	h := primaryServer(c.Spec.Hosts)
	attempt, err := w.Begin("fetch-kubeconfig")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(w.directory, "child-bootstrap.kubeconfig")
	if err := os.WriteFile(path, []byte("protected child config"), 0600); err != nil {
		t.Fatal(err)
	}
	node := rke2.VerifiedNode{Name: h.ID, UID: "child-node-uid", Role: h.Role, InternalIP: h.ManagementAddress}
	hash, err := w.saveStageEvidence("fetch-kubeconfig", attempt, h, node, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.completeVerified("fetch-kubeconfig", attempt, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSession(w.directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed child config"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSession(w.directory); err == nil {
		t.Fatal("changed child bytes retained verified handoff")
	}
}
func TestNodeProofRejectsWrongStageAndNode(t *testing.T) {
	w, c, _ := sessionFixture(t)
	h := primaryServer(c.Spec.Hosts)
	attempt, err := w.Begin("rke2-first-server")
	if err != nil {
		t.Fatal(err)
	}
	node := rke2.VerifiedNode{Name: h.ID, UID: "uid", Role: h.Role, InternalIP: "192.0.2.99"}
	if _, err := w.saveStageEvidence("rke2-first-server", attempt, h, node, ""); err == nil {
		t.Fatal("wrong node IP produced a proof")
	}
	node.InternalIP = h.ManagementAddress
	if _, err := w.saveStageEvidence("fetch-kubeconfig", attempt, h, node, ""); err == nil {
		t.Fatal("proof without matching running stage")
	}
}
