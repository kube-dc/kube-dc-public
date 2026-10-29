package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Typed operations are chosen wherever the cluster serves them, with no audit
// gate (the backend audits them), and fall back to the backend route.
func TestKMSTransitUsesTheTypedOperationsWhereServed(t *testing.T) {
	var bodies []map[string]any
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/apis/platform.kube-dc.com/v1alpha1":
			_, _ = w.Write([]byte(`{"resources":[{"name":"kmsencryptions"},{"name":"kmsdecryptions"}]}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/apis/platform.kube-dc.com/v1alpha1/namespaces/acme-web/"):
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			bodies = append(bodies, b)
			w.WriteHeader(http.StatusCreated)
			if strings.HasSuffix(r.URL.Path, "/kmsencryptions") {
				_, _ = w.Write([]byte(`{"status":{"ciphertext":"vault:v1:abc"}}`))
			} else {
				_, _ = w.Write([]byte(`{"status":{"plaintext":"aGk="}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	t.Setenv("HOME", t.TempDir())
	scope := &secretsScope{Domain: "example.test", APIServer: api.URL, Namespace: "acme-web", AccessToken: "t", K8sInsecure: true}
	transit, err := scope.kmsTransit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := transit.(typedKMSTransit); !ok {
		t.Fatalf("served, but %T was chosen", transit)
	}
	ct, err := transit.Encrypt(context.Background(), "acme-web", "app", "aGk=", "")
	if err != nil || ct != "vault:v1:abc" {
		t.Fatalf("encrypt %q %v", ct, err)
	}
	pt, err := transit.Decrypt(context.Background(), "acme-web", "app", ct, "Y3R4")
	if err != nil || pt != "aGk=" {
		t.Fatalf("decrypt %q %v", pt, err)
	}
	if bodies[0]["kind"] != "KMSEncryption" || bodies[0]["spec"].(map[string]any)["keyRef"].(map[string]any)["name"] != "app" ||
		bodies[1]["kind"] != "KMSDecryption" || bodies[1]["spec"].(map[string]any)["context"] != "Y3R4" {
		t.Fatalf("requests %+v", bodies)
	}

	// Not served: the backend route.
	t.Setenv("HOME", t.TempDir())
	none := httptest.NewTLSServer(http.NotFoundHandler())
	defer none.Close()
	scope = &secretsScope{Domain: "example.test", APIServer: none.URL, Namespace: "acme-web", AccessToken: "t", K8sInsecure: true}
	if transit, err = scope.kmsTransit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := transit.(legacyKMSTransit); !ok {
		t.Fatalf("not served, but %T was chosen", transit)
	}
}
