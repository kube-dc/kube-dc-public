package accept

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
)

func runWith(t *testing.T, k *fakeK8s) Report {
	t.Helper()
	rep, err := Run(context.Background(), Options{K8s: k, Cluster: "dc1", Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestStorageClass(t *testing.T) {
	t.Run("no StorageClass at all fails", func(t *testing.T) {
		k := healthy()
		k.objects["storage.k8s.io/v1/storageclasses"] = nil
		c := find(t, runWith(t, k), "storage/default-class")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "NO StorageClass") {
			t.Fatalf("want FAIL about no StorageClass, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("classes but no default fails, because PVCs hang silently", func(t *testing.T) {
		k := healthy()
		k.objects["storage.k8s.io/v1/storageclasses"] = []map[string]any{
			obj("", "fast", nil), obj("", "slow", nil),
		}
		c := find(t, runWith(t, k), "storage/default-class")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "NONE is marked default") {
			t.Fatalf("want FAIL about no default, got %s: %s", c.Outcome, c.Detail)
		}
		if !strings.Contains(c.Fix, "is-default-class") {
			t.Errorf("the fix must name the annotation, got: %s", c.Fix)
		}
	})

	t.Run("two defaults fails, because which one wins is undefined", func(t *testing.T) {
		k := healthy()
		k.objects["storage.k8s.io/v1/storageclasses"] = []map[string]any{
			defaultStorageClass("a"), defaultStorageClass("b"),
		}
		c := find(t, runWith(t, k), "storage/default-class")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "2 StorageClasses are marked default") {
			t.Fatalf("want FAIL about two defaults, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("the beta annotation still counts", func(t *testing.T) {
		k := healthy()
		k.objects["storage.k8s.io/v1/storageclasses"] = []map[string]any{
			{"metadata": map[string]any{"name": "legacy", "annotations": map[string]any{
				"storageclass.beta.kubernetes.io/is-default-class": "true",
			}}},
		}
		if c := find(t, runWith(t, k), "storage/default-class"); c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestSopsDecryption(t *testing.T) {
	t.Run("undecrypted ciphertext fails and never prints the value", func(t *testing.T) {
		k := healthy()
		secret := "ENC[AES256_GCM,data:c3VwZXJzZWNyZXQ=,iv:xx,tag:yy,type:str]"
		k.stage("/v1/secrets", obj("flux-system", "cluster-secrets", map[string]any{
			"data": map[string]any{"KEYCLOAK_ADMIN_PASSWORD": b64(secret)},
		}))
		c := find(t, runWith(t, k), "secrets/sops-decryption")
		if c.Outcome != Fail {
			t.Fatalf("want FAIL, got %s: %s", c.Outcome, c.Detail)
		}
		if !strings.Contains(c.Detail, "cluster-secrets:KEYCLOAK_ADMIN_PASSWORD") {
			t.Errorf("must name the secret and key, got: %s", c.Detail)
		}
		// The whole point: a check that leaks what it found is worse than the bug.
		for _, leak := range []string{secret, "c3VwZXJzZWNyZXQ=", "supersecret"} {
			if strings.Contains(c.Detail+c.Fix, leak) {
				t.Fatalf("the check leaked secret material: %s", c.Detail)
			}
		}
	})

	// The gap that made the original scoping useless: the Kustomization writes
	// the Secret into the COMPONENT's namespace, not flux-system.
	t.Run("ciphertext outside flux-system is still caught", func(t *testing.T) {
		k := healthy()
		k.stage("/v1/secrets", obj("kube-dc", "keycloak-admin", map[string]any{
			"data": map[string]any{"password": b64("ENC[AES256_GCM,data:zz,iv:x,tag:y,type:str]")},
		}))
		c := find(t, runWith(t, k), "secrets/sops-decryption")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "kube-dc/keycloak-admin:password") {
			t.Fatalf("want FAIL naming the namespaced secret, got %s: %s", c.Outcome, c.Detail)
		}
	})

	// Skipping large values would have hidden an encrypted certificate or
	// keyring; only the DECODE is bounded, never the value.
	t.Run("a large encrypted value is still caught", func(t *testing.T) {
		k := healthy()
		big := "ENC[AES256_GCM,data:" + strings.Repeat("A", 200000) + ",type:str]"
		k.stage("/v1/secrets", obj("kube-dc", "bundle", map[string]any{
			"data": map[string]any{"tls.crt": b64(big)},
		}))
		c := find(t, runWith(t, k), "secrets/sops-decryption")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "kube-dc/bundle:tls.crt") {
			t.Fatalf("want FAIL, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("helm release blobs are skipped, not decoded", func(t *testing.T) {
		k := healthy()
		k.stage("/v1/secrets", map[string]any{
			"metadata": map[string]any{"namespace": "kube-dc", "name": "sh.helm.release.v1.kube-dc.v9"},
			"type":     "helm.sh/release.v1",
			"data":     map[string]any{"release": b64("ENC[AES256_GCM,data:notreally]")},
		})
		if c := find(t, runWith(t, k), "secrets/sops-decryption"); c.Outcome != Pass {
			t.Fatalf("a helm release blob must not be scanned, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("an unreadable Secret list blocks rather than passing", func(t *testing.T) {
		k := healthy()
		k.objects["/v1/secrets"] = nil
		rep := runWith(t, k)
		c := find(t, rep, "secrets/sops-decryption")
		if c.Outcome != Skipped || !c.Required {
			t.Fatalf("want a REQUIRED skip, got %s required=%v", c.Outcome, c.Required)
		}
		if rep.State == StateUsable {
			t.Error(`"I could not tell" must not read as usable`)
		}
	})

	t.Run("plain secrets pass", func(t *testing.T) {
		k := healthy()
		k.stage("/v1/secrets", obj("flux-system", "cluster-secrets", map[string]any{
			"data": map[string]any{"TOKEN": b64("a-real-decrypted-value")},
		}))
		if c := find(t, runWith(t, k), "secrets/sops-decryption"); c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("non-base64 data is ignored rather than crashing", func(t *testing.T) {
		k := healthy()
		k.stage("/v1/secrets", obj("flux-system", "s", map[string]any{
			"data": map[string]any{"k": "!!!not base64!!!"},
		}))
		if c := find(t, runWith(t, k), "secrets/sops-decryption"); c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})
}

func TestClusterConfigPins(t *testing.T) {
	pins := func(data map[string]any) *fakeK8s {
		k := healthy()
		k.stage("/v1/configmaps", obj("flux-system", "cluster-config", map[string]any{"data": data}))
		return k
	}

	t.Run("a commented chart version fails", func(t *testing.T) {
		c := find(t, runWith(t, pins(map[string]any{
			"KUBE_DC_VERSION": "v0.5.87   # 2026-08-17 node-health. ROLLBACK: v0.5.86.",
		})), "config/version-pins")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "KUBE_DC_VERSION") {
			t.Fatalf("want FAIL naming the key, got %s: %s", c.Outcome, c.Detail)
		}
		if !strings.Contains(c.Fix, "improper constraint") {
			t.Errorf("the fix must name the Helm error the operator will see, got: %s", c.Fix)
		}
	})

	t.Run("a commented image tag is reported but does not block", func(t *testing.T) {
		rep := runWith(t, pins(map[string]any{"KUBE_DC_BACKEND_TAG": "v0.5.61 # dev build"}))
		c := find(t, rep, "config/version-pins")
		if c.Outcome != Fail {
			t.Fatalf("want it reported, got %s", c.Outcome)
		}
		if c.Required {
			t.Error("an image-tag comment tolerates substitution; it must not block usable")
		}
		if rep.State != StateUsable {
			t.Errorf("state = %s, want usable — a cosmetic pin comment is not a blocker", rep.State)
		}
	})

	// Helm parses a chart version as a semver constraint, which never contains
	// a '#' — so it does not need whitespace in front of it to be fatal.
	t.Run("a chart version with an attached comment fails too", func(t *testing.T) {
		c := find(t, runWith(t, pins(map[string]any{
			"KUBE_DC_VERSION": "1.2.3#temporary",
		})), "config/version-pins")
		if c.Outcome != Fail || !c.Required {
			t.Fatalf("want a required FAIL, got %s required=%v: %s", c.Outcome, c.Required, c.Detail)
		}
	})

	t.Run("a bare # inside a value is not a comment", func(t *testing.T) {
		c := find(t, runWith(t, pins(map[string]any{
			"S3_SECRET": "ab#cd", "KUBE_DC_VERSION": "v0.5.87",
		})), "config/version-pins")
		if c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("no cluster-config skips", func(t *testing.T) {
		if c := find(t, runWith(t, healthy()), "config/version-pins"); c.Outcome != Skipped {
			t.Fatalf("want SKIP, got %s", c.Outcome)
		}
	})
}

func TestLoadBalancerPools(t *testing.T) {
	pool := func(name string, addrs ...string) map[string]any {
		as := make([]any, 0, len(addrs))
		for _, a := range addrs {
			as = append(as, a)
		}
		return obj("metallb-system", name, map[string]any{"spec": map[string]any{"addresses": as}})
	}

	t.Run("overlapping pools fail, because MetalLB rejects the whole set", func(t *testing.T) {
		k := healthy()
		k.stage("metallb.io/v1beta1/ipaddresspools",
			pool("auto", "10.8.0.0/24"),
			pool("customer-vlan-apps", "10.8.0.6-10.8.0.9"))
		c := find(t, runWith(t, k), "network/loadbalancer-pools")
		if c.Outcome != Fail {
			t.Fatalf("want FAIL, got %s: %s", c.Outcome, c.Detail)
		}
		if !strings.Contains(c.Fix, "NO LoadBalancer Service gets an address") {
			t.Errorf("the fix must state the blast radius, got: %s", c.Fix)
		}
	})

	t.Run("disjoint pools pass", func(t *testing.T) {
		k := healthy()
		k.stage("metallb.io/v1beta1/ipaddresspools",
			pool("a", "10.8.0.0/28"),
			pool("b", "10.8.0.16-10.8.0.31"))
		if c := find(t, runWith(t, k), "network/loadbalancer-pools"); c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})

	// MetalLB validates addresses as one set, so a pool that overlaps ITSELF
	// disables allocation exactly as two overlapping pools do.
	t.Run("a pool that overlaps itself is caught", func(t *testing.T) {
		k := healthy()
		k.stage("metallb.io/v1beta1/ipaddresspools", pool("a", "10.8.0.0/24", "10.8.0.5/32"))
		c := find(t, runWith(t, k), "network/loadbalancer-pools")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "overlaps") {
			t.Fatalf("want FAIL, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("a malformed entry blocks, because MetalLB will reject it too", func(t *testing.T) {
		k := healthy()
		k.stage("metallb.io/v1beta1/ipaddresspools", pool("a", "10.8.0.5"))
		c := find(t, runWith(t, k), "network/loadbalancer-pools")
		if c.Outcome != Fail || !c.Required || !strings.Contains(c.Detail, "NOT checked for overlap") {
			t.Fatalf("want a required FAIL, got %s required=%v: %s", c.Outcome, c.Required, c.Detail)
		}
	})

	t.Run("a pool with no addresses at all is caught", func(t *testing.T) {
		k := healthy()
		k.stage("metallb.io/v1beta1/ipaddresspools", pool("empty"))
		c := find(t, runWith(t, k), "network/loadbalancer-pools")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "declare no addresses") {
			t.Fatalf("want FAIL, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("no MetalLB skips", func(t *testing.T) {
		if c := find(t, runWith(t, healthy()), "network/loadbalancer-pools"); c.Outcome != Skipped {
			t.Fatalf("want SKIP, got %s", c.Outcome)
		}
	})
}

func TestParseIPRange(t *testing.T) {
	cases := []struct{ in, lo, hi string }{
		{"10.8.0.0/24", "10.8.0.0", "10.8.0.255"},
		{"10.8.0.0/28", "10.8.0.0", "10.8.0.15"},
		{"10.8.0.6-10.8.0.9", "10.8.0.6", "10.8.0.9"},
		{"10.8.0.7/32", "10.8.0.7", "10.8.0.7"},
	}
	for _, c := range cases {
		r, ok := parseIPRange(c.in)
		if !ok {
			t.Fatalf("parse %q failed", c.in)
		}
		if r.lo.String() != c.lo || r.hi.String() != c.hi {
			t.Errorf("parse %q = %s..%s, want %s..%s", c.in, r.lo, r.hi, c.lo, c.hi)
		}
	}
	for _, bad := range []string{
		"not-an-ip",
		"10.8.0.7",                // MetalLB wants a CIDR, not a bare address
		"10.8.0.9-10.8.0.1",       // reversed
		"192.0.2.1-2001:db8::1",   // straddles address families
		"2001:db8::5-2001:db8::1", // reversed, v6
	} {
		if _, ok := parseIPRange(bad); ok {
			t.Errorf("%q must not parse as a valid pool range", bad)
		}
	}
}

func TestAdmissionWebhooks(t *testing.T) {
	// webhook builds one configuration with a single service-backed webhook.
	webhook := func(cfg, ns, svc, policy string, port int64, extra map[string]any) map[string]any {
		sm := map[string]any{"namespace": ns, "name": svc}
		if port != 0 {
			sm["port"] = port
		}
		h := map[string]any{"clientConfig": map[string]any{"service": sm}}
		if policy != "" {
			h["failurePolicy"] = policy
		}
		for k, v := range extra {
			h[k] = v
		}
		return obj("", cfg, map[string]any{"webhooks": []any{h}})
	}
	// slice builds an EndpointSlice exposing the named ports.
	slice := func(ns, svc string, ready bool, portNames ...string) map[string]any {
		ep := map[string]any{"addresses": []any{"10.0.0.1"}, "conditions": map[string]any{"ready": ready}}
		ps := []any{}
		for _, pn := range portNames {
			ps = append(ps, map[string]any{"name": pn, "port": int64(8443)})
		}
		o := obj(ns, svc+"-abcde", map[string]any{"endpoints": []any{ep}, "ports": ps})
		o["metadata"].(map[string]any)["labels"] = map[string]any{"kubernetes.io/service-name": svc}
		return o
	}
	// service declares port->name pairs, matching how a Service joins to a slice.
	service := func(ns, name string, ports map[int64]string) map[string]any {
		ps := []any{}
		for p, n := range ports {
			ps = append(ps, map[string]any{"port": p, "name": n})
		}
		return obj(ns, name, map[string]any{"spec": map[string]any{"ports": ps}})
	}

	t.Run("an unscoped Fail webhook with no backend blocks every write", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("kube-dc-vap", "kube-dc", "webhook-svc", "Fail", 0, nil))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("kube-dc", "webhook-svc", false, ""))
		k.stage("/v1/services", service("kube-dc", "webhook-svc", map[int64]string{443: ""}))
		c := find(t, runWith(t, k), "admission/webhooks")
		if c.Outcome != Fail || !c.Required {
			t.Fatalf("want a required FAIL, got %s required=%v: %s", c.Outcome, c.Required, c.Detail)
		}
		if !strings.Contains(c.Detail, "kube-dc-vap→kube-dc/webhook-svc") {
			t.Errorf("must name the webhook and its service, got: %s", c.Detail)
		}
	})

	t.Run("an unset failurePolicy is treated as Fail", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "svc", "", 0, nil))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); !c.Required || c.Outcome != Fail {
			t.Fatalf("the API default is Fail, got required=%v %s", c.Required, c.Outcome)
		}
	})

	// The false positive that would make this check untrustworthy: a healthy
	// cluster whose EndpointSlices are the only source of truth.
	t.Run("a webhook backed only by EndpointSlices passes", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "svc", "Fail", 0, nil))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", true, ""))
		k.stage("/v1/services", service("ns", "svc", map[int64]string{443: ""}))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("an endpoint with no conditions counts as ready", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "svc", "Fail", 0, nil))
		es := obj("ns", "svc-x", map[string]any{
			"endpoints": []any{map[string]any{"addresses": []any{"10.0.0.1"}}},
			"ports":     []any{map[string]any{"name": "", "port": int64(8443)}},
		})
		es["metadata"].(map[string]any)["labels"] = map[string]any{"kubernetes.io/service-name": "svc"}
		k.stage("discovery.k8s.io/v1/endpointslices", es)
		k.stage("/v1/services", service("ns", "svc", map[int64]string{443: ""}))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Outcome != Pass {
			t.Fatalf("absent conditions.ready means ready, got %s: %s", c.Outcome, c.Detail)
		}
	})

	// Endpoints is deprecated and can be present-but-stale when the
	// EndpointSlice controller has failed. Trusting it would report an
	// unreachable webhook as healthy — the exact failure this check exists for.
	t.Run("stale legacy Endpoints never substitute for missing slices", func(t *testing.T) {
		k := healthy()
		k.objects["discovery.k8s.io/v1/endpointslices"] = nil
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "svc", "Fail", 0, nil))
		k.stage("/v1/endpoints", obj("ns", "svc", map[string]any{
			"subsets": []any{map[string]any{"addresses": []any{map[string]any{"ip": "10.0.0.1"}}}},
		}))
		rep := runWith(t, k)
		c := find(t, rep, "admission/webhooks")
		if c.Outcome != Skipped || !c.Required {
			t.Fatalf("want a REQUIRED skip, got %s required=%v: %s", c.Outcome, c.Required, c.Detail)
		}
		if rep.State == StateUsable {
			t.Error("unverifiable webhook backends must not read as usable")
		}
	})

	// A multi-port Service can be ready on one port and dead on the one the
	// webhook actually calls.
	t.Run("a port with no ready endpoint is caught even when the Service is up", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "svc", "Fail", 443, nil))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", true, "metrics"))
		k.stage("/v1/services", service("ns", "svc", map[int64]string{443: "https", 9090: "metrics"}))
		c := find(t, runWith(t, k), "admission/webhooks")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "port 443 has no ready endpoint") {
			t.Fatalf("want FAIL naming the dead port, got %s: %s", c.Outcome, c.Detail)
		}
	})

	// A namespaceSelector cannot constrain a rule that is explicitly Cluster
	// scoped, so such a webhook still blocks and must stay required.
	t.Run("a Cluster-scoped rule is unscoped despite a namespaceSelector", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("nodes", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{""}, "scope": "Cluster", "resources": []any{"nodes"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); !c.Required || c.Outcome != Fail {
			t.Fatalf("a namespaceSelector does not constrain Cluster scope, got required=%v %s", c.Required, c.Outcome)
		}
	})

	t.Run("a scoped Fail webhook is reported but does not block", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("scoped", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"only": "here"}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		rep := runWith(t, k)
		c := find(t, rep, "admission/webhooks")
		if c.Outcome != Fail || c.Required {
			t.Fatalf("a selector this cannot evaluate must not block, got %s required=%v", c.Outcome, c.Required)
		}
		if rep.State != StateUsable {
			t.Errorf("state = %s, want usable", rep.State)
		}
	})

	t.Run("a matchCondition also counts as scoped", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("mc", "ns", "svc", "Fail", 0, map[string]any{
				"matchConditions": []any{map[string]any{"name": "x", "expression": "false"}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Required {
			t.Fatalf("a matchCondition must not block, got required=%v", c.Required)
		}
	})

	t.Run("an empty namespaceSelector still counts as unscoped", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "svc", "Fail", 0, map[string]any{"namespaceSelector": map[string]any{}}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); !c.Required {
			t.Fatal("an empty selector matches everything and must still block")
		}
	})

	t.Run("a webhook pointed at a port the Service does not declare is caught", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "svc", "Fail", 443, nil))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", true, ""))
		k.stage("/v1/services", service("ns", "svc", map[int64]string{9443: ""}))
		c := find(t, runWith(t, k), "admission/webhooks")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "no port 443") {
			t.Fatalf("want FAIL naming the port, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("an Ignore webhook with no backend is reported but does not block", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/mutatingwebhookconfigurations",
			webhook("m", "ns", "svc", "Ignore", 0, nil))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		rep := runWith(t, k)
		c := find(t, rep, "admission/webhooks")
		if c.Outcome != Fail || c.Required {
			t.Fatalf("want a non-required report, got %s required=%v", c.Outcome, c.Required)
		}
		if rep.State != StateUsable {
			t.Errorf("state = %s, want usable — an Ignore webhook does not block writes", rep.State)
		}
	})

	// A scope:"*" rule naming a cluster-scoped resource is NOT constrained by a
	// namespaceSelector, so it must still block.
	t.Run("a wildcard-scope rule on Nodes ignores the namespaceSelector", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("nodes", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{""}, "scope": "*", "resources": []any{"nodes"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); !c.Required {
			t.Fatal("a namespaceSelector cannot constrain a rule matching Nodes")
		}
	})

	t.Run("a CSR webhook is cluster-scoped despite the selector", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("csr", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{"certificates.k8s.io"}, "scope": "*",
					"resources": []any{"certificatesigningrequests"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); !c.Required {
			t.Fatal("CSRs are cluster-scoped, so the selector cannot constrain this webhook")
		}
	})

	t.Run("a subresource rule resolves to its parent resource", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("nodestatus", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{""}, "scope": "*", "resources": []any{"nodes/status"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); !c.Required {
			t.Fatal("nodes/status is still nodes, which is cluster-scoped")
		}
	})

	// A namespaceSelector matches the labels of the Namespace object itself for
	// requests on namespaces, so it genuinely does constrain such a webhook.
	t.Run("a selector does constrain a webhook on namespaces", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("ns-hook", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{""}, "scope": "*", "resources": []any{"namespaces"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Required {
			t.Fatal("namespaceSelector matches the Namespace object itself, so this IS scoped")
		}
	})

	// The namespaces exemption must survive an explicit scope: "Cluster".
	t.Run("a selector constrains a namespaces rule even at Cluster scope", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("ns-cluster", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{""}, "scope": "Cluster", "resources": []any{"namespaces"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Required {
			t.Fatal("namespaceSelector matches the Namespace object itself whatever the scope says")
		}
	})

	t.Run("a Cluster-scoped rule naming an unlisted CRD still blocks", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("crd", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{"example.com"}, "scope": "Cluster", "resources": []any{"widgets"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); !c.Required {
			t.Fatal("an explicit Cluster scope is authoritative even for an unlisted kind")
		}
	})

	// resources ["*"] says nothing on its own — it depends on the group.
	t.Run("a wildcard resource in an all-namespaced group stays scoped", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("apps", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{"apps"}, "scope": "*", "resources": []any{"*"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Required {
			t.Fatal("every apps/* kind is namespaced, so the selector constrains it")
		}
	})

	t.Run("a wildcard resource in a group with cluster-scoped kinds is unscoped", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("storage", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{"storage.k8s.io"}, "scope": "*", "resources": []any{"*"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); !c.Required {
			t.Fatal("storage.k8s.io contains storageclasses, which a selector cannot constrain")
		}
	})

	// A namespaced CRD is free to call itself "nodes.example.com"; matching on
	// the bare plural would treat its selector as inert.
	t.Run("a CRD colliding with a built-in plural stays scoped", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("crd-nodes", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{"example.com"}, "scope": "*", "resources": []any{"nodes"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Required {
			t.Fatal("example.com/nodes is a namespaced CRD, not core nodes")
		}
	})

	t.Run("a namespaced rule with a selector stays scoped", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("pods", "ns", "svc", "Fail", 0, map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"x": "y"}},
				"rules": []any{map[string]any{
					"apiGroups": []any{""}, "scope": "*", "resources": []any{"pods"},
				}},
			}))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", false, ""))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Required {
			t.Fatal("pods are namespaced, so the selector does constrain this webhook")
		}
	})

	t.Run("a ready slice cannot vouch for a Service that does not exist", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "gone", "Fail", 0, nil))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "gone", true, ""))
		c := find(t, runWith(t, k), "admission/webhooks")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "Service does not exist") {
			t.Fatalf("want FAIL, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("a UDP port cannot serve a webhook however ready it looks", func(t *testing.T) {
		k := healthy()
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			webhook("w", "ns", "svc", "Fail", 443, nil))
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", true, ""))
		k.stage("/v1/services", obj("ns", "svc", map[string]any{"spec": map[string]any{
			"ports": []any{map[string]any{"port": int64(443), "name": "", "protocol": "UDP"}},
		}}))
		c := find(t, runWith(t, k), "admission/webhooks")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "is UDP, not TCP") {
			t.Fatalf("want FAIL naming the protocol, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("a url-based webhook has nothing local to verify", func(t *testing.T) {
		k := healthy()
		k.stage("discovery.k8s.io/v1/endpointslices", slice("ns", "svc", true, ""))
		k.stage("admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
			obj("", "external", map[string]any{"webhooks": []any{
				map[string]any{"clientConfig": map[string]any{"url": "https://example.test/hook"}, "failurePolicy": "Fail"},
			}}))
		if c := find(t, runWith(t, k), "admission/webhooks"); c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})
}

