/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// M1-T12b spec bodies. Lives in a separate file from the
// BeforeSuite/AfterSuite scaffolding (managedsecret_tenant_test.go)
// so the diff per spec stays easy to review.
//
// Each spec calls requireTenant() first — if BeforeSuite couldn't
// fully provision the fixture (missing realm-access, project ns
// absent, etc.) the spec Skip()s with the recorded reason instead
// of failing on a nil-deref deep in the helper.

package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	securityv1alpha1 "github.com/shalb/kube-dc/api/security.kube-dc.com/v1alpha1"
	"github.com/shalb/kube-dc/tests/e2e/helpers"
)

// newBackendForJWT is a thin wrapper around helpers.NewBackendClient
// so spec authors can `cli := newBackendForJWT(jwt)` without
// re-typing the domain. Used by E13 where the spec needs to acquire
// a fresh token after a group change.
func newBackendForJWT(domain, jwt string) *helpers.BackendClient {
	return helpers.NewBackendClient(domain, jwt)
}

// backendReplicas returns the current `replicas` of the kube-dc
// backend Deployment. Used by E05 to detect the phase-1 multi-pod
// elevation-store gap. Returns 1 on lookup failure so the gate
// stays permissive (tests don't false-Skip on lookup errors).
func backendReplicas() int {
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: "kube-dc", Name: "kube-dc-backend"}
	if err := k8sClient.Get(context.Background(), key, dep); err != nil {
		return 1
	}
	if dep.Spec.Replicas != nil {
		return int(*dep.Spec.Replicas)
	}
	return 1
}

// backendStickyBTPApplied reports whether the source-IP
// BackendTrafficPolicy from chart v0.3.39 (T06-FU1) is present in
// the kube-dc namespace. We use an unstructured Get because the
// gateway.envoyproxy.io scheme isn't registered in the suite (the
// e2e_suite_test.go BeforeSuite only loads kube-dc + kube-ovn +
// security + HNC). Falling back to unstructured keeps the probe
// dependency-free.
func backendStickyBTPApplied() bool {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "gateway.envoyproxy.io",
		Version: "v1alpha1",
		Kind:    "BackendTrafficPolicy",
	})
	key := types.NamespacedName{Namespace: "kube-dc", Name: "kube-dc-backend-sticky"}
	return k8sClient.Get(context.Background(), key, u) == nil
}

// makeConsumerDeployment / makeConsumerStatefulSet / makeConsumerCronJob
// return constructors that build (replicas=0 / suspended) workloads
// referencing the synced Kubernetes Secret. We never actually want
// the scheduler to spin pods for these — the consumer scanner just
// needs to observe the API objects.
//
// Each helper returns (object, cleanup). The cleanup deletes the
// object even when the spec fails mid-way.
func makeConsumerDeployment(secretName string) func() (client.Object, func()) {
	return func() (client.Object, func()) {
		name := uniqueName("t12b-cons-dep")
		replicas := int32(0)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenant.projectNS},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:    "noop",
							Image:   "busybox:latest",
							Command: []string{"sleep", "infinity"},
							EnvFrom: []corev1.EnvFromSource{{
								SecretRef: &corev1.SecretEnvSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
								},
							}},
						}},
					},
				},
			},
		}
		return dep, func() { _ = k8sClient.Delete(context.Background(), dep) }
	}
}

func makeConsumerStatefulSet(secretName string) func() (client.Object, func()) {
	return func() (client.Object, func()) {
		name := uniqueName("t12b-cons-sts")
		replicas := int32(0)
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenant.projectNS},
			Spec: appsv1.StatefulSetSpec{
				ServiceName: name,
				Replicas:    &replicas,
				Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:    "noop",
							Image:   "busybox:latest",
							Command: []string{"sleep", "infinity"},
							Env: []corev1.EnvVar{{
								Name: "TOKEN",
								ValueFrom: &corev1.EnvVarSource{
									SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
										Key:                  "TOKEN",
									},
								},
							}},
						}},
					},
				},
			},
		}
		return sts, func() { _ = k8sClient.Delete(context.Background(), sts) }
	}
}

