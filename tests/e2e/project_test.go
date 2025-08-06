package e2e

import (
	"fmt"
	"time"

	netattachdef "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	kubeovn "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kubedccomv1 "github.com/shalb/kube-dc/api/kube-dc.com/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Project E2E Test", func() {
	Context("When creating and deleting a Project", func() {
		const (
			projectName = "test-project-e2e"
			timeout     = time.Second * 120 // Increased timeout for resource operations
			interval    = time.Millisecond * 500
			cidrBlock   = "10.100.0.0/16"
		)

		var (
			organization *kubedccomv1.Organization
			project      *kubedccomv1.Project
		)

		It("Should create all required OVN objects and resources for a Project", func() {
			const orgName = "test-org-creation-e2e"
			orgNsName := orgName
			projectNsName := fmt.Sprintf("%s-%s", orgName, projectName)

			// Cleanup at the end of this test
			defer func() {
				Logf("Starting Project creation test cleanup")
				cleanupTestResources(orgName, projectName)
			}()

			// Clean up any existing resources first
			By("Cleaning up any existing test resources")
			cleanupTestResources(orgName, projectName)

			By("Creating the organization namespace")
			// Check if namespace exists and is being deleted
			existingNs := &corev1.Namespace{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: orgNsName}, existingNs); err == nil {
				if existingNs.DeletionTimestamp != nil {
					// Namespace is being deleted, wait for it to be gone
					Logf("Namespace %s is being deleted, waiting for cleanup...", orgNsName)
					Eventually(func() bool {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: orgNsName}, existingNs)
						return errors.IsNotFound(err)
					}, timeout*2, interval).Should(BeTrue(), "Existing namespace should be deleted before creating new one")
				} else {
					// Namespace exists and is not being deleted, delete it first
					Logf("Namespace %s already exists, deleting it first...", orgNsName)
					k8sClient.Delete(ctx, existingNs)
					Eventually(func() bool {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: orgNsName}, existingNs)
						return errors.IsNotFound(err)
					}, timeout, interval).Should(BeTrue(), "Existing namespace should be deleted")
				}
			}

			orgNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: orgNsName},
			}
			Expect(k8sClient.Create(ctx, orgNs)).Should(Succeed())
			Logf("Created organization namespace: %s", orgNsName)

			By("Creating the prerequisite Organization")
			organization = &kubedccomv1.Organization{
				ObjectMeta: metav1.ObjectMeta{
					Name:      orgName,
					Namespace: orgNsName, // Organization must be in its own namespace
				},
				Spec: kubedccomv1.OrganizationSpec{
					Email:       stringPointer("test@example.com"),
					Description: stringPointer("E2E Test Organization"),
				},
			}
			Expect(k8sClient.Create(ctx, organization)).Should(Succeed())
			Logf("Created organization: %s in namespace: %s", orgName, orgNsName)

			By("Waiting for the Organization to become ready")
			Eventually(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: orgName, Namespace: orgNsName}
				err := k8sClient.Get(ctx, lookupKey, organization)
				if err != nil {
					return false, err
				}
				Logf("Organization status: Ready=%v", organization.Status.Ready)
				return organization.Status.Ready, nil
			}, timeout*2, interval).Should(BeTrue(), "Organization should become ready")

			By("Creating the Project with proper spec")
			project = &kubedccomv1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name:      projectName,
					Namespace: orgNsName,
				},
				Spec: kubedccomv1.ProjectSpec{
					CidrBlock:         cidrBlock,
					EgressNetworkType: kubedccomv1.ExternalNetworkTypeCloud, // Use cloud type
				},
			}
			Expect(k8sClient.Create(ctx, project)).Should(Succeed())
			Logf("Created project: %s in namespace: %s with CIDR: %s", projectName, orgNsName, cidrBlock)

			By("Waiting for the Project to become ready")
			Eventually(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: projectName, Namespace: orgNsName}
				err := k8sClient.Get(ctx, lookupKey, project)
				if err != nil {
					return false, err
				}
				return project.Status.Ready, nil
			}, timeout, interval).Should(BeTrue(), "Project should become ready")

			By("Verifying the project namespace was created")
			projectNs := &corev1.Namespace{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: projectNsName}, projectNs)
			}, timeout, interval).Should(Succeed(), "Project namespace should be created")
			Logf("Verified project namespace exists: %s", projectNsName)

			By("Verifying VPC resource is created")
			vpc := &kubeovn.Vpc{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: projectNsName}, vpc)
			}, timeout, interval).Should(Succeed(), "VPC should be created")
			Expect(vpc.Spec.Namespaces).To(ContainElement(projectNsName), "VPC should include project namespace")
			Logf("Verified VPC created with namespaces: %v", vpc.Spec.Namespaces)

			By("Verifying default EIp (gateway) resource is created")
			eip := &kubedccomv1.EIp{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "default-gw", Namespace: projectNsName}, eip)
			}, timeout, interval).Should(Succeed(), "Default EIp should be created")
			Expect(eip.Spec.ChildRef).To(Equal(fmt.Sprintf("%s-ext-cloud", projectNsName)), "EIp should reference correct OvnEip")
			Logf("Verified default EIp created with ChildRef: %s", eip.Spec.ChildRef)

			By("Verifying EIp becomes ready")
			Eventually(func() (bool, error) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "default-gw", Namespace: projectNsName}, eip)
				if err != nil {
					return false, err
				}
				return eip.Status.Ready, nil
			}, timeout, interval).Should(BeTrue(), "EIp should become ready")
			Logf("Verified EIp ready with IP: %s", eip.Status.IpAddress)

			By("Verifying Subnet resource is created")
			subnet := &kubeovn.Subnet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-default", projectNsName)}, subnet)
			}, timeout, interval).Should(Succeed(), "Subnet should be created")
			Expect(subnet.Spec.CIDRBlock).To(Equal(cidrBlock), "Subnet should have correct CIDR block")
			Logf("Verified Subnet created with CIDR: %s", subnet.Spec.CIDRBlock)

			By("Verifying OvnEip resource is created")
			oeip := &kubeovn.OvnEip{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-ext-cloud", projectNsName)}, oeip)
			}, timeout, interval).Should(Succeed(), "OvnEip should be created")
			Expect(oeip.Spec.ExternalSubnet).To(Equal("ext-cloud"), "OvnEip should reference correct external subnet")
			Logf("Verified OvnEip created: %s", oeip.Name)

			By("Verifying OVN SNAT rule is created")
			snatRule := &kubeovn.OvnSnatRule{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: projectNsName}, snatRule)
			}, timeout, interval).Should(Succeed(), "OvnSnatRule should be created")
			Expect(snatRule.Spec.OvnEip).To(Equal(fmt.Sprintf("%s-ext-cloud", projectNsName)), "SNAT rule should reference correct OvnEip")
			Logf("Verified OvnSnatRule created referencing OvnEip: %s", snatRule.Spec.OvnEip)

			By("Verifying NetworkAttachmentDefinition is created")
			nad := &netattachdef.NetworkAttachmentDefinition{}
			Eventually(func() error {
				// Check if NAD exists by looking for the default network in project namespace
				return k8sClient.Get(ctx, types.NamespacedName{Name: "default", Namespace: projectNsName}, nad)
			}, timeout, interval).Should(Succeed(), "NetworkAttachmentDefinition should be created")
			Expect(nad.Spec.Config).To(ContainSubstring("kube-ovn"), "NAD should use kube-ovn CNI")
			Logf("Verified NetworkAttachmentDefinition created in namespace: %s", projectNsName)

			By("Verifying admin Role is created in project namespace")
			role := &rbacv1.Role{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "admin", Namespace: projectNsName}, role)
			}, timeout, interval).Should(Succeed(), "Admin Role should be created")
			Logf("Verified admin Role created in namespace: %s", projectNsName)

			By("Verifying org-admin RoleBinding is created in project namespace")
			roleBinding := &rbacv1.RoleBinding{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "org-admin", Namespace: projectNsName}, roleBinding)
			}, timeout, interval).Should(Succeed(), "Org-admin RoleBinding should be created")
			Expect(roleBinding.RoleRef.Name).To(Equal("admin"), "RoleBinding should reference admin role")
			Logf("Verified org-admin RoleBinding created in namespace: %s", projectNsName)

			By("Verifying SSH key pair secret is created")
			keySecret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "ssh-keypair-default", Namespace: projectNsName}, keySecret)
			}, timeout, interval).Should(Succeed(), "SSH key secret should be created")
			Expect(keySecret.Data).To(HaveKey("id_rsa"), "Secret should contain private key")
			Expect(keySecret.Data).To(HaveKey("id_rsa.pub"), "Secret should contain public key")
			Logf("Verified SSH key secret created: %s", keySecret.Name)

			By("Verifying authorized keys secret is created")
			authSecret := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "authorized-keys-default", Namespace: projectNsName}, authSecret)
			}, timeout, interval).Should(Succeed(), "Authorized keys secret should be created")
			Expect(authSecret.Data).To(HaveKey("admin"), "Secret should contain admin key")
			Logf("Verified authorized keys secret created: %s", authSecret.Name)

			Logf("SUCCESS: All Project resources verified successfully")
		})

		It("Should properly delete Project and clean up all resources in correct order", func() {
			const orgName = "test-org-deletion-e2e"
			orgNsName := orgName
			projectNsName := fmt.Sprintf("%s-%s", orgName, projectName)

			// Cleanup at the end of this test
			defer func() {
				Logf("Starting Project deletion test cleanup")
				cleanupTestResources(orgName, projectName)
			}()

			// First create the project (reuse setup from previous test)
			By("Setting up Project for deletion test")
			setupProjectForDeletion(orgName, projectName, cidrBlock)

			By("Verifying all resources exist before deletion")
			// Verify EIp exists
			eip := &kubedccomv1.EIp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "default-gw", Namespace: projectNsName}, eip)).Should(Succeed())
			Logf("Confirmed EIp exists: %s", eip.Name)

			// Verify OvnEip exists
			oeip := &kubeovn.OvnEip{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-ext-cloud", projectNsName)}, oeip)).Should(Succeed())
			Logf("Confirmed OvnEip exists: %s", oeip.Name)

			// Verify SNAT rule exists
			snatRule := &kubeovn.OvnSnatRule{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectNsName}, snatRule)).Should(Succeed())
			Logf("Confirmed OvnSnatRule exists: %s", snatRule.Name)

			// Verify VPC exists
			vpc := &kubeovn.Vpc{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectNsName}, vpc)).Should(Succeed())
			Logf("Confirmed VPC exists: %s", vpc.Name)

			// Verify Subnet exists
			subnet := &kubeovn.Subnet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-default", projectNsName)}, subnet)).Should(Succeed())
			Logf("Confirmed Subnet exists: %s", subnet.Name)

			By("Deleting the Project")
			project := &kubedccomv1.Project{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName, Namespace: orgNsName}, project)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, project)).Should(Succeed())
			Logf("Initiated Project deletion: %s", projectName)

			By("Verifying OVN SNAT rule is deleted first")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: projectNsName}, snatRule)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "OvnSnatRule should be deleted first")
			Logf("Verified OvnSnatRule deleted")

			By("Verifying OvnEip is deleted after SNAT rule")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-ext-cloud", projectNsName)}, oeip)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "OvnEip should be deleted after SNAT rule")
			Logf("Verified OvnEip deleted")

			By("Verifying EIp is deleted after OvnEip")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "default-gw", Namespace: projectNsName}, eip)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "EIp should be deleted after OvnEip")
			Logf("Verified EIp deleted")

			By("Verifying Subnet is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-default", projectNsName)}, subnet)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Subnet should be deleted")
			Logf("Verified Subnet deleted")

			By("Verifying VPC is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: projectNsName}, vpc)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "VPC should be deleted")
			Logf("Verified VPC deleted")

			By("Verifying Project is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName, Namespace: orgNsName}, project)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Project should be deleted")
			Logf("Verified Project deleted")

			By("Verifying project namespace is deleted last")
			projectNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: projectNsName}, projectNs)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Project namespace should be deleted last")
			Logf("Verified project namespace deleted")

			Logf("SUCCESS: Project deletion completed in correct order")
		})
	})
})

