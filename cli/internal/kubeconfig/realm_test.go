package kubeconfig

import (
	"reflect"
	"testing"
)

func TestAddKubeDCContextRepairsLegacyUserRealmArgs(t *testing.T) {
	mgr, path := newTestManager(t)
	const (
		server   = "https://kube-api.kube-dc.cloud:6443"
		userName = "kube-dc@kube-dc.cloud/acme"
	)

	writeConfig(t, path, &Config{
		APIVersion:     "v1",
		Kind:           "Config",
		CurrentContext: "kube-dc/kube-dc.cloud/acme/production",
		Clusters: []NamedCluster{{
			Name:    "kube-dc-kube-dc.cloud-acme",
			Cluster: Cluster{Server: server},
		}},
		Users: []NamedUser{{
			Name: userName,
			User: User{Exec: &ExecConfig{
				APIVersion:      "client.authentication.k8s.io/v1",
				Command:         "kube-dc",
				Args:            []string{"credential", "--server", server},
				InteractiveMode: "IfAvailable",
			}},
		}},
		Contexts: []NamedContext{
			{
				Name:    "kube-dc/kube-dc.cloud/acme/production",
				Context: Context{Cluster: "kube-dc-kube-dc.cloud-acme", User: userName, Namespace: "acme-production"},
			},
			{
				Name:    "kube-dc/kube-dc.cloud/acme/staging",
				Context: Context{Cluster: "kube-dc-kube-dc.cloud-acme", User: userName, Namespace: "acme-staging"},
			},
		},
	})

	if err := mgr.AddKubeDCContext(AddContextParams{
		Server:      server,
		ClusterName: "kube-dc-kube-dc.cloud-acme",
		UserName:    userName,
		ContextName: "kube-dc/kube-dc.cloud/acme/production",
		Namespace:   "acme-production",
		SetCurrent:  true,
		Realm:       "acme",
	}); err != nil {
		t.Fatalf("AddKubeDCContext: %v", err)
	}

	got, err := mgr.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	wantArgs := []string{"credential", "--server", server, "--realm", "acme"}
	for _, user := range got.Users {
		if user.Name != userName {
			continue
		}
		if user.User.Exec == nil {
			t.Fatal("updated tenant user has no exec configuration")
		}
		if !reflect.DeepEqual(user.User.Exec.Args, wantArgs) {
			t.Fatalf("exec args = %#v, want %#v", user.User.Exec.Args, wantArgs)
		}
		if len(got.Contexts) != 2 {
			t.Fatalf("context count = %d, want 2; re-login must preserve sibling Project contexts", len(got.Contexts))
		}
		return
	}
	t.Fatalf("updated user %q was not found", userName)
}
