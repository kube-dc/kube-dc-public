package discover

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"time"
)

// FetchCA dials the API server's TLS endpoint, captures the served
// certificate chain, and returns the PEM-encoded chain UNLESS the server
// is using a publicly-trusted certificate (then returns ""). System trust
// handles verification for public chains, so embedding the CA buys
// nothing and risks staleness on rotation.
//
// dialTimeout caps the connection attempt; 5s is a sensible default for
// reachability probes. Used by:
//   - The cluster probe in this package (to build a trusting http.Client).
//   - `kube-dc bootstrap kubeconfig` (to embed a CA into the synthesised
//     kubeconfig template when system trust isn't enough).
func FetchCA(ctx context.Context, server string, dialTimeout time.Duration) (string, error) {
	host, err := tlsHostPort(server)
	if err != nil {
		return "", err
	}

	dialer := &net.Dialer{Timeout: dialTimeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", host, err)
	}
	defer rawConn.Close()

	// Verify against system trust first — if it's valid the API is fronted
	// by a publicly-trusted cert and there's nothing useful to embed.
	trustedConn := tls.Client(rawConn, &tls.Config{ServerName: tlsServerName(host)})
	if err := trustedConn.HandshakeContext(ctx); err == nil {
		_ = trustedConn.Close()
		return "", nil
	}
	_ = trustedConn.Close()

	rawConn2, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", fmt.Errorf("re-dial %s: %w", host, err)
	}
	defer rawConn2.Close()

	insecureConn := tls.Client(rawConn2, &tls.Config{
		ServerName:         tlsServerName(host),
		InsecureSkipVerify: true, // we only want the chain, not to trust it
	})
	if err := insecureConn.HandshakeContext(ctx); err != nil {
		return "", fmt.Errorf("tls handshake %s: %w", host, err)
	}
	defer insecureConn.Close()

	state := insecureConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("no peer certificates from %s", host)
	}

	// Embed the highest cert in the chain that's a CA. If the chain
	// contains an explicit root, use that; otherwise fall back to the
	// last intermediate or the leaf for self-signed clusters.
	var caCert *x509.Certificate
	for _, c := range state.PeerCertificates {
		if c.IsCA {
			caCert = c
		}
	}
	if caCert == nil {
		caCert = state.PeerCertificates[len(state.PeerCertificates)-1]
	}

	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCert.Raw,
	})), nil
}

func tlsHostPort(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("parse server URL %q: %w", server, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server URL has no host: %q", server)
	}
	if u.Port() != "" {
		return u.Host, nil
	}
	return u.Host + ":443", nil
}

func tlsServerName(hostPort string) string {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort
	}
	return host
}