func makeConsumerCronJob(secretName string) func() (client.Object, func()) {
	return func() (client.Object, func()) {
		name := uniqueName("t12b-cons-cj")
		suspend := true
		cj := &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenant.projectNS},
			Spec: batchv1.CronJobSpec{
				Schedule: "0 0 1 1 *", // Jan 1 once a year — won't fire during the test
				Suspend:  &suspend,
				JobTemplate: batchv1.JobTemplateSpec{
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyOnFailure,
								Containers: []corev1.Container{{
									Name:    "noop",
									Image:   "busybox:latest",
									Command: []string{"true"},
									Env: []corev1.EnvVar{{
										Name: "TOKEN",
										ValueFrom: &corev1.EnvVarSource{
											SecretKeyRef: &corev1.SecretKeySelector{
												LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
												Key:                  "TOKEN",
											},
										},
									}},
								}},
							},
						},
					},
				},
			},
		}
		return cj, func() { _ = k8sClient.Delete(context.Background(), cj) }
	}
}

// secret-create helper: cluster-admin path (the suite owns
// ManagedSecret CRD writes; the tenant value plane goes via the
// backend client). Returns the resource name + a cleanup func.
func makeTenantSecret(ctx context.Context, name string, sync bool) (string, func()) {
	ms := &securityv1alpha1.ManagedSecret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenant.projectNS},
		Spec: securityv1alpha1.ManagedSecretSpec{
			Type:        securityv1alpha1.SecretTypeOpaque,
			Description: "T12b tenant smoke fixture",
			Sync: securityv1alpha1.SecretSync{
				Enabled:          sync,
				TargetSecretName: name,
				RefreshInterval:  "30s",
			},
		},
	}
	Expect(k8sClient.Create(ctx, ms)).To(Succeed())
	return name, func() {
		_ = k8sClient.Delete(ctx, ms)
	}
}

// ---- T09: create endpoint round-trip --------------------------------

var _ = Describe("M1-T12b T09: POST /api/secrets/:ns/:name creates a ManagedSecret + (optional) first KV version", func() {
	It("creates with sync defaults; rejects duplicate names; seeds initialValues atomically", func() {
		requireDev()
		ctxLocal, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		secretName := uniqueName("t12b-create")
		// AfterCleanup — the spec creates via the backend so the CR
		// might exist by the time cleanup runs. Delete via the
		// cluster-admin client so we don't depend on the backend's
		// delete-with-destroy path here.
		DeferCleanup(func() {
			ms := &securityv1alpha1.ManagedSecret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: tenant.projectNS},
			}
			_ = k8sClient.Delete(context.Background(), ms)
		})

		By("POST /api/secrets/:ns/:name with initialValues — atomic CR+v1 write")
		body := map[string]any{
			"type":        "opaque",
			"description": "T09 E2E spec",
			"sync": map[string]any{
				"enabled":          true,
				"targetSecretName": secretName,
			},
			"initialValues": map[string]string{
				"USERNAME": "alice",
				"PASSWORD": "hunter2",
			},
		}
		var created struct {
			Name      string `json:"name"`
			KvVersion int    `json:"kvVersion"`
		}
		status, raw, err := tenant.devUser.Backend.Do(ctxLocal,
			"POST",
			fmt.Sprintf("/api/secrets/%s/%s", tenant.projectNS, secretName),
			body, &created)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "create body: %s", raw)
		Expect(created.Name).To(Equal(secretName))
		Expect(created.KvVersion).To(Equal(1))

		By("duplicate POST is rejected with 409 (idempotent re-create not supported)")
		status, _, err = tenant.devUser.Backend.Do(ctxLocal,
			"POST",
			fmt.Sprintf("/api/secrets/%s/%s", tenant.projectNS, secretName),
			body, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(409), "expected 409 on duplicate create")

		By("seeded values round-trip via GET ?includeValue=true (poll for OpenBao cache warm-up)")
		// Same warm-up race as E03/E07: tolerate ~1s of empty
		// {data: {}} before steady state.
		var get struct {
			Value struct {
				Data map[string]string `json:"data"`
			} `json:"value"`
		}
		Eventually(func() (string, error) {
			s, _, err := tenant.devUser.Backend.Do(ctxLocal,
				"GET",
				fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, secretName),
				nil, &get)
			if err != nil {
				return "", err
			}
			if s != 200 {
				return "", fmt.Errorf("status %d", s)
			}
			return get.Value.Data["USERNAME"], nil
		}, 15*time.Second, 500*time.Millisecond).Should(Equal("alice"))
		Expect(get.Value.Data["PASSWORD"]).To(Equal("hunter2"))
	})

	It("rejects invalid type with 400 before any KV write", func() {
		requireDev()
		ctxLocal, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		secretName := uniqueName("t12b-create-badtype")
		status, raw, err := tenant.devUser.Backend.Do(ctxLocal,
			"POST",
			fmt.Sprintf("/api/secrets/%s/%s", tenant.projectNS, secretName),
			map[string]any{"type": "bogus"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(400), "expected 400 on invalid type, got %d: %s", status, raw)
		// Make sure the CR didn't sneak through:
		ms := &securityv1alpha1.ManagedSecret{}
		err = k8sClient.Get(ctxLocal, types.NamespacedName{Name: secretName, Namespace: tenant.projectNS}, ms)
		Expect(err).To(HaveOccurred()) // expect NotFound
	})
})

