package e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kubedccomv1 "github.com/shalb/kube-dc/api/kube-dc.com/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	hncv1alpha2 "sigs.k8s.io/hierarchical-namespaces/api/v1alpha2"
)

const (
	quotaTestTimeout  = time.Minute * 3
	quotaTestInterval = time.Second * 2

	// Billing annotation keys (must match internal/organization/plan_resources.go)
	annPlanID       = "billing.kube-dc.com/plan-id"
	annSubscription = "billing.kube-dc.com/subscription"
	annAddons       = "billing.kube-dc.com/addons"
	annSuspendedAt  = "billing.kube-dc.com/suspended-at"

	// Resource names created by the controller
	hrqName        = "plan-quota"
	limitRangeName = "default-resource-limits"

	// ConfigMap location
	billingPlansCMName = "billing-plans"
	kubeDcNamespace    = "kube-dc"
)

// billingPlansYAML is a minimal billing-plans ConfigMap for testing.
// Uses small resource values so tests don't require a large cluster.
var billingPlansYAML = `plans:
  test-small:
    displayName: "Test Small"
    description: "E2E test plan - small"
    price: 10
    currency: "EUR"
    objectStorage: 10
    ipv4: 1
    requests:
      cpu: "2"
      memory: "4Gi"
      storage: "20Gi"
    pods: 50
    servicesLB: 10
    burstRatio: 2.0
    limitRange:
      defaultCPU: "250m"
      defaultMemory: "256Mi"
      defaultRequestCPU: "100m"
      defaultRequestMem: "128Mi"
      maxCPU: "2"
      maxMemory: "4Gi"
      minCPU: "10m"
      minMemory: "16Mi"
      maxPodCPU: "2"
      maxPodMemory: "4Gi"
      maxPVCStorage: "20Gi"
      minPVCStorage: "1Gi"
  test-large:
    displayName: "Test Large"
    description: "E2E test plan - large"
    price: 50
    currency: "EUR"
    objectStorage: 50
    ipv4: 2
    requests:
      cpu: "8"
      memory: "16Gi"
      storage: "100Gi"
    pods: 200
    servicesLB: 50
    burstRatio: 1.5
    limitRange:
      defaultCPU: "500m"
      defaultMemory: "512Mi"
      defaultRequestCPU: "250m"
      defaultRequestMem: "256Mi"
      maxCPU: "4"
      maxMemory: "8Gi"
      minCPU: "10m"
      minMemory: "16Mi"
      maxPodCPU: "8"
      maxPodMemory: "16Gi"
      maxPVCStorage: "100Gi"
      minPVCStorage: "1Gi"
suspendedPlan:
  cpu: "500m"
  memory: "1Gi"
  pods: 10
  servicesLB: 0
systemOverhead:
  cpuPerProject: 100
  memPerProject: 128
addons:
  test-turbo:
    displayName: "Test Turbo"
    description: "+2 vCPU +4 GB RAM"
    price: 10
    currency: "EUR"
    cpu: "2"
    memory: "4Gi"
    storage: "10Gi"
eipQuota:
  test-small: 1
  test-large: 2
`

// savedPlansYAML holds the original billing-plans data so it can be restored after tests.
var savedPlansYAML string

// saveBillingPlansConfigMap saves the current ConfigMap data for later restoration.
func saveBillingPlansConfigMap() {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: billingPlansCMName, Namespace: kubeDcNamespace}
	err := k8sClient.Get(ctx, key, cm)
	if errors.IsNotFound(err) {
		savedPlansYAML = ""
		Logf("No existing billing-plans ConfigMap to save")
		return
	}
	Expect(err).NotTo(HaveOccurred())
	savedPlansYAML = cm.Data["plans.yaml"]
	Logf("Saved original billing-plans ConfigMap (%d bytes)", len(savedPlansYAML))
}

// restoreBillingPlansConfigMap restores the original ConfigMap data saved before tests.
func restoreBillingPlansConfigMap() {
	if savedPlansYAML == "" {
		Logf("No saved billing-plans to restore")
		return
	}
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: billingPlansCMName, Namespace: kubeDcNamespace}
	Expect(k8sClient.Get(ctx, key, cm)).Should(Succeed())
	cm.Data["plans.yaml"] = savedPlansYAML
	Expect(k8sClient.Update(ctx, cm)).Should(Succeed())
	Logf("Restored original billing-plans ConfigMap")
}

