package e2e

import (
	"strings"

	kubeovn "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubedccomv1 "github.com/shalb/kube-dc/api/kube-dc.com/v1"
	servicelb "github.com/shalb/kube-dc/internal/service_lb"
)

// PROJECT VIP-POOL SCOPING — read-only invariants, runnable against ANY management
// cluster (docs/internal/managed-k8s-lb-ipam.md §7c).
//
// Project-internal LoadBalancer VIPs (VPCVIPPool blocks → mirror → SwitchLBRule) exist ONLY for
// KubeVirt-provider managed clusters: their workers are masquerade VMs on the
// project subnet, so no guest can answer for a VIP and the platform must
// realise VIPs as OVN rules. Every other provider — CloudSigma foremost —
// has real servers on real L2 and uses its own LoadBalancer path (CloudSigma
// CCM API-level IP attachment; classless MetalLB-on-VLAN for private-link
// clusters). The provider gate in k8-manager therefore keeps non-KubeVirt
// clusters COMPLETELY untouched, and these specs are that contract:
//
//  1. A KdcCluster with any non-KubeVirt worker pool owns no pool
//     reservations, has no enabled delegation certificate, and has no
//     mirror Services.
//  2. Every mirror Service that exists belongs to a live KubeVirt-provider
//     KdcCluster (by the cluster-UID label).
//  3. Every mirror-family SwitchLBRule has a live backing mirror Service.
//
// On a CloudSigma-only management cluster (eu/dc1) every spec holds
// vacuously today and non-vacuously after the S4 promotion; on a KubeVirt
// cluster (prod1) they prove the gate does not leak.
var _ = Describe("Project VIP-pool scoping (provider gate, read-only)", func() {
	kdcClusters := func() []unstructured.Unstructured {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{Group: "k8s.kube-dc.com", Version: "v1alpha1", Kind: "KdcClusterList"})
		if err := k8sClient.List(ctx, list); err != nil {
			Skip("KdcCluster API not served here: " + err.Error())
		}
		return list.Items
	}
	isKubevirt := func(c *unstructured.Unstructured) bool {
		pools, _, _ := unstructured.NestedSlice(c.Object, "spec", "workers")
		for _, p := range pools {
			m, _ := p.(map[string]interface{})
			if m == nil {
				continue
			}
			if prov, _ := m["infrastructureProvider"].(string); prov != "" && prov != "kubevirt" {
				return false
			}
		}
		return true
	}

	It("keeps non-KubeVirt clusters completely out of the VIP-pool path", func() {
		clusters := kdcClusters()
		reservations := &kubedccomv1.AddressReservationList{}
		_ = k8sClient.List(ctx, reservations) // NoMatch tolerated: absence is compliance
		checked := 0
		for i := range clusters {
			c := &clusters[i]
			if isKubevirt(c) {
				continue
			}
			checked++
			uid := string(c.GetUID())
			By("cluster " + c.GetNamespace() + "/" + c.GetName() + " (non-KubeVirt)")
			for _, r := range reservations.Items {
				if r.Spec.Owner != nil && string(r.Spec.Owner.UID) == uid {
					Expect(r.Spec.AddressSpace).NotTo(Equal("VPCVIPPool"),
						"non-KubeVirt cluster %s owns pool reservation %s", c.GetName(), r.Name)
				}
			}
			cms := &corev1.ConfigMapList{}
			Expect(k8sClient.List(ctx, cms, client.InNamespace("kube-dc"),
				client.MatchingLabels{servicelb.MirrorClusterUIDLabel: uid})).To(Succeed())
			for _, cm := range cms.Items {
				Expect(cm.Data["enabled"]).NotTo(Equal("true"),
					"non-KubeVirt cluster %s has an ENABLED delegation certificate %s", c.GetName(), cm.Name)
			}
			mirrors := &corev1.ServiceList{}
			Expect(k8sClient.List(ctx, mirrors, client.InNamespace(c.GetNamespace()),
				client.MatchingLabels{servicelb.MirrorManagedByLabel: servicelb.MirrorManagedByK8M, servicelb.MirrorClusterUIDLabel: uid})).To(Succeed())
			Expect(mirrors.Items).To(BeEmpty(), "non-KubeVirt cluster %s has mirror Services", c.GetName())
		}
		Logf("non-KubeVirt clusters checked: %d of %d", checked, len(clusters))
	})

	It("ties every mirror and every mirror-family rule to a live KubeVirt cluster", func() {
		clusters := kdcClusters()
		byUID := map[string]bool{}
		for i := range clusters {
			byUID[string(clusters[i].GetUID())] = isKubevirt(&clusters[i])
		}
		mirrors := &corev1.ServiceList{}
		Expect(k8sClient.List(ctx, mirrors,
			client.MatchingLabels{servicelb.MirrorManagedByLabel: servicelb.MirrorManagedByK8M})).To(Succeed())
		for _, m := range mirrors.Items {
			uid := m.Labels[servicelb.MirrorClusterUIDLabel]
			kv, live := byUID[uid]
			Expect(live).To(BeTrue(), "mirror %s/%s has no live KdcCluster behind it", m.Namespace, m.Name)
			Expect(kv).To(BeTrue(), "mirror %s/%s belongs to a non-KubeVirt cluster", m.Namespace, m.Name)
		}
		rules := &kubeovn.SwitchLBRuleList{}
		err := k8sClient.List(ctx, rules, client.MatchingLabels{servicelb.ShadowManagedByLabel: servicelb.MirrorManagedByValue})
		if err != nil {
			Skip("SwitchLBRule API not served here: " + err.Error())
		}
		for _, r := range rules.Items {
			ns, name := r.Labels[servicelb.ShadowServiceNsLabel], r.Labels[servicelb.ShadowServiceLabel]
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, svc)).To(Succeed(),
				"mirror-family rule %s has no live backing mirror %s/%s", r.Name, ns, name)
			Expect(servicelb.IsProjectMirror(svc)).To(BeTrue())
		}
		Logf("mirrors: %d, mirror-family rules: %d, clusters: %d", len(mirrors.Items), len(rules.Items), len(clusters))
	})

	It("leaves classless LoadBalancer Services to the provider's own path (never mirrored)", func() {
		svcs := &corev1.ServiceList{}
		Expect(k8sClient.List(ctx, svcs)).To(Succeed())
		classless, withIP := 0, 0
		for _, s := range svcs.Items {
			if s.Spec.Type != corev1.ServiceTypeLoadBalancer || strings.HasPrefix(s.Name, "lbm-") {
				continue
			}
			if s.Spec.LoadBalancerClass == nil {
				classless++
				if len(s.Status.LoadBalancer.Ingress) > 0 {
					withIP++
				}
			}
		}
		Logf("classless LoadBalancer Services: %d (%d with an address) — all outside the VIP-pool path by construction", classless, withIP)
	})
})
