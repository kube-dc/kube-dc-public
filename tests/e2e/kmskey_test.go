/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// E2E E10 — KMS encrypt/decrypt round-trip; rotate; decrypt-old-
// ciphertext-still-works. M3-T06 / dev-scope §12. Covers the M3 exit
// gate criterion (dev-scope §13 Phase 3): a tenant can encrypt and
// decrypt a payload, the controller can rotate the key, and OpenBao
// retains all key versions so previously-emitted ciphertext still
// decrypts.
//
// Split of responsibilities:
//
//   - kube-apiserver create / delete + status-mirror assertions use
//     the suite's cluster-admin k8sClient (matches the managedsecret
//     controller-side suite in managedsecret_test.go).
//
//   - encrypt / decrypt go through the per-user OpenBao OIDC token by
//     reusing the existing tenant fixture's devUser.Backend client.
//     Developer tier carries transit/encrypt + transit/decrypt grants
//     per the M3-T02 OrganizationGroup policy generator, so the
//     round-trip works without escalating to admin.
//
//   - Rotation is controller-driven via spec.rotation.interval — the
//     test waits for the reconciler's natural rotation tick instead
//     of calling the backend /rotate endpoint (which would need an
//     admin JWT we don't provision here).
//
// Skip cascade:
//
//   - Target project namespace missing            → Skip (cluster-mismatch)
//   - tenant fixture not provisioned              → Skip (no JWT)
//   - tenant.devUser.JWT empty                    → Skip (no dev JWT)
//   - KMSKey reconciler not reaching Ready=True   → fail with hint
//
// Cluster preconditions (operator runbook):
//
//   - OpenBao deployed + initialised
//   - hack/openbao-setup-controller-auth.sh run on this cluster
//     (v0.3.45+ includes the per-Org Transit policy paths)
//   - The KUBE_DC_E2E_PROJECT_NS project has its OrganizationGroup
//     → policy reconciliation completed (dev role's policy carries
//     the transit grants emitted by M3-T02 — see commit 7591899f)

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	securityv1alpha1 "github.com/shalb/kube-dc/api/security.kube-dc.com/v1alpha1"
)

// kmsRequireDevJWT returns the active dev BackendClient or Skip()s
// the spec with an actionable hint. The fixture might not have been
// provisioned at all (no Keycloak access) — both cases land here.
func kmsRequireDevJWT() {
	if tenant == nil {
		Skip("tenant fixture not initialised: " + tenantSkipReason)
	}
	if tenant.devUser.JWT == "" {
		Skip(fmt.Sprintf("developer JWT required for KMS encrypt/decrypt; set %s or provision the realm-access Secret", envDevJWT))
	}
}

// kmsBackendCall wraps a single Backend.Do call with a fresh 30s
// context so the per-request deadline never spans a long
// Eventually(...) wait between calls. The bug this guards against is
// subtle: a single test-wide ctxLocal created at the top of the spec
// will have expired by the time the post-rotation encrypt fires
// (interval + slack = ~60s > 30s). Re-creating the context per call
// keeps each round-trip's deadline tight while letting the spec take
// minutes overall.
//
// Cancel is called immediately after Do returns — `defer cancel()`
// would stack 5+ deferred calls on the It block and leak the
// timer goroutines until the spec exits. Explicit cancel is the
// idiomatic shape for short-lived per-call contexts.
func kmsBackendCall(method, path string, body, out any) (int, string, error) {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return tenant.devUser.Backend.Do(c, method, path, body, out)
}

