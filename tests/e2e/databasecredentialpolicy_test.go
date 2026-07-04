/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// E2E E11 — DatabaseCredentialPolicy `static-rotated` rotates the app
// user and updates the projected K8s Secret. dev-scope §12 + Phase 4
// exit gate (§13 "E11 green; rolling rotation does not interrupt
// live apps").
//
// What this spec actually exercises today:
//   - Create a DBCP referencing an existing Ready KdcDatabase.
//   - Wait for Ready=True/StaticRotated + the projected Secret with
//     the canonical 7-key shape (username/password/host/port/
//     database/engine/dsn) populated.
//   - Sleep past OpenBao's rotation_period (1m + slack). The
//     reconciler's rotation-aware requeue (controller.go:
//     rotationAwareRequeue) should fire within seconds of the
//     server-side rotation and re-project the new password into the
//     K8s Secret without any manual annotation patch. This is the
//     regression guard for the 2026-05-25 staleness diagnosis (see
//     docs/prd/openbao-static-role-auto-rotation-bug.md §11): the
//     previous version of this spec patched
//     dbcp.kube-dc.com/reconcile-requested-at to force a re-read,
//     which masked the 5m staleness window. If this assertion times
//     out, rotation-aware requeue has regressed.
//   - Verify the Secret password actually changed (v1 != v2) AND
//     status.lastRotatedTime advanced past the initial value (the
//     status mirror is the operator-facing signal; both must move).
//   - On AfterEach, delete the DBCP and verify the M4-T02 password-
//     drift fix: the engine's `<db>-app` Secret gets sync'd to
//     OpenBao's last-known password BEFORE the static-role is
//     deleted, so a future re-creation of the same DBCP doesn't trip
//     SQLSTATE 28P01.
//
// Rotation interval choice (1m, not less):
//   Both the DBCP controller and the OpenBao client enforce a hard
//   1-minute floor on rotation_period (see
//   internal/controller/security/databasecredentialpolicy_controller.go
//   "d >= time.Minute" and internal/openbao/database.go
//   "RotationPeriod < time.Minute"). Anything shorter is silently
//   coerced to the 30d default by the controller, which would make
//   the rotation step here a no-op against a tenant's actual
//   secret. 1m is the smallest value that exercises the real path.
//
// What this spec deliberately DOES NOT exercise yet:
//   - "Without dropping live connections (rolling strategy)" half of
//     the dev-scope E11 acceptance criterion. Rolling vs immediate
//     rotation strategy is a CRD field today with no controller-side
//     implementation — the controller passes spec.rotation.interval
//     as `rotation_period` on the OpenBao static-role and lets
//     OpenBao handle it; OpenBao's static-role rotation in fact
//     doesn't tear down existing connections (`ALTER USER ...
//     PASSWORD` is non-disruptive in PostgreSQL), but that's a
//     property of the engine, not something this spec proves.
//     Adding a live-connection assertion requires running a
//     workload pod connecting to PG continuously and is queued as
//     a Phase-2 follow-up once the rolling-vs-immediate semantics
//     have actual code behind them.
//
// Cluster preconditions (operator runbook):
//   - OpenBao deployed + initialised
//   - hack/openbao-setup-controller-auth.sh run on this cluster
//     (v0.3.55+ for the database/* policy paths)
//   - At least one Ready KdcDatabase with
//     status.openBaoEngineConfigured=true in the target project
//     namespace. The spec lists all KdcDatabases, picks the first
//     match, and Skip()s gracefully if none. db-manager v0.1.3+
//     stamps openBaoEngineConfigured; without it the spec can't
//     proceed because the DBCP reconciler will park on
//     DatabaseEngineUnconfigured.
//
// Skip cascade:
//   - Target project namespace missing            → Skip
//   - No Ready KdcDatabase with engine configured → Skip
//   - DBCP reconciler not reaching Ready=True     → fail with hint
//   - Rotation didn't land in ≤120s               → fail with hint

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	securityv1alpha1 "github.com/shalb/kube-dc/api/security.kube-dc.com/v1alpha1"
)

// kdcDatabaseGVK mirrors the constant in
// internal/controller/security/databasecredentialpolicy_controller.go
// — db-manager's KdcDatabase CRD lives in a separate Go module so we
// reference it via unstructured.
var kdcDatabaseGVK = schema.GroupVersionKind{
	Group:   "db.kube-dc.com",
	Version: "v1alpha1",
	Kind:    "KdcDatabase",
}

