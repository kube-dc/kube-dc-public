package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
	"github.com/shalb/kube-dc/cli/internal/platform"
)

// The native path must print exactly what the backend route printed:
// summariseKMSKey here mirrors ui/backend/controllers/kmsModule.js.
func TestNativeKMSKeySummaryMatchesTheBackendShape(t *testing.T) {
	var k k8sapi.Object
	raw := `{"apiVersion":"security.kube-dc.com/v1alpha1","kind":"KMSKey",
	  "metadata":{"name":"app","namespace":"acme-web","creationTimestamp":"2026-10-24T10:00:00Z"},
	  "spec":{"purpose":"backup","algorithm":"chacha20-poly1305","deletionPolicy":"retain","rotation":{"enabled":true,"interval":"90d"},"exportable":false},
	  "status":{"keyId":"acme-web-app","currentVersion":3,"minDecryptionVersion":2,"lastRotatedTime":"2026-10-20T00:00:00Z",
	    "conditions":[{"type":"Ready","status":"True"}]}}`
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		t.Fatal(err)
	}
	got := summariseKMSKey(&k)
	// What the backend answers for the same object (summariseKMSKey in kmsModule.js).
	var want backend.KMSKeySummary
	_ = json.Unmarshal([]byte(`{"name":"app","namespace":"acme-web","creationTimestamp":"2026-10-24T10:00:00Z",
	  "purpose":"backup","algorithm":"chacha20-poly1305","deletionPolicy":"retain","rotation":{"enabled":true,"interval":"90d"},"exportable":false,
	  "status":{"keyId":"acme-web-app","currentVersion":3,"minDecryptionVersion":2,"lastRotatedTime":"2026-10-20T00:00:00Z",
	    "conditions":[{"type":"Ready","status":"True"}]}}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native %+v\nbackend %+v", got, want)
	}
	// A new key without spec defaults or status reads like the backend's defaults.
	bare := summariseKMSKey(&k8sapi.Object{Metadata: k8sapi.ObjectMeta{Name: "b", Namespace: "acme-web"}})
	if bare.Purpose != "application" || bare.Algorithm != "aes256-gcm96" || bare.DeletionPolicy != "retain" || bare.Rotation.Enabled || bare.Status.Conditions == nil {
		t.Fatalf("defaults %+v", bare)
	}
}

func TestNativeKMSKeySpecKeepsTheBackendChecks(t *testing.T) {
	spec, err := kmsKeySpec(backend.CreateKMSKeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"purpose": "application", "algorithm": "aes256-gcm96", "deletionPolicy": "retain", "rotation": map[string]any{"enabled": false}, "exportable": false}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("defaults %v", spec)
	}
	if _, err := kmsKeySpec(backend.CreateKMSKeyOptions{Rotation: backend.KMSKeyRotation{Enabled: true}}); err == nil {
		t.Fatal("rotation without an interval accepted")
	}
}

// A cluster that serves the KMSKey resource, on an installation whose audit
// log records Kubernetes calls on security.kube-dc.com, gets the Kubernetes
// API. Otherwise the backend route, which writes the audit events itself,
// stays: an older installation, or one without that audit pipeline.
func TestKMSKeysUseTheNewestAuditedAPITheClusterServes(t *testing.T) {
	audited := &platform.Contract{}
	audited.Audit.KubernetesGroups = []string{"security.kube-dc.com"}
	for _, c := range []struct {
		name     string
		served   bool
		contract *platform.Contract
		want     string
	}{
		{"served and audited", true, audited, "main.nativeKMSKeys"},
		{"served, not audited", true, &platform.Contract{}, "main.legacyKMSKeys"},
		{"no contract", true, nil, "main.legacyKMSKeys"},
		{"older installation", false, audited, "main.legacyKMSKeys"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir()) // a fresh capability cache
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/apis/security.kube-dc.com/v1alpha1" || !c.served {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(`{"resources":[{"name":"kmskeys"},{"name":"kmskeys/status"}]}`))
			}))
			defer api.Close()
			scope := &secretsScope{Domain: "example.test", APIServer: api.URL, Namespace: "acme-web", AccessToken: "t", K8sInsecure: true, Contract: c.contract}
			keys, err := scope.kmsKeys(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := reflect.TypeOf(keys).String(); got != c.want {
				t.Fatalf("%s, want %s", got, c.want)
			}
		})
	}
}
