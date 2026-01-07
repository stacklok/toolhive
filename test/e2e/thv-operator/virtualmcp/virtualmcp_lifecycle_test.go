package virtualmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
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
				Config: vmcpconfig.Config{Group: mcpGroupName},
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

// ReadinessResponse represents the /readyz endpoint response
type ReadinessResponse struct {
	Status string `json:"status"`
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}

// VirtualMCPServer K8s Manager Infrastructure Tests
// These tests verify the K8s manager integration that was implemented as part of THV-2884.
// Unlike the dynamic backend tests above (which are Pending until watcher is implemented),
// these tests verify the infrastructure is in place: manager creation, readiness probes,
// and endpoint behavior.
var _ = Describe("VirtualMCPServer K8s Manager Infrastructure", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-k8s-manager-infra-group"
		vmcpServerName  = "test-vmcp-k8s-manager-infra"
		backendName     = "backend-k8s-manager-infra-fetch"
		timeout         = 3 * time.Minute
		pollingInterval = 2 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for K8s manager infrastructure tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for K8s manager infrastructure E2E tests", timeout, pollingInterval)

		By("Creating backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
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
		Expect(k8sClient.Create(ctx, backend)).To(Succeed())

		By("Waiting for backend MCPServer to be ready")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backendName,
				Namespace: testNamespace,
			}, server)
			if err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}

			if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
				return nil
			}
			return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
		}, timeout, pollingInterval).Should(Succeed(), "Backend should be ready")

		By("Creating VirtualMCPServer with discovered auth source (dynamic mode)")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				Config: vmcpconfig.Config{Group: mcpGroupName},
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered", // This triggers K8s manager creation
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

		By(fmt.Sprintf("VirtualMCPServer is ready on NodePort: %d", vmcpNodePort))

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
		_ = k8sClient.Delete(ctx, vmcpServer)

		By("Cleaning up backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, backend)

		By("Cleaning up MCPGroup")
		group := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, group)
	})

	Context("Readiness Probe Integration", func() {
		It("should expose /readyz endpoint", func() {
			vmcpURL := fmt.Sprintf("http://localhost:%d", vmcpNodePort)

			By("Checking /readyz endpoint is accessible")
			Eventually(func() error {
				resp, err := http.Get(vmcpURL + "/readyz")
				if err != nil {
					return fmt.Errorf("failed to connect to /readyz: %w", err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					body, _ := io.ReadAll(resp.Body)
					return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
				}

				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "/readyz should return 200 OK")
		})

		It("should return dynamic mode status", func() {
			vmcpURL := fmt.Sprintf("http://localhost:%d", vmcpNodePort)

			By("Getting /readyz response")
			resp, err := http.Get(vmcpURL + "/readyz")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			By("Parsing readiness response")
			var readiness ReadinessResponse
			err = json.NewDecoder(resp.Body).Decode(&readiness)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying dynamic mode is enabled")
			Expect(readiness.Status).To(Equal("ready"), "Status should be ready")
			Expect(readiness.Mode).To(Equal("dynamic"), "Mode should be dynamic since outgoingAuth.source is 'discovered'")
		})

		It("should indicate cache sync in dynamic mode", func() {
			vmcpURL := fmt.Sprintf("http://localhost:%d", vmcpNodePort)

			By("Verifying cache is synced")
			resp, err := http.Get(vmcpURL + "/readyz")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			var readiness ReadinessResponse
			err = json.NewDecoder(resp.Body).Decode(&readiness)
			Expect(err).NotTo(HaveOccurred())

			// In dynamic mode with synced cache, status should be "ready"
			Expect(readiness.Status).To(Equal("ready"))
			Expect(readiness.Mode).To(Equal("dynamic"))
			// Reason should be empty when ready
			Expect(readiness.Reason).To(BeEmpty())
		})
	})

	Context("K8s Manager Lifecycle", func() {
		It("should start with K8s manager running", func() {
			By("Verifying pod is running")
			Eventually(func() error {
				pods := &corev1.PodList{}
				err := k8sClient.List(ctx, pods,
					ctrlclient.InNamespace(testNamespace),
					ctrlclient.MatchingLabels{"app.kubernetes.io/instance": vmcpServerName})
				if err != nil {
					return fmt.Errorf("failed to list pods: %w", err)
				}

				if len(pods.Items) == 0 {
					return fmt.Errorf("no pods found")
				}

				pod := pods.Items[0]
				if pod.Status.Phase != corev1.PodRunning {
					return fmt.Errorf("pod not running yet, phase: %s", pod.Status.Phase)
				}

				// Check pod is ready
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady {
						if condition.Status != corev1.ConditionTrue {
							return fmt.Errorf("pod not ready: %s", condition.Message)
						}
						return nil
					}
				}

				return fmt.Errorf("pod ready condition not found")
			}, timeout, pollingInterval).Should(Succeed(), "Pod should be running and ready")
		})

		It("should have healthy container status", func() {
			By("Getting pod name")
			pods := &corev1.PodList{}
			err := k8sClient.List(ctx, pods,
				ctrlclient.InNamespace(testNamespace),
				ctrlclient.MatchingLabels{"app.kubernetes.io/instance": vmcpServerName})
			Expect(err).NotTo(HaveOccurred())
			Expect(pods.Items).NotTo(BeEmpty(), "Should have at least one pod")

			podName := pods.Items[0].Name

			By("Checking container status")
			Eventually(func() error {
				pod := &corev1.Pod{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      podName,
					Namespace: testNamespace,
				}, pod)
				if err != nil {
					return err
				}

				// Check all containers are ready
				for _, status := range pod.Status.ContainerStatuses {
					if !status.Ready {
						return fmt.Errorf("container %s not ready", status.Name)
					}
				}

				return nil
			}, timeout, pollingInterval).Should(Succeed(), "All containers should be ready")
		})
	})

	Context("Health Endpoints", func() {
		It("should expose /health endpoint that always returns 200", func() {
			vmcpURL := fmt.Sprintf("http://localhost:%d", vmcpNodePort)

			By("Checking /health endpoint")
			resp, err := http.Get(vmcpURL + "/health")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var health map[string]string
			err = json.NewDecoder(resp.Body).Decode(&health)
			Expect(err).NotTo(HaveOccurred())
			Expect(health["status"]).To(Equal("ok"))
		})

		It("should distinguish between /health and /readyz", func() {
			vmcpURL := fmt.Sprintf("http://localhost:%d", vmcpNodePort)

			By("Getting /health response")
			healthResp, err := http.Get(vmcpURL + "/health")
			Expect(err).NotTo(HaveOccurred())
			defer healthResp.Body.Close()

			By("Getting /readyz response")
			readyResp, err := http.Get(vmcpURL + "/readyz")
			Expect(err).NotTo(HaveOccurred())
			defer readyResp.Body.Close()

			// Both should return 200 when ready
			Expect(healthResp.StatusCode).To(Equal(http.StatusOK))
			Expect(readyResp.StatusCode).To(Equal(http.StatusOK))

			// Parse both responses
			var health map[string]string
			err = json.NewDecoder(healthResp.Body).Decode(&health)
			Expect(err).NotTo(HaveOccurred())

			var readiness ReadinessResponse
			err = json.NewDecoder(readyResp.Body).Decode(&readiness)
			Expect(err).NotTo(HaveOccurred())

			// Health is simple status
			Expect(health).To(HaveKey("status"))
			Expect(health).NotTo(HaveKey("mode"))

			// Readiness includes mode information
			Expect(readiness.Status).To(Equal("ready"))
			Expect(readiness.Mode).To(Equal("dynamic"))
		})
	})

	Context("Status Endpoint", func() {
		It("should expose /status endpoint with group reference", func() {
			vmcpURL := fmt.Sprintf("http://localhost:%d", vmcpNodePort)

			By("Checking /status endpoint")
			resp, err := http.Get(vmcpURL + "/status")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var status map[string]interface{}
			err = json.NewDecoder(resp.Body).Decode(&status)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying group_ref is present")
			Expect(status).To(HaveKey("group_ref"))
			groupRef, ok := status["group_ref"].(string)
			Expect(ok).To(BeTrue())
			Expect(groupRef).To(ContainSubstring(mcpGroupName))
		})

		It("should list discovered backends", func() {
			vmcpURL := fmt.Sprintf("http://localhost:%d", vmcpNodePort)

			By("Getting /status response")
			resp, err := http.Get(vmcpURL + "/status")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			var status map[string]interface{}
			err = json.NewDecoder(resp.Body).Decode(&status)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying backends are listed")
			Expect(status).To(HaveKey("backends"))
			backends, ok := status["backends"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(backends).NotTo(BeEmpty(), "Should have at least one backend")

			// Verify backend structure
			backend := backends[0].(map[string]interface{})
			Expect(backend).To(HaveKey("name"))
			Expect(backend).To(HaveKey("health"))
			Expect(backend).To(HaveKey("transport"))
		})
	})
})
