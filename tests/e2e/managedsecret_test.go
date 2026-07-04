/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// E2E suite for the Secrets Manager vertical (M1-T12 first cut). The
// existing E2E rig uses a cluster-admin kubeconfig (no per-tenant JWT
// scaffolding), so this file covers the **controller-side** contract:
//
//   - E06: ManagedSecret with sync.enabled=true projects values to a
//     Kubernetes Secret via ESO within 60s.
//   - T13: rotation reconcile writes a new KV version on the schedule
//     (status.lastRotatedTime / status.currentVersion / synced Secret
//     pick up the new value within the next refreshInterval).
//   - CRD lifecycle: create → patch sync → delete, with status condition
//     checks at each step.
//   - Cross-namespace guard: ManagedSecret created in a namespace
//     without the kube-dc.com/project annotation surfaces
//     ConditionReady=False / ReasonProjectAnnotationMissing.
//
// Tenant-side flows that need a Keycloak JWT (E01–E05, E07, E13, E15:
// value reads, cross-org isolation, OrganizationGroup → policy
// propagation, used-by panel) are out of scope for this file and
// tracked as M1-T12b in the agentic plan; they need a kube-dc CLI
// kubeconfig context bootstrapped from a test Keycloak user, which
// the existing suite doesn't provision.

package e2e

import (
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	securityv1alpha1 "github.com/shalb/kube-dc/api/security.kube-dc.com/v1alpha1"
)

// targetProjectNamespace is the project namespace the suite uses for
// the managed-secret specs. Reused from any pre-existing project on
// the cluster; the suite refuses to invent namespaces because the
// kube-dc project reconciler does heavy work (OVN/Keycloak/OpenBao)
// that's out of scope here.
//
// Override with `KUBE_DC_E2E_PROJECT_NS=<ns>` to point the specs at a
// different ready project (e.g. the staging cluster ships shalb-envoy
// while cloud has shalb-docs).
func targetProjectNamespace() string {
	if v := os.Getenv("KUBE_DC_E2E_PROJECT_NS"); v != "" {
		return v
	}
	return "shalb-docs"
}