// dbcpFindReadyKdcDatabase scans the project namespace for a Ready
// KdcDatabase that db-manager has already registered with OpenBao
// (status.openBaoEngineConfigured=true). Picks the first eligible.
// Returns "" when none — the caller Skip()s.
//
// Prefers PostgreSQL over MariaDB only because the chart's mariadb
// fixtures on the test clusters are less stable; either engine works
// with the M4-T02 reconciler.
func dbcpFindReadyKdcDatabase(ctxLocal context.Context, ns string) (name, engine string) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group: kdcDatabaseGVK.Group, Version: kdcDatabaseGVK.Version, Kind: "KdcDatabaseList",
	})
	if err := k8sClient.List(ctxLocal, list, client.InNamespace(ns)); err != nil {
		Logf("dbcpFindReadyKdcDatabase: list failed: %v", err)
		return "", ""
	}
	type candidate struct{ name, engine string }
	var pg, mariadb []candidate
	for i := range list.Items {
		db := &list.Items[i]
		configured, _, _ := unstructured.NestedBool(db.Object, "status", "openBaoEngineConfigured")
		if !configured {
			continue
		}
		// Require phase=Ready (or absent — older db-manager versions
		// don't stamp it). The .status.conditions[type=Ready] check is
		// the strictest signal but not all engine implementations
		// surface it consistently, so we fall back to spec.engine.
		phase, _, _ := unstructured.NestedString(db.Object, "status", "phase")
		if phase != "" && phase != "Ready" {
			continue
		}
		eng, _, _ := unstructured.NestedString(db.Object, "spec", "engine")
		switch eng {
		case "postgresql":
			pg = append(pg, candidate{db.GetName(), eng})
		case "mariadb":
			mariadb = append(mariadb, candidate{db.GetName(), eng})
		}
	}
	if len(pg) > 0 {
		return pg[0].name, pg[0].engine
	}
	if len(mariadb) > 0 {
		return mariadb[0].name, mariadb[0].engine
	}
	return "", ""
}

// dbcpProjectedSecretPassword reads the projected target Secret and
// returns the password key (the canonical "username/password/host/
// port/database/engine/dsn" projection — see M4-T02
// applyTargetSecret). Returns ("", false) when the Secret doesn't
// exist yet or has no password field; callers Eventually() against
// this.
func dbcpProjectedSecretPassword(ctxLocal context.Context, ns, name string) (string, bool) {
	sec := &corev1.Secret{}
	if err := k8sClient.Get(ctxLocal, types.NamespacedName{Name: name, Namespace: ns}, sec); err != nil {
		return "", false
	}
	pw, ok := sec.Data["password"]
	if !ok || len(pw) == 0 {
		return "", false
	}
	return string(pw), true
}

