package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/accept"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/breakglass"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/openbao"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// guidedPlatformEngine reuses Apply and the same finalization functions as
// bootstrap init. The coordinator supplies every client and execution gate.
type guidedPlatformEngine struct{ token string }

func (d guidedPlatformEngine) Run(ctx context.Context, e setup.PlatformExecution) error {
	o := e.Options
	o.NoTTY = true // Access credentials never enter the saved transcript.
	plan, err := clusterinit.BuildPlan(&o, clusterinit.FleetState{})
	if err != nil {
		return err
	}
	if err := clusterinit.Apply(ctx, clusterinit.ApplyOptions{
		Plan: plan, FleetRepo: o.Repo, NodeExternalIP: o.NodeExternalIP, NodeCIDR: e.Environment["NODE_CIDR"],
		RequestedIngressLayer: o.IngressAddressLayer, Sets: o.Sets, NodeNICs: o.NodeNICs, ObjectStorage: o.ObjectStorage(), VMStorage: o.VMStorage(), ImageAccel: o.ImageAccel(),
		KubeconfigPath: e.Kubeconfig, Runner: e.Session.Scripts, Git: e.Session.Git, K8s: e.Session.K8s,
		GitHubToken: d.token, Provider: o.Provider, ProviderHost: o.ProviderHost, GitHubOwner: o.GitHubOwner, GitHubRepo: o.GitHubRepo, NoPush: o.NoPush,
		Out: e.Out, Reporter: e.Reporter, Prepared: &e.Prepared,
	}); err != nil {
		return err
	}
	if o.NoPush {
		return nil
	}
	result := runPostApplyWithRuntime(ctx, e.Out, &o, e.Reporter, true, plan.ManagedServicesMode == "on", &postApplyRuntime{Session: e.Session, Kubeconfig: e.Kubeconfig, Token: d.token})
	if err := result.ActionRequired(); err != nil {
		return err
	}
	return step(e.Reporter, clusterinit.StepPlatformAccept, func() error {
		report, err := accept.Run(ctx, accept.Options{K8s: e.Session.K8s, Cluster: o.Name, Domain: o.Domain, Out: e.Out})
		if err != nil {
			return err
		}
		if report.State != accept.StateUsable {
			return fmt.Errorf("platform acceptance has unmet required checks")
		}
		return nil
	})
}

func effectObservation(ids []string, value any) setup.PlatformObservation {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	o := setup.PlatformObservation{Verified: true}
	for _, id := range ids {
		o.Checks = append(o.Checks, setup.EffectCheck{ID: id, SHA256: hash})
	}
	return o
}

func (d guidedPlatformEngine) Observe(ctx context.Context, e setup.PlatformExecution, id clusterinit.StepID) (setup.PlatformObservation, error) {
	if id != clusterinit.StepFluxInstall && id != clusterinit.StepReconcile && id != clusterinit.StepOIDCCutover {
		return d.observeOnce(ctx, e, id)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		observation, err := d.observeOnce(ctx, e, id)
		if err == nil {
			return observation, nil
		}
		select {
		case <-ctx.Done():
			return setup.PlatformObservation{}, fmt.Errorf("platform postconditions were not confirmed within the verification window")
		case <-time.After(time.Second):
		}
	}
}