// ensureBillingPlansConfigMap creates or updates the billing-plans ConfigMap in the kube-dc namespace.
func ensureBillingPlansConfigMap() {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: billingPlansCMName, Namespace: kubeDcNamespace}

	err := k8sClient.Get(ctx, key, cm)
	if errors.IsNotFound(err) {
		Logf("Creating billing-plans ConfigMap in %s namespace", kubeDcNamespace)
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      billingPlansCMName,
				Namespace: kubeDcNamespace,
			},
			Data: map[string]string{
				"plans.yaml": billingPlansYAML,
			},
		}
		Expect(k8sClient.Create(ctx, cm)).Should(Succeed())
		Logf("SUCCESS: Created billing-plans ConfigMap")
	} else {
		Expect(err).NotTo(HaveOccurred())
		Logf("billing-plans ConfigMap already exists (version: %s), updating with test plans", cm.ResourceVersion)
		cm.Data["plans.yaml"] = billingPlansYAML
		Expect(k8sClient.Update(ctx, cm)).Should(Succeed())
		Logf("SUCCESS: Updated billing-plans ConfigMap with test plans")
	}
}

// cleanupQuotaTestOrg removes the org and namespace created for quota tests.
func cleanupQuotaTestOrg(orgName string) {
	Logf("Cleaning up quota test org: %s", orgName)

	// Delete Organization
	org := &kubedccomv1.Organization{}
	orgKey := types.NamespacedName{Name: orgName, Namespace: orgName}
	if err := k8sClient.Get(ctx, orgKey, org); err == nil {
		// Remove finalizers if stuck
		if len(org.Finalizers) > 0 {
			org.Finalizers = nil
			_ = k8sClient.Update(ctx, org)
		}
		_ = k8sClient.Delete(ctx, org)
		// Wait for deletion
		Eventually(func() bool {
			return errors.IsNotFound(k8sClient.Get(ctx, orgKey, org))
		}, time.Minute, time.Second*2).Should(BeTrue())
	}

	// Delete namespace
	ns := &corev1.Namespace{}
	nsKey := types.NamespacedName{Name: orgName}
	if err := k8sClient.Get(ctx, nsKey, ns); err == nil {
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func() bool {
			return errors.IsNotFound(k8sClient.Get(ctx, nsKey, ns))
		}, time.Minute*2, time.Second*2).Should(BeTrue())
	}

	Logf("Cleanup complete for %s", orgName)
}

// createQuotaTestOrg creates a namespace + Organization with billing annotations.
func createQuotaTestOrg(orgName, planID, subscriptionStatus string) *kubedccomv1.Organization {
	By(fmt.Sprintf("Creating namespace %s", orgName))
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: orgName},
	}
	Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

	By(fmt.Sprintf("Creating Organization %s with plan=%s status=%s", orgName, planID, subscriptionStatus))
	org := &kubedccomv1.Organization{
		ObjectMeta: metav1.ObjectMeta{
			Name:      orgName,
			Namespace: orgName,
			Annotations: map[string]string{
				annPlanID:       planID,
				annSubscription: subscriptionStatus,
			},
		},
		Spec: kubedccomv1.OrganizationSpec{
			Description: func() *string { s := "E2E Quota Test Organization"; return &s }(),
			Email:       func() *string { s := "quota-test@example.com"; return &s }(),
		},
	}
	Expect(k8sClient.Create(ctx, org)).Should(Succeed())
	Logf("SUCCESS: Created Organization %s with plan %s", orgName, planID)
	return org
}