func TestCertificates(t *testing.T) {
	cert := func(ns, name, ready string) map[string]any {
		return obj(ns, name, map[string]any{"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": ready}},
		}})
	}

	t.Run("an unissued certificate fails even when the console works", func(t *testing.T) {
		k := healthy()
		k.stage("cert-manager.io/v1/certificates",
			cert("kube-dc", "console-tls", "True"),
			cert("kube-dc", "keycloak-tls", "False"))
		c := find(t, runWith(t, k), "certificates/issued")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "kube-dc/keycloak-tls") {
			t.Fatalf("want FAIL naming the certificate, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("a certificate with no Ready condition counts as not issued", func(t *testing.T) {
		k := healthy()
		k.stage("cert-manager.io/v1/certificates", obj("ns", "fresh", nil))
		if c := find(t, runWith(t, k), "certificates/issued"); c.Outcome != Fail {
			t.Fatalf("want FAIL, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("all ready passes", func(t *testing.T) {
		k := healthy()
		k.stage("cert-manager.io/v1/certificates", cert("a", "one", "True"), cert("b", "two", "True"))
		if c := find(t, runWith(t, k), "certificates/issued"); c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("no cert-manager skips", func(t *testing.T) {
		if c := find(t, runWith(t, healthy()), "certificates/issued"); c.Outcome != Skipped {
			t.Fatalf("want SKIP, got %s", c.Outcome)
		}
	})
}

func TestHelmReleases(t *testing.T) {
	hr := func(ns, name, ready string, suspend bool) map[string]any {
		spec := map[string]any{}
		if suspend {
			spec["suspend"] = true
		}
		o := obj(ns, name, map[string]any{"spec": spec})
		if ready != "" {
			o["status"] = map[string]any{
				"conditions": []any{map[string]any{"type": "Ready", "status": ready}},
			}
		}
		return o
	}

	t.Run("a failing release fails even though its Kustomization is Ready", func(t *testing.T) {
		k := healthy()
		k.stage("helm.toolkit.fluxcd.io/v2/helmreleases",
			hr("kube-dc", "kube-dc", "False", false),
			hr("cloudsigma", "other", "True", false))
		rep := runWith(t, k)
		if rep.State == StateUsable {
			t.Error("a wedged chart must not read as usable")
		}
		c := find(t, rep, "flux/helmreleases")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "kube-dc/kube-dc") {
			t.Fatalf("want FAIL naming the release, got %s: %s", c.Outcome, c.Detail)
		}
		if !strings.Contains(c.Fix, "Kustomization that applied these is Ready") {
			t.Errorf("the fix must explain why Flux still looks green, got: %s", c.Fix)
		}
	})

	t.Run("a suspended release is reported but does not fail", func(t *testing.T) {
		k := healthy()
		k.stage("helm.toolkit.fluxcd.io/v2/helmreleases",
			hr("kube-dc", "kube-dc", "", true),
			hr("x", "y", "True", false))
		rep := runWith(t, k)
		c := find(t, rep, "flux/helmreleases")
		if c.Outcome != Pass {
			t.Fatalf("suspending is a deliberate act, want PASS, got %s: %s", c.Outcome, c.Detail)
		}
		if !strings.Contains(c.Detail, "SUSPENDED") || !strings.Contains(c.Detail, "kube-dc/kube-dc") {
			t.Errorf("a suspended release must still be surfaced, got: %s", c.Detail)
		}
		if rep.State != StateUsable {
			t.Errorf("state = %s, want usable", rep.State)
		}
	})

	t.Run("a release with no status counts as not Ready", func(t *testing.T) {
		k := healthy()
		k.stage("helm.toolkit.fluxcd.io/v2/helmreleases", hr("ns", "fresh", "", false))
		if c := find(t, runWith(t, k), "flux/helmreleases"); c.Outcome != Fail {
			t.Fatalf("want FAIL, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("an older stored apiVersion is still found", func(t *testing.T) {
		k := healthy()
		k.stage("helm.toolkit.fluxcd.io/v2beta1/helmreleases", hr("ns", "old", "False", false))
		if c := find(t, runWith(t, k), "flux/helmreleases"); c.Outcome != Fail {
			t.Fatalf("want FAIL from the v2beta1 CRD, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("no HelmReleases skips", func(t *testing.T) {
		if c := find(t, runWith(t, healthy()), "flux/helmreleases"); c.Outcome != Skipped {
			t.Fatalf("want SKIP, got %s", c.Outcome)
		}
	})
}

func TestManagementSnat(t *testing.T) {
	rule := func(name string, status map[string]any) map[string]any {
		return obj("", name, map[string]any{"spec": map[string]any{"ovnEip": "ovn-cluster-ext-cloud", "vpcSubnet": "ovn-default"}, "status": status})
	}
	eip := func(ready bool, v4 string) map[string]any {
		return obj("", "ovn-cluster-ext-cloud", map[string]any{"spec": map[string]any{"type": "lrp"},
			"status": map[string]any{"ready": ready, "v4Ip": v4}})
	}

	t.Run("no management rule at all is skipped, not usable-blocking", func(t *testing.T) {
		k := healthy()
		c := find(t, runWith(t, k), "network/management-snat")
		if c.Outcome != Skipped || c.Required {
			t.Fatalf("want non-required SKIP, got %s required=%v", c.Outcome, c.Required)
		}
	})

	// The webdock 2026-08-31 shape: rule declared, status empty, no OvnEip.
	t.Run("rule present but unpublished and OvnEip missing fails with the recipe", func(t *testing.T) {
		k := healthy()
		k.stage("kubeovn.io/v1/ovn-snat-rules", rule("ovn-cluster-to-ext-cloud", map[string]any{}))
		c := find(t, runWith(t, k), "network/management-snat")
		if c.Outcome != Fail || !c.Required {
			t.Fatalf("want required FAIL, got %s required=%v: %s", c.Outcome, c.Required, c.Detail)
		}
		if !strings.Contains(c.Detail, "OvnEip ovn-cluster-ext-cloud is missing") {
			t.Errorf("detail must name the missing OvnEip, got: %s", c.Detail)
		}
		if !strings.Contains(c.Fix, "NotReady") || !strings.Contains(c.Fix, "create OvnEip") {
			t.Errorf("fix must state the blast radius and the recipe, got: %s", c.Fix)
		}
	})

	t.Run("OvnEip present but rule still unpublished is reported as such", func(t *testing.T) {
		k := healthy()
		k.stage("kubeovn.io/v1/ovn-snat-rules", rule("ovn-cluster-to-ext-cloud", map[string]any{"ready": false}))
		k.stage("kubeovn.io/v1/ovn-eips", eip(true, "100.64.0.4"))
		c := find(t, runWith(t, k), "network/management-snat")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "has not published it") {
			t.Fatalf("want FAIL naming the unpublished rule, got %s: %s", c.Outcome, c.Detail)
		}
	})

	t.Run("published address passes", func(t *testing.T) {
		k := healthy()
		k.stage("kubeovn.io/v1/ovn-snat-rules", rule("ovn-cluster-to-ext-cloud",
			map[string]any{"vpc": "ovn-cluster", "v4Eip": "100.64.0.4", "v4IpCidr": "10.100.0.0/16", "ready": true}))
		k.stage("kubeovn.io/v1/ovn-eips", eip(true, "100.64.0.4"))
		c := find(t, runWith(t, k), "network/management-snat")
		if c.Outcome != Pass || !strings.Contains(c.Detail, "100.64.0.4") {
			t.Fatalf("want PASS with the address, got %s: %s", c.Outcome, c.Detail)
		}
	})
}

func TestManagementGatewayPair(t *testing.T) {
	rule := obj("", "ovn-cluster-to-ext-cloud", map[string]any{
		"spec":   map[string]any{"ovnEip": "ovn-cluster-ext-cloud"},
		"status": map[string]any{"v4Eip": "100.64.0.4", "ready": true}})
	withNB := func(nb string) *fakeK8s {
		k := healthy()
		k.stage("kubeovn.io/v1/ovn-snat-rules", rule)
		k.podNames = []string{"ovn-central-0"}
		k.execOut = map[string]string{"ovn-central-0": nb}
		return k
	}
	t.Run("no rule is skipped", func(t *testing.T) {
		if c := find(t, runWith(t, healthy()), "network/management-gw-pair"); c.Outcome != Skipped {
			t.Fatalf("want SKIP, got %s: %s", c.Outcome, c.Detail)
		}
	})
	// webdock 2026-08-31: LRP present, LSP absent.
	t.Run("router port without its switch port fails with the heal", func(t *testing.T) {
		c := find(t, runWith(t, withNB("LRP=100.64.0.4/16\n")), "network/management-gw-pair")
		if c.Outcome != Fail || !c.Required || !strings.Contains(c.Detail, "switch port ext-cloud-ovn-cluster is missing") {
			t.Fatalf("want required FAIL naming the missing LSP, got %s: %s", c.Outcome, c.Detail)
		}
		if !strings.Contains(c.Fix, "lrp-del ovn-cluster-ext-cloud") || !strings.Contains(c.Fix, "#6469") {
			t.Errorf("fix must carry the heal and the race warning: %s", c.Fix)
		}
	})
	t.Run("address drift between port and rule fails", func(t *testing.T) {
		c := find(t, runWith(t, withNB("LRP=100.64.0.9/16\nLSPTYPE=router\nLSPOPTS={router-port=ovn-cluster-ext-cloud}\n")), "network/management-gw-pair")
		if c.Outcome != Fail || !strings.Contains(c.Detail, "published 100.64.0.4") {
			t.Fatalf("want FAIL on drift, got %s: %s", c.Outcome, c.Detail)
		}
	})
	t.Run("complete pair passes", func(t *testing.T) {
		c := find(t, runWith(t, withNB("LRP=100.64.0.4/16\nLSPTYPE=router\nLSPOPTS={router-port=ovn-cluster-ext-cloud}\n")), "network/management-gw-pair")
		if c.Outcome != Pass {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})
	t.Run("unreadable NB is a skip, not a false fail", func(t *testing.T) {
		c := find(t, runWith(t, withNB("")), "network/management-gw-pair")
		if c.Outcome != Skipped {
			t.Fatalf("want SKIP, got %s: %s", c.Outcome, c.Detail)
		}
	})
}

func TestDefaultVPCPatchPairs(t *testing.T) {
	subnet := func(name string, ready bool) map[string]any {
		return obj("", name, map[string]any{"spec": map[string]any{"vpc": "ovn-cluster"},
			"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": map[bool]string{true: "True", false: "False"}[ready]}}}})
	}
	with := func(nb string, subnets ...map[string]any) *fakeK8s {
		k := healthy()
		k.stage("kubeovn.io/v1/subnets", subnets...)
		k.podNames = []string{"ovn-central-0"}
		k.execOut = map[string]string{"ovn-central-0": nb}
		return k
	}
	// webdock 2026-08-31: ext-cloud healed, infra-net still orphaned.
	t.Run("router port without switch port fails and names the subnet", func(t *testing.T) {
		k := with("ext-cloud LRP=100.64.0.4/16 LSP=router\ninfra-net LRP=100.66.0.1/16 LSP=\n",
			subnet("ext-cloud", true), subnet("infra-net", false), obj("", "join", map[string]any{"spec": map[string]any{"vpc": "ovn-cluster"}}))
		c := find(t, runWith(t, k), "network/default-vpc-patch-pairs")
		if c.Outcome != Fail || !c.Required || !strings.Contains(c.Detail, "infra-net: router port ovn-cluster-infra-net") {
			t.Fatalf("want required FAIL naming infra-net, got %s: %s", c.Outcome, c.Detail)
		}
		if strings.Contains(c.Detail, "ext-cloud") {
			t.Errorf("healthy ext-cloud must not be reported: %s", c.Detail)
		}
	})
	t.Run("complete pairs with Ready subnets pass", func(t *testing.T) {
		k := with("ext-cloud LRP=100.64.0.4/16 LSP=router\ninfra-net LRP=100.66.0.1/16 LSP=router\n",
			subnet("ext-cloud", true), subnet("infra-net", true))
		c := find(t, runWith(t, k), "network/default-vpc-patch-pairs")
		if c.Outcome != Pass || !strings.Contains(c.Detail, "ext-cloud, infra-net") {
			t.Fatalf("want PASS, got %s: %s", c.Outcome, c.Detail)
		}
	})
	t.Run("tenant-VPC subnets are ignored", func(t *testing.T) {
		k := healthy()
		k.stage("kubeovn.io/v1/subnets", obj("", "t-default", map[string]any{"spec": map[string]any{"vpc": "t"}}))
		if c := find(t, runWith(t, k), "network/default-vpc-patch-pairs"); c.Outcome != Skipped {
			t.Fatalf("want SKIP, got %s: %s", c.Outcome, c.Detail)
		}
	})
}