var _ = Describe("E11: DatabaseCredentialPolicy static-rotated rotation (M4-T06 / dev-scope §12)", func() {

	var (
		ns        string
		dbcpName  string
		dbcpKey   types.NamespacedName
		kdcDBName string
		engine    string
	)

	BeforeEach(func() {
		ns = targetProjectNamespace()
		dbcpName = fmt.Sprintf("e2e-dbcp-%d", time.Now().UnixNano())
		dbcpKey = types.NamespacedName{Namespace: ns, Name: dbcpName}

		var probe corev1.Namespace
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: ns}, &probe); err != nil {
			Skip(fmt.Sprintf("project namespace %q not present on this cluster; set KUBE_DC_E2E_PROJECT_NS to override", ns))
		}

		kdcDBName, engine = dbcpFindReadyKdcDatabase(ctx, ns)
		if kdcDBName == "" {
			Skip(fmt.Sprintf(
				"no Ready KdcDatabase with status.openBaoEngineConfigured=true in %q; "+
					"E11 needs an existing engine to bind a DBCP to. Provision one with the create-database skill / kubectl apply, "+
					"wait for `kubectl -n %s get kdcdatabase` to show Ready, then re-run the spec.", ns, ns))
		}
		Logf("E11: binding DBCP %s/%s to KdcDatabase %s (engine=%s)", ns, dbcpName, kdcDBName, engine)
	})

	AfterEach(func() {
		if dbcpName == "" {
			return
		}
		p := &securityv1alpha1.DatabaseCredentialPolicy{}
		if err := k8sClient.Get(ctx, dbcpKey, p); err == nil {
			By("AfterEach: deleting test DBCP " + dbcpName)
			Expect(k8sClient.Delete(ctx, p)).To(Succeed())
			// Wait for finalizer to clear so AfterEach doesn't leave a
			// stuck CR behind on suite failure.
			Eventually(func() bool {
				got := &securityv1alpha1.DatabaseCredentialPolicy{}
				err := k8sClient.Get(ctx, dbcpKey, got)
				return err != nil // expect NotFound
			}, 90*time.Second, 3*time.Second).Should(BeTrue(),
				"DBCP finalizer should clear within 90s")
		}
	})

	// Core E11 spec. Phases:
	//   1. create DBCP w/ minimum rotation interval (1m — the
	//      controller floor; smaller values silently coerce to 30d)
	//   2. wait for Ready=True/StaticRotated + projected Secret w/
	//      canonical 7-key shape
	//   3. capture v1 password + initial status.lastRotatedTime
	//   4. eventually-wait (≤180s) for the projected Secret password
	//      to change WITHOUT a manual reconcile nudge — proves
	//      rotation-aware requeue is wiring OpenBao's server-side
	//      rotation back into the K8s Secret on its own
	//   5. assert status.lastRotatedTime advanced past
	//      initialLastRotatedTime
	It("creates the policy, projects the canonical Secret, and picks up an OpenBao rotation", func() {
		Logf("BEGIN: E11 lifecycle for %s/%s on %s", ns, dbcpName, kdcDBName)

		// --- 1. create ----------------------------------------------------
		By("Creating a DBCP with rotation.interval=1m (controller floor)")
		tru := true
		p := &securityv1alpha1.DatabaseCredentialPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: dbcpName, Namespace: ns},
			Spec: securityv1alpha1.DatabaseCredentialPolicySpec{
				DatabaseRef: corev1.LocalObjectReference{Name: kdcDBName},
				Mode:        securityv1alpha1.DBCredentialModeStaticRotated,
				// Username defaults to "app" via the webhook; let the
				// default fire so we exercise the canonical path.
				Rotation: securityv1alpha1.DBCredentialRotation{
					Interval: securityv1alpha1.KubeDCDuration("1m"),
				},
				Sync: securityv1alpha1.DBCredentialSync{Enabled: &tru},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())

		// --- 2. wait for Ready + projected Secret -------------------------
		By("Waiting for DBCP Ready=True/StaticRotated + projected Secret (≤120s)")
		// The first reconcile chain is:
		//   ensure static-role  (~OpenBao 1-2s)
		//   wait for static-creds first availability  (~OpenBao 1-2s)
		//   write target Secret
		//   stamp Ready=True/StaticRotated.
		// 120s is generous slack for cluster-loaded reconcile queues.
		Eventually(func() (string, error) {
			got := &securityv1alpha1.DatabaseCredentialPolicy{}
			if err := k8sClient.Get(ctx, dbcpKey, got); err != nil {
				return "", err
			}
			for _, c := range got.Status.Conditions {
				if c.Type == securityv1alpha1.ConditionReady {
					if c.Status == metav1.ConditionTrue {
						return c.Reason, nil
					}
					return "Ready=" + string(c.Status) + "/" + c.Reason, nil
				}
			}
			return "", nil
		}, 120*time.Second, 3*time.Second).Should(Equal("StaticRotated"),
			"controller must reach Ready=True/StaticRotated within 120s; check the kube-dc-manager logs for OpenBao auth / EnsureStaticRole errors")

		// Target Secret is named after the policy when sync.targetSecretName
		// is unset (webhook default). Verify the canonical 7-key shape so
		// any future drift on the projection contract surfaces here.
		By("Verifying the projected target Secret has the canonical key set")
		Eventually(func() bool {
			sec := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: dbcpName, Namespace: ns}, sec); err != nil {
				return false
			}
			for _, k := range []string{"username", "password", "host", "port", "database", "engine", "dsn"} {
				if len(sec.Data[k]) == 0 {
					return false
				}
			}
			return true
		}, 60*time.Second, 2*time.Second).Should(BeTrue(),
			"projected Secret must have all 7 canonical keys populated")

		// --- 3. capture v1 password + initial lastRotatedTime ------------
		v1, ok := dbcpProjectedSecretPassword(ctx, ns, dbcpName)
		Expect(ok).To(BeTrue(), "projected Secret should have a password by now")
		Expect(v1).NotTo(BeEmpty(), "v1 password must not be empty")
		Logf("E11: captured v1 password (len=%d, prefix=%s...)", len(v1), v1[:min(4, len(v1))])

		// status.lastRotatedTime should mirror OpenBao's
		// last_vault_rotation. Capture it now so the post-rotation
		// assertion can prove ADVANCEMENT (not just non-nil — the
		// initial value is already non-nil because the controller
		// stamps it on first ReadStaticCredentials).
		initial := &securityv1alpha1.DatabaseCredentialPolicy{}
		Expect(k8sClient.Get(ctx, dbcpKey, initial)).To(Succeed())
		Expect(initial.Status.LastRotatedTime).NotTo(BeNil(),
			"status.lastRotatedTime must be set by the time Secret is projected")
		v1RotatedAt := initial.Status.LastRotatedTime.Time
		Logf("E11: captured initial lastRotatedTime=%s", v1RotatedAt.Format(time.RFC3339))

		// --- 4. wait for OpenBao's rotation_period to elapse ------------
		// OpenBao rotates server-side at rotation_period (1m — the
		// controller's minimum floor). With the rotation-aware requeue
		// fix (controller.go::rotationAwareRequeue), the reconciler wakes
		// itself within ~10s of OpenBao's next tick, so we should NOT need
		// to patch the reconcile annotation. The eventual Should() below
		// (≤180s) covers 1m rotation_period + 1 OpenBao queue tick +
		// rotation-aware requeue (~70s) + slack.
		//
		// Regression guard: if this eventual times out, rotation-aware
		// requeue has likely regressed back to the 5m ceiling. Verify
		// with `kubectl -n openbao exec ... -- bao read /v1/<org>/
		// database/static-roles/<projectNS>-<db>-<policy>` that
		// rotation_period landed on the role, AND check the manager
		// log for the `requeueAfter` field on the DBCP reconcile event.

		// --- 5. expect Secret password + lastRotatedTime to advance -----
		By("Waiting for rotation-aware requeue to pick up OpenBao's server-side rotation (≤180s, no manual nudge)")
		Eventually(func() (bool, error) {
			pw, ok := dbcpProjectedSecretPassword(ctx, ns, dbcpName)
			if !ok {
				return false, nil
			}
			return pw != v1, nil
		}, 180*time.Second, 3*time.Second).Should(BeTrue(),
			"projected Secret password must change WITHOUT a manual reconcile nudge — rotation-aware requeue (controller.go::rotationAwareRequeue) should fire within ~70s of OpenBao's server-side rotation. If this fails, check the kube-dc-manager log for `requeueAfter` on the DBCP reconcile event; a value of 5m means the requeue has regressed to the ceiling.")

		// status.lastRotatedTime is the operator-facing rotation signal
		// (UIs + CLI `db credentials describe` both read it). Assert it
		// genuinely advanced — non-nil isn't enough since the initial
		// value above was already non-nil.
		By("Asserting status.lastRotatedTime advanced past the initial value")
		got := &securityv1alpha1.DatabaseCredentialPolicy{}
		Expect(k8sClient.Get(ctx, dbcpKey, got)).To(Succeed())
		Expect(got.Status.LastRotatedTime).NotTo(BeNil(),
			"status.lastRotatedTime must still be set after rotation")
		Expect(got.Status.LastRotatedTime.Time.After(v1RotatedAt)).To(BeTrue(),
			"status.lastRotatedTime did not advance (initial=%s, got=%s)",
			v1RotatedAt.Format(time.RFC3339), got.Status.LastRotatedTime.Format(time.RFC3339))
		Logf("E11: rotation observed; lastRotatedTime advanced %s → %s",
			v1RotatedAt.Format(time.RFC3339), got.Status.LastRotatedTime.Format(time.RFC3339))
	})

	// Companion micro-spec: the M4-T02 password-drift fix (commits
	// aa069259/d445501f). On DBCP delete, the engine's per-user Secret
	// (`<dbName>-app` for default username) must be synced to OpenBao's
	// last-known password BEFORE the static-role is removed. Without
	// this fix, a subsequent DBCP create on the same KdcDatabase 28P01s
	// because db-manager re-registers OpenBao with the stale Secret.
	//
	// This spec exercises the lifecycle by creating, briefly verifying
	// the projection, then deleting and re-creating a fresh DBCP. If
	// the drift fix regressed, the second Ready wait will time out
	// with SQLSTATE 28P01 / Reason=OpenBaoUnavailable.
	It("delete+recreate cycle stays healthy (M4-T02 password-drift cleanup regression guard)", func() {
		Logf("BEGIN: E11 delete+recreate regression guard for %s on %s", ns, kdcDBName)

		makeDBCP := func(name string) *securityv1alpha1.DatabaseCredentialPolicy {
			tru := true
			return &securityv1alpha1.DatabaseCredentialPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: securityv1alpha1.DatabaseCredentialPolicySpec{
					DatabaseRef: corev1.LocalObjectReference{Name: kdcDBName},
					Mode:        securityv1alpha1.DBCredentialModeStaticRotated,
					Rotation:    securityv1alpha1.DBCredentialRotation{Interval: securityv1alpha1.KubeDCDuration("24h")},
					Sync:        securityv1alpha1.DBCredentialSync{Enabled: &tru},
				},
			}
		}
		waitReady := func(name string) {
			key := types.NamespacedName{Namespace: ns, Name: name}
			Eventually(func() string {
				got := &securityv1alpha1.DatabaseCredentialPolicy{}
				if err := k8sClient.Get(ctx, key, got); err != nil {
					return ""
				}
				for _, c := range got.Status.Conditions {
					if c.Type == securityv1alpha1.ConditionReady && c.Status == metav1.ConditionTrue {
						return c.Reason
					}
				}
				return ""
			}, 120*time.Second, 3*time.Second).Should(Equal("StaticRotated"),
				"%s/%s must reach Ready=True/StaticRotated within 120s — if this is the second DBCP and we hit SQLSTATE 28P01, the M4-T02 password-drift fix has regressed", ns, name)
		}

		first := makeDBCP(dbcpName)
		By("Phase 1: create the first DBCP and wait for Ready")
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		waitReady(dbcpName)

		By("Phase 1: delete the first DBCP (finalizer should sync the engine Secret password before removing the static-role)")
		Expect(k8sClient.Delete(ctx, first)).To(Succeed())
		Eventually(func() bool {
			got := &securityv1alpha1.DatabaseCredentialPolicy{}
			err := k8sClient.Get(ctx, dbcpKey, got)
			return err != nil
		}, 90*time.Second, 3*time.Second).Should(BeTrue(),
			"first DBCP finalizer should clear within 90s")

		// Nudge db-manager to reconcile the KdcDatabase between phases.
		// The regression we're guarding against (M4-T02 password-drift)
		// fires when db-manager re-registers OpenBao with a stale engine
		// Secret password — i.e. the engine Secret didn't get sync'd to
		// OpenBao's last-known value before DeleteStaticRole. Forcing a
		// db-manager reconcile here ensures the database/config write
		// path runs at least once with whatever password the finalizer
		// just sync'd into the Secret; if the finalizer skipped the
		// sync, the next Ensure-then-verify will 28P01 here rather than
		// silently passing because db-manager hadn't reconciled yet.
		By("Phase 1.5: nudge db-manager to re-run its KdcDatabase reconcile (deterministic regression check)")
		nudgePatch := []byte(fmt.Sprintf(
			`{"metadata":{"annotations":{%q:%q}}}`,
			"db.kube-dc.com/reconcile-requested-at",
			time.Now().UTC().Format(time.RFC3339Nano)))
		nudgeDB := &unstructured.Unstructured{}
		nudgeDB.SetGroupVersionKind(kdcDatabaseGVK)
		nudgeDB.SetNamespace(ns)
		nudgeDB.SetName(kdcDBName)
		if err := k8sClient.Patch(ctx, nudgeDB, client.RawPatch(types.MergePatchType, nudgePatch)); err != nil {
			// db-manager doesn't watch arbitrary annotations, but the
			// metadata change still bumps resourceVersion which CAN
			// trigger a reconcile depending on the controller's watch
			// predicate. If db-manager is configured to ignore this
			// annotation, the test still works because the regular 30s
			// reconcile loop covers the time window we sleep below.
			Logf("E11: KdcDatabase annotation nudge failed (non-fatal, sleep below covers the reconcile window): %v", err)
		}
		// One db-manager reconcile interval (30s controller default per
		// the M4-T01 reconciler) + 5s slack. This wait is what gives
		// the test its deterministic teeth: if password drift regressed,
		// db-manager will Ensure-then-OpenBao-verify with the STALE
		// engine Secret in this window, and the verify will 28P01 —
		// surfacing as Reason=OpenBaoUnavailable on the KdcDatabase's
		// OpenBaoEngineConfigured condition. The Phase-2 Ready wait
		// then catches that.
		time.Sleep(35 * time.Second)

		// Re-use the same dbcpName to make the AfterEach clean up the
		// second one. New time-suffixed name would orphan it if the
		// suite panics before AfterEach.
		dbcpName = fmt.Sprintf("e2e-dbcp-recreate-%d", time.Now().UnixNano())
		dbcpKey = types.NamespacedName{Namespace: ns, Name: dbcpName}

		By("Phase 2: re-create a fresh DBCP on the same KdcDatabase and wait for Ready (regression check)")
		second := makeDBCP(dbcpName)
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		waitReady(dbcpName)
		Logf("E11: delete+recreate cycle stayed healthy — M4-T02 password-drift fix holds")
	})
})

// min keeps the spec's log line bounded when the password is shorter
// than 4 chars (test-fixture-only OpenBao plugins can produce short
// passwords). Inlined here to avoid pulling in golang.org/x/exp/constraints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
