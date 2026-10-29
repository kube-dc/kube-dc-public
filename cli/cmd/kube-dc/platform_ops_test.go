package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/backend"
)

func TestPlatformOperationsAreUsedWhereServed(t *testing.T) {
	var got []string
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/platform.kube-dc.com/v1alpha1" {
			_, _ = w.Write([]byte(`{"resources":[{"name":"kmskeyrotations"},{"name":"databasecredentials"},{"name":"databasecredentialissues"},{"name":"databasecredentialrotations"},{"name":"managedsecretversiondestructions"}]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/apis/platform.kube-dc.com/v1alpha1/namespaces/acme-web/")+" "+string(raw))
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"status":{"username":"app","password":"pw","ttl":60}}`))
		case strings.HasSuffix(r.URL.Path, "databasecredentialissues"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"status":{"username":"v","password":"p2","leaseId":"l","leaseDuration":3600}}`))
		default:
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"status":{}}`))
		}
	}))
	defer api.Close()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	s := &secretsScope{Domain: "example.test", APIServer: api.URL, Namespace: "acme-web", AccessToken: "t", K8sInsecure: true}

	if r, err := s.rotateKMSKey(ctx, "app"); err != nil || !r.Rotated {
		t.Fatalf("kms rotate %+v %v", r, err)
	}
	if c, err := s.getDBCredentials(ctx, "pol"); err != nil || c.Password != "pw" || c.TTL != 60 {
		t.Fatalf("db read %+v %v", c, err)
	}
	if l, err := s.issueDBCredentials(ctx, "dyn"); err != nil || l.LeaseDuration != 3600 || l.Password != "p2" {
		t.Fatalf("db issue %+v %v", l, err)
	}
	if r, err := s.rotateDBCredentialPolicy(ctx, "pol", false); err != nil || !r.Rotated {
		t.Fatalf("db rotate %+v %v", r, err)
	}
	if r, err := s.destroySecretVersion(ctx, "db", 3); err != nil || r.DestroyedVersion != 3 {
		t.Fatalf("destroy %+v %v", r, err)
	}
	if len(got) != 5 {
		t.Fatalf("calls %v", got)
	}
	check := func(i int, prefix, field, want string) {
		t.Helper()
		parts := strings.SplitN(got[i], " ", 3)
		if parts[0]+" "+parts[1] != prefix {
			t.Errorf("call %d = %s, want %s", i, got[i], prefix)
		}
		if field == "" {
			return
		}
		var body map[string]any
		_ = json.Unmarshal([]byte(parts[2]), &body)
		spec, _ := body["spec"].(map[string]any)
		ref, _ := spec[field].(map[string]any)
		if ref["name"] != want {
			t.Errorf("call %d %s = %v, want %s", i, field, spec, want)
		}
	}
	check(0, "POST kmskeyrotations", "keyRef", "app")
	check(1, "GET databasecredentials/pol", "", "")
	check(2, "POST databasecredentialissues", "policyRef", "dyn")
	check(3, "POST databasecredentialrotations", "policyRef", "pol")
	check(4, "POST managedsecretversiondestructions", "secretRef", "db")
	if !strings.Contains(got[4], `"versions":[3]`) {
		t.Errorf("destroy body %s", got[4])
	}
}

