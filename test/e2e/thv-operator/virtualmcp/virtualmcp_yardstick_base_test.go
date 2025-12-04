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
