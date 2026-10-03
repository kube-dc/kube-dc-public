package setup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func firstServerEvidence(uid string) rke2.VerifiedNode {
	return rke2.VerifiedNode{Name: "server-1", UID: uid, Role: "server", InternalIP: "192.0.2.10"}
}

func childAPIServer(t *testing.T, name string, response string) (*httptest.Server, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(101), Subject: pkix.Name{CommonName: "Test child CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverCert := &x509.Certificate{SerialNumber: big.NewInt(102), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverCert, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/server-1" || r.TLS == nil || r.TLS.ServerName != "kube-api.example.test" {
			http.Error(w, "wrong API target", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, response)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func childAPIConfigPath(t *testing.T, c Compiled, caPEM []byte) string {
	t.Helper()
	cfg := childConfig(t, c)
	cfg.Clusters[c.Spec.Name].CertificateAuthorityData = caPEM
	path := filepath.Join(t.TempDir(), "private", "child.yaml")
	handoff, err := NewExclusiveKubeconfigHandoff(c, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff(cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVerifyChildBootstrapAPIUsesReviewedIPAndTLSName(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	response := `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":"fresh-node","labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}],"conditions":[{"type":"Ready","status":"False"}]}}`
	server, ca := childAPIServer(t, "kube-api.example.test", response)
	path := childAPIConfigPath(t, c, ca)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "unrelated.yaml"))
	config, err := verifyChildBootstrapAPI(context.Background(), c, path, server.URL, firstServerEvidence("fresh-node"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != server.URL || config.TLSClientConfig.ServerName != "kube-api.example.test" || config.Timeout != childAPIVerifyTimeout {
		t.Fatalf("bootstrap API config lost its direct transport or TLS name: %+v", config)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	proxy, err := config.Proxy(request)
	if err != nil || proxy != nil {
		t.Fatalf("bootstrap API could use a workstation proxy: %v, %v", proxy, err)
	}
}

func TestPublishVerifiedChildBootstrapKubeconfigUsesDirectEndpoint(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	response := `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":"fresh-node","labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}]}}`
	server, ca := childAPIServer(t, "kube-api.example.test", response)
	publicPath := childAPIConfigPath(t, c, ca)
	bootstrapPath := filepath.Join(t.TempDir(), "private", "bootstrap.yaml")
	if err := publishVerifiedChildBootstrapKubeconfig(context.Background(), c, publicPath, bootstrapPath, server.URL, firstServerEvidence("fresh-node")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(bootstrapPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap kubeconfig is not private: %v, %v", info, err)
	}
	bootstrap, err := clientcmd.LoadFromFile(bootstrapPath)
	if err != nil || bootstrap.Clusters[c.Spec.Name].Server != server.URL || bootstrap.Clusters[c.Spec.Name].TLSServerName != "kube-api.example.test" {
		t.Fatalf("bootstrap kubeconfig lost direct-IP TLS binding: %+v, %v", bootstrap, err)
	}
	public, err := clientcmd.LoadFromFile(publicPath)
	if err != nil || public.Clusters[c.Spec.Name].Server != "https://kube-api.example.test:6443" {
		t.Fatalf("public kubeconfig was changed: %+v, %v", public, err)
	}
	restConfig, err := clientcmd.BuildConfigFromFlags("", bootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Nodes().Get(context.Background(), "server-1", metav1.GetOptions{}); err != nil {
		t.Fatalf("published bootstrap config cannot reach the verified API: %v", err)
	}
	if err := publishVerifiedChildBootstrapKubeconfig(context.Background(), c, publicPath, bootstrapPath, server.URL, firstServerEvidence("fresh-node")); err == nil {
		t.Fatal("bootstrap config was replaced by a second publish")
	}
}

func TestOpenVerifiedChildSessionIgnoresAmbientContextAndMock(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	response := `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":"fresh-node","labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}]}}`
	server, ca := childAPIServer(t, "kube-api.example.test", response)
	var requests atomic.Int32
	upstream := server.Config.Handler
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		upstream.ServeHTTP(w, r)
	})
	publicPath := childAPIConfigPath(t, c, ca)
	bootstrapPath := filepath.Join(t.TempDir(), "private", "bootstrap.yaml")
	if err := publishVerifiedChildBootstrapKubeconfig(context.Background(), c, publicPath, bootstrapPath, server.URL, firstServerEvidence("fresh-node")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBE_DC_MOCK", "cloud")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "unrelated.yaml"))
	session, err := openVerifiedChildSession(context.Background(), c, bootstrapPath, server.URL, firstServerEvidence("fresh-node"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.Scenario != "" || session.K8s == nil || session.Flux == nil || session.Scripts == nil {
		t.Fatalf("child session did not use real explicit ports: %+v", session)
	}
	before := requests.Load()
	if _, err := session.K8s.ListCRDs(context.Background()); err == nil || requests.Load() <= before {
		t.Fatalf("child K8s port did not reach the direct API: %v", err)
	}
	if _, err := openVerifiedChildSession(context.Background(), c, publicPath, server.URL, firstServerEvidence("fresh-node")); err == nil {
		t.Fatal("public-endpoint config was accepted as the direct child session")
	}
	if _, err := openVerifiedChildSession(context.Background(), c, bootstrapPath, server.URL, firstServerEvidence("replacement")); err == nil {
		t.Fatal("changed child Node UID was accepted for a session")
	}
}

func TestPublishVerifiedChildBootstrapKubeconfigBlocksWrongUIDBeforeWrite(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	response := `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":"replacement","labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}]}}`
	server, ca := childAPIServer(t, "kube-api.example.test", response)
	publicPath := childAPIConfigPath(t, c, ca)
	bootstrapPath := filepath.Join(t.TempDir(), "private", "bootstrap.yaml")
	if err := publishVerifiedChildBootstrapKubeconfig(context.Background(), c, publicPath, bootstrapPath, server.URL, firstServerEvidence("original")); err == nil {
		t.Fatal("changed child Node UID was accepted")
	}
	if _, err := os.Stat(filepath.Dir(bootstrapPath)); !os.IsNotExist(err) {
		t.Fatalf("failed identity check created a bootstrap credential directory: %v", err)
	}
}

func TestVerifyChildBootstrapAPIRejectsWrongNodeAndTLS(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	for _, tc := range []struct{ name, response, uid string }{
		{"missing UID", `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}]}}`, "fresh-node"},
		{"changed UID", `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":"replacement","labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}]}}`, "original"},
		{"wrong role", `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":"wrong","labels":{}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}]}}`, "wrong"},
		{"wrong IP", `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":"wrong","labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.99"}]}}`, "wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, ca := childAPIServer(t, "kube-api.example.test", tc.response)
			path := childAPIConfigPath(t, c, ca)
			if _, err := verifyChildBootstrapAPI(context.Background(), c, path, server.URL, firstServerEvidence(tc.uid)); err == nil || !strings.Contains(err.Error(), "did not confirm") {
				t.Fatalf("wrong child API node was accepted: %v", err)
			}
		})
	}
	server, ca := childAPIServer(t, "wrong.example.test", `{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":"uid"}}`)
	path := childAPIConfigPath(t, c, ca)
	if _, err := verifyChildBootstrapAPI(context.Background(), c, path, server.URL, firstServerEvidence("uid")); err == nil {
		t.Fatal("API certificate for another DNS name was accepted")
	}
}

func TestVerifyChildBootstrapAPIRejectsReplacedOrPublicKubeconfig(t *testing.T) {
	c, _ := rke2OperationsFixture(t)
	server, ca := childAPIServer(t, "kube-api.example.test", `{}`)
	path := childAPIConfigPath(t, c, ca)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyChildBootstrapAPI(context.Background(), c, path, server.URL, firstServerEvidence("uid")); err == nil {
		t.Fatal("group-readable child kubeconfig was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyChildBootstrapAPI(context.Background(), c, link, server.URL, firstServerEvidence("uid")); err == nil {
		t.Fatal("linked child kubeconfig was accepted")
	}
}
