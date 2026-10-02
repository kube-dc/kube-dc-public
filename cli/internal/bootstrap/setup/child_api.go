package setup

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const maxChildKubeconfigBytes = 4 << 20
const childAPIVerifyTimeout = 20 * time.Second

// VerifyChildBootstrapAPI checks the fresh cluster through the reviewed first
// server's management IP. TLS still verifies kube-api.<domain>, the name in
// the RKE2 server certificate. No public DNS or platform ingress is needed.
// It returns a config for the same direct transport after identity is checked.
// The caller must not use it as production apply authorization.
func VerifyChildBootstrapAPI(ctx context.Context, c Compiled, path string, firstServer rke2.VerifiedNode) (*rest.Config, error) {
	if err := checkCompiled(c); err != nil {
		return nil, err
	}
	primary := primaryServer(c.Spec.Hosts)
	if net.ParseIP(primary.ManagementAddress) == nil {
		return nil, fmt.Errorf("reviewed first server has no management IP")
	}
	return verifyChildBootstrapAPI(ctx, c, path, directChildAPIEndpoint(primary), firstServer)
}

func directChildAPIEndpoint(primary Host) string {
	return "https://" + net.JoinHostPort(primary.ManagementAddress, "6443")
}

// PublishVerifiedChildBootstrapKubeconfig creates a second private config for
// the bootstrap scripts. It dials the reviewed first-server IP and preserves
// the public API hostname for TLS verification. The original public-endpoint
// config stays separate for use after platform networking becomes available.
func PublishVerifiedChildBootstrapKubeconfig(ctx context.Context, c Compiled, publicPath, bootstrapPath string, firstServer rke2.VerifiedNode) error {
	if err := checkCompiled(c); err != nil {
		return err
	}
	primary := primaryServer(c.Spec.Hosts)
	if net.ParseIP(primary.ManagementAddress) == nil {
		return fmt.Errorf("reviewed first server has no management IP")
	}
	return publishVerifiedChildBootstrapKubeconfig(ctx, c, publicPath, bootstrapPath, directChildAPIEndpoint(primary), firstServer)
}

func publishVerifiedChildBootstrapKubeconfig(ctx context.Context, c Compiled, publicPath, bootstrapPath, endpoint string, firstServer rke2.VerifiedNode) error {
	if publicPath == bootstrapPath {
		return fmt.Errorf("bootstrap kubeconfig must have a separate private path")
	}
	verified, err := verifyChildBootstrapAPI(ctx, c, publicPath, endpoint, firstServer)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := c.Spec.Name
	config := clientcmdapi.NewConfig()
	config.CurrentContext = name
	config.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	config.Clusters[name] = &clientcmdapi.Cluster{
		Server: endpoint, TLSServerName: verified.TLSClientConfig.ServerName,
		CertificateAuthorityData: append([]byte(nil), verified.TLSClientConfig.CAData...),
	}
	config.AuthInfos[name] = &clientcmdapi.AuthInfo{
		ClientCertificateData: append([]byte(nil), verified.TLSClientConfig.CertData...),
		ClientKeyData:         append([]byte(nil), verified.TLSClientConfig.KeyData...),
		Token:                 verified.BearerToken,
	}
	if err := checkChildKubeconfigWithTLSName(config, name, endpoint, "kube-api."+c.Init.Domain); err != nil {
		return err
	}
	body, err := clientcmd.Write(*config)
	if err != nil {
		return fmt.Errorf("serialize bootstrap kubeconfig: %w", err)
	}
	return writeExclusivePrivateKubeconfig(bootstrapPath, body)
}

func verifyChildBootstrapAPI(ctx context.Context, c Compiled, path, endpoint string, firstServer rke2.VerifiedNode) (*rest.Config, error) {
	return verifyChildAPI(ctx, c, path, endpoint, "https://kube-api."+c.Init.Domain+":6443", "", firstServer)
}

// VerifyDirectChildBootstrapAPI rechecks the private, direct-IP config before
// a child-cluster session uses it. It does not authorize platform writes.
func VerifyDirectChildBootstrapAPI(ctx context.Context, c Compiled, path string, firstServer rke2.VerifiedNode) (*rest.Config, error) {
	if err := checkCompiled(c); err != nil {
		return nil, err
	}
	primary := primaryServer(c.Spec.Hosts)
	if net.ParseIP(primary.ManagementAddress) == nil {
		return nil, fmt.Errorf("reviewed first server has no management IP")
	}
	endpoint := directChildAPIEndpoint(primary)
	return verifyDirectChildBootstrapAPI(ctx, c, path, endpoint, firstServer)
}

