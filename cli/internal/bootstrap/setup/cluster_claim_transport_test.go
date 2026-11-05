package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
)

// Exercise the actual verified TLS/client-go path against an isolated API
// fixture. The endpoint override changes only the test server address.
func TestClusterReservationUsesVerifiedChildTransportAndRejectsNodeDrift(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(fmt.Sprint(drift), func(t *testing.T) {
			var endpoint string
			var creates atomic.Int32
			c, _, _ := clusterReservationFixture(t, func(c *SessionCoordinator) []byte {
				uid := "verified-primary-uid"
				if drift {
					uid = "replacement-node"
				}
				response := fmt.Sprintf(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"server-1","uid":%q,"labels":{"node-role.kubernetes.io/control-plane":"true"}},"status":{"addresses":[{"type":"InternalIP","address":"192.0.2.10"}]}}`, uid)
				server, ca := childAPIServer(t, "kube-api.example.test", response)
				endpoint = server.URL
				upstream := server.Config.Handler
				// Publish immutable copies across the server's request goroutines.
				var lease atomic.Pointer[coordinationv1.Lease]
				server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v1/nodes/server-1" {
						upstream.ServeHTTP(w, r)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if r.TLS == nil || r.TLS.ServerName != "kube-api.example.test" {
						w.WriteHeader(400)
						return
					}
					switch {
					case r.Method == http.MethodPost && r.URL.Path == "/apis/coordination.k8s.io/v1/namespaces/kube-system/leases":
						creates.Add(1)
						var got coordinationv1.Lease
						body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
						if err != nil {
							w.WriteHeader(400)
							return
						}
						if _, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &got); err != nil {
							w.WriteHeader(400)
							return
						}
						got.APIVersion, got.Kind, got.UID, got.ResourceVersion = "coordination.k8s.io/v1", "Lease", "tls-lease-instance", "opaque-v1"
						if !lease.CompareAndSwap(nil, got.DeepCopy()) {
							w.WriteHeader(409)
							return
						}
						w.WriteHeader(http.StatusCreated)
						_ = json.NewEncoder(w).Encode(&got)
					case r.Method == http.MethodGet && r.URL.Path == "/apis/coordination.k8s.io/v1/namespaces/kube-system/leases/kube-dc-installer":
						if got := lease.Load(); got != nil {
							_ = json.NewEncoder(w).Encode(got)
							return
						}
						w.WriteHeader(404)
						_ = json.NewEncoder(w).Encode(&metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404})
					default:
						w.WriteHeader(400)
					}
				})
				config := childConfig(t, c.compiled)
				config.Clusters[c.compiled.Spec.Name].Server = endpoint
				config.Clusters[c.compiled.Spec.Name].TLSServerName = "kube-api.example.test"
				config.Clusters[c.compiled.Spec.Name].CertificateAuthorityData = ca
				body, err := clientcmd.Write(*config)
				if err != nil {
					t.Fatal(err)
				}
				return body
			})
			ambient := filepath.Join(t.TempDir(), "ambient.yaml")
			if err := os.WriteFile(ambient, []byte("unrelated config"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KUBECONFIG", ambient)
			t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
			c.clusterClient = func(ctx context.Context, compiled Compiled, path string, node rke2.VerifiedNode) (kubernetes.Interface, error) {
				config, err := verifyDirectChildBootstrapAPI(ctx, compiled, path, endpoint, node)
				if err != nil {
					return nil, err
				}
				return kubernetes.NewForConfig(config)
			}
			err := c.reserveChildCluster(context.Background())
			if drift {
				if err == nil || creates.Load() != 0 {
					t.Fatalf("changed Node UID admitted a Lease mutation: %v %d", err, creates.Load())
				}
			} else {
				if err != nil || creates.Load() != 1 {
					t.Fatalf("verified child failed: %v", err)
				}
				status, err := c.Status(context.Background())
				if err != nil || status.Cluster.State != "held" || !status.Cluster.MatchesProof {
					t.Fatalf("%+v %v", status.Cluster, err)
				}
			}
			body, _ := os.ReadFile(ambient)
			if string(body) != "unrelated config" {
				t.Fatal("child reservation changed ambient config")
			}
		})
	}
}
