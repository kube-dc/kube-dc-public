package setup

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// NewExclusiveKubeconfigHandoff prepares an explicit, private destination for
// the child admin config. It creates the file only after a valid config arrives
// and never merges with or changes the operator's ambient kubeconfig. The
// caller must retain the returned path for the child-cluster session.
func NewExclusiveKubeconfigHandoff(c Compiled, path string) (KubeconfigHandoff, error) {
	if err := checkCompiled(c); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return nil, fmt.Errorf("child kubeconfig needs an explicit absolute file path")
	}
	name := c.Spec.Name
	server := "https://kube-api." + c.Init.Domain + ":6443"
	return func(cfg *clientcmdapi.Config) error {
		if err := checkChildKubeconfig(cfg, name, server); err != nil {
			return err
		}
		body, err := clientcmd.Write(*cfg)
		if err != nil {
			return fmt.Errorf("serialize child kubeconfig: %w", err)
		}
		return writeExclusivePrivateKubeconfig(path, body)
	}, nil
}

func writeExclusivePrivateKubeconfig(path string, body []byte) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return fmt.Errorf("child kubeconfig needs an explicit absolute file path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create private kubeconfig directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("child kubeconfig directory must be private and cannot be a link")
	}
	file, err := os.CreateTemp(dir, ".kubeconfig-*")
	if err != nil {
		return fmt.Errorf("create private kubeconfig file: %w", err)
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err := file.Write(body); err != nil {
		file.Close()
		return fmt.Errorf("write private kubeconfig: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync private kubeconfig: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close private kubeconfig: %w", err)
	}
	// Link creates the final name atomically and fails if any file or
	// symlink already owns it. A retry must review existing state.
	if err := os.Link(temp, path); err != nil {
		return fmt.Errorf("publish private kubeconfig without replacing an existing file: %w", err)
	}
	folder, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open kubeconfig directory for sync: %w", err)
	}
	syncErr := folder.Sync()
	closeErr := folder.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("sync kubeconfig directory: %v; close: %v", syncErr, closeErr)
	}
	return nil
}

func checkChildKubeconfig(cfg *clientcmdapi.Config, name, server string) error {
	return checkChildKubeconfigWithTLSName(cfg, name, server, "")
}

func checkChildKubeconfigWithTLSName(cfg *clientcmdapi.Config, name, server, tlsName string) error {
	if cfg == nil || cfg.CurrentContext != name || len(cfg.Contexts) != 1 || len(cfg.Clusters) != 1 || len(cfg.AuthInfos) != 1 {
		return fmt.Errorf("child kubeconfig does not contain only the reviewed cluster")
	}
	context := cfg.Contexts[name]
	cluster := cfg.Clusters[name]
	auth := cfg.AuthInfos[name]
	if context == nil || cluster == nil || auth == nil || context.Cluster != name || context.AuthInfo != name ||
		cluster.Server != server || cluster.TLSServerName != tlsName || cluster.InsecureSkipTLSVerify ||
		len(cluster.CertificateAuthorityData) == 0 || cluster.CertificateAuthority != "" || cluster.ProxyURL != "" {
		return fmt.Errorf("child kubeconfig has an unexpected context, server, or TLS trust")
	}
	if err := checkInlineCA(cluster.CertificateAuthorityData); err != nil {
		return err
	}
	hasCert, hasKey := len(auth.ClientCertificateData) != 0, len(auth.ClientKeyData) != 0
	if hasCert != hasKey || (!hasCert && auth.Token == "") || auth.ClientCertificate != "" || auth.ClientKey != "" ||
		auth.TokenFile != "" || auth.Exec != nil || auth.AuthProvider != nil {
		return fmt.Errorf("child kubeconfig needs inline authentication")
	}
	if hasCert {
		pair, err := tls.X509KeyPair(auth.ClientCertificateData, auth.ClientKeyData)
		if err != nil || pair.Leaf == nil || time.Now().Before(pair.Leaf.NotBefore) || time.Now().After(pair.Leaf.NotAfter) {
			return fmt.Errorf("child kubeconfig has an invalid client certificate and key")
		}
	}
	return nil
}

func checkInlineCA(data []byte) error {
	count := 0
	now := time.Now()
	for len(data) != 0 {
		block, rest := pem.Decode(data)
		if block == nil {
			if strings.TrimSpace(string(data)) != "" {
				return fmt.Errorf("child kubeconfig has invalid CA PEM")
			}
			break
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("child kubeconfig has invalid CA PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			return fmt.Errorf("child kubeconfig has an invalid CA certificate")
		}
		count++
		data = rest
	}
	if count == 0 {
		return fmt.Errorf("child kubeconfig has no valid CA certificate")
	}
	return nil
}
