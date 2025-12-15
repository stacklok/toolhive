package virtualmcp

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Yardstick Base", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-yardstick-group"
		vmcpServerName  = "test-vmcp-yardstick"
		backend1Name    = "yardstick-a"
		backend2Name    = "yardstick-b"
		timeout         = 3 * time.Minute
		pollingInterval = 3 * time.Second // Increased from 1s to reduce K8s API pressure
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for yardstick backends")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for yardstick-based E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServers in parallel")
		// Create both MCPServer resources without waiting
		backend1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend1Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend1)).To(Succeed())

		backend2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend2Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend2)).To(Succeed())

		// Wait for both backends to be running in parallel
		By("Waiting for both backend MCPServers to be running")
		Eventually(func() error {
			server1 := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1Name,
				Namespace: testNamespace,
			}, server1); err != nil {
				return fmt.Errorf("backend1: failed to get server: %w", err)
			}
			if server1.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend1 not ready yet, phase: %s", server1.Status.Phase)
			}

			server2 := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, server2); err != nil {
				return fmt.Errorf("backend2: failed to get server: %w", err)
			}
			if server2.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend2 not ready yet, phase: %s", server2.Status.Phase)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed(), "Both MCPServers should be running")

		By("Creating VirtualMCPServer with prefix conflict resolution")
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
				Aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: "prefix",
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By(fmt.Sprintf("VirtualMCPServer accessible at http://localhost:%d", vmcpNodePort))
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

	Context("when testing basic yardstick aggregation", func() {
		It("should be accessible via NodePort", func() {
			By("Testing HTTP connectivity to VirtualMCPServer")
			httpClient := &http.Client{Timeout: 10 * time.Second}
			url := fmt.Sprintf("http://localhost:%d/health", vmcpNodePort)

			Eventually(func() error {
				resp, err := httpClient.Get(url)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
				}
				return nil
			}, 2*time.Minute, pollingInterval).Should(Succeed())
		})

		It("should aggregate echo tools from both yardstick backends", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-yardstick-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "VirtualMCPServer should aggregate tools from backends")

			By(fmt.Sprintf("VirtualMCPServer aggregates %d tools", len(tools.Tools)))
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  Aggregated tool: %s - %s\n", tool.Name, tool.Description)
			}

			// With prefix conflict resolution, both yardstick backends should expose "echo" tool
			// prefixed with their workload name: yardstick-a_echo, yardstick-b_echo
			Expect(len(tools.Tools)).To(BeNumerically(">=", 2),
				"VirtualMCPServer should aggregate echo tools from both backends")

			// Verify we have prefixed tools from both backends
			toolNames := make([]string, len(tools.Tools))
			for i, tool := range tools.Tools {
				toolNames[i] = tool.Name
			}
			GinkgoWriter.Printf("All aggregated tool names: %v\n", toolNames)

			// Check that we have tools from both backends (with prefixes)
			hasBackend1Tool := false
			hasBackend2Tool := false
			for _, name := range toolNames {
				if strings.Contains(name, backend1Name) {
					hasBackend1Tool = true
				}
				if strings.Contains(name, backend2Name) {
					hasBackend2Tool = true
				}
			}
			Expect(hasBackend1Tool).To(BeTrue(), "Should have tool from backend 1")
			Expect(hasBackend2Tool).To(BeTrue(), "Should have tool from backend 2")
		})

		It("should successfully call echo tool through VirtualMCPServer", func() {
			// Use shared helper to test tool listing and calling
			TestToolListingAndCall(vmcpNodePort, "toolhive-yardstick-test", "echo", "hello123")
		})
	})

	Context("when verifying VirtualMCPServer status", func() {
		It("should have correct aggregation configuration", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.Aggregation).ToNot(BeNil())
			Expect(vmcpServer.Spec.Aggregation.ConflictResolution).To(Equal("prefix"))
		})

		It("should discover both yardstick backends in the group", func() {
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(backends).To(HaveLen(2), "Should discover both yardstick backends in the group")

			backendNames := make([]string, len(backends))
			for i, backend := range backends {
				backendNames[i] = backend.Name
			}
			Expect(backendNames).To(ContainElements(backend1Name, backend2Name))
		})

		It("should have VirtualMCPServer in Ready phase", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady))
		})

		It("should reflect backend health changes in status", func() {
			By("Verifying VirtualMCPServer initially has 2 backends")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady))
			Expect(vmcpServer.Status.BackendCount).To(Equal(2))
			Expect(vmcpServer.Status.DiscoveredBackends).To(HaveLen(2))

			By("Updating backend to use invalid image to make it unhealthy")
			// Update yardstick-a to use a non-existent image, causing ImagePullBackOff
			Eventually(func() error {
				backend := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend1Name,
					Namespace: testNamespace,
				}, backend)
				if err != nil {
					return err
				}
				backend.Spec.Image = "non-existent-image:invalid"
				return k8sClient.Update(ctx, backend)
			}, timeout, pollingInterval).Should(Succeed())
			By("Waiting for MCPServer to transition to Pending state (proxy not ready)")
			Eventually(func() mcpv1alpha1.MCPServerPhase {
				backend := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend1Name,
					Namespace: testNamespace,
				}, backend)
				if err != nil {
					return ""
				}
				return backend.Status.Phase
			}, timeout, pollingInterval).Should(Equal(mcpv1alpha1.MCPServerPhasePending),
				"MCPServer should be Pending when backend container has image pull issues but proxy is still running")

			By("Waiting for VirtualMCPServer to transition to Degraded phase")
			Eventually(func() mcpv1alpha1.VirtualMCPServerPhase {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcpServer)
				if err != nil {
					return ""
				}
				return vmcpServer.Status.Phase
			}, timeout, pollingInterval).Should(Equal(mcpv1alpha1.VirtualMCPServerPhaseDegraded),
				"VirtualMCPServer should enter Degraded phase when a backend is unavailable")

			By("Verifying backend count reflects one ready backend")
			// Re-fetch VirtualMCPServer to ensure we have the latest status
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)).To(Succeed(), "Should be able to fetch VirtualMCPServer")
			Expect(vmcpServer.Status.BackendCount).To(Equal(1), "Should have 1 ready backend")

			By("Verifying discovered backends list shows one unavailable backend")
			Expect(vmcpServer.Status.DiscoveredBackends).To(HaveLen(2), "Should track both backends")

			// Check that one backend is unavailable and one is ready
			backendStatuses := make(map[string]string)
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				backendStatuses[backend.Name] = backend.Status
			}
			Expect(backendStatuses[backend1Name]).To(Equal(mcpv1alpha1.BackendStatusUnavailable))
			Expect(backendStatuses[backend2Name]).To(Equal(mcpv1alpha1.BackendStatusReady))

			By("Restoring backend to use valid image")
			// Restore yardstick-a's image back to the valid YardstickImage
			Eventually(func() error {
				backend := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend1Name,
					Namespace: testNamespace,
				}, backend)
				if err != nil {
					return err
				}
				backend.Spec.Image = images.YardstickServerImage
				return k8sClient.Update(ctx, backend)
			}, timeout, pollingInterval).Should(Succeed())
			By("Deleting the Deployment pod to force immediate recreation with new image")
			// Manually delete the pod to force immediate recreation rather than waiting
			// for the normal rolling update process, which speeds up the E2E test
			podToDelete := &corev1.Pod{}
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend1Name + "-0", // Pod name pattern
					Namespace: testNamespace,
				}, podToDelete)
				if err != nil {
					return err
				}
				return k8sClient.Delete(ctx, podToDelete)
			}, timeout, pollingInterval).Should(Succeed())

			By("Waiting for backend to be Running again")
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

			By("Waiting for VirtualMCPServer to return to Ready phase")
			Eventually(func() mcpv1alpha1.VirtualMCPServerPhase {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcpServer)
				if err != nil {
					return ""
				}
				return vmcpServer.Status.Phase
			}, timeout, pollingInterval).Should(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady),
				"VirtualMCPServer should return to Ready phase when all backends are restored")

			By("Verifying both backends are ready")
			// Re-fetch VirtualMCPServer to ensure we have the latest status
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)).To(Succeed(), "Should be able to fetch VirtualMCPServer")
			Expect(vmcpServer.Status.BackendCount).To(Equal(2), "Should have 2 ready backends")
			Expect(vmcpServer.Status.DiscoveredBackends).To(HaveLen(2), "Should track both backends")

			restoredBackendStatuses := make(map[string]string)
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				restoredBackendStatuses[backend.Name] = backend.Status
			}
			Expect(restoredBackendStatuses[backend1Name]).To(Equal(mcpv1alpha1.BackendStatusReady))
			Expect(restoredBackendStatuses[backend2Name]).To(Equal(mcpv1alpha1.BackendStatusReady))
		})
	})

	Context("when testing group membership changes trigger reconciliation", func() {
		backend3Name := "yardstick-c"
		backend4Name := "yardstick-d"

		It("should have two discovered backends initially", func() {
			status, err := GetVirtualMCPServerStatus(ctx, k8sClient, vmcpServerName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(status.BackendCount).To(Equal(2), "Should have 2 initial backends")
			Expect(status.DiscoveredBackends).To(HaveLen(2), "Should have 2 discovered backends")

			backendNames := make([]string, len(status.DiscoveredBackends))
			for i, backend := range status.DiscoveredBackends {
				backendNames[i] = backend.Name
			}
			Expect(backendNames).To(ContainElements(backend1Name, backend2Name))

			By(fmt.Sprintf("Initial backends: %v", backendNames))
		})

		It("should discover a new backend when added to the group", func() {
			By("Creating a new yardstick backend MCPServer and adding to the group")
			CreateMCPServerAndWait(ctx, k8sClient, backend3Name, testNamespace,
				mcpGroupName, images.YardstickServerImage, timeout, pollingInterval)

			By("Waiting for VirtualMCPServer to reconcile and discover the new backend")
			Eventually(func() error {
				status, err := GetVirtualMCPServerStatus(ctx, k8sClient, vmcpServerName, testNamespace)
				if err != nil {
					return err
				}

				if status.BackendCount != 3 {
					return fmt.Errorf("expected 3 backends, got %d", status.BackendCount)
				}

				if len(status.DiscoveredBackends) != 3 {
					return fmt.Errorf("expected 3 discovered backends, got %d", len(status.DiscoveredBackends))
				}

				backendNames := make([]string, len(status.DiscoveredBackends))
				for i, backend := range status.DiscoveredBackends {
					backendNames[i] = backend.Name
				}

				if !slices.Contains(backendNames, backend3Name) {
					return fmt.Errorf("new backend %s not found in discovered backends: %v", backend3Name, backendNames)
				}

				return nil
			}, timeout, pollingInterval).Should(Succeed(), "VirtualMCPServer should discover the new backend")

		})

		It("should remove a backend when deleted from the group", func() {
			By("Creating a dedicated backend MCPServer for deletion test")
			CreateMCPServerAndWait(ctx, k8sClient, backend4Name, testNamespace,
				mcpGroupName, images.YardstickServerImage, timeout, pollingInterval)

			By("Waiting for VirtualMCPServer to discover the new backend (should have 4 backends)")
			Eventually(func() error {
				status, err := GetVirtualMCPServerStatus(ctx, k8sClient, vmcpServerName, testNamespace)
				if err != nil {
					return err
				}
				if status.BackendCount != 4 {
					return fmt.Errorf("expected 4 backends before deletion, got %d", status.BackendCount)
				}
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By("Deleting the dedicated backend MCPServer from the group")
			backend4 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend4Name,
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Delete(ctx, backend4)).To(Succeed())

			By("Waiting for VirtualMCPServer to reconcile and remove the deleted backend")
			Eventually(func() error {
				status, err := GetVirtualMCPServerStatus(ctx, k8sClient, vmcpServerName, testNamespace)
				if err != nil {
					return err
				}

				if status.BackendCount != 3 {
					return fmt.Errorf("expected 3 backends after removal, got %d", status.BackendCount)
				}

				if len(status.DiscoveredBackends) != 3 {
					return fmt.Errorf("expected 3 discovered backends after removal, got %d", len(status.DiscoveredBackends))
				}

				backendNames := make([]string, len(status.DiscoveredBackends))
				for i, backend := range status.DiscoveredBackends {
					backendNames[i] = backend.Name
				}

				if slices.Contains(backendNames, backend4Name) {
					return fmt.Errorf("deleted backend %s still found in discovered backends: %v", backend4Name, backendNames)
				}

				return nil
			}, timeout, pollingInterval).Should(Succeed(), "VirtualMCPServer should remove the deleted backend")

		})

		It("should remain ready throughout membership changes", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(HasCondition(vmcpServer, "Ready", "True")).To(BeTrue(),
				"VirtualMCPServer should remain ready after membership changes")
		})

		AfterAll(func() {
			By("Cleaning up additional backends from membership test")
			for _, backendName := range []string{backend3Name, backend4Name} {
				backend := &mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backendName,
						Namespace: testNamespace,
					},
				}
				_ = k8sClient.Delete(ctx, backend)
			}
		})
	})
})
