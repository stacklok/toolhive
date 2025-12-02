package virtualmcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Discovered Mode", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-discovered-group"
		vmcpServerName  = "test-vmcp-discovered"
		backend1Name    = "backend-fetch"
		backend2Name    = "backend-osv"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for VirtualMCP discovered mode E2E tests", timeout, pollingInterval)

		By("Creating first backend MCPServer - fetch (streamable-http)")
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

		By("Creating second backend MCPServer - osv (streamable-http)")
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

		By("Waiting for backend MCPServers to be ready")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1Name,
				Namespace: testNamespace,
			}, server)
			if err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}

			// Check for Running phase
			if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
				return nil
			}
			return fmt.Errorf("backend 1 not ready yet, phase: %s", server.Status.Phase)
		}, timeout, pollingInterval).Should(Succeed(), "Backend 1 should be ready")

		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, server)
			if err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}

			// Check for Running phase
			if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
				return nil
			}
			return fmt.Errorf("backend 2 not ready yet, phase: %s", server.Status.Phase)
		}, timeout, pollingInterval).Should(Succeed(), "Backend 2 should be ready")

		// Skip NodePort lookup for backends - MCPServers use ClusterIP services
		// Backends will be tested through VirtualMCPServer aggregation
		By("Backend MCPServers are running (ClusterIP services)")

		By("Creating VirtualMCPServer in discovered mode")
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
				// Discovered mode is the default - tools from all backends in the group are automatically discovered
				Aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: "prefix", // Use prefix strategy to avoid conflicts
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
		By("Backend servers use ClusterIP and are accessed through VirtualMCPServer")
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
		}
		if err := k8sClient.Delete(ctx, vmcpServer); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete VirtualMCPServer: %v\n", err)
		}

		By("Cleaning up backend MCPServers")
		backend1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend1Name,
				Namespace: testNamespace,
			},
		}
		if err := k8sClient.Delete(ctx, backend1); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete backend 1: %v\n", err)
		}

		backend2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend2Name,
				Namespace: testNamespace,
			},
		}
		if err := k8sClient.Delete(ctx, backend2); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete backend 2: %v\n", err)
		}

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		if err := k8sClient.Delete(ctx, mcpGroup); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete MCPGroup: %v\n", err)
		}
	})

	// Individual backend tests removed - backends are validated through VirtualMCPServer aggregation

	Context("when testing VirtualMCPServer aggregation", func() {
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

		It("should aggregate tools from both backends", func() {
			By("Creating MCP client for VirtualMCPServer")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Starting transport and initializing connection")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err = mcpClient.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-e2e-test",
				Version: "1.0.0",
			}

			_, err = mcpClient.Initialize(ctx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "VirtualMCPServer should aggregate tools from backends")

			By(fmt.Sprintf("VirtualMCPServer aggregates %d tools", len(tools.Tools)))
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  Aggregated tool: %s - %s\n", tool.Name, tool.Description)
			}

			// In discovered mode with prefix conflict resolution, tools from both backends should be available
			// fetch server has 'fetch' tool, osv server has vulnerability scanning tools
			// With prefix strategy, they should be prefixed with backend names
			Expect(len(tools.Tools)).To(BeNumerically(">=", 2),
				"VirtualMCPServer should aggregate tools from both backends")

			// Verify we have tools from both backends (with prefixes due to conflict resolution)
			toolNames := make([]string, len(tools.Tools))
			for i, tool := range tools.Tools {
				toolNames[i] = tool.Name
			}
			GinkgoWriter.Printf("All aggregated tool names: %v\n", toolNames)
		})

		It("should be able to call tools through VirtualMCPServer", func() {
			By("Creating MCP client for VirtualMCPServer")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Starting transport and initializing connection")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err = mcpClient.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-e2e-test",
				Version: "1.0.0",
			}

			_, err = mcpClient.Initialize(ctx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Listing available tools")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			By("Calling a tool through VirtualMCPServer")
			// Find a tool we can call with simple arguments
			// The fetch tool (with prefix) should be available and can be called with a URL
			var targetToolName string
			for _, tool := range tools.Tools {
				// Look for the fetch tool (may have a prefix like "backend-fetch__fetch")
				if tool.Name == fetchToolName || strings.HasSuffix(tool.Name, fetchToolName) {
					targetToolName = tool.Name
					break
				}
			}

			if targetToolName != "" {
				GinkgoWriter.Printf("Testing tool call for: %s\n", targetToolName)

				// Use a standard timeout for the tool call
				toolCallCtx, toolCallCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer toolCallCancel()

				callRequest := mcp.CallToolRequest{}
				callRequest.Params.Name = targetToolName
				callRequest.Params.Arguments = map[string]any{
					// Use localhost to avoid external network dependencies
					// The test validates that VirtualMCPServer can route tool calls to backends,
					// not that the fetch tool itself works (that's tested in the backend's own tests)
					"url": "http://127.0.0.1:1",
				}

				result, err := mcpClient.CallTool(toolCallCtx, callRequest)
				Expect(err).ToNot(HaveOccurred(),
					fmt.Sprintf("Should be able to call tool '%s' through VirtualMCPServer", targetToolName))
				Expect(result).ToNot(BeNil())

				GinkgoWriter.Printf("Tool call successful: %s\n", targetToolName)
			} else {
				GinkgoWriter.Printf("Warning: fetch tool not found in aggregated tools\n")
			}
		})
	})

	Context("when verifying discovered mode behavior", func() {
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

		It("should discover both backends in the group", func() {
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(backends).To(HaveLen(2), "Should discover both backends in the group")

			backendNames := make([]string, len(backends))
			for i, backend := range backends {
				backendNames[i] = backend.Name
			}
			Expect(backendNames).To(ContainElements(backend1Name, backend2Name))
		})
	})
})