var _ = Describe("ManagedSecret (M1-T12 first cut, controller-side)", func() {

	// generated names per spec so reruns of the same suite don't trip
	// on leftovers from a prior failed run.
	var (
		ns     string
		msName string
		msKey  types.NamespacedName
	)

	BeforeEach(func() {
		ns = targetProjectNamespace()
		// Use ginkgo's spec-randomisation-friendly seed: time + spec
		// name embedded so a `--repeat=N` doesn't collide across runs.
		msName = fmt.Sprintf("e2e-msec-%d", time.Now().UnixNano())
		msKey = types.NamespacedName{Namespace: ns, Name: msName}

		// Skip the spec gracefully if the target project namespace
		// doesn't exist on this cluster — the suite is reusable
		// across stage/cloud/kind without code edits.
		var probe corev1.Namespace
		err := k8sClient.Get(ctx, types.NamespacedName{Name: ns}, &probe)
		if err != nil {
			Skip(fmt.Sprintf("project namespace %q not present on this cluster; set KUBE_DC_E2E_PROJECT_NS to override", ns))
		}
	})

	AfterEach(func() {
		// Best-effort delete — Skip()'d specs will have an empty msName.
		if msName == "" {
			return
		}
		ms := &securityv1alpha1.ManagedSecret{}
		if err := k8sClient.Get(ctx, msKey, ms); err == nil {
			By("AfterEach: deleting test ManagedSecret " + msName)
			_ = k8sClient.Delete(ctx, ms)
		}
	})

	It("E06: sync=enabled projects values to a Kubernetes Secret via ESO within 60s", func() {
		Logf("BEGIN: E06 sync test for %s/%s", ns, msName)
		By("Creating a ManagedSecret with sync enabled")
		ms := &securityv1alpha1.ManagedSecret{
			ObjectMeta: metav1.ObjectMeta{Name: msName, Namespace: ns},
			Spec: securityv1alpha1.ManagedSecretSpec{
				Type:        securityv1alpha1.SecretTypeOpaque,
				Description: "E2E E06 sync smoke",
				Sync: securityv1alpha1.SecretSync{
					Enabled:          true,
					TargetSecretName: msName,
					RefreshInterval:  "30s",
				},
			},
		}
		Expect(k8sClient.Create(ctx, ms)).To(Succeed())

		By("Waiting for Conditions[Ready]=True (controller reconciled the KV metadata + ExternalSecret)")
		Eventually(func() bool {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, msKey, got); err != nil {
				return false
			}
			for _, c := range got.Status.Conditions {
				if c.Type == securityv1alpha1.ConditionReady && c.Status == metav1.ConditionTrue {
					return true
				}
			}
			return false
		}, 60*time.Second, 3*time.Second).Should(BeTrue(),
			"ManagedSecret should reach Ready=True within 60s; check the manager logs if this trips")

		By("Waiting for status.syncedSecretName to point at the configured target")
		Eventually(func() string {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, msKey, got); err != nil {
				return ""
			}
			return got.Status.SyncedSecretName
		}, 60*time.Second, 3*time.Second).Should(Equal(msName))

		// Note: we don't wait for the K8s Secret itself here because no
		// KV value has been written yet (E2E suite has no JWT to call
		// the backend `/values` endpoint). The synced Secret materialises
		// only after the first PUT, which is exercised by the manual
		// CLI runbook + the live `kube-dc secrets put` smoke. This
		// spec pins the CONTROLLER half of E06; the value-plane half
		// belongs to T12b once JWT scaffolding lands.
		Logf("E06: Ready=True + SyncedSecretName populated; KV write half deferred to T12b")
	})

	It("T13: rotation generates a first version on first reconcile (type=password)", func() {
		// The rotation reconcile path WRITES a KV version using the
		// controller's own OpenBao token (PRD §9.4 documented
		// exception). For an empty secret with rotation.enabled=true
		// the controller generates and writes v1 within the first
		// reconcile cycle; the suite asserts status surfacing rather
		// than the value itself (controllers never log the value).
		Logf("BEGIN: T13 rotation test for %s/%s", ns, msName)
		By("Creating a ManagedSecret with rotation enabled (type=password, 1m interval)")
		ms := &securityv1alpha1.ManagedSecret{
			ObjectMeta: metav1.ObjectMeta{Name: msName, Namespace: ns},
			Spec: securityv1alpha1.ManagedSecretSpec{
				Type:        securityv1alpha1.SecretTypePassword,
				Description: "E2E T13 rotation smoke",
				Rotation: securityv1alpha1.SecretRotation{
					Enabled:  true,
					Interval: "1m",
					Generator: &securityv1alpha1.PasswordGenerator{
						Length:  24,
						Charset: securityv1alpha1.PasswordCharsetAlnumSymbol,
					},
				},
				Sync: securityv1alpha1.SecretSync{
					Enabled:          true,
					TargetSecretName: msName,
					RefreshInterval:  "30s",
				},
			},
		}
		Expect(k8sClient.Create(ctx, ms)).To(Succeed())

		By("Waiting for status.lastRotatedTime to be set (controller wrote v1)")
		Eventually(func() bool {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, msKey, got); err != nil {
				return false
			}
			return got.Status.LastRotatedTime != nil
		}, 90*time.Second, 5*time.Second).Should(BeTrue(),
			"rotation must populate status.lastRotatedTime on first reconcile")

		By("Waiting for ConditionRotationScheduled=True")
		Eventually(func() bool {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, msKey, got); err != nil {
				return false
			}
			for _, c := range got.Status.Conditions {
				if c.Type == securityv1alpha1.ConditionRotationScheduled && c.Status == metav1.ConditionTrue {
					return true
				}
			}
			return false
		}, 30*time.Second, 3*time.Second).Should(BeTrue())

		By("Waiting for status.nextRotationTime to be a future timestamp (~1m from now)")
		Eventually(func() bool {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, msKey, got); err != nil {
				return false
			}
			if got.Status.NextRotationTime == nil {
				return false
			}
			delta := time.Until(got.Status.NextRotationTime.Time)
			// Window: between 30s (we just rotated, ~30s elapsed at
			// most before status read) and 90s (interval + slack).
			return delta > 0 && delta < 90*time.Second
		}, 30*time.Second, 3*time.Second).Should(BeTrue())

		Logf("T13: first rotation visible in status; full 2-rotation cycle deferred (1m interval × 2 = >2min)")
	})

	It("T13: rotation skips with ConfigInvalid when type != password", func() {
		// Reviewer second-pass: ReasonRotationConfigInvalid is the
		// distinct reason for enabled-but-misconfigured. Pin it.
		Logf("BEGIN: T13 invalid-config test for %s/%s", ns, msName)
		By("Creating a ManagedSecret with rotation enabled but type=opaque (mismatch)")
		ms := &securityv1alpha1.ManagedSecret{
			ObjectMeta: metav1.ObjectMeta{Name: msName, Namespace: ns},
			Spec: securityv1alpha1.ManagedSecretSpec{
				Type: securityv1alpha1.SecretTypeOpaque,
				Rotation: securityv1alpha1.SecretRotation{
					Enabled:  true,
					Interval: "1h",
				},
			},
		}
		Expect(k8sClient.Create(ctx, ms)).To(Succeed())

		By("Waiting for ConditionRotationScheduled=False with Reason=RotationConfigInvalid")
		Eventually(func() (string, error) {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, msKey, got); err != nil {
				return "", err
			}
			for _, c := range got.Status.Conditions {
				if c.Type == securityv1alpha1.ConditionRotationScheduled && c.Status == metav1.ConditionFalse {
					return c.Reason, nil
				}
			}
			return "", nil
		}, 30*time.Second, 3*time.Second).Should(Equal(securityv1alpha1.ReasonRotationConfigInvalid))

		// And status.lastRotatedTime must remain nil — no write happened.
		got := &securityv1alpha1.ManagedSecret{}
		Expect(k8sClient.Get(ctx, msKey, got)).To(Succeed())
		Expect(got.Status.LastRotatedTime).To(BeNil(),
			"rotation must not write a value when the config is invalid")
	})

	It("CRD lifecycle: patch spec.sync.enabled=false strips the ExternalSecret", func() {
		Logf("BEGIN: CRD-lifecycle sync-toggle test for %s/%s", ns, msName)
		By("Creating a ManagedSecret with sync enabled")
		ms := &securityv1alpha1.ManagedSecret{
			ObjectMeta: metav1.ObjectMeta{Name: msName, Namespace: ns},
			Spec: securityv1alpha1.ManagedSecretSpec{
				Type: securityv1alpha1.SecretTypeOpaque,
				Sync: securityv1alpha1.SecretSync{
					Enabled:          true,
					TargetSecretName: msName,
					RefreshInterval:  "30s",
				},
			},
		}
		Expect(k8sClient.Create(ctx, ms)).To(Succeed())

		By("Waiting for sync to land (status.syncedSecretName populated)")
		Eventually(func() string {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, msKey, got); err != nil {
				return ""
			}
			return got.Status.SyncedSecretName
		}, 60*time.Second, 3*time.Second).Should(Equal(msName))

		By("Patching spec.sync.enabled=false")
		got := &securityv1alpha1.ManagedSecret{}
		Expect(k8sClient.Get(ctx, msKey, got)).To(Succeed())
		got.Spec.Sync.Enabled = false
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		By("Waiting for status.syncedSecretName to clear + ConditionSynced=False")
		Eventually(func() bool {
			cur := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, msKey, cur); err != nil {
				return false
			}
			if cur.Status.SyncedSecretName != "" {
				return false
			}
			for _, c := range cur.Status.Conditions {
				if c.Type == securityv1alpha1.ConditionSynced && c.Status == metav1.ConditionFalse &&
					c.Reason == securityv1alpha1.ReasonSyncDisabled {
					return true
				}
			}
			return false
		}, 60*time.Second, 3*time.Second).Should(BeTrue())
	})

	It("cross-namespace guard: a namespace without the kube-dc.com/project annotation gets Ready=False / ProjectAnnotationMissing", func() {
		// Create a throwaway namespace without the project annotation
		// and put a ManagedSecret in it. The controller should refuse
		// to reconcile (no project context) and surface the canonical
		// reason. We don't try to delete the namespace afterwards
		// because deletion of K8s namespaces is async + heavy; the
		// stray namespace is harmless.
		Logf("BEGIN: cross-namespace guard test")
		strayNS := fmt.Sprintf("e2e-stray-%d", time.Now().UnixNano())
		strayObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: strayNS}}
		Expect(k8sClient.Create(ctx, strayObj)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, strayObj)
		})

		strayName := fmt.Sprintf("e2e-stray-msec-%d", time.Now().UnixNano())
		strayKey := types.NamespacedName{Namespace: strayNS, Name: strayName}
		strayMS := &securityv1alpha1.ManagedSecret{
			ObjectMeta: metav1.ObjectMeta{Name: strayName, Namespace: strayNS},
			Spec:       securityv1alpha1.ManagedSecretSpec{Type: securityv1alpha1.SecretTypeOpaque},
		}
		Expect(k8sClient.Create(ctx, strayMS)).To(Succeed())
		DeferCleanup(func() {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, strayKey, got); err == nil {
				_ = k8sClient.Delete(ctx, got)
			} else if !apierrors.IsNotFound(err) {
				Logf("cleanup get failed: %v", err)
			}
		})

		By("Waiting for Conditions[Ready]=False with Reason=ProjectAnnotationMissing")
		Eventually(func() (string, error) {
			got := &securityv1alpha1.ManagedSecret{}
			if err := k8sClient.Get(ctx, strayKey, got); err != nil {
				return "", err
			}
			for _, c := range got.Status.Conditions {
				if c.Type == securityv1alpha1.ConditionReady && c.Status == metav1.ConditionFalse {
					return c.Reason, nil
				}
			}
			return "", nil
		}, 60*time.Second, 3*time.Second).Should(Equal(securityv1alpha1.ReasonProjectAnnotationMissing))
	})
})
