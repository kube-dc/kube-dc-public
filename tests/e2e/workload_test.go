package e2e

import (
	"fmt"
	"time"

	netattachdef "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kubedccomv1 "github.com/shalb/kube-dc/api/kube-dc.com/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var _ = Describe("Workload E2E Tests", func() {
	Context("When deploying workloads in a project", func() {
		const (
			orgName     = "test-org-workload-e2e"
			projectName = "test-project-workload"
			timeout     = time.Second * 120 // Extended timeout for workload operations
			interval    = time.Millisecond * 500
			cidrBlock   = "10.200.0.0/16"
		)

		var (
			projectNsName string
		)

		BeforeEach(func() {
			projectNsName = fmt.Sprintf("%s-%s", orgName, projectName)

			// Clean up any existing resources first
			By("Cleaning up any existing test resources")
			cleanupTestResources(orgName, projectName)

			// Set up organization and project for workload tests
			By("Setting up prerequisite organization and project")
			setupProjectForWorkloadTest(orgName, projectName, cidrBlock)
		})

		AfterEach(func() {
			By("Cleaning up workload test resources")
			cleanupTestResources(orgName, projectName)
		})

		It("Should create and manage EIP resources successfully", func() {
			By("Creating a custom EIP resource")
			customEip := &kubedccomv1.EIp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eip",
					Namespace: projectNsName,
				},
				Spec: kubedccomv1.EIpSpec{
					ExternalNetworkType: kubedccomv1.ExternalNetworkTypeCloud,
				},
			}
			Expect(k8sClient.Create(ctx, customEip)).Should(Succeed())
			Logf("Created custom EIp: %s in namespace: %s", customEip.Name, customEip.Namespace)

			By("Waiting for custom EIP to become ready")
			Eventually(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: "test-eip", Namespace: projectNsName}
				err := k8sClient.Get(ctx, lookupKey, customEip)
				if err != nil {
					return false, err
				}
				return customEip.Status.Ready, nil
			}, timeout, interval).Should(BeTrue(), "Custom EIp should become ready")

			By("Verifying EIP has IP address assigned")
			Expect(customEip.Status.IpAddress).NotTo(BeEmpty(), "EIp should have IP address assigned")
			Logf("EIp %s assigned IP address: %s", customEip.Name, customEip.Status.IpAddress)

			By("Cleaning up custom EIP")
			Expect(k8sClient.Delete(ctx, customEip)).Should(Succeed())
		})

		It("Should create and manage FIP resources successfully", func() {
			By("Creating a FIP resource with specific IP")
			customFip := &kubedccomv1.FIp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-fip",
					Namespace: projectNsName,
				},
				Spec: kubedccomv1.FIpSpec{
					IpAddress:           "10.200.1.100", // Within project CIDR
					ExternalNetworkType: kubedccomv1.ExternalNetworkTypeCloud,
				},
			}
			Expect(k8sClient.Create(ctx, customFip)).Should(Succeed())
			Logf("Created FIp: %s with IP: %s", customFip.Name, customFip.Spec.IpAddress)

			By("Waiting for FIP to become ready")
			Eventually(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: "test-fip", Namespace: projectNsName}
				err := k8sClient.Get(ctx, lookupKey, customFip)
				if err != nil {
					return false, err
				}
				return customFip.Status.Ready, nil
			}, timeout, interval).Should(BeTrue(), "FIp should become ready")
			By("Verifying FIP status")
			Expect(customFip.Status.ExternalIP).NotTo(BeEmpty(), "FIp should have external IP assigned")
			Logf("FIp %s ready with external IP: %s", customFip.Name, customFip.Status.ExternalIP)

			By("Cleaning up FIP")
			Expect(k8sClient.Delete(ctx, customFip)).Should(Succeed())
		})

		It("Should create and manage VM interface FIP resources successfully", func() {
			By("Creating a FIP with VM interface targeting - testing new vmTarget functionality")
			vmInterfaceFip := &kubedccomv1.FIp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vm-interface-fip",
					Namespace: projectNsName,
				},
				Spec: kubedccomv1.FIpSpec{
					VMTarget: &kubedccomv1.VMTarget{
						VMName: "nonexistent-vm", // This will test error handling
					},
					ExternalNetworkType: kubedccomv1.ExternalNetworkTypeCloud,
				},
			}
			Expect(k8sClient.Create(ctx, vmInterfaceFip)).Should(Succeed())
			Logf("Created VM interface FIp: %s targeting VM: %s", vmInterfaceFip.Name, vmInterfaceFip.Spec.VMTarget.VMName)

			By("Verifying FIP handles missing VM gracefully")
			Consistently(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: "test-vm-interface-fip", Namespace: projectNsName}
				err := k8sClient.Get(ctx, lookupKey, vmInterfaceFip)
				if err != nil {
					return false, err
				}
				// Should not be ready since VM doesn't exist
				return !vmInterfaceFip.Status.Ready, nil
			}, time.Second*10, interval).Should(BeTrue(), "VM interface FIp should not be ready when VM doesn't exist")

			By("Verifying VM interface FIP CRD fields are properly set")
			Expect(vmInterfaceFip.Spec.VMTarget).NotTo(BeNil(), "VMTarget should be set")
			Expect(vmInterfaceFip.Spec.VMTarget.VMName).To(Equal("nonexistent-vm"), "VM name should match")
			Expect(vmInterfaceFip.Spec.IpAddress).To(BeEmpty(), "IpAddress should be empty when using vmTarget")
			Logf("VM interface FIp validation successful - vmTarget properly configured")

			By("Cleaning up VM interface FIP")
			Expect(k8sClient.Delete(ctx, vmInterfaceFip)).Should(Succeed())
		})

		It("Should deploy and expose nginx pod with LoadBalancer service", func() {
			By("Creating a dedicated EIP for nginx service")
			nginxEip := &kubedccomv1.EIp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nginx-service-eip",
					Namespace: projectNsName,
				},
				Spec: kubedccomv1.EIpSpec{
					ExternalNetworkType: kubedccomv1.ExternalNetworkTypeCloud,
				},
			}
			Expect(k8sClient.Create(ctx, nginxEip)).Should(Succeed())
			Logf("Created nginx service EIp: %s", nginxEip.Name)

			By("Waiting for nginx EIP to become ready")
			Eventually(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: "nginx-service-eip", Namespace: projectNsName}
				err := k8sClient.Get(ctx, lookupKey, nginxEip)
				if err != nil {
					return false, err
				}
				return nginxEip.Status.Ready, nil
			}, timeout, interval).Should(BeTrue(), "Nginx EIp should become ready")
			Logf("Nginx EIp ready with IP: %s", nginxEip.Status.IpAddress)

			By("Creating nginx deployment")
			nginxDeployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nginx-test",
					Namespace: projectNsName,
					Labels: map[string]string{
						"app": "nginx-test",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(1),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "nginx-test",
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "nginx-test",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "nginx",
									Image: "nginx:alpine",
									Ports: []corev1.ContainerPort{
										{
											ContainerPort: 80,
											Protocol:      corev1.ProtocolTCP,
										},
									},
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("100m"),
											corev1.ResourceMemory: resource.MustParse("128Mi"),
										},
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("50m"),
											corev1.ResourceMemory: resource.MustParse("64Mi"),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nginxDeployment)).Should(Succeed())
			Logf("Created nginx deployment: %s", nginxDeployment.Name)

			By("Waiting for nginx deployment to be ready")
			Eventually(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: "nginx-test", Namespace: projectNsName}
				err := k8sClient.Get(ctx, lookupKey, nginxDeployment)
				if err != nil {
					return false, err
				}
				return nginxDeployment.Status.ReadyReplicas == 1, nil
			}, timeout, interval).Should(BeTrue(), "Nginx deployment should be ready")

			By("Creating LoadBalancer service for nginx")
			nginxService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nginx-service-lb",
					Namespace: projectNsName,
					Annotations: map[string]string{
						"service.nlb.kube-dc.com/bind-on-eip": "nginx-service-eip",
					},
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeLoadBalancer,
					Selector: map[string]string{
						"app": "nginx-test",
					},
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							Protocol:   corev1.ProtocolTCP,
							Port:       80,
							TargetPort: intstr.FromInt(80),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nginxService)).Should(Succeed())
			Logf("Created nginx LoadBalancer service: %s", nginxService.Name)

			By("Waiting for service to get external IP")
			Eventually(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: "nginx-service-lb", Namespace: projectNsName}
				err := k8sClient.Get(ctx, lookupKey, nginxService)
				if err != nil {
					return false, err
				}
				return len(nginxService.Status.LoadBalancer.Ingress) > 0, nil
			}, timeout, interval).Should(BeTrue(), "Service should get external IP")

			By("Verifying service external IP")
			Expect(nginxService.Status.LoadBalancer.Ingress).To(HaveLen(1), "Service should have one ingress IP")
			externalIP := nginxService.Status.LoadBalancer.Ingress[0].IP
			Expect(externalIP).NotTo(BeEmpty(), "Service should have external IP assigned")
			Logf("Nginx service exposed on external IP: %s", externalIP)

			By("Cleaning up nginx resources")
			Expect(k8sClient.Delete(ctx, nginxService)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, nginxDeployment)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, nginxEip)).Should(Succeed())
		})

		It("Should verify VM and workload deployment capabilities", func() {
			By("Verifying project namespace has NetworkAttachmentDefinition for VMs")
			nad := &netattachdef.NetworkAttachmentDefinition{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "default", Namespace: projectNsName}, nad)
			}, timeout, interval).Should(Succeed(), "NetworkAttachmentDefinition should exist for VM networking")

			By("Verifying NAD has correct kube-ovn configuration")
			Expect(nad.Spec.Config).To(ContainSubstring("kube-ovn"), "NAD should be configured for kube-ovn")
			Expect(nad.Spec.Config).To(ContainSubstring(projectNsName), "NAD should reference project namespace")
			Logf("NetworkAttachmentDefinition verified: %s", nad.Name)

			By("Creating a test pod to verify workload deployment")
			testPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workload-pod",
					Namespace: projectNsName,
					Labels: map[string]string{
						"app": "test-workload",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "test-container",
							Image:   "busybox:latest",
							Command: []string{"sleep", "300"},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("32Mi"),
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			}
			Expect(k8sClient.Create(ctx, testPod)).Should(Succeed())
			Logf("Created test pod: %s", testPod.Name)

			By("Waiting for test pod to be running")
			Eventually(func() (bool, error) {
				lookupKey := types.NamespacedName{Name: "test-workload-pod", Namespace: projectNsName}
				err := k8sClient.Get(ctx, lookupKey, testPod)
				if err != nil {
					return false, err
				}
				return testPod.Status.Phase == corev1.PodRunning, nil
			}, timeout, interval).Should(BeTrue(), "Test pod should be running")

			Logf("Test pod %s is running successfully", testPod.Name)

			By("Verifying pod networking and project isolation")
			// Pod should be running in the project namespace with proper network configuration
			Expect(testPod.Status.PodIP).NotTo(BeEmpty(), "Pod should have IP address assigned")
			Logf("Test pod IP: %s", testPod.Status.PodIP)

			By("Cleaning up test pod")
			Expect(k8sClient.Delete(ctx, testPod)).Should(Succeed())
		})
	})
})

// Helper function to set up project for workload test
func setupProjectForWorkloadTest(orgName, projectName, cidrBlock string) {
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
			Email:       stringPointer("workload-test@example.com"),
			Description: stringPointer("Workload E2E Test Organization"),
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
	}, time.Second*120, time.Millisecond*500).Should(BeTrue())

	Logf("Workload test infrastructure ready: org=%s, project=%s", orgName, projectName)
}

// Helper functions for pointer types
func int32Ptr(i int32) *int32 {
	return &i
}

func boolPtr(b bool) *bool {
	return &b
}