func TestImportSecretNative(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		var got []string
		api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			got = append(got, r.Method+" "+r.URL.Path+" "+string(raw))
			switch {
			case r.URL.Path == "/api/v1/namespaces/acme-web/secrets/legacy":
				_, _ = w.Write([]byte(`{"data":{"password":"cHc="}}`))
			case r.Method == http.MethodPut && failWrite:
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"kind":"Status","message":"boom"}`))
			case r.Method == http.MethodPut:
				_, _ = w.Write([]byte(`{"status":{"version":1}}`))
			case r.Method == http.MethodPost:
				w.WriteHeader(201)
				_, _ = w.Write(raw)
			default:
				_, _ = w.Write([]byte(`{}`))
			}
		}))
		t.Setenv("HOME", t.TempDir())
		s := &secretsScope{Domain: "example.test", APIServer: api.URL, Namespace: "acme-web", AccessToken: "t", K8sInsecure: true}
		version, err := s.importSecret(context.Background(), "db", "acme-web", "legacy", "opaque", "Imported")
		api.Close()
		if failWrite {
			if err == nil || len(got) != 4 || !strings.HasPrefix(got[3], "DELETE /apis/security.kube-dc.com/v1alpha1/namespaces/acme-web/managedsecrets/db") {
				t.Fatalf("failed write: err %v, calls %v", err, got)
			}
			continue
		}
		if err != nil || version != 1 || len(got) != 4 {
			t.Fatalf("import: v%d %v, calls %v", version, err, got)
		}
		if !strings.Contains(got[1], `"enabled":false`) || strings.Contains(got[1], "targetSecretName") {
			t.Errorf("created with sync on: %s", got[1])
		}
		if !strings.HasPrefix(got[2], "PUT /apis/platform.kube-dc.com/v1alpha1/namespaces/acme-web/managedsecretvalues/db") || !strings.Contains(got[2], `"password":"pw"`) {
			t.Errorf("value write: %s", got[2])
		}
		if !strings.HasPrefix(got[3], "PATCH ") || !strings.Contains(got[3], `"targetSecretName":"legacy"`) || !strings.Contains(got[3], `"enabled":true`) {
			t.Errorf("sync patch: %s", got[3])
		}
	}
}

func TestSecretValueReadAndDestroyAreTyped(t *testing.T) {
	var got []string
	destroyStatus := 201
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/platform.kube-dc.com/v1alpha1" {
			_, _ = w.Write([]byte(`{"resources":[{"name":"managedsecretvalues"},{"name":"managedsecretversiondestructions"}]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(raw))
		switch {
		case strings.HasSuffix(r.URL.Path, "/managedsecrets/svc"):
			_, _ = w.Write([]byte(`{"metadata":{"name":"svc","namespace":"acme-web"},"spec":{"type":"opaque","publicationRef":{"name":"pg"}}}`))
		case strings.HasSuffix(r.URL.Path, "/managedsecrets/db") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"metadata":{"name":"db","namespace":"acme-web"},"spec":{"type":"password","sync":{"enabled":true,"targetSecretName":"db"}}}`))
		case strings.HasSuffix(r.URL.Path, "/managedsecretvalues/db"):
			_, _ = w.Write([]byte(`{"data":{"password":"pw"},"status":{"version":3,"createdTime":"t3","storage":{"currentVersion":3,"createdTime":"t1","maxVersions":10}}}`))
		case strings.HasSuffix(r.URL.Path, "managedsecretversiondestructions"):
			w.WriteHeader(destroyStatus)
			_, _ = w.Write([]byte(`{"kind":"Status","message":"spec.versions must list 1-64 versions"}`))
		case strings.HasPrefix(r.URL.Path, "/api/secrets/"):
			_, _ = w.Write([]byte(`{"name":"db","namespace":"acme-web","deleted":true,"destroyed":true,"value":{"data":{"k":"backend"}}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer api.Close()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	s := &secretsScope{Domain: "example.test", APIServer: api.URL, Namespace: "acme-web", AccessToken: "t", K8sInsecure: true}
	orig := newBackendClient
	defer func() { newBackendClient = orig }()
	newBackendClient = func(*secretsScope) (*backend.Client, error) {
		c, err := backend.New("example.test", "t", "", true)
		if c != nil {
			c.BaseURL = api.URL
		}
		return c, err
	}

	sum, err := s.getSecretWithValue(ctx, "db")
	if err != nil || sum.Value == nil || sum.Value.Data["password"] != "pw" || sum.Value.Metadata.Version != 3 || sum.OpenBao == nil || sum.OpenBao.CreatedTime != "t1" || sum.Type != "password" || !sum.Sync.Enabled {
		t.Fatalf("typed get --value: %+v %v", sum, err)
	}
	got = nil
	if sum, err := s.getSecretWithValue(ctx, "svc"); err != nil || sum.Value == nil || sum.Value.Data["k"] != "backend" {
		t.Fatalf("managed-service credentials use the backend: %+v %v (%v)", sum, err, got)
	}

	got = nil
	res, err := s.destroySecret(ctx, "db")
	if err != nil || !res.Destroyed || len(got) != 3 || !strings.Contains(got[1], `"allVersions":true`) || !strings.HasPrefix(got[2], "DELETE /apis/security.kube-dc.com/v1alpha1/namespaces/acme-web/managedsecrets/db") {
		t.Fatalf("typed destroy: %+v %v %v", res, err, got)
	}
	got = nil
	destroyStatus = 422
	if res, err := s.destroySecret(ctx, "db"); err != nil || !res.Destroyed || !strings.HasPrefix(got[len(got)-1], "DELETE /api/secrets/acme-web/db") {
		t.Fatalf("an older operation falls back to the backend: %+v %v %v", res, err, got)
	}
	got = nil
	if _, err := s.destroySecret(ctx, "svc"); err == nil || len(got) != 1 {
		t.Fatalf("managed-service credentials: %v %v", err, got)
	}
}

func TestListConsumersScansWorkloads(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/security.kube-dc.com/v1alpha1/namespaces/acme-web/managedsecrets/db":
			_, _ = w.Write([]byte(`{"metadata":{"name":"db"},"spec":{"sync":{"enabled":true,"targetSecretName":"db-synced"}}}`))
		case "/api/v1/namespaces/acme-web/pods":
			_, _ = w.Write([]byte(`{"items":[
				{"metadata":{"name":"web-abc-1","ownerReferences":[{"kind":"ReplicaSet","controller":true}]},"spec":{"containers":[{"name":"app","envFrom":[{"secretRef":{"name":"db-synced"}}]}]}},
				{"metadata":{"name":"debug","namespace":"acme-web"},"spec":{"imagePullSecrets":[{"name":"db-synced"}],"volumes":[{"name":"creds","secret":{"secretName":"db-synced"}}]}}]}`))
		case "/apis/apps/v1/namespaces/acme-web/deployments":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"web","namespace":"acme-web"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"PW","valueFrom":{"secretKeyRef":{"name":"db-synced"}}}]}],"volumes":[{"name":"p","projected":{"sources":[{"secret":{"name":"db-synced"}}]}}]}}}},
				{"metadata":{"name":"other"},"spec":{"template":{"spec":{"containers":[{"name":"x","envFrom":[{"secretRef":{"name":"db"}}]}]}}}}]}`))
		case "/apis/batch/v1/namespaces/acme-web/cronjobs":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"backup"},"spec":{"jobTemplate":{"spec":{"template":{"spec":{"initContainers":[{"name":"i","envFrom":[{"secretRef":{"name":"db-synced"}}]}]}}}}}}]}`))
		case "/apis/apps/v1/namespaces/acme-web/daemonsets":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"kind":"Status","message":"forbidden"}`))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	}))
	defer api.Close()
	t.Setenv("HOME", t.TempDir())
	s := &secretsScope{Domain: "example.test", APIServer: api.URL, Namespace: "acme-web", AccessToken: "t", K8sInsecure: true}
	list, err := s.listConsumers(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, it := range list.Items {
		rows = append(rows, it.Kind+"/"+it.Name+"="+strings.Join(it.References, ","))
	}
	want := []string{"CronJob/backup=envFrom:i", "Deployment/web=env:app/PW,projected:p", "Pod/debug=imagePullSecret,volume:creds"}
	if list.Secret != "db-synced" || strings.Join(rows, " ") != strings.Join(want, " ") {
		t.Fatalf("consumers %s: %v", list.Secret, rows)
	}
	if len(list.Errors) != 1 || list.Errors[0]["kind"] != "DaemonSet" || list.Errors[0]["status"] != 403 {
		t.Fatalf("errors %v", list.Errors)
	}
}