// ---- E03 — developer can put + get a secret in their own project ----

var _ = Describe("M1-T12b E03: developer can put + get a secret in their own project", func() {
	It("PUT /values then GET ?includeValue=true both 200; values round-trip; audit logs allowed", func() {
		requireDev()
		ctxLocal, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		secretName := uniqueName("t12b-dev")
		_, cleanup := makeTenantSecret(ctxLocal, secretName, true)
		DeferCleanup(cleanup)

		By("developer writes the first version via PUT /values")
		writeBody := map[string]any{"data": map[string]string{
			"DATABASE_URL": "postgres://dev:dev@db/dev",
			"API_TOKEN":    "abc-123-xyz",
		}}
		status, body, err := tenant.devUser.Backend.Do(ctxLocal,
			"PUT",
			fmt.Sprintf("/api/secrets/%s/%s/values", tenant.projectNS, secretName),
			writeBody, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "PUT /values response: %s", body)

		By("developer reads the metadata + value via GET ?includeValue=true (poll for OpenBao cache warm-up)")
		// Right after a backend rollout the per-(sub, org, role)
		// OpenBao token cache on each pod is cold; the first request
		// per pod triggers a fresh /login. With sticky source-IP
		// routing the PUT + GET should land on the same pod, but the
		// PUT can complete its login on pod-A while a near-instant
		// GET still resolves the connection to pod-B before the
		// gateway's hash converges. Result: empty {data: {}} that
		// resolves itself within a second. Poll for ~10s so the suite
		// catches the steady-state behaviour instead of the warm-up
		// race (M1-T12b D follow-up; see agentic plan for detail).
		var resp struct {
			Name  string `json:"name"`
			Value struct {
				Data map[string]string `json:"data"`
			} `json:"value"`
		}
		Eventually(func() (string, error) {
			status, body, err := tenant.devUser.Backend.Do(ctxLocal,
				"GET",
				fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, secretName),
				nil, &resp)
			if err != nil {
				return "", err
			}
			if status != 200 {
				return "", fmt.Errorf("GET ?includeValue body: %s", body)
			}
			return resp.Value.Data["DATABASE_URL"], nil
		}, 15*time.Second, 500*time.Millisecond).Should(Equal("postgres://dev:dev@db/dev"))
		Expect(resp.Name).To(Equal(secretName))
		Expect(resp.Value.Data["API_TOKEN"]).To(Equal("abc-123-xyz"))
	})
})