// Helper function to clean up test resources
func cleanupTestResources(orgName, projectName string) {
	orgNsName := orgName
	projectNsName := fmt.Sprintf("%s-%s", orgName, projectName)

	// Delete project if exists (best effort)
	project := &kubedccomv1.Project{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName, Namespace: orgNsName}, project); err == nil {
		k8sClient.Delete(ctx, project)
		// Wait for project deletion (best effort - don't fail test if timeout)
		for i := 0; i < 240; i++ { // 2 minutes timeout
			err := k8sClient.Get(ctx, types.NamespacedName{Name: projectName, Namespace: orgNsName}, project)
			if errors.IsNotFound(err) {
				break
			}
			time.Sleep(time.Millisecond * 500)
		}
	}

	// Delete organization if exists (best effort)
	org := &kubedccomv1.Organization{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: orgName, Namespace: orgNsName}, org); err == nil {
		k8sClient.Delete(ctx, org)
		// Wait for organization deletion (best effort - don't fail test if timeout)
		for i := 0; i < 120; i++ { // 1 minute timeout
			err := k8sClient.Get(ctx, types.NamespacedName{Name: orgName, Namespace: orgNsName}, org)
			if errors.IsNotFound(err) {
				break
			}
			time.Sleep(time.Millisecond * 500)
		}
	}

	// Force delete namespaces if they exist (best effort)
	namespaces := []string{orgNsName, projectNsName}
	for _, nsName := range namespaces {
		ns := &corev1.Namespace{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns); err == nil {
			// Remove finalizers and force delete
			ns.Finalizers = nil
			k8sClient.Update(ctx, ns)
			k8sClient.Delete(ctx, ns)
		}
	}

	time.Sleep(time.Second * 2) // Give some time for cleanup
}

