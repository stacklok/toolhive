package virtualmcp

import (
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("VirtualMCPServer Setup and Lifecycle", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-mcp-group"
		vmcpServerName  = "test-vmcp-server"
		mcpServerName   = "test-backend-server"
		timeout         = 3 * time.Minute
		pollingInterval = 5 * time.Second
	)

	// vmcpServiceName returns the service name for a VirtualMCPServer
	// The controller creates services with a "vmcp-" prefix
	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Creating MCPServer backend")
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef: mcpGroupName,
				Image:    "ghcr.io/stacklok/mcp-test-server:latest",
				Env: []mcpv1alpha1.EnvVar{
					{
						Name:  "MCP_SERVER_NAME",
						Value: "test-backend",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, mcpServer)).To(Succeed())

		By("Creating MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for VirtualMCP E2E tests",
			},
		}
		Expect(k8sClient.Create(ctx, mcpGroup)).To(Succeed())

		By("Waiting for MCPGroup to be ready")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			}, mcpGroup)
			if err != nil {
				return false
			}
			return mcpGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
		}, timeout, pollingInterval).Should(BeTrue())

		By("Creating VirtualMCPServer with NodePort")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				GroupRef: mcpv1alpha1.GroupRef{
					Name: mcpGroupName,
				},
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer deployment to be created")
		Eventually(func() error {
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
		}, timeout, pollingInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, vmcpServer)

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpGroup)

		By("Cleaning up MCPServer")
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpServer)
	})

	Context("when VirtualMCPServer is created", func() {
		It("should exist in the cluster", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reference the correct MCPGroup", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).NotTo(HaveOccurred())
			Expect(vmcpServer.Spec.GroupRef.Name).To(Equal(mcpGroupName))
		})

		It("should have anonymous auth configured", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).NotTo(HaveOccurred())
			Expect(vmcpServer.Spec.IncomingAuth).NotTo(BeNil())
			Expect(vmcpServer.Spec.IncomingAuth.Type).To(Equal("anonymous"))
		})

		It("should use NodePort service type", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).NotTo(HaveOccurred())
			Expect(vmcpServer.Spec.ServiceType).To(Equal("NodePort"))
		})
	})

	Context("when VirtualMCPServer resources are created by controller", func() {
		It("should create a Deployment with correct labels", func() {
			By(fmt.Sprintf("Looking for deployment %s", vmcpServerName))
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, deployment)
			}, timeout, pollingInterval).Should(Succeed(), "Deployment should be created")

			// Verify deployment has the correct labels
			Expect(deployment.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "virtualmcpserver"))
			Expect(deployment.Labels).To(HaveKeyWithValue("app.kubernetes.io/instance", vmcpServerName))
		})

		It("should have a ready Deployment", func() {
			deployment := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, deployment)
				if err != nil {
					return false
				}
				return deployment.Status.ReadyReplicas > 0 &&
					deployment.Status.ReadyReplicas == *deployment.Spec.Replicas
			}, timeout, pollingInterval).Should(BeTrue(), "Deployment should have ready replicas")
		})

		It("should create a NodePort Service with correct configuration", func() {
			serviceName := vmcpServiceName()
			By(fmt.Sprintf("Looking for NodePort service %s", serviceName))
			service := &corev1.Service{}
			var nodePort int32
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      serviceName,
					Namespace: testNamespace,
				}, service)
				if err != nil {
					return err
				}

				// Verify it's a NodePort service
				if service.Spec.Type != corev1.ServiceTypeNodePort {
					return fmt.Errorf("service is not NodePort type, got: %s", service.Spec.Type)
				}

				// Check that it has ports assigned
				if len(service.Spec.Ports) == 0 {
					return fmt.Errorf("service has no ports")
				}

				nodePort = service.Spec.Ports[0].NodePort
				if nodePort == 0 {
					return fmt.Errorf("nodePort not assigned yet")
				}

				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By(fmt.Sprintf("Service created with NodePort: %d", nodePort))

			// Verify service has the correct labels
			Expect(service.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "virtualmcpserver"))
			Expect(service.Labels).To(HaveKeyWithValue("app.kubernetes.io/instance", vmcpServerName))

			// Verify service selects the correct pods
			Expect(service.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/name", "virtualmcpserver"))
			Expect(service.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/instance", vmcpServerName))
		})

		It("should have running and ready Pods", func() {
			podList := &corev1.PodList{}
			Eventually(func() int {
				err := k8sClient.List(ctx, podList,
					client.InNamespace(testNamespace),
					client.MatchingLabels{
						"app.kubernetes.io/name":     "virtualmcpserver",
						"app.kubernetes.io/instance": vmcpServerName,
					})
				if err != nil {
					return 0
				}
				return len(podList.Items)
			}, timeout, pollingInterval).Should(BeNumerically(">", 0), "Should have at least one pod")

			// Verify pods are running and all containers are ready
			Eventually(func() bool {
				err := k8sClient.List(ctx, podList,
					client.InNamespace(testNamespace),
					client.MatchingLabels{
						"app.kubernetes.io/name":     "virtualmcpserver",
						"app.kubernetes.io/instance": vmcpServerName,
					})
				if err != nil || len(podList.Items) == 0 {
					return false
				}

				for _, pod := range podList.Items {
					if pod.Status.Phase != corev1.PodRunning {
						return false
					}
					// Check that all containers are ready
					for _, condition := range pod.Status.Conditions {
						if condition.Type == corev1.ContainersReady && condition.Status != corev1.ConditionTrue {
							return false
						}
					}
				}
				return true
			}, timeout, pollingInterval).Should(BeTrue(), "All pods should be running and ready")
		})

		It("should have a Ready status condition", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcpServer)
				if err != nil {
					return false
				}

				// Check if status has conditions
				if len(vmcpServer.Status.Conditions) == 0 {
					return false
				}

				// Look for Ready condition
				for _, condition := range vmcpServer.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == "True" {
						return true
					}
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue(), "VirtualMCPServer should have Ready condition")
		})

		It("should expose the vmcp container with correct ports", func() {
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, deployment)
			Expect(err).NotTo(HaveOccurred())

			// Find the vmcp container
			var vmcpContainer *corev1.Container
			for i, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == "vmcp" {
					vmcpContainer = &deployment.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(vmcpContainer).NotTo(BeNil(), "Should have a vmcp container")

			// Verify container has the necessary ports
			Expect(vmcpContainer.Ports).NotTo(BeEmpty(), "vmcp container should expose ports")
		})

		It("should be accessible via NodePort on localhost", func() {
			By("Getting the NodePort from the service")
			serviceName := vmcpServiceName()
			var nodePort int32
			service := &corev1.Service{}
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      serviceName,
					Namespace: testNamespace,
				}, service)
				if err != nil {
					return err
				}

				if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].NodePort == 0 {
					return fmt.Errorf("nodePort not assigned")
				}

				nodePort = service.Spec.Ports[0].NodePort
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By("Waiting for vmcp pod to be ready")
			Eventually(func() bool {
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList,
					client.InNamespace(testNamespace),
					client.MatchingLabels{
						"app.kubernetes.io/name":     "virtualmcpserver",
						"app.kubernetes.io/instance": vmcpServerName,
					})
				if err != nil || len(podList.Items) == 0 {
					return false
				}

				pod := podList.Items[0]
				if pod.Status.Phase != corev1.PodRunning {
					return false
				}

				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue(), "Pod should be ready")

			By(fmt.Sprintf("Testing connectivity to http://localhost:%d", nodePort))

			// Try to connect to the service
			Eventually(func() error {
				url := fmt.Sprintf("http://localhost:%d/health", nodePort)
				resp, err := http.Get(url)
				if err != nil {
					return fmt.Errorf("failed to connect to %s: %w", url, err)
				}
				defer resp.Body.Close()

				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
				return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})
	})

	Context("when querying VirtualMCPServer configuration", func() {
		It("should show the aggregation configuration", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).NotTo(HaveOccurred())

			if vmcpServer.Spec.Aggregation != nil {
				By(fmt.Sprintf("Aggregation config: %+v", vmcpServer.Spec.Aggregation))
			} else {
				By("No aggregation configuration set (using defaults)")
			}
		})

		It("should show incoming auth configuration", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).NotTo(HaveOccurred())

			Expect(vmcpServer.Spec.IncomingAuth).NotTo(BeNil(), "Should have incoming auth configuration")
			By(fmt.Sprintf("Incoming auth type: %s", vmcpServer.Spec.IncomingAuth.Type))
		})

		It("should show outgoing auth configuration or use defaults", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).NotTo(HaveOccurred())

			if vmcpServer.Spec.OutgoingAuth != nil {
				By(fmt.Sprintf("Outgoing auth source: %s", vmcpServer.Spec.OutgoingAuth.Source))
			} else {
				By("No outgoing auth configuration set (using defaults)")
			}
		})
	})
})