func verifyDirectChildBootstrapAPI(ctx context.Context, c Compiled, path, endpoint string, firstServer rke2.VerifiedNode) (*rest.Config, error) {
	return verifyChildAPI(ctx, c, path, endpoint, endpoint, "kube-api."+c.Init.Domain, firstServer)
}

// OpenVerifiedChildSession binds real bootstrap ports to the direct child
// transport. The caller still needs approved readiness, target ownership, and
// an effects review before it starts any platform mutation.
func OpenVerifiedChildSession(ctx context.Context, c Compiled, bootstrapPath string, firstServer rke2.VerifiedNode) (*bootstrap.Session, error) {
	if err := checkCompiled(c); err != nil {
		return nil, err
	}
	primary := primaryServer(c.Spec.Hosts)
	if net.ParseIP(primary.ManagementAddress) == nil {
		return nil, fmt.Errorf("reviewed first server has no management IP")
	}
	return openVerifiedChildSession(ctx, c, bootstrapPath, directChildAPIEndpoint(primary), firstServer)
}

func openVerifiedChildSession(ctx context.Context, c Compiled, bootstrapPath, endpoint string, firstServer rke2.VerifiedNode) (*bootstrap.Session, error) {
	verified, err := verifyDirectChildBootstrapAPI(ctx, c, bootstrapPath, endpoint, firstServer)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return bootstrap.NewRealSessionWithConfig(bootstrap.Options{FleetRepoPath: c.Init.Repo, Kubeconfig: bootstrapPath, Cluster: c.Spec.Name}, verified)
}

func verifyChildAPI(ctx context.Context, c Compiled, path, endpoint, expectedServer, expectedTLSName string, firstServer rke2.VerifiedNode) (*rest.Config, error) {
	if err := checkCompiled(c); err != nil {
		return nil, err
	}
	primary := primaryServer(c.Spec.Hosts)
	if firstServer.Name != primary.ID || firstServer.Role != "server" || firstServer.InternalIP != primary.ManagementAddress || firstServer.UID == "" {
		return nil, fmt.Errorf("child bootstrap API needs the pinned first-server node evidence")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("child bootstrap API needs an explicit absolute kubeconfig path")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("child kubeconfig directory is missing or not private")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("child kubeconfig is missing or not private")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open child kubeconfig: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("child kubeconfig changed while opening it")
	}
	body, err := io.ReadAll(io.LimitReader(f, maxChildKubeconfigBytes+1))
	if err != nil || len(body) > maxChildKubeconfigBytes {
		return nil, fmt.Errorf("child kubeconfig cannot be read within its size limit")
	}
	config, err := clientcmd.Load(body)
	if err != nil {
		return nil, fmt.Errorf("child kubeconfig cannot be parsed: %w", err)
	}
	serverName := "kube-api." + c.Init.Domain
	if err := checkChildKubeconfigWithTLSName(config, c.Spec.Name, expectedServer, expectedTLSName); err != nil {
		return nil, err
	}
	restConfig, err := clientcmd.NewNonInteractiveClientConfig(*config, c.Spec.Name, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("build child API config: %w", err)
	}
	restConfig.Host = endpoint
	restConfig.TLSClientConfig.ServerName = serverName
	// This bootstrap transport must dial the reviewed IP, even when the
	// workstation has a proxy in its environment.
	restConfig.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	restConfig.Timeout = childAPIVerifyTimeout
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("build child API client: %w", err)
	}
	verifyCtx, cancel := context.WithTimeout(ctx, childAPIVerifyTimeout)
	defer cancel()
	node, err := client.CoreV1().Nodes().Get(verifyCtx, primary.ID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("query reviewed first server in child API: %w", err)
	}
	if node.Name != primary.ID || string(node.UID) != firstServer.UID || !hasControlPlaneRole(node.Labels) || !hasInternalIP(node.Status.Addresses, primary.ManagementAddress) {
		return nil, fmt.Errorf("child API did not confirm the reviewed first server")
	}
	return restConfig, nil
}

func hasControlPlaneRole(labels map[string]string) bool {
	_, controlPlane := labels["node-role.kubernetes.io/control-plane"]
	_, master := labels["node-role.kubernetes.io/master"]
	return controlPlane || master
}

func hasInternalIP(addresses []corev1.NodeAddress, want string) bool {
	expected := net.ParseIP(want)
	for _, address := range addresses {
		if address.Type == corev1.NodeInternalIP && expected.Equal(net.ParseIP(address.Address)) {
			return true
		}
	}
	return false
}
