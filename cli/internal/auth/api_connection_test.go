package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

func TestVerifyAPIConnectionSettings(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})
	assertClientGoWorks := func(t *testing.T, config *rest.Config) {
		t.Helper()
		transport, err := rest.TransportFor(config)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		defer client.CloseIdleConnections()
		response, err := client.Get(config.Host + "/version")
		if err != nil {
			t.Fatalf("client-go TLS failed: %v", err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("HTTP %d", response.StatusCode)
		}
	}
	t.Run("explicit TLS server name", func(t *testing.T) {
		server := strings.Replace(api.URL, "127.0.0.1", "localhost", 1)
		assertClientGoWorks(t, &rest.Config{Host: server, TLSClientConfig: rest.TLSClientConfig{CAData: ca, ServerName: "example.com"}})
		err := VerifyAPIConnection(context.Background(), server, string(ca), APIConnection{TLSServerName: "example.com"})
		if err != nil {
			t.Fatalf("preflight rejected working TLS name: %v", err)
		}
		if err := VerifyAPIConnection(context.Background(), server, string(ca), APIConnection{TLSServerName: "wrong.invalid"}); err == nil {
			t.Fatal("wrong TLS name was accepted")
		}
	})
	t.Run("API reachable through a proxy", func(t *testing.T) {
		apiURL, _ := url.Parse(api.URL)
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodConnect {
				http.Error(w, "CONNECT required", 405)
				return
			}
			upstream, err := net.Dial("tcp", apiURL.Host)
			if err != nil {
				http.Error(w, "upstream failed", 502)
				return
			}
			downstream, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				upstream.Close()
				return
			}
			_, _ = downstream.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			go func() { defer upstream.Close(); defer downstream.Close(); _, _ = io.Copy(upstream, downstream) }()
			go func() { defer upstream.Close(); defer downstream.Close(); _, _ = io.Copy(downstream, upstream) }()
		}))
		defer proxy.Close()
		proxyURL, _ := url.Parse(proxy.URL)
		server := "https://127.0.0.1:1"
		assertClientGoWorks(t, &rest.Config{Host: server, TLSClientConfig: rest.TLSClientConfig{CAData: ca}, Proxy: http.ProxyURL(proxyURL)})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := VerifyAPIConnection(ctx, server, string(ca), APIConnection{ProxyURL: proxy.URL})
		if err != nil {
			t.Fatalf("preflight ignored working proxy: %v", err)
		}
	})
}

func TestVerifyExplicitPinnedServerCertificate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		BasicConstraintsValid: true, IsCA: false, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	api.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	api.StartTLS()
	defer api.Close()
	pinned := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	transport, err := rest.TransportFor(&rest.Config{Host: api.URL, TLSClientConfig: rest.TLSClientConfig{CAData: pinned}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get(api.URL + "/version")
	if err != nil {
		t.Fatalf("client-go rejected explicit pinned trust: %v", err)
	}
	response.Body.Close()
	err = VerifyAPIServer(context.Background(), api.URL, string(pinned))
	if err != nil {
		t.Fatalf("explicit server pin rejected: %v", err)
	}
	if err := ValidateCABundle(string(pinned)); err == nil {
		t.Fatal("discovery must still reject non-CA pins")
	}
	fullChain := string(pinned) + testDiscoveryCA(t, true)
	if err := VerifyAPIServer(context.Background(), api.URL, fullChain); err != nil {
		t.Fatalf("explicit full chain rejected: %v", err)
	}
}

func TestVerifyAnnotatedCABundle(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	ca := append([]byte("# Operator CA bundle\nsubject=Example CA\n"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})...)
	transport, err := rest.TransportFor(&rest.Config{Host: api.URL, TLSClientConfig: rest.TLSClientConfig{CAData: ca}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get(api.URL + "/version")
	if err != nil {
		t.Fatalf("client-go rejected annotated CA bundle: %v", err)
	}
	response.Body.Close()
	err = VerifyAPIServer(context.Background(), api.URL, string(ca))
	if err != nil {
		t.Fatalf("operator annotations rejected: %v", err)
	}
	normalized, err := NormalizeTrustBundle(string(ca))
	if err != nil || strings.Contains(normalized, "subject=") || strings.Contains(normalized, "Operator") {
		t.Fatalf("annotations were persisted: %v", err)
	}
	if err := ValidateCABundle(string(ca)); err == nil {
		t.Fatal("discovery must still reject annotations")
	}
}

func TestExplicitTrustRejectsNonCertificatePEM(t *testing.T) {
	ca := testDiscoveryCA(t, true)
	for _, bundle := range []string{
		ca + "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n" + ca,
		ca + "-----BEGIN CERTIFICATE-----\nmalformed",
		"-----BEGIN CERTIFICATE-----\nmalformed\n" + ca,
		ca + "-----BEGIN PUBLIC KEY-----\nkey\n-----END PUBLIC KEY-----",
		"annotations without certificates",
	} {
		if _, err := NormalizeTrustBundle(bundle); err == nil {
			t.Fatal("invalid operator trust accepted")
		}
		if err := ValidateCABundle(bundle); err == nil {
			t.Fatal("invalid discovery trust accepted")
		}
	}
}

func TestVerifyAPIConnectionDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer target.Close()
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("preflight sent credentials or an unexpected path")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer api.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}))
	if err := VerifyAPIServer(context.Background(), api.URL, ca); err != nil {
		t.Fatal(err)
	}
	if followed.Load() {
		t.Fatal("API preflight followed a redirect")
	}
}

func TestVerifyAPIConnectionCancellation(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer api.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := VerifyAPIServer(ctx, api.URL, ca)
	if !errors.Is(err, context.DeadlineExceeded) || IsCertificateTrustError(err) {
		t.Fatalf("timeout misclassified: %v", err)
	}
}
