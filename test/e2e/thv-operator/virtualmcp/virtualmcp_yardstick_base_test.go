package virtualmcp

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for yardstick backends")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for yardstick-based E2E tests", timeout, pollingInterval)

		By("Creating first yardstick backend MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backend1Name, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating second yardstick backend MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backend2Name, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

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
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout)

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
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
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
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-yardstick-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing available tools")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			By("Finding an echo tool to call")
			var targetToolName string
			for _, tool := range tools.Tools {
				// Look for any echo tool (may have prefix)
				if strings.Contains(tool.Name, "echo") {
					targetToolName = tool.Name
					break
				}
			}
			Expect(targetToolName).ToNot(BeEmpty(), "Should find an echo tool")

			By(fmt.Sprintf("Calling echo tool: %s", targetToolName))
			// Yardstick echo tool requires alphanumeric input
			testInput := "hello123"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = targetToolName
			callRequest.Params.Arguments = map[string]any{
				"input": testInput,
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(),
				fmt.Sprintf("Should be able to call tool '%s' through VirtualMCPServer", targetToolName))
			Expect(result).ToNot(BeNil())

			// Verify the echo response contains our input
			GinkgoWriter.Printf("Tool call result: %+v\n", result)
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")
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
			By("Waiting for MCPServer to transition to Failed state")
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
			}, timeout, pollingInterval).Should(Equal(mcpv1alpha1.MCPServerPhaseFailed),
				"MCPServer should transition to Failed when image is invalid")

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
			Expect(vmcpServer.Status.BackendCount).To(Equal(1), "Should have 1 ready backend")

			By("Verifying discovered backends list shows one unavailable backend")
			Expect(vmcpServer.Status.DiscoveredBackends).To(HaveLen(2), "Should track both backends")

			// Check that one backend is unavailable and one is ready
			backendStatuses := make(map[string]string)
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				backendStatuses[backend.Name] = backend.Status
			}
			Expect(backendStatuses[backend1Name]).To(Equal("unavailable"))
			Expect(backendStatuses[backend2Name]).To(Equal("ready"))

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
				backend.Spec.Image = YardstickImage
				return k8sClient.Update(ctx, backend)
			}, timeout, pollingInterval).Should(Succeed())
			By("Deleting the StatefulSet pod to force recreation with new image")
			// StatefulSets don't automatically recreate pods when spec changes
			// We need to delete the pod to force it to be recreated with the new image
			podToDelete := &corev1.Pod{}
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend1Name + "-0", // StatefulSet pod name
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
			Expect(vmcpServer.Status.BackendCount).To(Equal(2), "Should have 2 ready backends")
			Expect(vmcpServer.Status.DiscoveredBackends).To(HaveLen(2), "Should track both backends")

			restoredBackendStatuses := make(map[string]string)
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				restoredBackendStatuses[backend.Name] = backend.Status
			}
			Expect(restoredBackendStatuses[backend1Name]).To(Equal("ready"))
			Expect(restoredBackendStatuses[backend2Name]).To(Equal("ready"))
		})
	})
})