func (d guidedPlatformEngine) observeOnce(ctx context.Context, e setup.PlatformExecution, id clusterinit.StepID) (setup.PlatformObservation, error) {
	failed := setup.PlatformObservation{}
	check := func(name string, value any) (setup.PlatformObservation, error) {
		return effectObservation([]string{name}, value), nil
	}
	switch id {
	case clusterinit.StepScaffold:
		if err := clusterinit.VerifyPreparedPublication(e.Prepared); err != nil {
			return failed, err
		}
		return check("prepared-publication", e.Prepared.Hash)
	case clusterinit.StepCommitPush:
		binding, err := setup.InspectGitWorkingBinding(ctx, e.Options.Repo)
		if err != nil {
			return failed, err
		}
		diff, err := e.Session.Git.Diff(ctx, e.Options.Repo)
		if err != nil || len(diff.Files) != 0 || (!e.Options.NoPush && binding.Head != binding.RemoteHead) {
			return failed, fmt.Errorf("publication is not a clean confirmed Git revision")
		}
		return check("git-publication", binding)
	case clusterinit.StepIngressNodes:
		plan, err := clusterinit.BuildPlan(&e.Options, clusterinit.FleetState{})
		if err != nil {
			return failed, err
		}
		labels, err := e.Session.K8s.NodeLabels(ctx)
		if err != nil {
			return failed, err
		}
		if len(plan.IngressNodes) == 0 {
			return failed, fmt.Errorf("no reviewed ingress node set")
		}
		for _, node := range plan.IngressNodes {
			if labels[node][plan.IngressNodeLabel] != "true" {
				return failed, fmt.Errorf("ingress label is not confirmed")
			}
		}
		return check("ingress-nodes", plan.IngressNodes)
	case clusterinit.StepFluxInstall:
		objects, err := e.Session.K8s.ListResourceObjects(ctx, "kustomize.toolkit.fluxcd.io", "v1", "kustomizations", "flux-system")
		if err != nil {
			return failed, err
		}
		for _, obj := range objects {
			refNamespace := nestedString(obj, "spec", "sourceRef", "namespace")
			if nestedString(obj, "metadata", "name") == "flux-system" && nestedString(obj, "metadata", "uid") != "" && nestedString(obj, "spec", "path") == "./clusters/"+e.Options.Name && nestedString(obj, "spec", "decryption", "provider") == "sops" && nestedString(obj, "spec", "sourceRef", "kind") == "GitRepository" && nestedString(obj, "spec", "sourceRef", "name") == "flux-system" && (refNamespace == "" || refNamespace == "flux-system") {
				sources, err := e.Session.K8s.ListResourceObjects(ctx, "source.toolkit.fluxcd.io", "v1", "gitrepositories", "flux-system")
				if err != nil {
					return failed, err
				}
				binding, err := setup.InspectGitBinding(ctx, e.Options.Repo)
				if err != nil {
					return failed, err
				}
				for _, source := range sources {
					if nestedString(source, "metadata", "name") == "flux-system" && nestedString(source, "metadata", "uid") != "" && guidedSourceMatches(nestedString(source, "spec", "url"), binding.Remote, e.Options) && nestedString(source, "spec", "ref", "branch") == binding.Branch && resourceReady(source) && strings.HasSuffix(nestedString(source, "status", "artifact", "revision"), ":"+binding.Head) {
						return check("flux-bootstrap", []any{nestedValue(obj, "spec"), nestedString(obj, "metadata", "uid"), nestedString(source, "metadata", "uid")})
					}
				}
			}
		}
		return failed, fmt.Errorf("Flux source or root decryption/path is not confirmed")
	case clusterinit.StepBreakGlass:
		sa, err := e.Client.CoreV1().ServiceAccounts(breakglass.BreakGlassNamespace).Get(ctx, breakglass.BreakGlassServiceAccountName, metav1.GetOptions{})
		if err != nil {
			return failed, err
		}
		secret, err := e.Client.CoreV1().Secrets(breakglass.BreakGlassNamespace).Get(ctx, breakglass.BreakGlassSecretName, metav1.GetOptions{})
		if err != nil {
			return failed, err
		}
		binding, err := e.Client.RbacV1().ClusterRoleBindings().Get(ctx, breakglass.BreakGlassClusterRoleBinding, metav1.GetOptions{})
		if err != nil {
			return failed, err
		}
		if sa.UID == "" || secret.UID == "" || binding.UID == "" || binding.RoleRef.Name != "cluster-admin" || binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.APIGroup != "rbac.authorization.k8s.io" || len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != sa.Name || binding.Subjects[0].Namespace != sa.Namespace || secret.Annotations["kubernetes.io/service-account.uid"] != string(sa.UID) || secret.Annotations["kubernetes.io/service-account.name"] != sa.Name || string(secret.Type) != "kubernetes.io/service-account-token" || len(secret.Data["token"]) == 0 || len(secret.Data["ca.crt"]) == 0 {
			return failed, fmt.Errorf("break-glass identity is incomplete")
		}
		body, err := e.Session.SOPS.Decrypt(ctx, filepath.Join(e.Options.Repo, "clusters", e.Options.Name, breakglass.BreakGlassFileBasename))
		if err != nil {
			return failed, err
		}
		defer func() { clear(body) }()
		config, err := clientcmd.Load(body)
		if err != nil || len(config.Contexts) != 1 || len(config.Clusters) != 1 || len(config.AuthInfos) != 1 {
			return failed, fmt.Errorf("encrypted recovery kubeconfig is invalid")
		}
		context := config.Contexts[e.Options.Name]
		if context == nil || config.CurrentContext != e.Options.Name {
			return failed, fmt.Errorf("recovery context differs from the reviewed cluster")
		}
		cluster, auth := config.Clusters[context.Cluster], config.AuthInfos[context.AuthInfo]
		expectedServer := e.Environment["KUBE_API_EXTERNAL_URL"]
		if expectedServer == "" {
			expectedServer = "https://kube-api." + e.Options.Domain + ":6443"
		}
		if cluster == nil || auth == nil || cluster.Server != expectedServer || cluster.InsecureSkipTLSVerify || cluster.CertificateAuthority != "" || !bytes.Equal(cluster.CertificateAuthorityData, secret.Data["ca.crt"]) || auth.Token != string(secret.Data["token"]) || auth.Exec != nil || auth.AuthProvider != nil || auth.TokenFile != "" {
			return failed, fmt.Errorf("encrypted recovery credential does not match its live ServiceAccount")
		}
		return check("break-glass", []string{string(sa.UID), string(secret.UID), string(binding.UID)})
	case clusterinit.StepReconcile, clusterinit.StepGPUInventory, clusterinit.StepGPUOperator, clusterinit.StepGPUHAMi, clusterinit.StepGPUProduct:
		graph, err := e.Session.K8s.DiscoverFluxGraph(ctx)
		if err != nil {
			return failed, err
		}
		target := "platform"
		name := "platform-reconcile"
		if id != clusterinit.StepReconcile {
			name = string(id)
			for _, t := range gpuReconcileTargets(e.Options.GPU()) {
				if t.step == id {
					target = t.name
				}
			}
		}
		for _, node := range graph.Nodes {
			if node.Name == target && node.Ready {
				return check(name, []string{node.Name, node.Path})
			}
		}
		return failed, fmt.Errorf("selected reconciliation target is not Ready")
	case clusterinit.StepOpenBao:
		pods, err := e.Session.OpenBao.PodList(ctx)
		if err != nil || len(pods) == 0 {
			return failed, fmt.Errorf("OpenBao pod inventory is not confirmed")
		}
		for _, pod := range pods {
			status, err := e.Session.OpenBao.Status(ctx, pod)
			if err != nil || !status.Initialized || status.Sealed || status.StorageType != "raft" {
				return failed, fmt.Errorf("OpenBao is not initialized and unsealed on every replica")
			}
		}
		for _, key := range []string{openbao.AnnotationBootstrapFinalized, openbao.AnnotationControllerAuthInstalled} {
			value, err := e.Session.OpenBao.GetAnnotation(ctx, "openbao", key)
			if err != nil || value == "" {
				return failed, fmt.Errorf("OpenBao controller/bootstrap markers are incomplete")
			}
		}
		body, err := e.Session.SOPS.Decrypt(ctx, filepath.Join(e.Options.Repo, "clusters", e.Options.Name, "secrets.enc.yaml"))
		if err != nil {
			return failed, err
		}
		defer func() { clear(body) }()
		var manifest struct {
			StringData map[string]string `json:"stringData"`
		}
		if err := yaml.Unmarshal(body, &manifest); err != nil {
			return failed, fmt.Errorf("OpenBao escrow cannot be decoded")
		}
		for i := 1; i <= 5; i++ {
			if manifest.StringData[fmt.Sprintf("OPENBAO_UNSEAL_KEY_%d", i)] == "" {
				return failed, fmt.Errorf("OpenBao encrypted custody is incomplete")
			}
		}
		return effectObservation([]string{"openbao-health", "openbao-escrow"}, pods), nil
	case clusterinit.StepKeycloakOIDC:
		clients, err := verifyGuidedKeycloakClients(ctx, e)
		if err != nil {
			return failed, err
		}
		return check("keycloak-clients", clients)
	case clusterinit.StepOIDCCutover:
		nodes, err := resolveCutoverNodes(ctx, e.Kubeconfig, nil, oidcCutoverSSHUser(&e.Options), false)
		if err != nil {
			return failed, err
		}
		nodes = overrideFinalizeCutoverHosts(nodes, &e.Options)
		if err := verifyManagedServicesCutover(ctx, e.Session, nodes); err != nil {
			return failed, err
		}
		return check("oidc-cutover", len(nodes))
	case clusterinit.StepManagedServices:
		pins, err := managedServicesOverlayPins(e.Options.Repo, e.Options.Name)
		if err != nil {
			return failed, err
		}
		reader, err := pinnedManagedServicesKubectl(ctx, e.Kubeconfig)
		if err != nil {
			return failed, err
		}
		if err := verifyManagedServicesRelease(ctx, reader, pins); err != nil {
			return failed, err
		}
		if err := checkManagedServicesPublished(ctx, reader, pins); err != nil {
			return failed, err
		}
		plane, err := reader.Get(ctx, "", "servicedataplane", pins["SERVICES_DATAPLANE_NAME"])
		if err != nil {
			return failed, err
		}
		if nestedString(plane, "spec", "backupAdmission", "state") != "Active" || nestedInt(plane, "spec", "backupAdmission", "revision") < 1 {
			return failed, fmt.Errorf("backup admission is not Active")
		}
		nodes, err := resolveCutoverNodes(ctx, e.Kubeconfig, nil, oidcCutoverSSHUser(&e.Options), false)
		if err != nil {
			return failed, err
		}
		nodes = overrideFinalizeCutoverHosts(nodes, &e.Options)
		inventory, err := resolveManagedServicesOSHostnames(ctx, e.Session.SSH, nodes)
		if err != nil {
			return failed, err
		}
		system, err := reader.Get(ctx, "", "namespace", "kube-system")
		if err != nil {
			return failed, err
		}
		leases, err := reader.Get(ctx, "kube-system", "leases", "")
		if err != nil {
			return failed, err
		}
		currentRevision := nestedInt(plane, "spec", "backupAdmission", "revision")
		expected, err := buildManagedServicesAdmissionTopology(inventory, leases, nestedString(system, "metadata", "uid"), currentRevision-1)
		if err != nil {
			return failed, err
		}
		encoded, _ := json.Marshal(nestedValue(plane, "spec", "backupAdmission"))
		var observed managedServicesAdmissionTopology
		if json.Unmarshal(encoded, &observed) != nil || !reflect.DeepEqual(expected, observed) {
			return failed, fmt.Errorf("backup admission differs from live API server identities")
		}
		return effectObservation([]string{"managed-services", "backup-admission"}, []any{pins, nestedValue(plane, "spec", "backupAdmission")}), nil
	case clusterinit.StepPlatformAccept:
		report, err := accept.Run(ctx, accept.Options{K8s: e.Session.K8s, Cluster: e.Options.Name, Domain: e.Options.Domain})
		if err != nil || report.State != accept.StateUsable {
			return failed, fmt.Errorf("required platform acceptance checks do not pass")
		}
		return check("platform-acceptance", report.State)
	}
	return failed, fmt.Errorf("stage has no read-only recovery driver")
}