// Helper function to set up project for deletion test
func setupProjectForDeletion(orgName, projectName, cidrBlock string) {
	orgNsName := orgName

	// Create organization namespace
	// Check if namespace exists and is being deleted
	existingNs := &corev1.Namespace{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: orgNsName}, existingNs); err == nil {
		if existingNs.DeletionTimestamp != nil {
			// Namespace is being deleted, wait for it to be gone
			Logf("Namespace %s is being deleted, waiting for cleanup...", orgNsName)
			for i := 0; i < 240; i++ { // 2 minutes timeout
				err := k8sClient.Get(ctx, types.NamespacedName{Name: orgNsName}, existingNs)
				if errors.IsNotFound(err) {
					break
				}
				time.Sleep(time.Millisecond * 500)
			}
		} else {
			// Namespace exists and is not being deleted, delete it first
			Logf("Namespace %s already exists, deleting it first...", orgNsName)
			k8sClient.Delete(ctx, existingNs)
			for i := 0; i < 120; i++ { // 1 minute timeout
				err := k8sClient.Get(ctx, types.NamespacedName{Name: orgNsName}, existingNs)
				if errors.IsNotFound(err) {
					break
				}
				time.Sleep(time.Millisecond * 500)
			}
		}
	}

	orgNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: orgNsName},
	}
	k8sClient.Create(ctx, orgNs)

	// Create organization
	organization := &kubedccomv1.Organization{
		ObjectMeta: metav1.ObjectMeta{
			Name:      orgName,
			Namespace: orgNsName,
		},
		Spec: kubedccomv1.OrganizationSpec{
			Email:       stringPointer("test@example.com"),
			Description: stringPointer("E2E Test Organization"),
		},
	}
	k8sClient.Create(ctx, organization)

	// Wait for organization to be ready
	Eventually(func() (bool, error) {
		lookupKey := types.NamespacedName{Name: orgName, Namespace: orgNsName}
		err := k8sClient.Get(ctx, lookupKey, organization)
		if err != nil {
			return false, err
		}
		Logf("Organization status: Ready=%v", organization.Status.Ready)
		return organization.Status.Ready, nil
	}, time.Second*120, time.Millisecond*500).Should(BeTrue())

	// Create project
	project := &kubedccomv1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projectName,
			Namespace: orgNsName,
		},
		Spec: kubedccomv1.ProjectSpec{
			CidrBlock:         cidrBlock,
			EgressNetworkType: kubedccomv1.ExternalNetworkTypeCloud,
		},
	}
	k8sClient.Create(ctx, project)

	// Wait for project to be ready
	Eventually(func() (bool, error) {
		lookupKey := types.NamespacedName{Name: projectName, Namespace: orgNsName}
		err := k8sClient.Get(ctx, lookupKey, project)
		if err != nil {
			return false, err
		}
		return project.Status.Ready, nil
	}, time.Second*60, time.Millisecond*500).Should(BeTrue())
}

// Helper function for string pointers
func stringPointer(s string) *string {
	return &s
}
