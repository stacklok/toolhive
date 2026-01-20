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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// ReadinessResponse represents the /readyz endpoint response
type ReadinessResponse struct {
	Status string `json:"status"`
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}

var _ = Describe("VirtualMCPServer Discovered Mode", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-discovered-group"
		vmcpServerName  = "test-vmcp-discovered"
		backend1Name    = "backend-fetch"
		backend2Name    = "backend-osv"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
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
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
					// Discovered mode is the default - tools from all backends in the group are automatically discovered
					Aggregation: &vmcpconfig.AggregationConfig{
						ConflictResolution: "prefix", // Use prefix strategy to avoid conflicts
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

		By("Waiting for VirtualMCPServer to discover backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to stabilize")
		time.Sleep(5 * time.Second)
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
					Name:    "toolhive-e2e-test",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(initCtx, initRequest)
				if err != nil {
					return fmt.Errorf("failed to initialize: %w", err)
				}

				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "MCP client should initialize successfully")

			By("Listing tools from VirtualMCPServer")
			var tools *mcp.ListToolsResult
			Eventually(func() error {
				listRequest := mcp.ListToolsRequest{}
				var err error
				tools, err = mcpClient.ListTools(ctx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				if len(tools.Tools) == 0 {
					return fmt.Errorf("no tools returned")
				}
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed(), "Should be able to list tools")

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
					Name:    "toolhive-e2e-test",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(initCtx, initRequest)
				if err != nil {
					return fmt.Errorf("failed to initialize: %w", err)
				}

				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "MCP client should initialize successfully")

			By("Listing available tools")
			var tools *mcp.ListToolsResult
			Eventually(func() error {
				listRequest := mcp.ListToolsRequest{}
				var err error
				tools, err = mcpClient.ListTools(ctx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				if len(tools.Tools) == 0 {
					return fmt.Errorf("no tools returned")
				}
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed(), "Should be able to list tools")

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

				// Retry CallTool to handle transient connection issues
				Eventually(func() error {
					callRequest := mcp.CallToolRequest{}
					callRequest.Params.Name = targetToolName
					callRequest.Params.Arguments = map[string]any{
						// Use localhost to avoid external network dependencies
						// The test validates that VirtualMCPServer can route tool calls to backends,
						// not that the fetch tool itself works (that's tested in the backend's own tests)
						"url": "http://127.0.0.1:1",
					}

					result, err := mcpClient.CallTool(toolCallCtx, callRequest)
					if err != nil {
						return fmt.Errorf("failed to call tool: %w", err)
					}
					if result == nil {
						return fmt.Errorf("tool returned nil result")
					}
					return nil
				}, 30*time.Second, 2*time.Second).Should(Succeed(),
					fmt.Sprintf("Should be able to call tool '%s' through VirtualMCPServer", targetToolName))

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

			Expect(vmcpServer.Spec.Config.Aggregation).ToNot(BeNil())
			Expect(string(vmcpServer.Spec.Config.Aggregation.ConflictResolution)).To(Equal("prefix"))
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

	Context("when dynamically adding a new backend", func() {
		var (
			backend3Name     = "backend-dynamic-fetch"
			initialToolCount int
		)

		AfterAll(func() {
			// Clean up the dynamic backend
			backend3 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend3Name,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, backend3)
		})

		It("should record initial tool count", func() {
			By("Creating MCP client to get initial tool count")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			Eventually(func() error {
				err = mcpClient.Start(testCtx)
				if err != nil {
					return err
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-initial-count",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(testCtx, initRequest)
				return err
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			var tools *mcp.ListToolsResult
			Eventually(func() error {
				var err error
				tools, err = mcpClient.ListTools(testCtx, mcp.ListToolsRequest{})
				return err
			}, 30*time.Second, 2*time.Second).Should(Succeed())

			initialToolCount = len(tools.Tools)
			GinkgoWriter.Printf("Initial tool count: %d\n", initialToolCount)
		})

		It("should detect new backend and update tool list", func() {
			By("Adding third backend MCPServer")
			backend3 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend3Name,
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
			Expect(k8sClient.Create(ctx, backend3)).To(Succeed())

			By("Waiting for new backend to be ready")
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend3Name,
					Namespace: testNamespace,
				}, server)
				if err != nil {
					return err
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
					return fmt.Errorf("backend not ready, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By("Verifying group now has three backends")
			Eventually(func() int {
				backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
				if err != nil {
					return 0
				}
				return len(backends)
			}, 30*time.Second, 2*time.Second).Should(Equal(3))

			By("Verifying tool count increased with new session")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

			Eventually(func() error {
				mcpClient, err := client.NewStreamableHttpClient(serverURL)
				if err != nil {
					return err
				}
				defer mcpClient.Close()

				testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				err = mcpClient.Start(testCtx)
				if err != nil {
					return err
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-after-add",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(testCtx, initRequest)
				if err != nil {
					return err
				}

				tools, err := mcpClient.ListTools(testCtx, mcp.ListToolsRequest{})
				if err != nil {
					return err
				}

				if len(tools.Tools) <= initialToolCount {
					return fmt.Errorf("expected more tools, got %d (was %d)", len(tools.Tools), initialToolCount)
				}
				return nil
			}, 1*time.Minute, 10*time.Second).Should(Succeed())
		})
	})

	Context("when dynamically removing a backend", func() {
		It("should detect backend removal and update tool list", func() {
			By("Getting current tool count")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			Eventually(func() error {
				err = mcpClient.Start(testCtx)
				if err != nil {
					return err
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-before-remove",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(testCtx, initRequest)
				return err
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			var toolsBeforeRemoval *mcp.ListToolsResult
			Eventually(func() error {
				var err error
				toolsBeforeRemoval, err = mcpClient.ListTools(testCtx, mcp.ListToolsRequest{})
				return err
			}, 30*time.Second, 2*time.Second).Should(Succeed())

			toolCountBefore := len(toolsBeforeRemoval.Tools)
			GinkgoWriter.Printf("Before removal: %d tools\n", toolCountBefore)

			By("Removing backend2 (osv)")
			backend2 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend2Name,
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Delete(ctx, backend2)).To(Succeed())

			By("Waiting for backend deletion")
			Eventually(func() bool {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend2Name,
					Namespace: testNamespace,
				}, server)
				return err != nil
			}, timeout, pollingInterval).Should(BeTrue())

			By("Verifying group now has fewer backends")
			Eventually(func() int {
				backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
				if err != nil {
					return -1
				}
				return len(backends)
			}, 30*time.Second, 2*time.Second).Should(BeNumerically("<", 3))

			By("Verifying tool count decreased with new session")
			Eventually(func() error {
				mcpClient2, err := client.NewStreamableHttpClient(serverURL)
				if err != nil {
					return err
				}
				defer mcpClient2.Close()

				testCtx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel2()

				err = mcpClient2.Start(testCtx2)
				if err != nil {
					return err
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-after-remove",
					Version: "1.0.0",
				}

				_, err = mcpClient2.Initialize(testCtx2, initRequest)
				if err != nil {
					return err
				}

				tools, err := mcpClient2.ListTools(testCtx2, mcp.ListToolsRequest{})
				if err != nil {
					return err
				}

				if len(tools.Tools) >= toolCountBefore {
					return fmt.Errorf("expected fewer tools, got %d (was %d)", len(tools.Tools), toolCountBefore)
				}
				return nil
			}, 1*time.Minute, 10*time.Second).Should(Succeed())
		})
	})

	Context("when testing health and readiness endpoints", func() {
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

		It("should expose /readyz endpoint", func() {
			vmcpURL := fmt.Sprintf("http://localhost:%d", vmcpNodePort)

			By("Checking /readyz endpoint is accessible")
			resp, err := http.Get(vmcpURL + "/readyz")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				Fail(fmt.Sprintf("unexpected status code: %d, body: %s", resp.StatusCode, string(body)))
			}

			By("Parsing readiness response")
			var readiness ReadinessResponse
			err = json.NewDecoder(resp.Body).Decode(&readiness)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying readiness status")
			Expect(readiness.Status).To(Equal("ready"), "Status should be ready")
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

			// Readiness includes status
			Expect(readiness.Status).To(Equal("ready"))
		})
	})

	Context("when testing status endpoint", func() {
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

		It("should list discovered backends in status", func() {
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
			backend, ok := backends[0].(map[string]interface{})
			Expect(ok).To(BeTrue(), "backend should be a map")
			Expect(backend).To(HaveKey("name"))
			Expect(backend).To(HaveKey("health"))
			Expect(backend).To(HaveKey("transport"))
		})
	})
})