// ---- E04 — viewer can list metadata but is denied on value reveal ----

var _ = Describe("M1-T12b E04: viewer cannot reveal stored values", func() {
	It("GET /metadata 200; GET ?includeValue=true returns 403", func() {
		requireDev() // need dev to seed a value first
		requireViewer()
		ctxLocal, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		secretName := uniqueName("t12b-viewer-target")
		_, cleanup := makeTenantSecret(ctxLocal, secretName, true)
		DeferCleanup(cleanup)

		// Seed a value via the developer (so there's something to deny).
		_, _, err := tenant.devUser.Backend.Do(ctxLocal,
			"PUT",
			fmt.Sprintf("/api/secrets/%s/%s/values", tenant.projectNS, secretName),
			map[string]any{"data": map[string]string{"K": "v"}}, nil)
		Expect(err).NotTo(HaveOccurred())

		By("viewer lists secrets (metadata-only — allowed)")
		status, _, err := tenant.viewerUser.Backend.Do(ctxLocal,
			"GET",
			fmt.Sprintf("/api/secrets/%s", tenant.projectNS),
			nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "viewer list should be allowed")

		By("viewer tries ?includeValue=true — expect 403")
		status, body, err := tenant.viewerUser.Backend.Do(ctxLocal,
			"GET",
			fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, secretName),
			nil, nil)
		Expect(err).NotTo(HaveOccurred())
		// OpenBao denies the read via the user role's policy; backend
		// surfaces the 403 from the role-fallback loop.
		Expect(status).To(Equal(403), "viewer ?includeValue body: %s", body)
	})
})

// ---- E02 — developer cannot read values from a DIFFERENT project ----

var _ = Describe("M1-T12b E02: developer cannot read values from another project in the same org", func() {
	It("returns 403 on a cross-project value read", func() {
		requireDev()
		// E02 is only meaningful when the DEV identity is genuinely
		// scoped to one project. If KUBE_DC_E2E_DEV_JWT is in fact an
		// org-admin (a common smoke shortcut when realm-admin
		// provisioning isn't available — see the agentic plan for the
		// shalb realm-access drift note), cross-project access is
		// expected and we can't validate denial. Skip with a clear
		// pointer instead of false-failing.
		claims, err := helpers.DecodeJWT(tenant.devUser.JWT)
		if err == nil {
			if groups, ok := claims["groups"].([]any); ok {
				for _, g := range groups {
					if s, _ := g.(string); s == "org-admin" {
						Skip("KUBE_DC_E2E_DEV_JWT carries the org-admin group — cross-project access is granted, so this denial spec cannot run. Provision a project-scoped developer JWT to enable.")
					}
				}
			}
		}
		if tenant.secondaryProject == "" {
			Skip("KUBE_DC_E2E_SECONDARY_PROJECT unset; cross-project E02 needs a sibling project")
		}
		secondaryNS := tenant.org + "-" + tenant.secondaryProject
		// Probe that the secondary project namespace exists; skip if not.
		ctxLocal, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		probe := &corev1.Namespace{}
		if err := k8sClient.Get(ctxLocal, types.NamespacedName{Name: secondaryNS}, probe); err != nil {
			Skip(fmt.Sprintf("secondary project namespace %s absent; cannot run E02", secondaryNS))
		}

		// Pick any existing ManagedSecret in the secondary project,
		// or skip if none exist (we don't create one because that
		// would require a developer in the secondary project that
		// this suite hasn't provisioned).
		list := &securityv1alpha1.ManagedSecretList{}
		Expect(k8sClient.List(ctxLocal, list, client.InNamespace(secondaryNS))).To(Succeed())
		if len(list.Items) == 0 {
			Skip(fmt.Sprintf("no ManagedSecrets in %s; cannot test cross-project denial", secondaryNS))
		}
		target := list.Items[0].Name

		By("developer (only authorised on `docs`) reads from `jumbolot` — expect 403")
		status, body, err := tenant.devUser.Backend.Do(ctxLocal,
			"GET",
			fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", secondaryNS, target),
			nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(403), "cross-project GET body: %s", body)
	})
})