var _ = Describe("E10: KMS encrypt/decrypt round-trip + rotation (M3-T06 / dev-scope §12)", func() {

	var (
		ns      string
		keyName string
		keyKey  types.NamespacedName
	)

	BeforeEach(func() {
		ns = targetProjectNamespace()
		// One unique name per spec run — Ginkgo's `--repeat` flag
		// reruns the same Describe block, so collisions on the second
		// pass would mask real failures.
		keyName = fmt.Sprintf("e2e-kms-%d", time.Now().UnixNano())
		keyKey = types.NamespacedName{Namespace: ns, Name: keyName}

		var probe corev1.Namespace
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: ns}, &probe); err != nil {
			Skip(fmt.Sprintf("project namespace %q not present on this cluster; set KUBE_DC_E2E_PROJECT_NS to override", ns))
		}
	})

	AfterEach(func() {
		if keyName == "" {
			return
		}
		k := &securityv1alpha1.KMSKey{}
		if err := k8sClient.Get(ctx, keyKey, k); err == nil {
			By("AfterEach: deleting test KMSKey " + keyName)
			_ = k8sClient.Delete(ctx, k)
		}
	})

	// Core E10 spec — the long one. Splits naturally into 7 phases:
	//   1. create + ready
	//   2. encrypt under v1
	//   3. decrypt → matches
	//   4. controller-driven rotation → v2
	//   5. encrypt under v2
	//   6. decrypt v2 ciphertext → matches
	//   7. decrypt v1 ciphertext (older version) → still matches
	It("encrypts and decrypts; rotation bumps version; old-version ciphertext stays decryptable", func() {
		kmsRequireDevJWT()

		Logf("BEGIN: E10 full round-trip for %s/%s", ns, keyName)

		// --- 1. create + ready ---------------------------------------
		By("Creating a KMSKey with rotation enabled (45s interval)")
		// 45s is a balance: long enough that the controller's
		// first-reconcile-no-burn rule (anchor on version-1
		// creation_time) doesn't trip immediately on creation; short
		// enough that the test doesn't drag the suite out. The
		// Eventually(s) windows below build in slack on top.
		k := &securityv1alpha1.KMSKey{
			ObjectMeta: metav1.ObjectMeta{Name: keyName, Namespace: ns},
			Spec: securityv1alpha1.KMSKeySpec{
				Purpose:        securityv1alpha1.KMSPurposeApplication,
				Algorithm:      securityv1alpha1.KMSAlgorithmAES256GCM96,
				DeletionPolicy: securityv1alpha1.KMSDeletionRetain,
				Rotation: securityv1alpha1.KMSRotation{
					Enabled:  true,
					Interval: "45s",
				},
			},
		}
		Expect(k8sClient.Create(ctx, k)).To(Succeed())

		By("Waiting for KMSKey to reach Ready=True + status.currentVersion=1 (≤90s)")
		Eventually(func() (int, error) {
			got := &securityv1alpha1.KMSKey{}
			if err := k8sClient.Get(ctx, keyKey, got); err != nil {
				return 0, err
			}
			ready := false
			for _, c := range got.Status.Conditions {
				if c.Type == securityv1alpha1.ConditionReady && c.Status == metav1.ConditionTrue {
					ready = true
					break
				}
			}
			if !ready {
				return 0, nil
			}
			return got.Status.CurrentVersion, nil
		}, 90*time.Second, 3*time.Second).Should(Equal(1),
			"controller must reach Ready=True with v1 within 90s; check the manager logs for OpenBao policy/auth errors")

		// Per-call contexts via kmsBackendCall — a single test-wide
		// ctxLocal would expire during the ~45-120s rotation wait
		// below, then every post-rotation Backend.Do would fail with
		// context.DeadlineExceeded BEFORE reaching the assertion.
		// Reviewer-flagged on the first cut.

		// --- 2. encrypt under v1 -------------------------------------
		plaintextA := "kube-dc-e10-alpha-v1"
		By(fmt.Sprintf("Encrypting plaintext_A=%q via the developer's OpenBao OIDC token", plaintextA))
		var encA struct {
			Ciphertext string `json:"ciphertext"`
			KeyName    string `json:"keyName"`
		}
		status, raw, err := kmsBackendCall(
			"POST",
			fmt.Sprintf("/api/kms/%s/%s/encrypt", ns, keyName),
			map[string]any{"plaintext": plaintextA},
			&encA,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "encrypt failed: %s", raw)
		Expect(encA.Ciphertext).To(HavePrefix("vault:v1:"),
			"first encrypt should land on key version 1; got %q", encA.Ciphertext)
		ciphertextA_v1 := encA.Ciphertext

		// --- 3. decrypt → matches ------------------------------------
		By("Decrypting ciphertext_A_v1 → expect plaintext_A back")
		var dec struct {
			Plaintext string `json:"plaintext"`
			KeyName   string `json:"keyName"`
		}
		status, raw, err = kmsBackendCall(
			"POST",
			fmt.Sprintf("/api/kms/%s/%s/decrypt", ns, keyName),
			map[string]any{"ciphertext": ciphertextA_v1},
			&dec,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "decrypt failed: %s", raw)
		Expect(dec.Plaintext).To(Equal(plaintextA),
			"first-version round-trip must match exactly")

		// --- 4. controller-driven rotation → v2 ----------------------
		By("Waiting for the controller's first auto-rotation tick → status.currentVersion >= 2 (≤120s)")
		// First-reconcile-no-burn rule: rotation fires `interval` after
		// LastRotatedTime, which the controller stamps from version-1
		// creation_time on a brand new key (or from r.clock() as a
		// fallback). With interval=45s the next rotation tick should
		// happen within ~45-60s of creation; we allow a generous 120s
		// for any reconcile-queue slack.
		Eventually(func() (int, error) {
			got := &securityv1alpha1.KMSKey{}
			if err := k8sClient.Get(ctx, keyKey, got); err != nil {
				return 0, err
			}
			return got.Status.CurrentVersion, nil
		}, 120*time.Second, 5*time.Second).Should(BeNumerically(">=", 2),
			"controller-driven rotation should bump currentVersion within 2 minutes of creation")

		// Pull the latest version number for the next assertion.
		got := &securityv1alpha1.KMSKey{}
		Expect(k8sClient.Get(ctx, keyKey, got)).To(Succeed())
		latestVer := got.Status.CurrentVersion
		Expect(latestVer).To(BeNumerically(">=", 2))

		// --- 5. encrypt under the new version ------------------------
		plaintextB := "kube-dc-e10-beta-v2"
		By(fmt.Sprintf("Encrypting plaintext_B=%q (should land on the new key version %d)", plaintextB, latestVer))
		var encB struct {
			Ciphertext string `json:"ciphertext"`
		}
		status, raw, err = kmsBackendCall(
			"POST",
			fmt.Sprintf("/api/kms/%s/%s/encrypt", ns, keyName),
			map[string]any{"plaintext": plaintextB},
			&encB,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "encrypt-after-rotation failed: %s", raw)
		Expect(encB.Ciphertext).To(HavePrefix(fmt.Sprintf("vault:v%d:", latestVer)),
			"encrypt-after-rotation should land on the latest version; got %q", encB.Ciphertext)
		ciphertextB_vN := encB.Ciphertext

		// --- 6. decrypt new-version ciphertext -----------------------
		By(fmt.Sprintf("Decrypting ciphertext_B (v%d) → expect plaintext_B back", latestVer))
		status, raw, err = kmsBackendCall(
			"POST",
			fmt.Sprintf("/api/kms/%s/%s/decrypt", ns, keyName),
			map[string]any{"ciphertext": ciphertextB_vN},
			&dec,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "decrypt-new-version failed: %s", raw)
		Expect(dec.Plaintext).To(Equal(plaintextB),
			"new-version round-trip must match exactly")

		// --- 7. decrypt old-version ciphertext (the M3 exit-gate claim)
		By("Decrypting ciphertext_A (v1) AFTER rotation → still expect plaintext_A " +
			"(OpenBao Transit retains all versions; M3 exit-gate claim)")
		status, raw, err = kmsBackendCall(
			"POST",
			fmt.Sprintf("/api/kms/%s/%s/decrypt", ns, keyName),
			map[string]any{"ciphertext": ciphertextA_v1},
			&dec,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "decrypt-old-version failed: %s", raw)
		Expect(dec.Plaintext).To(Equal(plaintextA),
			"OLD ciphertext (v1) must still decrypt after rotation — the M3 exit-gate claim. "+
				"If this trips, check status.minDecryptionVersion didn't advance past 1 unexpectedly.")

		Logf("E10: full round-trip green (v1 encrypt → decrypt; rotate to v%d; v%d encrypt → decrypt; v1 cipher still decryptable)",
			latestVer, latestVer)
	})

	// Secondary spec: the 64 KiB plaintext cap is contractual — pin it
	// so a future loosening of the body-parser limit doesn't silently
	// extend the documented boundary.
	It("rejects plaintext that exceeds the 64 KiB cap with HTTP 413", func() {
		kmsRequireDevJWT()
		Logf("BEGIN: E10-cap test for %s/%s", ns, keyName)

		By("Creating a KMSKey (no rotation needed for the cap test)")
		k := &securityv1alpha1.KMSKey{
			ObjectMeta: metav1.ObjectMeta{Name: keyName, Namespace: ns},
			Spec: securityv1alpha1.KMSKeySpec{
				Purpose:        securityv1alpha1.KMSPurposeApplication,
				Algorithm:      securityv1alpha1.KMSAlgorithmAES256GCM96,
				DeletionPolicy: securityv1alpha1.KMSDeletionRetain,
			},
		}
		Expect(k8sClient.Create(ctx, k)).To(Succeed())

		By("Waiting for Ready=True + status.currentVersion=1 (≤90s)")
		Eventually(func() (int, error) {
			got := &securityv1alpha1.KMSKey{}
			if err := k8sClient.Get(ctx, keyKey, got); err != nil {
				return 0, err
			}
			return got.Status.CurrentVersion, nil
		}, 90*time.Second, 3*time.Second).Should(Equal(1))

		// 64 KiB + 1 byte → just over the cap. The backend's
		// MAX_PLAINTEXT_BYTES check decodes the JSON-string utf-8
		// bytes, which is what we count here.
		over := make([]byte, 64*1024+1)
		for i := range over {
			over[i] = 'a'
		}

		By("POST /encrypt with a 64KiB+1 plaintext → expect HTTP 413")
		status, raw, err := kmsBackendCall(
			"POST",
			fmt.Sprintf("/api/kms/%s/%s/encrypt", ns, keyName),
			map[string]any{"plaintext": string(over)},
			nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(413),
			"server-side cap violation must surface as 413, not 400 or 500; body: %s", raw)
		Expect(raw).To(ContainSubstring("64 KiB"),
			"413 body should mention the 64 KiB cap for an actionable error; got: %s", raw)
	})

	// Sanity spec: encrypt + decrypt with an explicit context. Pin the
	// "matching context required" contract because the UI surfaces it
	// as an optional field — easy to forget on the decrypt side.
	It("matches plaintext when encrypt + decrypt share the same context", func() {
		kmsRequireDevJWT()
		Logf("BEGIN: E10-context test for %s/%s", ns, keyName)

		k := &securityv1alpha1.KMSKey{
			ObjectMeta: metav1.ObjectMeta{Name: keyName, Namespace: ns},
			Spec: securityv1alpha1.KMSKeySpec{
				Purpose:        securityv1alpha1.KMSPurposeApplication,
				Algorithm:      securityv1alpha1.KMSAlgorithmAES256GCM96,
				DeletionPolicy: securityv1alpha1.KMSDeletionRetain,
			},
		}
		Expect(k8sClient.Create(ctx, k)).To(Succeed())

		By("Waiting for Ready=True + v1 (≤90s)")
		Eventually(func() (int, error) {
			got := &securityv1alpha1.KMSKey{}
			if err := k8sClient.Get(ctx, keyKey, got); err != nil {
				return 0, err
			}
			return got.Status.CurrentVersion, nil
		}, 90*time.Second, 3*time.Second).Should(Equal(1))

		// OpenBao context must be base64. Use a stable tenant-id-like
		// value so the assertion is reproducible.
		context64 := base64.StdEncoding.EncodeToString([]byte("kube-dc-e10-ctx"))
		plaintext := "context-bound-payload"

		By("Encrypt with context")
		var enc struct {
			Ciphertext string `json:"ciphertext"`
		}
		status, raw, err := kmsBackendCall(
			"POST",
			fmt.Sprintf("/api/kms/%s/%s/encrypt", ns, keyName),
			map[string]any{"plaintext": plaintext, "context": context64},
			&enc,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "encrypt-with-context: %s", raw)
		Expect(enc.Ciphertext).To(HavePrefix("vault:v1:"))

		By("Decrypt with the same context → matches")
		var dec struct {
			Plaintext string `json:"plaintext"`
		}
		status, raw, err = kmsBackendCall(
			"POST",
			fmt.Sprintf("/api/kms/%s/%s/decrypt", ns, keyName),
			map[string]any{"ciphertext": enc.Ciphertext, "context": context64},
			&dec,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200), "decrypt-with-context: %s", raw)
		Expect(dec.Plaintext).To(Equal(plaintext))

		// NOTE: an earlier version of this spec asserted that
		// decrypt-WITHOUT-context returns non-200. That assertion
		// was incorrect: OpenBao Transit only BINDS ciphertext to
		// context when the key was created with `derived: true`.
		// kube-dc KMSKeys are non-derived by default (the CRD
		// doesn't expose `derived` in Phase 1 — see dev-scope §1.4),
		// so context is informational but NOT cryptographically
		// load-bearing for the current key shape. The positive
		// round-trip (encrypt+context → decrypt+context → matches)
		// above is the meaningful end-to-end check on Phase-1 keys.
		// Reintroduce the negative assertion if/when the API gains
		// `spec.derived: true` — until then it would fail
		// spuriously against any real OpenBao deployment.
	})
})
