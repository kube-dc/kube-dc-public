package auth

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCustomCADoesNotRemoveSystemTrust(t *testing.T) {
	systemRoots, err := x509.SystemCertPool()
	if err != nil {
		t.Skipf("system trust store unavailable: %v", err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	client := CreateHTTPClient(string(caPEM), false)
	defer client.CloseIdleConnections()
	// The supplied private CA is usable with verification still enabled.
	response, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	tlsConfig := client.Transport.(*http.Transport).TLSClientConfig
	if tlsConfig.InsecureSkipVerify {
		t.Fatal("supplying a CA must not disable certificate verification")
	}
	// OAuth and token refresh may go to a publicly signed Keycloak endpoint,
	// so all system roots must remain trusted alongside the API's private CA.
	systemRoots.AddCert(srv.Certificate())
	if tlsConfig.RootCAs == nil || !tlsConfig.RootCAs.Equal(systemRoots) {
		t.Fatal("private API CA replaced the system roots used to trust Keycloak")
	}
}