// ---- E01 — cross-org isolation (deferred unless Org B provisioned) ----

var _ = Describe("M1-T12b E01: cross-org isolation", func() {
	It("Org A token cannot read Org B's secrets — Skip when no Org B fixture", func() {
		requireTenant()
		// Provisioning a sibling Organization needs a full Org sync
		// (Keycloak realm + Kube-OVN VPC + per-project ESO/OpenBao
		// bootstrap), which can take 60-90s. Out of scope for the
		// current suite. Document the manual smoke instead:
		//
		//   1. Create Organization `kube-dc-e2e-orgb` (any valid org).
		//   2. Provision a test user in its realm.
		//   3. Try `GET /api/secrets/<shalb-docs>/...` with that user's
		//      token — expect 403 (jwt org guard fires before OpenBao).
		Skip("E01 needs a sibling Organization fixture; tracked as T12b-future and verified manually via the runbook in docs/internal/openbao-runbook.md")
	})
})

// ---- E05 — org-admin elevation flow ---------------------------------

var _ = Describe("M1-T12b E05: org-admin elevation flow", func() {
	BeforeEach(func() {
		requireOrgAdmin()
		// E05-a needs a developer to seed the value before the
		// org-admin reads it; require both up-front.
		if tenant.devUser.JWT == "" {
			Skip(fmt.Sprintf("developer identity needed to seed a value; set %s", envDevJWT))
		}
		// E05 hits the M1-T06-FU1 limitation when backend.replicas > 1
		// AND the source-IP consistent-hash BackendTrafficPolicy
		// isn't applied: the in-memory elevation store is per-pod
		// (util/elevation.js docstring), so the POST /elevate can
		// land on pod-A while the subsequent value read lands on
		// pod-B and the lookup misses. v0.3.39+ ships
		// templates/backend-traffic-policy.yaml which pins each
		// source IP to one pod via Envoy Gateway consistent-hash —
		// when that's applied + backend.replicas > 1 this spec
		// runs cleanly. Detect the gap by probing for the BTP and
		// Skip with a pointer if it isn't applied.
		if backendReplicas() > 1 && !backendStickyBTPApplied() {
			Skip(fmt.Sprintf(
				"E05 needs sticky sessions OR a shared elevation store: backend has %d replicas and the source-IP BackendTrafficPolicy is not applied. Apply v0.3.39's chart (or `backend.consistentHash.enabled=true` value) OR scale the backend to 1 replica.",
				backendReplicas()))
		}
	})

	It("E05-a (audit-stamp mode): elevation_id is stamped on value reads during the active window", func() {
		ctxLocal, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		secretName := uniqueName("t12b-elev")
		_, cleanup := makeTenantSecret(ctxLocal, secretName, true)
		DeferCleanup(cleanup)

		// Seed a value as developer (org-admin gets project access
		// via the `org-admin` group's cross-project admin policy).
		_, _, err := tenant.devUser.Backend.Do(ctxLocal,
			"PUT",
			fmt.Sprintf("/api/secrets/%s/%s/values", tenant.projectNS, secretName),
			map[string]any{"data": map[string]string{"K": "v"}}, nil)
		Expect(err).NotTo(HaveOccurred())

		By("org-admin POSTs /elevate with a reason")
		var grant struct {
			ElevationID string `json:"elevationId"`
			TTLSeconds  int    `json:"ttlSeconds"`
		}
		status, body, err := tenant.adminUser.Backend.Do(ctxLocal,
			"POST",
			fmt.Sprintf("/api/orgs/%s/projects/%s/elevate", tenant.org, tenant.project),
			map[string]any{"reason": "T12b E05-a smoke"}, &grant)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "elevate body: %s", body)
		Expect(grant.ElevationID).To(HavePrefix("elv-"))
		Expect(grant.TTLSeconds).To(BeNumerically(">", 800)) // ~15m - a few seconds

		By("org-admin reads the secret value during the active elevation")
		status, body, err = tenant.adminUser.Backend.Do(ctxLocal,
			"GET",
			fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, secretName),
			nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "read during elevation: %s", body)

		// Audit ingest into Loki has variable latency (5–60s in our
		// experience on stage/cloud). Poll for up to 2 minutes; on
		// failure, log what we DID see so a real ingest break is
		// distinguishable from a missing elevation_id stamp.
		By("audit stream stamps elevation_id on the value-read event (poll until visible)")
		matchedID := ""
		var lastSeenEvents int
		var lastSeenSample string
		Eventually(func() error {
			var list struct {
				Events []struct {
					Body map[string]any `json:"body"`
				} `json:"events"`
			}
			s, b, err := tenant.adminUser.Backend.Do(ctxLocal,
				"GET",
				fmt.Sprintf("/api/audit/orgs/%s/projects/%s?service=secrets&limit=200", tenant.org, tenant.project),
				nil, &list)
			if err != nil {
				return err
			}
			if s != 200 {
				return fmt.Errorf("audit query %d: %s", s, b)
			}
			lastSeenEvents = len(list.Events)
			for _, ev := range list.Events {
				if ev.Body["action"] == "secrets.value.read" && ev.Body["resource"] == secretName {
					if id, _ := ev.Body["elevation_id"].(string); id == grant.ElevationID {
						matchedID = id
						return nil
					}
					// Sample what we DID see for the same resource
					// so the failure message is actionable.
					lastSeenSample = fmt.Sprintf("read for %s had elevation_id=%v (want %s)",
						secretName, ev.Body["elevation_id"], grant.ElevationID)
				}
			}
			return fmt.Errorf("not yet visible (events polled: %d, last sample: %s)", lastSeenEvents, lastSeenSample)
		}, 2*time.Minute, 5*time.Second).Should(Succeed(),
			"expected secrets.value.read on %s with elevation_id=%s within 2m; last sample: %s",
			secretName, grant.ElevationID, lastSeenSample)
		Expect(matchedID).To(Equal(grant.ElevationID))

		By("DELETE /elevate ends the window")
		status, body, err = tenant.adminUser.Backend.Do(ctxLocal,
			"DELETE",
			fmt.Sprintf("/api/orgs/%s/projects/%s/elevate", tenant.org, tenant.project),
			nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "release body: %s", body)
	})

	// E05-b (gate-enforced mode) opt-in. Requires
	// ELEVATION_ENFORCE=1 on the backend; flipping it from the test
	// would patch a Deployment and restart pods (~30s), so we leave
	// it opt-in for now.
	It("E05-b (gate-enforced mode): pure org-admin without elevation → 403; opt-in via KUBE_DC_E2E_ELEVATION_ENFORCE=true", func() {
		if envOrDefault(envElevationEnforce, "") != "true" {
			Skip("set " + envElevationEnforce + "=true after deploying the backend with ELEVATION_ENFORCE=1 to run E05-b")
		}
		ctxLocal, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		secretName := uniqueName("t12b-elev-gate")
		_, cleanup := makeTenantSecret(ctxLocal, secretName, true)
		DeferCleanup(cleanup)

		// Seed value as developer.
		_, _, err := tenant.devUser.Backend.Do(ctxLocal,
			"PUT",
			fmt.Sprintf("/api/secrets/%s/%s/values", tenant.projectNS, secretName),
			map[string]any{"data": map[string]string{"K": "v"}}, nil)
		Expect(err).NotTo(HaveOccurred())

		By("org-admin tries to read WITHOUT elevation — expect 403")
		status, body, err := tenant.adminUser.Backend.Do(ctxLocal,
			"GET",
			fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, secretName),
			nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(403), "no-elevation read body: %s", body)
		Expect(body).To(ContainSubstring("elevation"))

		By("org-admin elevates and tries again — expect 200")
		_, _, err = tenant.adminUser.Backend.Do(ctxLocal,
			"POST",
			fmt.Sprintf("/api/orgs/%s/projects/%s/elevate", tenant.org, tenant.project),
			map[string]any{"reason": "E05-b gate smoke"}, nil)
		Expect(err).NotTo(HaveOccurred())
		status, _, err = tenant.adminUser.Backend.Do(ctxLocal,
			"GET",
			fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, secretName),
			nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))
	})
})