var _ = Describe("Organization Quota Reconciliation", func() {

	BeforeAll(func() {
		By("Saving original billing-plans ConfigMap")
		saveBillingPlansConfigMap()
	})

	AfterAll(func() {
		By("Restoring original billing-plans ConfigMap")
		restoreBillingPlansConfigMap()
	})

	BeforeEach(func() {
		By("Ensuring billing-plans ConfigMap has test plans")
		ensureBillingPlansConfigMap()
	})

	Context("When an Organization is created with an active billing plan", func() {
		const orgName = "test-quota-e2e"

		AfterEach(func() {
			cleanupQuotaTestOrg(orgName)
		})

		It("Should create HRQ and LimitRange with correct resource values", func() {
			// Cleanup any leftover from previous runs
			cleanupQuotaTestOrg(orgName)

			createQuotaTestOrg(orgName, "test-small", "active")

			By("Waiting for the Organization to become Ready")
			orgKey := types.NamespacedName{Name: orgName, Namespace: orgName}
			Eventually(func() bool {
				org := &kubedccomv1.Organization{}
				if err := k8sClient.Get(ctx, orgKey, org); err != nil {
					return false
				}
				return org.Status.Ready
			}, quotaTestTimeout, quotaTestInterval).Should(BeTrue(), "Organization should become ready")
			Logf("SUCCESS: Organization %s is Ready", orgName)

			By("Verifying HierarchicalResourceQuota was created")
			hrq := &hncv1alpha2.HierarchicalResourceQuota{}
			hrqKey := types.NamespacedName{Name: hrqName, Namespace: orgName}
			Eventually(func() error {
				return k8sClient.Get(ctx, hrqKey, hrq)
			}, quotaTestTimeout, quotaTestInterval).Should(Succeed(), "HRQ 'plan-quota' should be created")
			Logf("SUCCESS: HRQ found in namespace %s", orgName)

			// Verify HRQ labels
			Expect(hrq.Labels).To(HaveKeyWithValue("billing.kube-dc.com/auto-managed", "true"))
			Expect(hrq.Labels).To(HaveKeyWithValue("billing.kube-dc.com/plan-id", "test-small"))
			Logf("SUCCESS: HRQ labels are correct")

			// Verify HRQ resource limits match test-small plan
			// test-small: cpu=2, memory=4Gi, storage=20Gi, pods=50
			// Plus system overhead: cpuPerProject=100m * projectsLimit(3) = 300m
			// Plus system overhead: memPerProject=128Mi * projectsLimit(3) = 384Mi
			hard := hrq.Spec.Hard
			Expect(hard).To(HaveKey(corev1.ResourceRequestsCPU))
			Expect(hard).To(HaveKey(corev1.ResourceRequestsMemory))
			Expect(hard).To(HaveKey(corev1.ResourceRequestsStorage))
			Expect(hard).To(HaveKey(corev1.ResourcePods))

			// CPU: 2 cores + 300m overhead = 2300m
			cpuQty := hard[corev1.ResourceRequestsCPU]
			Logf("HRQ requests.cpu = %s (milliValue=%d)", cpuQty.String(), cpuQty.MilliValue())
			Expect(cpuQty.Cmp(resource.MustParse("2"))).To(BeNumerically(">=", 0),
				"HRQ CPU should be >= 2 cores (base plan)")

			// Memory: 4Gi + 384Mi overhead
			memQty := hard[corev1.ResourceRequestsMemory]
			Logf("HRQ requests.memory = %s", memQty.String())
			Expect(memQty.Cmp(resource.MustParse("4Gi"))).To(BeNumerically(">=", 0),
				"HRQ memory should be >= 4Gi (base plan)")

			// Storage: 20Gi
			storageQty := hard[corev1.ResourceRequestsStorage]
			Logf("HRQ requests.storage = %s", storageQty.String())
			Expect(storageQty.Cmp(resource.MustParse("20Gi"))).To(Equal(0),
				"HRQ storage should be exactly 20Gi")

			// Pods: 50
			podsQty := hard[corev1.ResourcePods]
			Logf("HRQ pods = %s", podsQty.String())
			Expect(podsQty.Cmp(resource.MustParse("50"))).To(Equal(0),
				"HRQ pods should be exactly 50")

			// Verify limits exist (burst ratio applied)
			Expect(hard).To(HaveKey(corev1.ResourceLimitsCPU))
			Expect(hard).To(HaveKey(corev1.ResourceLimitsMemory))
			limitsCPU := hard[corev1.ResourceLimitsCPU]
			Logf("HRQ limits.cpu = %s", limitsCPU.String())
			Expect(limitsCPU.Cmp(cpuQty)).To(BeNumerically(">", 0),
				"HRQ limits.cpu should be > requests.cpu (burst ratio)")

			Logf("SUCCESS: HRQ resource values are correct for test-small plan")

			By("Verifying LimitRange was created")
			lr := &corev1.LimitRange{}
			lrKey := types.NamespacedName{Name: limitRangeName, Namespace: orgName}
			Eventually(func() error {
				return k8sClient.Get(ctx, lrKey, lr)
			}, quotaTestTimeout, quotaTestInterval).Should(Succeed(), "LimitRange should be created")
			Logf("SUCCESS: LimitRange found in namespace %s", orgName)

			// Verify LimitRange labels
			Expect(lr.Labels).To(HaveKeyWithValue("billing.kube-dc.com/auto-managed", "true"))
			Expect(lr.Labels).To(HaveKeyWithValue("billing.kube-dc.com/plan-id", "test-small"))

			// Verify LimitRange has container, pod, and PVC limits
			Expect(lr.Spec.Limits).To(HaveLen(3), "LimitRange should have 3 items: container, pod, PVC")

			containerLimit := lr.Spec.Limits[0]
			Expect(containerLimit.Type).To(Equal(corev1.LimitTypeContainer))
			Expect(containerLimit.Default).To(HaveKey(corev1.ResourceCPU))
			Expect(containerLimit.Default).To(HaveKey(corev1.ResourceMemory))
			Expect(containerLimit.DefaultRequest).To(HaveKey(corev1.ResourceCPU))
			Expect(containerLimit.DefaultRequest).To(HaveKey(corev1.ResourceMemory))
			Expect(containerLimit.Max).To(HaveKey(corev1.ResourceCPU))
			Expect(containerLimit.Min).To(HaveKey(corev1.ResourceCPU))

			// Verify default values match test-small plan
			defCPU := containerLimit.Default[corev1.ResourceCPU]
			Expect(defCPU.Cmp(resource.MustParse("250m"))).To(Equal(0))
			defMem := containerLimit.Default[corev1.ResourceMemory]
			Expect(defMem.Cmp(resource.MustParse("256Mi"))).To(Equal(0))
			defReqCPU := containerLimit.DefaultRequest[corev1.ResourceCPU]
			Expect(defReqCPU.Cmp(resource.MustParse("100m"))).To(Equal(0))
			defReqMem := containerLimit.DefaultRequest[corev1.ResourceMemory]
			Expect(defReqMem.Cmp(resource.MustParse("128Mi"))).To(Equal(0))

			podLimit := lr.Spec.Limits[1]
			Expect(podLimit.Type).To(Equal(corev1.LimitTypePod))

			pvcLimit := lr.Spec.Limits[2]
			Expect(pvcLimit.Type).To(Equal(corev1.LimitTypePersistentVolumeClaim))
			pvcMax := pvcLimit.Max[corev1.ResourceStorage]
			Expect(pvcMax.Cmp(resource.MustParse("20Gi"))).To(Equal(0))

			Logf("SUCCESS: LimitRange values are correct for test-small plan")
		})
	})

	Context("When an Organization plan is changed", func() {
		const orgName = "test-quota-planchange-e2e"

		AfterEach(func() {
			cleanupQuotaTestOrg(orgName)
		})

		It("Should update HRQ resources when the plan annotation changes", func() {
			cleanupQuotaTestOrg(orgName)

			createQuotaTestOrg(orgName, "test-small", "active")

			By("Waiting for HRQ to be created with test-small plan")
			hrqKey := types.NamespacedName{Name: hrqName, Namespace: orgName}
			Eventually(func() error {
				return k8sClient.Get(ctx, hrqKey, &hncv1alpha2.HierarchicalResourceQuota{})
			}, quotaTestTimeout, quotaTestInterval).Should(Succeed())
			Logf("SUCCESS: Initial HRQ created for test-small plan")

			By("Changing the plan annotation to test-large")
			org := &kubedccomv1.Organization{}
			orgKey := types.NamespacedName{Name: orgName, Namespace: orgName}
			Expect(k8sClient.Get(ctx, orgKey, org)).Should(Succeed())

			annotations := org.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations[annPlanID] = "test-large"
			org.SetAnnotations(annotations)
			Expect(k8sClient.Update(ctx, org)).Should(Succeed())
			Logf("Updated plan annotation to test-large")

			By("Waiting for HRQ to be updated with test-large resources")
			// test-large: cpu=8, memory=16Gi, storage=100Gi, pods=200
			Eventually(func() bool {
				hrq := &hncv1alpha2.HierarchicalResourceQuota{}
				if err := k8sClient.Get(ctx, hrqKey, hrq); err != nil {
					return false
				}
				storageQty, ok := hrq.Spec.Hard[corev1.ResourceRequestsStorage]
				if !ok {
					return false
				}
				// test-large storage = 100Gi
				return storageQty.Cmp(resource.MustParse("100Gi")) == 0
			}, quotaTestTimeout, quotaTestInterval).Should(BeTrue(),
				"HRQ storage should update to 100Gi after plan change to test-large")
			Logf("SUCCESS: HRQ updated to test-large resources")

			// Verify pods also updated
			hrq := &hncv1alpha2.HierarchicalResourceQuota{}
			Expect(k8sClient.Get(ctx, hrqKey, hrq)).Should(Succeed())
			podsQty := hrq.Spec.Hard[corev1.ResourcePods]
			Expect(podsQty.Cmp(resource.MustParse("200"))).To(Equal(0),
				"HRQ pods should be 200 after plan change")

			// Verify HRQ label updated
			Expect(hrq.Labels).To(HaveKeyWithValue("billing.kube-dc.com/plan-id", "test-large"))
			Logf("SUCCESS: HRQ labels updated to test-large")

			By("Verifying LimitRange updated to test-large defaults")
			lr := &corev1.LimitRange{}
			lrKey := types.NamespacedName{Name: limitRangeName, Namespace: orgName}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, lrKey, lr); err != nil {
					return false
				}
				if len(lr.Spec.Limits) == 0 {
					return false
				}
				// test-large defaultCPU = 500m
				defaultCPU := lr.Spec.Limits[0].Default[corev1.ResourceCPU]
				return defaultCPU.Cmp(resource.MustParse("500m")) == 0
			}, quotaTestTimeout, quotaTestInterval).Should(BeTrue(),
				"LimitRange should update to test-large defaults")
			Logf("SUCCESS: LimitRange updated to test-large defaults")
		})
	})

	Context("When an Organization subscription is suspended", func() {
		const orgName = "test-quota-suspend-e2e"

		AfterEach(func() {
			cleanupQuotaTestOrg(orgName)
		})

		It("Should reduce HRQ to suspended plan limits", func() {
			cleanupQuotaTestOrg(orgName)

			createQuotaTestOrg(orgName, "test-small", "active")

			By("Waiting for HRQ to be created with test-small plan")
			hrqKey := types.NamespacedName{Name: hrqName, Namespace: orgName}
			Eventually(func() error {
				return k8sClient.Get(ctx, hrqKey, &hncv1alpha2.HierarchicalResourceQuota{})
			}, quotaTestTimeout, quotaTestInterval).Should(Succeed())
			Logf("SUCCESS: Initial HRQ created")

			By("Setting subscription status to suspended")
			org := &kubedccomv1.Organization{}
			orgKey := types.NamespacedName{Name: orgName, Namespace: orgName}
			Expect(k8sClient.Get(ctx, orgKey, org)).Should(Succeed())

			annotations := org.GetAnnotations()
			annotations[annSubscription] = "suspended"
			annotations[annSuspendedAt] = "2099-01-01T00:00:00Z" // Far future so grace period doesn't expire
			org.SetAnnotations(annotations)
			Expect(k8sClient.Update(ctx, org)).Should(Succeed())
			Logf("Updated subscription to suspended")

			By("Waiting for HRQ to be reduced to suspended plan limits")
			// suspendedPlan: cpu=500m, memory=1Gi, pods=10
			Eventually(func() bool {
				hrq := &hncv1alpha2.HierarchicalResourceQuota{}
				if err := k8sClient.Get(ctx, hrqKey, hrq); err != nil {
					return false
				}
				podsQty, ok := hrq.Spec.Hard[corev1.ResourcePods]
				if !ok {
					return false
				}
				return podsQty.Cmp(resource.MustParse("10")) == 0
			}, quotaTestTimeout, quotaTestInterval).Should(BeTrue(),
				"HRQ pods should be reduced to 10 (suspended plan)")

			hrq := &hncv1alpha2.HierarchicalResourceQuota{}
			Expect(k8sClient.Get(ctx, hrqKey, hrq)).Should(Succeed())
			cpuQty := hrq.Spec.Hard[corev1.ResourceRequestsCPU]
			podsQtyFinal := hrq.Spec.Hard[corev1.ResourcePods]
			Logf("Suspended HRQ cpu=%s, pods=%s", cpuQty.String(), podsQtyFinal.String())
			Expect(cpuQty.Cmp(resource.MustParse("500m"))).To(Equal(0),
				"HRQ CPU should be 500m (suspended plan)")

			Logf("SUCCESS: HRQ reduced to suspended plan limits")
		})
	})

	Context("When an Organization with billing plan is deleted", func() {
		const orgName = "test-quota-delete-e2e"

		It("Should clean up HRQ and LimitRange", func() {
			cleanupQuotaTestOrg(orgName)

			createQuotaTestOrg(orgName, "test-small", "active")

			By("Waiting for HRQ and LimitRange to be created")
			hrqKey := types.NamespacedName{Name: hrqName, Namespace: orgName}
			lrKey := types.NamespacedName{Name: limitRangeName, Namespace: orgName}
			Eventually(func() error {
				return k8sClient.Get(ctx, hrqKey, &hncv1alpha2.HierarchicalResourceQuota{})
			}, quotaTestTimeout, quotaTestInterval).Should(Succeed())
			Eventually(func() error {
				return k8sClient.Get(ctx, lrKey, &corev1.LimitRange{})
			}, quotaTestTimeout, quotaTestInterval).Should(Succeed())
			Logf("SUCCESS: HRQ and LimitRange exist before deletion")

			By("Deleting the Organization")
			org := &kubedccomv1.Organization{}
			orgKey := types.NamespacedName{Name: orgName, Namespace: orgName}
			Expect(k8sClient.Get(ctx, orgKey, org)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, org)).Should(Succeed())
			Logf("Organization delete requested")

			By("Waiting for Organization to be deleted")
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, orgKey, &kubedccomv1.Organization{}))
			}, quotaTestTimeout, quotaTestInterval).Should(BeTrue(), "Organization should be deleted")
			Logf("SUCCESS: Organization deleted")

			By("Verifying HRQ is cleaned up")
			// HRQ should be deleted by the controller's Delete() function,
			// or will be garbage collected when the namespace is deleted
			Eventually(func() bool {
				err := k8sClient.Get(ctx, hrqKey, &hncv1alpha2.HierarchicalResourceQuota{})
				return errors.IsNotFound(err)
			}, quotaTestTimeout, quotaTestInterval).Should(BeTrue(),
				"HRQ should be deleted after organization deletion")
			Logf("SUCCESS: HRQ cleaned up")

			By("Verifying LimitRange is cleaned up")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, lrKey, &corev1.LimitRange{})
				return errors.IsNotFound(err)
			}, quotaTestTimeout, quotaTestInterval).Should(BeTrue(),
				"LimitRange should be deleted after organization deletion")
			Logf("SUCCESS: LimitRange cleaned up")

			// Final cleanup: delete the namespace
			ns := &corev1.Namespace{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: orgName}, ns); err == nil {
				_ = k8sClient.Delete(ctx, ns)
			}

			Logf("SUCCESS: Organization deletion test complete - HRQ and LimitRange properly cleaned up")
		})
	})

	Context("When an Organization has addons", func() {
		const orgName = "test-quota-addons-e2e"

		AfterEach(func() {
			cleanupQuotaTestOrg(orgName)
		})

		It("Should include addon resources in HRQ", func() {
			cleanupQuotaTestOrg(orgName)

			By("Creating Organization with test-small plan and a turbo addon")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: orgName},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			org := &kubedccomv1.Organization{
				ObjectMeta: metav1.ObjectMeta{
					Name:      orgName,
					Namespace: orgName,
					Annotations: map[string]string{
						annPlanID:       "test-small",
						annSubscription: "active",
						annAddons:       `[{"addonId":"test-turbo","quantity":1}]`,
					},
				},
				Spec: kubedccomv1.OrganizationSpec{
					Description: func() *string { s := "E2E Addon Test Org"; return &s }(),
					Email:       func() *string { s := "addon-test@example.com"; return &s }(),
				},
			}
			Expect(k8sClient.Create(ctx, org)).Should(Succeed())
			Logf("Created org with test-small + test-turbo addon")

			By("Waiting for HRQ to be created with addon resources included")
			hrqKey := types.NamespacedName{Name: hrqName, Namespace: orgName}
			Eventually(func() error {
				return k8sClient.Get(ctx, hrqKey, &hncv1alpha2.HierarchicalResourceQuota{})
			}, quotaTestTimeout, quotaTestInterval).Should(Succeed())

			hrq := &hncv1alpha2.HierarchicalResourceQuota{}
			Expect(k8sClient.Get(ctx, hrqKey, hrq)).Should(Succeed())

			// test-small: cpu=2, memory=4Gi, storage=20Gi
			// test-turbo addon: cpu=2, memory=4Gi, storage=10Gi
			// Total base: cpu=4, memory=8Gi, storage=30Gi (plus overhead)
			cpuQty := hrq.Spec.Hard[corev1.ResourceRequestsCPU]
			storageQty := hrq.Spec.Hard[corev1.ResourceRequestsStorage]
			Logf("HRQ with addon: cpu=%s, storage=%s", cpuQty.String(), storageQty.String())

			// CPU should be >= 4 (base 2 + addon 2, plus overhead)
			Expect(cpuQty.Cmp(resource.MustParse("4"))).To(BeNumerically(">=", 0),
				"HRQ CPU should be >= 4 cores (base + addon)")

			// Storage should be exactly 30Gi (base 20 + addon 10, no overhead on storage)
			Expect(storageQty.Cmp(resource.MustParse("30Gi"))).To(Equal(0),
				"HRQ storage should be 30Gi (base 20 + addon 10)")

			Logf("SUCCESS: Addon resources correctly included in HRQ")
		})
	})

	Context("When no billing plan is assigned", func() {
		const orgName = "test-quota-noplan-e2e"

		AfterEach(func() {
			cleanupQuotaTestOrg(orgName)
		})

		It("Should not create HRQ or LimitRange", func() {
			cleanupQuotaTestOrg(orgName)

			By("Creating Organization without billing annotations")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: orgName},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			org := &kubedccomv1.Organization{
				ObjectMeta: metav1.ObjectMeta{
					Name:      orgName,
					Namespace: orgName,
				},
				Spec: kubedccomv1.OrganizationSpec{
					Description: func() *string { s := "E2E No Plan Test Org"; return &s }(),
					Email:       func() *string { s := "noplan-test@example.com"; return &s }(),
				},
			}
			Expect(k8sClient.Create(ctx, org)).Should(Succeed())

			By("Waiting for Organization to become Ready")
			orgKey := types.NamespacedName{Name: orgName, Namespace: orgName}
			Eventually(func() bool {
				o := &kubedccomv1.Organization{}
				if err := k8sClient.Get(ctx, orgKey, o); err != nil {
					return false
				}
				return o.Status.Ready
			}, quotaTestTimeout, quotaTestInterval).Should(BeTrue())

			By("Verifying HRQ was NOT created")
			hrqKey := types.NamespacedName{Name: hrqName, Namespace: orgName}
			Consistently(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, hrqKey, &hncv1alpha2.HierarchicalResourceQuota{}))
			}, time.Second*10, time.Second*2).Should(BeTrue(),
				"HRQ should NOT be created when no plan is assigned")

			By("Verifying LimitRange was NOT created")
			lrKey := types.NamespacedName{Name: limitRangeName, Namespace: orgName}
			Consistently(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, lrKey, &corev1.LimitRange{}))
			}, time.Second*10, time.Second*2).Should(BeTrue(),
				"LimitRange should NOT be created when no plan is assigned")

			Logf("SUCCESS: No HRQ or LimitRange created for org without billing plan")
		})
	})
})

// Compile-time check that we use the imported packages
var _ = client.ObjectKey{}
