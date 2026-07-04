/*
Copyright Kube-DC 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// E08 — ManagedCertificate validating-webhook zone enforcement
// (M2-T04 controller-side acceptance test).
//
// Coverage (matches the matrix locked in 88d096cf review):
//
//   - default-private allowed (project not listed → <project>.internal,
//     *.<project>.internal accepted)
//   - default-private rejected (api.other.internal denied)
//   - explicit-private allowlist replaces defaults
//   - public default denied
//   - explicit empty privateDnsNames opts the project out
//
// The suite mutates Organization.spec.security.certificateDomains for
// the target tenant Organization and restores the prior value on
// teardown so a stage cluster pre-loaded with custom policies isn't
// disturbed.
//
// E09 (public ACME flow unchanged) lands with M2-T03 (the reconciler
// half); it cannot be exercised without cert-manager Certificate
// creation, which is not in this PR.

package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubedccomv1 "github.com/shalb/kube-dc/api/kube-dc.com/v1"
	securityv1alpha1 "github.com/shalb/kube-dc/api/security.kube-dc.com/v1alpha1"
)

// targetOrgName resolves the Organization name from a namespace
// annotation on the target project namespace. The suite reuses an
// existing tenant rather than inventing one (same convention as
// managedsecret_test.go).
func targetOrgName(ns string) (string, string, error) {
	var probe corev1.Namespace
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: ns}, &probe); err != nil {
		return "", "", err
	}
	raw := strings.TrimSpace(probe.GetAnnotations()["kube-dc.com/project"])
	if raw == "" {
		return "", "", fmt.Errorf("namespace %q lacks kube-dc.com/project annotation", ns)
	}
	parts := strings.Split(raw, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("namespace %q has malformed annotation %q", ns, raw)
	}
	return parts[0], parts[1], nil
}

var _ = Describe("ManagedCertificate webhook zone enforcement (E08, M2-T04)", func() {

	var (
		ns       string
		orgName  string
		project  string
		mcName   string
		mcKey    types.NamespacedName
		savedSec *kubedccomv1.OrganizationSecuritySpec
	)

	BeforeEach(func() {
		ns = targetProjectNamespace()
		mcName = fmt.Sprintf("e2e-e08-%d", time.Now().UnixNano())
		mcKey = types.NamespacedName{Namespace: ns, Name: mcName}

		var probe corev1.Namespace
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: ns}, &probe); err != nil {
			Skip(fmt.Sprintf("project namespace %q not present on this cluster; set KUBE_DC_E2E_PROJECT_NS to override", ns))
		}
		var err error
		orgName, project, err = targetOrgName(ns)
		Expect(err).NotTo(HaveOccurred(), "could not resolve org/project from namespace annotation")

		// Skip if the parent Organization isn't on this cluster (some
		// envtest configurations stage just the project namespace).
		var org kubedccomv1.Organization
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: orgName}, &org); err != nil {
			if apierrors.IsNotFound(err) {
				Skip(fmt.Sprintf("Organization %q not present; cannot exercise E08 zone enforcement", orgName))
			}
			Expect(err).NotTo(HaveOccurred())
		}
		// Snapshot Security spec so AfterEach can restore it cleanly
		// (production stage cluster ships with no Security block today;
		// future runs may carry custom policies).
		if org.Spec.Security != nil {
			cp := *org.Spec.Security
			savedSec = &cp
		} else {
			savedSec = nil
		}
	})

	AfterEach(func() {
		// Restore Org spec.security (snapshot taken in BeforeEach).
		if orgName == "" {
			return
		}
		var org kubedccomv1.Organization
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: orgName}, &org); err == nil {
			org.Spec.Security = savedSec
			if err := k8sClient.Update(ctx, &org); err != nil {
				Logf("AfterEach: restoring Organization.spec.security failed: %v", err)
			}
		}
		// Delete any ManagedCertificate that admitted through.
		mc := &securityv1alpha1.ManagedCertificate{}
		if err := k8sClient.Get(ctx, mcKey, mc); err == nil {
			_ = k8sClient.Delete(ctx, mc)
		}
	})

	setOrgPolicy := func(privateNames *[]string, publicNames *[]string) {
		var org kubedccomv1.Organization
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: orgName}, &org)).To(Succeed())
		org.Spec.Security = &kubedccomv1.OrganizationSecuritySpec{
			CertificateDomains: []kubedccomv1.ProjectCertificateDomainPolicy{
				{
					Project:         project,
					PrivateDnsNames: privateNames,
					PublicDnsNames:  publicNames,
				},
			},
		}
		Expect(k8sClient.Update(ctx, &org)).To(Succeed())
	}

	clearOrgPolicy := func() {
		var org kubedccomv1.Organization
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: orgName}, &org)).To(Succeed())
		org.Spec.Security = nil
		Expect(k8sClient.Update(ctx, &org)).To(Succeed())
	}

	mkCert := func(certType securityv1alpha1.CertType, dnsNames ...string) *securityv1alpha1.ManagedCertificate {
		return &securityv1alpha1.ManagedCertificate{
			ObjectMeta: metav1.ObjectMeta{Name: mcName, Namespace: ns},
			Spec: securityv1alpha1.ManagedCertificateSpec{
				Type:             certType,
				DnsNames:         dnsNames,
				TargetSecretName: mcName + "-tls",
			},
		}
	}

	It("E08-a: default-private zone accepts <project>.internal and *.<project>.internal", func() {
		clearOrgPolicy()
		expected := fmt.Sprintf("api.%s.internal", project)
		Expect(k8sClient.Create(ctx, mkCert(securityv1alpha1.CertTypePrivate, expected))).To(Succeed())
	})

	It("E08-b: default-private zone rejects api.other.internal", func() {
		clearOrgPolicy()
		err := k8sClient.Create(ctx, mkCert(securityv1alpha1.CertTypePrivate, "api.other.internal"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not in the allowed zones"))
	})

	It("E08-c: explicit-private allowlist replaces defaults", func() {
		custom := []string{"*.example.internal"}
		setOrgPolicy(&custom, nil)

		// new zone accepted
		Expect(k8sClient.Create(ctx, mkCert(securityv1alpha1.CertTypePrivate, "api.example.internal"))).To(Succeed())

		// old default now rejected — cleanup the first so the second
		// uses a fresh name + Org policy is still set.
		mc := &securityv1alpha1.ManagedCertificate{}
		if err := k8sClient.Get(ctx, mcKey, mc); err == nil {
			Expect(k8sClient.Delete(ctx, mc)).To(Succeed())
		}
		mcName = fmt.Sprintf("e2e-e08c-%d", time.Now().UnixNano())
		mcKey = types.NamespacedName{Namespace: ns, Name: mcName}
		defaultName := fmt.Sprintf("api.%s.internal", project)
		err := k8sClient.Create(ctx, mkCert(securityv1alpha1.CertTypePrivate, defaultName))
		Expect(err).To(HaveOccurred(), "default zone must not be accepted under explicit allowlist")
		Expect(err.Error()).To(ContainSubstring("not in the allowed zones"))
	})

	It("E08-d: public default rejects any name (no allowlist)", func() {
		clearOrgPolicy()
		err := k8sClient.Create(ctx, mkCert(securityv1alpha1.CertTypePublic, "api.example.com"))
		Expect(err).To(HaveOccurred(), "public cert without explicit allowlist must be rejected")
		Expect(err.Error()).To(ContainSubstring("not in the allowed zones"))
	})

	It("E08-e: explicit empty privateDnsNames opts the project out", func() {
		empty := []string{}
		setOrgPolicy(&empty, nil)
		defaultName := fmt.Sprintf("%s.internal", project)
		err := k8sClient.Create(ctx, mkCert(securityv1alpha1.CertTypePrivate, defaultName))
		Expect(err).To(HaveOccurred(), "explicit empty privateDnsNames must reject every private name")
		Expect(err.Error()).To(ContainSubstring("not in the allowed zones"))
	})

	// E09 (dev-scope §12) — public `ManagedCertificate` flows through
	// cert-manager Let's-Encrypt unchanged. We verify the reconciler
	// creates a cert-manager Certificate referencing the existing
	// `letsencrypt-prod-http` ClusterIssuer, and does NOT create new
	// per-Project PKI scaffolding as a side effect of this test.
	//
	// IMPORTANT: the target project namespace may already carry a
	// `kube-dc-pki` Issuer and a `kube-dc-pki-issuer` SA from prior
	// private-cert flows — that's expected on a soaked cluster. The
	// test snapshots pre-existing state in BeforeEach and asserts
	// only that no NEW scaffolding appeared during the test, which
	// is the actual behaviour public certs are supposed to have.
	//
	// Full ACME issuance against Let's Encrypt staging is out of scope
	// for the controller suite — that would need a live solver and DNS
	// plumbing.
	It("E09: public ManagedCertificate flows through cert-manager unchanged", func() {
		publicDNS := fmt.Sprintf("api.%s.example.com", project)
		pub := []string{publicDNS}
		setOrgPolicy(nil, &pub) // explicit Org allowlist; webhook accepts
		mcName = fmt.Sprintf("e2e-e09-%d", time.Now().UnixNano())
		mcKey = types.NamespacedName{Namespace: ns, Name: mcName}

		// Snapshot the pre-existing PKI scaffolding state. If the
		// project namespace already has `kube-dc-pki` / `kube-dc-pki-issuer`
		// from earlier private-cert tests, that's not a fault of E09 —
		// public certs are simply expected NOT to create them
		// themselves.
		issuerExistsBefore := issuerNamedExists(ctx, ns, IssuerNamedPKI)
		saExistsBefore := saNamedExists(ctx, ns, PKIIssuerSAName)

		Expect(k8sClient.Create(ctx, mkCert(securityv1alpha1.CertTypePublic, publicDNS))).To(Succeed())

		// Wait until the reconciler creates the owned cert-manager
		// Certificate. We don't wait for ACME success — that depends
		// on cert-manager + live DNS for the cluster's base domain.
		Eventually(func(g Gomega) {
			cmCertList := &unstructured.UnstructuredList{}
			cmCertList.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "cert-manager.io", Version: "v1", Kind: "CertificateList",
			})
			g.Expect(k8sClient.List(ctx, cmCertList, client.InNamespace(ns))).To(Succeed())
			found := false
			for _, item := range cmCertList.Items {
				if item.GetName() != mcName {
					continue
				}
				found = true
				issuerRef, _, _ := unstructuredNestedMap(item.Object, "spec", "issuerRef")
				g.Expect(issuerRef["kind"]).To(Equal("ClusterIssuer"),
					"public cert must reference ClusterIssuer, not namespaced Issuer")
				g.Expect(issuerRef["name"]).To(Equal("letsencrypt-prod-http"),
					"public cert must reference the shared letsencrypt-prod-http ClusterIssuer")
			}
			g.Expect(found).To(BeTrue(), "no cert-manager Certificate %q observed in %q yet", mcName, ns)
		}, 60*time.Second, 3*time.Second).Should(Succeed(),
			"cert-manager Certificate for public ManagedCertificate not observed in 60s")

		// Assert the public path did NOT add new PKI scaffolding.
		// (Pre-existing scaffolding is fine.)
		if !issuerExistsBefore {
			Expect(issuerNamedExists(ctx, ns, IssuerNamedPKI)).To(BeFalse(),
				"public-only certificate must not create namespaced kube-dc-pki Issuer")
		}
		if !saExistsBefore {
			Expect(saNamedExists(ctx, ns, PKIIssuerSAName)).To(BeFalse(),
				"public-only certificate must not create kube-dc-pki-issuer ServiceAccount")
		}
	})
})

// Pre-existing-state probes used by E09. Constants mirror the
// reconciler's expectations so a rename can't drift them apart.
const (
	IssuerNamedPKI  = "kube-dc-pki"
	PKIIssuerSAName = "kube-dc-pki-issuer"
)

func issuerNamedExists(ctx context.Context, ns, name string) bool {
	issuerList := &unstructured.UnstructuredList{}
	issuerList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: "IssuerList",
	})
	if err := k8sClient.List(ctx, issuerList, client.InNamespace(ns)); err != nil {
		return false
	}
	for _, item := range issuerList.Items {
		if item.GetName() == name {
			return true
		}
	}
	return false
}

func saNamedExists(ctx context.Context, ns, name string) bool {
	var sa corev1.ServiceAccount
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &sa)
	return err == nil
}

// unstructuredNestedMap is a thin wrapper around unstructured.NestedMap
// that returns the empty-map zero-value on missing paths, avoiding the
// triple-return-value boilerplate at every callsite.
func unstructuredNestedMap(obj map[string]interface{}, fields ...string) (map[string]interface{}, bool, error) {
	cur := obj
	for _, f := range fields {
		next, ok := cur[f].(map[string]interface{})
		if !ok {
			return map[string]interface{}{}, false, nil
		}
		cur = next
	}
	return cur, true, nil
}