// ---- E07 — import an existing K8s Secret ----------------------------

var _ = Describe("M1-T12b E07: import existing K8s Secret as a managed secret", func() {
	It("creates the ManagedSecret + seeds KV from the source; cross-namespace import is gated by opt-in", func() {
		requireDev()
		ctxLocal, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		srcName := uniqueName("t12b-src")
		// Source Secret created by cluster-admin (same project namespace).
		src := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: srcName, Namespace: tenant.projectNS},
			Type:       corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"USERNAME": []byte("alice"),
				"PASSWORD": []byte("hunter2"),
			},
		}
		Expect(k8sClient.Create(ctxLocal, src)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctxLocal, src) })

		importedName := uniqueName("t12b-imported")
		body := map[string]any{
			"sourceSecretName": srcName,
			"type":             "opaque",
			"description":      "T12b E07 import",
		}
		var resp struct {
			Name      string `json:"name"`
			KVVersion int    `json:"kvVersion"`
		}
		By("developer POSTs /import to adopt the existing Secret")
		status, raw, err := tenant.devUser.Backend.Do(ctxLocal,
			"POST",
			fmt.Sprintf("/api/secrets/%s/%s/import", tenant.projectNS, importedName),
			body, &resp)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "import body: %s", raw)
		Expect(resp.Name).To(Equal(importedName))
		Expect(resp.KVVersion).To(BeNumerically(">=", 1))

		// Cleanup the imported ManagedSecret.
		DeferCleanup(func() {
			ms := &securityv1alpha1.ManagedSecret{
				ObjectMeta: metav1.ObjectMeta{Name: importedName, Namespace: tenant.projectNS},
			}
			_ = k8sClient.Delete(ctxLocal, ms)
		})

		By("the imported values round-trip via GET ?includeValue=true (poll for OpenBao cache warm-up)")
		// Same race as E03: post-PUT/POST GET can see {data: {}} for
		// ~1s during cold-pod token warm-up. Poll for steady state.
		var get struct {
			Value struct {
				Data map[string]string `json:"data"`
			} `json:"value"`
		}
		Eventually(func() (string, error) {
			s, _, err := tenant.devUser.Backend.Do(ctxLocal,
				"GET",
				fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, importedName),
				nil, &get)
			if err != nil {
				return "", err
			}
			if s != 200 {
				return "", fmt.Errorf("status %d", s)
			}
			return get.Value.Data["USERNAME"], nil
		}, 15*time.Second, 500*time.Millisecond).Should(Equal("alice"))
		Expect(get.Value.Data["PASSWORD"]).To(Equal("hunter2"))
	})
})