// Verification logs no credentials and retains only selected client fields.
func verifyGuidedKeycloakClients(ctx context.Context, e setup.PlatformExecution) ([]map[string]any, error) {
	host := e.Environment["KEYCLOAK_HOSTNAME"]
	if host == "" {
		host = "login." + e.Options.Domain
	}
	base := "https://" + host
	parsed, err := url.Parse(base)
	if err != nil || parsed.Hostname() != host || parsed.User != nil {
		return nil, fmt.Errorf("invalid reviewed Keycloak hostname")
	}
	secret, err := e.Client.CoreV1().Secrets("keycloak").Get(ctx, "keycloak", metav1.GetOptions{})
	var password []byte
	if err == nil {
		password = secret.Data["admin-password"]
	}
	if len(password) == 0 {
		fallback, err := e.Client.CoreV1().Secrets("flux-system").Get(ctx, "cluster-secrets", metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("Keycloak admin credential could not be read")
		}
		password = fallback.Data["KEYCLOAK_ADMIN_PASSWORD"]
	}
	if len(password) == 0 {
		return nil, fmt.Errorf("Keycloak admin credential is empty")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	ca, err := loadTrustedCAFromOptions(&e.Options)
	if err != nil {
		return nil, err
	}
	if (ca == nil && e.Options.TrustedCAFingerprint != "") || (ca != nil && ca.Fingerprint != e.Options.TrustedCAFingerprint) {
		return nil, fmt.Errorf("identity trust roots changed after review")
	}
	if ca != nil && !roots.AppendCertsFromPEM(ca.PEM) {
		return nil, fmt.Errorf("reviewed identity CA is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, token string, body io.Reader) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, base+path, body)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("verified Keycloak request failed")
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		if err != nil || len(data) > 1<<20 || res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Keycloak postcondition request was not confirmed")
		}
		return data, nil
	}
	form := url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {"admin"}, "password": {string(password)}}
	body, err := request(http.MethodPost, "/realms/master/protocol/openid-connect/token", "", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	defer func() { clear(body) }()
	var auth struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &auth) != nil || auth.AccessToken == "" {
		return nil, fmt.Errorf("Keycloak verification authentication failed")
	}
	var observed []map[string]any
	for _, id := range []string{"flux-web", "grafana", "kube-dc-admin", "kube-dc-admin-console", "kube-dc-admin-backend"} {
		data, err := request(http.MethodGet, "/admin/realms/master/clients?clientId="+url.QueryEscape(id), auth.AccessToken, nil)
		if err != nil {
			return nil, err
		}
		var clients []map[string]any
		if json.Unmarshal(data, &clients) != nil || len(clients) != 1 {
			return nil, fmt.Errorf("required OIDC client is missing or duplicated")
		}
		c := clients[0]
		if c["clientId"] != id || c["enabled"] != true {
			return nil, fmt.Errorf("required OIDC client is disabled or mismatched")
		}
		public := id == "kube-dc-admin" || id == "kube-dc-admin-console"
		if c["publicClient"] != public || (public && nestedString(c, "attributes", "pkce.code.challenge.method") != "S256") {
			return nil, fmt.Errorf("OIDC client ownership or PKCE differs")
		}
		observed = append(observed, map[string]any{"id": c["id"], "clientId": id, "publicClient": public, "redirectUris": c["redirectUris"], "webOrigins": c["webOrigins"]})
	}
	return observed, nil
}

var _ setup.PlatformEngine = guidedPlatformEngine{}

func guidedSourceMatches(source, remote string, o clusterinit.InitOptions) bool {
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "https" && u.Scheme != "ssh") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	_, path, ok := remoteIdentity(remote)
	if !ok {
		return false
	}
	if strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git") != strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".git") {
		return false
	}
	host, err := clusterinit.ResolveRepositoryProviderHost(context.Background(), o.Provider, o.ProviderHost, o.Repo)
	if err != nil {
		return false
	}
	if u.Scheme == "https" {
		authority := u.Host
		if u.Port() == "443" {
			authority = u.Hostname()
		}
		return u.User == nil && strings.EqualFold(authority, host)
	}
	expected, _, ok := remoteIdentity(remote)
	return ok && strings.EqualFold(u.Host, expected)
}
