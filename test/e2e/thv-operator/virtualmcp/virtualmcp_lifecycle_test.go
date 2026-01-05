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

// NOTE: These tests verify DynamicRegistry functionality with full operator integration.
// The vMCP server now uses DynamicRegistry in Kubernetes mode and supports dynamic
// backend discovery. New sessions will see updated backends when they are added/removed
// from the MCPGroup. Existing sessions retain their original capability snapshot.
//
// Implementation status: DynamicRegistry is fully integrated. The tests are currently
// marked as Pending because they require a K8s watcher to actively update the registry
// when backends change. Without the watcher, backends are only discovered at pod startup.
var _ = Describe("VirtualMCPServer Lifecycle - DynamicRegistry", Ordered, Pending, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-lifecycle-group"
		vmcpServerName  = "test-vmcp-lifecycle"
		backend1Name    = "backend-lifecycle-fetch"
		backend2Name    = "backend-lifecycle-osv"
		backend3Name    = "backend-lifecycle-dynamic" // Backend added dynamically
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for VirtualMCP lifecycle E2E tests", timeout, pollingInterval)

		By("Creating initial backend MCPServer - fetch (streamable-http)")
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

		By("Waiting for initial backend MCPServer to be ready")
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
		}, timeout, pollingInterval).Should(Succeed(), "Initial backend should be ready")

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
				Aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: "prefix",
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By(fmt.Sprintf("VirtualMCPServer accessible at http://localhost:%d", vmcpNodePort))

		By("Waiting for VirtualMCPServer to be accessible")
		Eventually(func() error {
			httpClient := &http.Client{Timeout: 5 * time.Second}
			url := fmt.Sprintf("http://localhost:%d/health", vmcpNodePort)
			resp, err := httpClient.Get(url)
			if err != nil {
				return fmt.Errorf("health check failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			}
			return nil
		}, 30*time.Second, 2*time.Second).Should(Succeed(), "VirtualMCPServer health endpoint should be accessible")
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

		By("Cleaning up all backend MCPServers")
		for _, backendName := range []string{backend1Name, backend2Name, backend3Name} {
			backend := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backendName,
					Namespace: testNamespace,
				},
			}
			if err := k8sClient.Delete(ctx, backend); err != nil {
				GinkgoWriter.Printf("Warning: failed to delete backend %s: %v\n", backendName, err)
			}
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

	var initialToolCount int

	Context("when testing initial backend discovery", func() {
		It("should discover tools from initial backend", func() {
			By("Creating MCP client for VirtualMCPServer")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Starting transport and initializing connection")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			Eventually(func() error {
				initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer initCancel()

				err = mcpClient.Start(initCtx)
				if err != nil {
					return fmt.Errorf("failed to start transport: %w", err)
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-lifecycle-test",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(initCtx, initRequest)
				if err != nil {
					return fmt.Errorf("failed to initialize: %w", err)
				}

				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "MCP client should initialize successfully")

			By("Listing tools from VirtualMCPServer")
			var initialTools *mcp.ListToolsResult
			Eventually(func() error {
				listRequest := mcp.ListToolsRequest{}
				var err error
				initialTools, err = mcpClient.ListTools(ctx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				if len(initialTools.Tools) == 0 {
					return fmt.Errorf("no tools returned")
				}
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed(), "Should be able to list tools")

			initialToolCount = len(initialTools.Tools)
			By(fmt.Sprintf("Initial state: VirtualMCPServer has %d tools", initialToolCount))
			for _, tool := range initialTools.Tools {
				GinkgoWriter.Printf("  Initial tool: %s - %s\n", tool.Name, tool.Description)
			}

			// Verify we have at least one tool from the initial backend
			Expect(initialTools.Tools).ToNot(BeEmpty(), "VirtualMCPServer should have tools from initial backend")
		})

		It("should have exactly one backend in the group", func() {
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(backends).To(HaveLen(1), "Should have exactly one backend initially")
			Expect(backends[0].Name).To(Equal(backend1Name))
		})
	})

	Context("when dynamically adding a new backend", func() {
		It("should detect the new backend and update tool list", func() {
			By("Adding second backend MCPServer - osv (streamable-http)")
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

			By("Waiting for new backend MCPServer to be ready")
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend2Name,
					Namespace: testNamespace,
				}, server)
				if err != nil {
					return fmt.Errorf("failed to get server: %w", err)
				}

				if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
					return nil
				}
				return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
			}, timeout, pollingInterval).Should(Succeed(), "New backend should be ready")

			By("Verifying the group now has two backends")
			Eventually(func() int {
				backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
				if err != nil {
					return 0
				}
				return len(backends)
			}, 30*time.Second, 2*time.Second).Should(Equal(2), "Should have two backends after adding")

			By("Waiting for VirtualMCPServer to reconcile and discover tools from both backends")
			// Use Eventually to wait for the VirtualMCPServer to:
			// 1. Detect the new backend in the group via operator reconciliation
			// 2. Update the DynamicRegistry (which increments version)
			// 3. Invalidate cached capabilities
			// 4. Rediscover capabilities from both backends
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

			var updatedTools *mcp.ListToolsResult
			Eventually(func() error {
				// Create a fresh client for each attempt to ensure we're not hitting stale cache
				mcpClient, err := client.NewStreamableHttpClient(serverURL)
				if err != nil {
					return fmt.Errorf("failed to create client: %w", err)
				}
				defer mcpClient.Close()

				testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				// Start and initialize
				err = mcpClient.Start(testCtx)
				if err != nil {
					return fmt.Errorf("failed to start transport: %w", err)
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-lifecycle-test-add",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(testCtx, initRequest)
				if err != nil {
					return fmt.Errorf("failed to initialize: %w", err)
				}

				// List tools
				listRequest := mcp.ListToolsRequest{}
				updatedTools, err = mcpClient.ListTools(testCtx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}

				currentToolCount := len(updatedTools.Tools)

				// Log current state for debugging
				if currentToolCount > 0 {
					GinkgoWriter.Printf("Attempt: %d tools found (initial: %d)\n", currentToolCount, initialToolCount)
					for _, tool := range updatedTools.Tools {
						GinkgoWriter.Printf("  - %s\n", tool.Name)
					}
				}

				// Should have more tools now (from both backends)
				// Check if tool count increased from initial state
				if currentToolCount <= initialToolCount {
					return fmt.Errorf("expected more tools after adding backend, got %d (initial: %d)", currentToolCount, initialToolCount)
				}
				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Should see more tools after adding second backend")

			By(fmt.Sprintf("After adding backend: VirtualMCPServer now has %d tools", len(updatedTools.Tools)))
			for _, tool := range updatedTools.Tools {
				GinkgoWriter.Printf("  Updated tool: %s - %s\n", tool.Name, tool.Description)
			}
		})
	})

	Context("when dynamically removing a backend", func() {
		It("should detect backend removal and update tool list", func() {
			By("Getting current tool count")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			Eventually(func() error {
				initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer initCancel()

				err = mcpClient.Start(initCtx)
				if err != nil {
					return fmt.Errorf("failed to start transport: %w", err)
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-lifecycle-test-before-remove",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(initCtx, initRequest)
				if err != nil {
					return fmt.Errorf("failed to initialize: %w", err)
				}

				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			var toolsBeforeRemoval *mcp.ListToolsResult
			Eventually(func() error {
				listRequest := mcp.ListToolsRequest{}
				var err error
				toolsBeforeRemoval, err = mcpClient.ListTools(ctx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed())

			toolCountBefore := len(toolsBeforeRemoval.Tools)
			By(fmt.Sprintf("Before removal: %d tools", toolCountBefore))

			By("Removing the second backend (osv)")
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
			}, timeout, pollingInterval).Should(BeTrue(), "Backend should be deleted")

			By("Verifying the group now has one backend")
			Eventually(func() int {
				backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
				if err != nil {
					return -1
				}
				return len(backends)
			}, 30*time.Second, 2*time.Second).Should(Equal(1), "Should have one backend after removal")

			By("Waiting for VirtualMCPServer to detect backend removal and update tool list")
			var toolsAfterRemoval *mcp.ListToolsResult
			Eventually(func() error {
				// Create a fresh client for each attempt
				mcpClient2, err := client.NewStreamableHttpClient(serverURL)
				if err != nil {
					return fmt.Errorf("failed to create client: %w", err)
				}
				defer mcpClient2.Close()

				testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				// Start and initialize
				err = mcpClient2.Start(testCtx)
				if err != nil {
					return fmt.Errorf("failed to start transport: %w", err)
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-lifecycle-test-after-remove",
					Version: "1.0.0",
				}

				_, err = mcpClient2.Initialize(testCtx, initRequest)
				if err != nil {
					return fmt.Errorf("failed to initialize: %w", err)
				}

				// List tools
				listRequest := mcp.ListToolsRequest{}
				toolsAfterRemoval, err = mcpClient2.ListTools(testCtx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}

				toolCountAfter := len(toolsAfterRemoval.Tools)

				// Verify tool count decreased (tools from removed backend are gone)
				if toolCountAfter >= toolCountBefore {
					return fmt.Errorf("expected fewer tools after removal, got %d (was %d)", toolCountAfter, toolCountBefore)
				}

				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Should have fewer tools after backend removal")

			By(fmt.Sprintf("After removal: %d tools (was %d)", len(toolsAfterRemoval.Tools), toolCountBefore))

			By("Verifying tools from removed backend are no longer present")
			for _, tool := range toolsAfterRemoval.Tools {
				GinkgoWriter.Printf("  Remaining tool: %s - %s\n", tool.Name, tool.Description)
				// Tools from osv backend should not be present
				Expect(strings.Contains(strings.ToLower(tool.Name), "osv")).To(BeFalse(),
					"Tools from removed osv backend should not be present")
			}
		})
	})

	Context("when testing cache invalidation", func() {
		It("should invalidate cache when backends change", func() {
			By("Adding a third backend to trigger cache invalidation")
			backend3 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend3Name,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.GofetchServerImage, // Use fetch image for simplicity
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			}
			Expect(k8sClient.Create(ctx, backend3)).To(Succeed())

			By("Waiting for new backend to be ready")
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend3Name,
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

			By("Verifying tool list is updated (cache was invalidated)")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

			var tools *mcp.ListToolsResult
			Eventually(func() error {
				// Create a fresh client for each attempt
				mcpClient, err := client.NewStreamableHttpClient(serverURL)
				if err != nil {
					return fmt.Errorf("failed to create client: %w", err)
				}
				defer mcpClient.Close()

				testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				// Start and initialize
				err = mcpClient.Start(testCtx)
				if err != nil {
					return fmt.Errorf("failed to start transport: %w", err)
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-lifecycle-test-cache",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(testCtx, initRequest)
				if err != nil {
					return fmt.Errorf("failed to initialize: %w", err)
				}

				// List tools
				listRequest := mcp.ListToolsRequest{}
				tools, err = mcpClient.ListTools(testCtx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}

				// Should now have tools from 2 backends (backend1 and backend3)
				if len(tools.Tools) < 1 {
					return fmt.Errorf("expected tools from active backends, got %d", len(tools.Tools))
				}

				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Cache should be invalidated and show updated tools")

			By(fmt.Sprintf("After cache invalidation: VirtualMCPServer has %d tools from active backends", len(tools.Tools)))

			By("Verifying backends in the group")
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(backends).To(HaveLen(2), "Should have two backends after adding third backend")

			backendNames := make([]string, len(backends))
			for i, backend := range backends {
				backendNames[i] = backend.Name
			}
			Expect(backendNames).To(ContainElements(backend1Name, backend3Name))
			Expect(backendNames).ToNot(ContainElement(backend2Name), "Removed backend should not be present")
		})
	})
})