// ---- E15 — used-by consumer scanner ---------------------------------

var _ = Describe("M1-T12b E15: used-by panel lists Deployments + StatefulSets + CronJobs referencing the synced Secret", func() {
	It("creates representative workloads and verifies the scanner finds each kind", func() {
		requireDev()
		ctxLocal, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		secretName := uniqueName("t12b-consumers")
		_, cleanup := makeTenantSecret(ctxLocal, secretName, true)
		DeferCleanup(cleanup)

		// Seed a value so the synced K8s Secret materialises.
		_, _, err := tenant.devUser.Backend.Do(ctxLocal,
			"PUT",
			fmt.Sprintf("/api/secrets/%s/%s/values", tenant.projectNS, secretName),
			map[string]any{"data": map[string]string{"TOKEN": "abc"}}, nil)
		Expect(err).NotTo(HaveOccurred())

		// Create Deployment + StatefulSet + CronJob that reference
		// the (eventually-synced) Secret. We use a paused / replicas:0
		// shape where possible so the scheduler doesn't try to actually
		// run them — we just need the API objects to exist.
		By("creating consumer workloads (replicas=0 to avoid scheduling churn)")
		consumers := []func() (client.Object, func()){
			makeConsumerDeployment(secretName),
			makeConsumerStatefulSet(secretName),
			makeConsumerCronJob(secretName),
		}
		for _, fn := range consumers {
			obj, cleanup := fn()
			Expect(k8sClient.Create(ctxLocal, obj)).To(Succeed())
			DeferCleanup(cleanup)
		}

		By("GET /consumers eventually lists Deployment + StatefulSet + CronJob")
		Eventually(func() error {
			var resp struct {
				Items []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"items"`
			}
			s, b, err := tenant.devUser.Backend.Do(ctxLocal,
				"GET",
				fmt.Sprintf("/api/secrets/%s/%s/consumers", tenant.projectNS, secretName),
				nil, &resp)
			if err != nil {
				return err
			}
			if s != 200 {
				return fmt.Errorf("consumers %d: %s", s, b)
			}
			seen := map[string]bool{}
			for _, it := range resp.Items {
				seen[it.Kind] = true
			}
			missing := []string{}
			for _, k := range []string{"Deployment", "StatefulSet", "CronJob"} {
				if !seen[k] {
					missing = append(missing, k)
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("missing kinds: %s", strings.Join(missing, ", "))
			}
			return nil
		}, 60*time.Second, 5*time.Second).Should(Succeed())
	})
})

// ---- E13 — OrganizationGroup spec change propagates within 30s ------

var _ = Describe("M1-T12b E13: removing a user from the OrganizationGroup denies access within 30s", func() {
	It("revokes value-read access after the user's group binding is removed", func() {
		requireDev()
		if tenant.admin == nil || tenant.devUser.UserID == "" {
			Skip("E13 needs Keycloak realm-admin access AND a provisioned dev user (UserID); set " + envRealmAdminPassword + " to enable")
		}
		ctxLocal, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		secretName := uniqueName("t12b-propagation")
		_, cleanup := makeTenantSecret(ctxLocal, secretName, true)
		DeferCleanup(cleanup)

		// Seed value so the user has something to read.
		_, _, err := tenant.devUser.Backend.Do(ctxLocal,
			"PUT",
			fmt.Sprintf("/api/secrets/%s/%s/values", tenant.projectNS, secretName),
			map[string]any{"data": map[string]string{"K": "v"}}, nil)
		Expect(err).NotTo(HaveOccurred())

		By("baseline: developer reads the value — 200")
		status, body, err := tenant.devUser.Backend.Do(ctxLocal,
			"GET",
			fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, secretName),
			nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "baseline read: %s", body)

		By("remove the developer user from the developer group via Keycloak")
		Expect(tenant.admin.RemoveUserFromGroup(ctxLocal, tenant.devUser.UserID, devGroupName)).To(Succeed())
		DeferCleanup(func() {
			// Restore for downstream specs.
			_ = tenant.admin.AddUserToGroup(ctxLocal, tenant.devUser.UserID, devGroupName)
		})

		By("acquire a FRESH token (so the new groups[] is reflected) and verify denial within 30s")
		Eventually(func() error {
			fresh, err := tenant.admin.LoginAsUser(ctxLocal, tenant.devUser.Username)
			if err != nil {
				return err
			}
			cli := newBackendForJWT(tenant.domain, fresh)
			s, _, err := cli.Do(ctxLocal,
				"GET",
				fmt.Sprintf("/api/secrets/%s/%s?includeValue=true", tenant.projectNS, secretName),
				nil, nil)
			if err != nil {
				return err
			}
			if s == 403 {
				return nil
			}
			return fmt.Errorf("still %d, want 403", s)
		}, 30*time.Second, 3*time.Second).Should(Succeed())
	})
})
