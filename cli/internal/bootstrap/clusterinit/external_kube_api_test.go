package clusterinit

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// productized from production incident 2026-08-11: on a MetalLB VIP front door
// where Envoy is host-bind on apiserver nodes, the :6443 Envoy listener is
// removed and external kube-api must instead be served off-Envoy via a
// ClusterIP+externalIPs Service to the control-plane hosts. WriteExternalKubeAPIVIP
// scaffolds that; if it regresses, every future VIP-fronted install silently
// loses external kubectl (exactly the incident).
func TestWriteExternalKubeAPIVIP(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "clusters", "c1", "addons-config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	kpath := filepath.Join(dir, "kustomization.yaml")
	if err := os.WriteFile(kpath,
		[]byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../../addons/metallb-config\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	vip := "203.0.113.16"
	cp := []string{"192.0.2.2", "192.0.2.3", "192.0.2.4"}
	if err := WriteExternalKubeAPIVIP(repo, "c1", vip, cp, io.Discard); err != nil {
		t.Fatalf("write: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "kube-api-external.yaml"))
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	man := string(body)
	for _, want := range []string{
		"kind: Service", "kind: EndpointSlice", "name: kube-api-external",
		"namespace: kube-system", "externalIPs:", vip, "port: 6443",
		cp[0], cp[1], cp[2],
	} {
		if !strings.Contains(man, want) {
			t.Errorf("manifest missing %q\n---\n%s", want, man)
		}
	}
	// externalIPs must be a quoted literal, never an empty substitution.
	if strings.Contains(man, `externalIPs:`) && !strings.Contains(man, `"`+vip+`"`) {
		t.Errorf("VIP not written as a quoted literal (empty-substitution footgun risk)")
	}

	kbody, _ := os.ReadFile(kpath)
	if !strings.Contains(string(kbody), "- kube-api-external.yaml") {
		t.Errorf("kustomization not wired:\n%s", kbody)
	}

	// Idempotent: re-run must not error, must not duplicate the resource, and
	// must not clobber the existing manifest.
	if err := WriteExternalKubeAPIVIP(repo, "c1", vip, cp, io.Discard); err != nil {
		t.Fatalf("second run: %v", err)
	}
	kbody2, _ := os.ReadFile(kpath)
	if n := strings.Count(string(kbody2), "- kube-api-external.yaml"); n != 1 {
		t.Errorf("resource wired %d times, want exactly 1", n)
	}

	// Validation: bad VIP and no control-plane IPs both fail closed.
	if err := WriteExternalKubeAPIVIP(t.TempDir(), "c1", "not-an-ip", cp, io.Discard); err == nil {
		t.Errorf("expected error on invalid VIP")
	}
	repo2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo2, "clusters", "c1", "addons-config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteExternalKubeAPIVIP(repo2, "c1", vip, []string{"", "  "}, io.Discard); err == nil {
		t.Errorf("expected error when no control-plane host IPs are supplied")
	}
}
