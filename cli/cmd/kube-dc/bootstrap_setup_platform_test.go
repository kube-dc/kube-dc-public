package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/setup"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDirectServicesWritesCheckMutationGuard(t *testing.T) {
	denied := errors.New("reviewed owner changed")
	ctx := ports.WithMutationGuard(context.Background(), func(context.Context) error { return denied })
	t.Setenv("PATH", t.TempDir())
	k := managedServicesKubectl{kubeconfig: "/private/child", contextName: "reviewed"}
	if err := k.ResetHelmRelease(ctx, "namespace", "release", "version", "nonce", 1); !errors.Is(err, denied) {
		t.Fatalf("reset bypassed guard: %v", err)
	}
	if err := applyManagedServicesAdmission(ctx, k, "plane", managedServicesAdmissionTopology{Revision: 1, State: "Active", Servers: []managedServicesAdmissionServer{{Address: "192.0.2.10:6443"}}}); !errors.Is(err, denied) {
		t.Fatalf("admission bypassed guard: %v", err)
	}
}

type guidedSOPSFixture struct {
	ports.SOPSClient
	body []byte
}

func (s guidedSOPSFixture) Decrypt(context.Context, string) ([]byte, error) {
	return append([]byte(nil), s.body...), nil
}

func TestGuidedBreakGlassPostconditionChecksLiveCredentialBinding(t *testing.T) {
	for _, kind := range []string{"correct", "different-token", "wrong-subject"} {
		t.Run(kind, func(t *testing.T) {
			token := "live-private-token"
			subject := "break-glass"
			if kind == "different-token" {
				token = "obsolete-private-token"
			}
			if kind == "wrong-subject" {
				subject = "foreign"
			}
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: demo\ncontexts:\n- name: demo\n  context: {cluster: demo, user: demo}\nclusters:\n- name: demo\n  cluster: {server: 'https://kube-api.example.test:6443', certificate-authority-data: Y2E=}\nusers:\n- name: demo\n  user: {token: %s}\n", token)
			client := fake.NewClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "break-glass", Namespace: "kube-system", UID: "account-uid"}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "break-glass-token", Namespace: "kube-system", UID: "token-uid", Annotations: map[string]string{"kubernetes.io/service-account.name": "break-glass", "kubernetes.io/service-account.uid": "account-uid"}}, Type: corev1.SecretTypeServiceAccountToken, Data: map[string][]byte{"token": []byte("live-private-token"), "ca.crt": []byte("ca")}}, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "break-glass", UID: "binding-uid"}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "cluster-admin"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: subject, Namespace: "kube-system"}}})
			x := setup.PlatformExecution{Options: clusterinit.InitOptions{Name: "demo", Domain: "example.test", Repo: "/fixture"}, Client: client, Session: &bootstrap.Session{SOPS: guidedSOPSFixture{body: []byte(config)}}}
			observation, err := (guidedPlatformEngine{}).observeOnce(context.Background(), x, clusterinit.StepBreakGlass)
			if kind == "correct" {
				if err != nil || !observation.Verified {
					t.Fatalf("valid binding failed: %v", err)
				}
			} else if err == nil || observation.Verified {
				t.Fatal("foreign recovery credential accepted")
			}
			encoded := fmt.Sprint(observation)
			if strings.Contains(encoded, "private-token") {
				t.Fatal("receipt contains credential contents")
			}
		})
	}
}

type guidedFluxFixture struct {
	ports.K8sClient
	sourceReads int
}

func (f *guidedFluxFixture) ListResourceObjects(_ context.Context, group, version, resource, namespace string) ([]map[string]any, error) {
	if resource == "gitrepositories" {
		f.sourceReads++
		return nil, errors.New("must not inspect unrelated source")
	}
	return []map[string]any{{"metadata": map[string]any{"name": "flux-system", "uid": "root-uid"}, "spec": map[string]any{"path": "./clusters/demo", "decryption": map[string]any{"provider": "sops"}, "sourceRef": map[string]any{"kind": "GitRepository", "name": "foreign-source"}}}}, nil
}
func TestGuidedFluxRecoveryRejectsForeignSourceRef(t *testing.T) {
	f := &guidedFluxFixture{}
	x := setup.PlatformExecution{Options: clusterinit.InitOptions{Name: "demo"}, Session: &bootstrap.Session{K8s: f}}
	result, err := (guidedPlatformEngine{}).observeOnce(context.Background(), x, clusterinit.StepFluxInstall)
	if err == nil || result.Verified || f.sourceReads != 0 {
		t.Fatal("root linked to another source reached positive source verification")
	}
}

func TestGuidedFluxSourceChecksProviderPathAndTransport(t *testing.T) {
	o := clusterinit.InitOptions{Provider: clusterinit.ProviderGitHub}
	for _, tc := range []struct {
		source, remote string
		want           bool
	}{
		{"https://github.com/reviewed/fleet.git", "git@github.com:reviewed/fleet.git", true},
		{"https://github.com/other/fleet.git", "git@github.com:reviewed/fleet.git", false},
		{"https://wrong.example/reviewed/fleet.git", "git@github.com:reviewed/fleet.git", false},
		{"http://github.com/reviewed/fleet.git", "git@github.com:reviewed/fleet.git", false},
		{"https://user:private@github.com/reviewed/fleet.git", "git@github.com:reviewed/fleet.git", false},
	} {
		if got := guidedSourceMatches(tc.source, tc.remote, o); got != tc.want {
			t.Fatalf("unexpected source match for %q: %v", tc.source, got)
		}
	}
}
