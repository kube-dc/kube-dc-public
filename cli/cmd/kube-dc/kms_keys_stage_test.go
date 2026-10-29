package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
	"github.com/shalb/kube-dc/cli/internal/platform"
)

// Live acceptance against an installation, skipped unless asked for:
//
//	KUBE_DC_STAGE_TOKEN=<project token> KUBE_DC_STAGE_DOMAIN=stage.kube-dc.com \
//	KUBE_DC_STAGE_NAMESPACE=shalb-dev go test ./cmd/kube-dc -run StageKMSKeys
//
// The installation's contract must declare security.kube-dc.com audited. The
// native path and the backend route must answer the same lists; a disposable
// KMSKey is created, described and deleted natively.
func TestStageKMSKeysNativeMatchesBackend(t *testing.T) {
	token, domain, ns := os.Getenv("KUBE_DC_STAGE_TOKEN"), os.Getenv("KUBE_DC_STAGE_DOMAIN"), os.Getenv("KUBE_DC_STAGE_NAMESPACE")
	if token == "" || domain == "" || ns == "" {
		t.Skip("set KUBE_DC_STAGE_TOKEN, KUBE_DC_STAGE_DOMAIN and KUBE_DC_STAGE_NAMESPACE")
	}
	t.Setenv("HOME", t.TempDir())
	// The API server's CA, as kube-dc login gets it.
	resp, err := http.Get("https://backend." + domain + "/.well-known/kube-dc-config")
	if err != nil {
		t.Fatal(err)
	}
	var disc struct {
		APIServer string `json:"apiServer"`
		CA        string `json:"certificateAuthorityData"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&disc)
	resp.Body.Close()
	ca, _ := base64.StdEncoding.DecodeString(disc.CA)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// The installation's own contract decides, as for kube-dc itself; it must
	// declare the group audited for the native path to be tested.
	contract, err := platform.FetchContract(ctx, http.DefaultClient, platform.DefaultCache(), "https://backend."+domain+"/.well-known/kube-dc-config")
	if err != nil {
		t.Fatal(err)
	}
	if !contract.Audits("security.kube-dc.com") {
		t.Skipf("%s does not declare security.kube-dc.com audited", domain)
	}
	scope := &secretsScope{Domain: domain, APIServer: disc.APIServer, Namespace: ns, AccessToken: token, K8sCACert: string(ca), Contract: contract}
	keys, err := scope.kmsKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys.(nativeKMSKeys); !ok {
		t.Fatalf("the installation serves kmskeys but %T was chosen", keys)
	}
	bc, err := scope.backend()
	if err != nil {
		t.Fatal(err)
	}
	legacy := legacyKMSKeys{bc}

	name := fmt.Sprintf("b-accept-%d", time.Now().Unix()%1000000)
	// schedule: the controller removes the Transit key after the countdown, so the probe leaves nothing behind.
	created, err := keys.Create(ctx, ns, name, backend.CreateKMSKeyOptions{DeletionPolicy: "schedule", Rotation: backend.KMSKeyRotation{Enabled: true, Interval: "90d"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = keys.Delete(context.Background(), ns, name) })
	if created.Name != name || created.Purpose != "application" || !created.Rotation.Enabled || created.Rotation.Interval != "90d" {
		t.Fatalf("created %+v", created)
	}
	time.Sleep(5 * time.Second) // let the controller write status once
	nl, err := keys.List(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	ll, err := legacy.List(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	byName := func(l *backend.KMSKeyList) map[string]backend.KMSKeySummary {
		m := map[string]backend.KMSKeySummary{}
		for _, k := range l.Items {
			m[k.Name] = k
		}
		return m
	}
	n, l := byName(nl), byName(ll)
	if _, ok := n[name]; !ok {
		t.Fatalf("native list misses %s", name)
	}
	for k, v := range l {
		if nv, ok := n[k]; !ok || !reflect.DeepEqual(nv, v) {
			t.Errorf("%s differs:\nnative  %+v\nbackend %+v", k, nv, v)
		}
	}
	got, err := keys.Get(ctx, ns, name)
	if err != nil || got.Name != name {
		t.Fatalf("describe %+v %v", got, err)
	}
	if _, err := keys.Delete(ctx, ns, name); err != nil {
		t.Fatal(err)
	}
	t.Logf("native list/create/describe/delete on %s: %d keys, same as the backend route", ns, len(n))

	// Certificates and credential policies take the same path; their lists
	// must equal the backend's.
	certs, err := scope.certificates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := certs.(nativeCertificates); !ok {
		t.Fatalf("certificates: %T was chosen", certs)
	}
	nc, err := certs.List(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	lc, err := legacyCertificates{bc}.List(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(nc, lc) {
		t.Errorf("certificate lists differ:\nnative  %+v\nbackend %+v", nc, lc)
	}
	creds, err := scope.credentialPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := creds.(nativeCredentialPolicies); !ok {
		t.Fatalf("credential policies: %T was chosen", creds)
	}
	np, err := creds.List(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	lp, err := legacyCredentialPolicies{bc}.List(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(np, lp) {
		t.Errorf("credential policy lists differ:\nnative  %+v\nbackend %+v", np, lp)
	}
	t.Logf("certificates (%d) and credential policies (%d) native, same as the backend route", len(nc.Items), len(np.Items))

	// Typed KMS operations, where served: each path decrypts what the other
	// encrypted. The key is retained until provisioned, then scheduled.
	transit, err := scope.kmsTransit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	typed, ok := transit.(typedKMSTransit)
	if !ok {
		t.Logf("%s does not serve the typed KMS operations; skipped", domain)
		return
	}
	k8s, err := scope.k8s()
	if err != nil {
		t.Fatal(err)
	}
	opsKey := fmt.Sprintf("b-transit-%d", time.Now().Unix()%1000000)
	if _, err := k8s.Create(ctx, k8sapi.KMSKeys, ns, opsKey, map[string]any{"purpose": "application", "deletionPolicy": "retain"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = k8s.Patch(context.Background(), k8sapi.KMSKeys, ns, opsKey, map[string]any{"spec": map[string]any{"deletionPolicy": "schedule"}})
		_ = k8s.Delete(context.Background(), k8sapi.KMSKeys, ns, opsKey)
	})
	for i := 0; i < 40; i++ {
		obj, err := k8s.Get(ctx, k8sapi.KMSKeys, ns, opsKey)
		if err == nil {
			if id, _ := obj.Status["keyId"].(string); id != "" {
				break
			}
		}
		time.Sleep(3 * time.Second)
	}
	plaintext := base64.StdEncoding.EncodeToString([]byte("cross-path " + opsKey))
	ct, err := typed.Encrypt(ctx, ns, opsKey, plaintext, "")
	if err != nil {
		t.Fatalf("typed encrypt: %v", err)
	}
	viaBackend, err := legacyKMSTransit{bc}.Decrypt(ctx, ns, opsKey, ct, "")
	if err != nil || viaBackend != plaintext {
		t.Fatalf("backend decrypt of a typed ciphertext: %q %v", viaBackend, err)
	}
	ct2, err := legacyKMSTransit{bc}.Encrypt(ctx, ns, opsKey, plaintext, "")
	if err != nil {
		t.Fatal(err)
	}
	viaTyped, err := typed.Decrypt(ctx, ns, opsKey, ct2, "")
	if err != nil || viaTyped != plaintext {
		t.Fatalf("typed decrypt of a backend ciphertext: %q %v", viaTyped, err)
	}
	t.Logf("typed and backend KMS operations interoperate on %s/%s", ns, opsKey)
}
