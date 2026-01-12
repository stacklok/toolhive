package virtualmcp

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// VirtualMCPServer Status Reporting E2E Tests
// These tests verify that the K8sReporter correctly updates the VirtualMCPServer.Status
// subresource with runtime status information from the vMCP runtime.
//
// This validates:
// - Status.Phase is updated (Pending → Ready)
// - Status.BackendCount reflects discovered backends
// - Status.DiscoveredBackends contains backend information
// - Status.Conditions are properly set (ServerReady, BackendsDiscovered)
// - Status.ObservedGeneration matches resource Generation
//
// Related: Issue #3147 (StatusReporter abstraction), Issue #2854 (Status reporting)
var _ = Describe("VirtualMCPServer Status Reporting", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-status-reporting-group"
		vmcpServerName  = "test-vmcp-status-reporting"
		backend1Name    = "backend-status-fetch"
		backend2Name    = "backend-status-osv"
		timeout         = 3 * time.Minute
		pollingInterval = 2 * time.Second
	)

	BeforeAll(func() {
		By("Creating MCPGroup for status reporting tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for status reporting E2E tests", timeout, pollingInterval)

		By("Creating first backend MCPServer - fetch")
		backend1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend1Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     images.GofetchServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
			},
		}
		Expect(k8sClient.Create(ctx, backend1)).To(Succeed())

		By("Waiting for first backend to be ready")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1Name,
				Namespace: testNamespace,
			}, server)
			if err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}

			if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
				return nil
			}
			return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
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

		By("Cleaning up backend MCPServers")
		for _, backendName := range []string{backend1Name, backend2Name} {
			backend := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backendName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, backend)
		}

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpGroup)
	})

	Context("Initial Status Updates", func() {
		It("should create VirtualMCPServer and verify initial status", func() {
			By("Creating VirtualMCPServer")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
						Aggregation: &vmcpconfig.AggregationConfig{
							ConflictResolution: "prefix",
						},
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
					ServiceType: "NodePort",
				},
			}
			Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

			By("Waiting for VirtualMCPServer to be ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

			By("Verifying status Phase is Ready")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcp.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady),
				"Status.Phase should be Ready after VirtualMCPServer starts")

			By("Verifying BackendCount is updated")
			Expect(vmcp.Status.BackendCount).To(BeNumerically(">", 0),
				"Status.BackendCount should be greater than 0 after backend discovery")
			GinkgoWriter.Printf("BackendCount: %d\n", vmcp.Status.BackendCount)

			By("Verifying DiscoveredBackends is populated")
			Expect(vmcp.Status.DiscoveredBackends).ToNot(BeEmpty(),
				"Status.DiscoveredBackends should be populated with discovered backends")

			for _, backend := range vmcp.Status.DiscoveredBackends {
				GinkgoWriter.Printf("Discovered backend: %s (status: %s, url: %s)\n",
					backend.Name, backend.Status, backend.URL)
				Expect(backend.Name).ToNot(BeEmpty(), "Backend name should not be empty")
				Expect(backend.Status).ToNot(BeEmpty(), "Backend status should not be empty")
				Expect(backend.URL).ToNot(BeEmpty(), "Backend URL should not be empty")
			}

			By("Verifying Conditions are set")
			Expect(vmcp.Status.Conditions).ToNot(BeEmpty(),
				"Status.Conditions should contain at least one condition")

			// Verify Ready condition exists and is True
			foundReady := false
			for _, condition := range vmcp.Status.Conditions {
				GinkgoWriter.Printf("Condition: %s = %s (reason: %s, message: %s)\n",
					condition.Type, condition.Status, condition.Reason, condition.Message)
				if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
					foundReady = true
				}
			}
			Expect(foundReady).To(BeTrue(), "Ready condition should be present and True")

			By("Verifying ObservedGeneration matches Generation")
			Expect(vmcp.Status.ObservedGeneration).To(Equal(vmcp.Generation),
				"Status.ObservedGeneration should match resource Generation")
		})
	})

	Context("Dynamic Backend Changes", func() {
		It("should update status when backend is added", func() {
			By("Getting current backend count")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			initialBackendCount := vmcp.Status.BackendCount
			GinkgoWriter.Printf("Initial backend count: %d\n", initialBackendCount)

			By("Adding second backend MCPServer - osv")
			backend2 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend2Name,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.OSVMCPServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			}
			Expect(k8sClient.Create(ctx, backend2)).To(Succeed())

			By("Waiting for new backend to be ready")
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend2Name,
					Namespace: testNamespace,
				}, server)
				if err != nil {
					return err
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
					return fmt.Errorf("backend not running yet, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By("Verifying BackendCount increased")
			Eventually(func() int {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return -1
				}
				return vmcp.Status.BackendCount
			}, timeout, pollingInterval).Should(BeNumerically(">", initialBackendCount),
				"BackendCount should increase after adding a new backend")

			By("Verifying DiscoveredBackends list updated")
			vmcp = &mcpv1alpha1.VirtualMCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcp.Status.DiscoveredBackends).To(HaveLen(vmcp.Status.BackendCount), "Number of DiscoveredBackends should match BackendCount")
			GinkgoWriter.Printf("Updated backend count: %d\n", vmcp.Status.BackendCount)
		})

		It("should update status when backend is removed", func() {
			By("Getting current backend count")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			beforeRemovalCount := vmcp.Status.BackendCount
			GinkgoWriter.Printf("Backend count before removal: %d\n", beforeRemovalCount)

			By("Removing second backend")
			backend2 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend2Name,
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Delete(ctx, backend2)).To(Succeed())

			By("Waiting for backend deletion to complete")
			Eventually(func() bool {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend2Name,
					Namespace: testNamespace,
				}, server)
				return err != nil // Should fail to get when deleted
			}, timeout, pollingInterval).Should(BeTrue())

			By("Verifying BackendCount decreased")
			Eventually(func() int {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return -1
				}
				return vmcp.Status.BackendCount
			}, timeout, pollingInterval).Should(BeNumerically("<", beforeRemovalCount),
				"BackendCount should decrease after removing a backend")

			By("Verifying DiscoveredBackends list updated")
			vmcp = &mcpv1alpha1.VirtualMCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcp.Status.DiscoveredBackends).To(HaveLen(vmcp.Status.BackendCount), "Number of DiscoveredBackends should match BackendCount")
			GinkgoWriter.Printf("Updated backend count after removal: %d\n", vmcp.Status.BackendCount)
		})
	})

	Context("Status Fields Validation", func() {
		It("should have properly formatted backend status", func() {
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying each discovered backend has required fields")
			for _, backend := range vmcp.Status.DiscoveredBackends {
				Expect(backend.Name).ToNot(BeEmpty(), "Backend name should not be empty")
				Expect(backend.Status).To(BeElementOf(
					mcpv1alpha1.BackendStatusReady,
					mcpv1alpha1.BackendStatusDegraded,
					mcpv1alpha1.BackendStatusUnavailable,
					mcpv1alpha1.BackendStatusUnknown,
				), "Backend status should be a valid status value")
				Expect(backend.URL).ToNot(BeEmpty(), "Backend URL should not be empty")
				Expect(backend.LastHealthCheck.IsZero()).To(BeFalse(),
					"LastHealthCheck should be set")
			}
		})

		It("should have valid condition timestamps", func() {
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying condition timestamps are recent")
			now := time.Now()
			for _, condition := range vmcp.Status.Conditions {
				Expect(condition.LastTransitionTime.IsZero()).To(BeFalse(),
					"Condition LastTransitionTime should be set")

				// Timestamps should be within reasonable range (not future, not too old)
				transitionTime := condition.LastTransitionTime.Time
				Expect(transitionTime.Before(now.Add(1*time.Minute))).To(BeTrue(),
					"Condition timestamp should not be in the future")
				Expect(transitionTime.After(now.Add(-10*time.Minute))).To(BeTrue(),
					"Condition timestamp should be recent (within last 10 minutes)")
			}
		})

		It("should maintain status consistency during updates", func() {
			By("Repeatedly checking status remains consistent")
			Consistently(func() error {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return err
				}

				// Status should remain Ready
				if vmcp.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseReady {
					return fmt.Errorf("phase changed to %s", vmcp.Status.Phase)
				}

				// BackendCount should match DiscoveredBackends length
				if len(vmcp.Status.DiscoveredBackends) != vmcp.Status.BackendCount {
					return fmt.Errorf("backend count mismatch: count=%d, len(backends)=%d",
						vmcp.Status.BackendCount, len(vmcp.Status.DiscoveredBackends))
				}

				// ObservedGeneration should match Generation
				if vmcp.Status.ObservedGeneration != vmcp.Generation {
					return fmt.Errorf("observed generation mismatch: observed=%d, generation=%d",
						vmcp.Status.ObservedGeneration, vmcp.Generation)
				}

				return nil
			}, 20*time.Second, 2*time.Second).Should(Succeed(),
				"Status should remain consistent over time")
		})
	})
})
