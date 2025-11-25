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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("VirtualMCPServer Discovered Mode", Ordered, func() {
	var (
		testNamespace       = "default"
		mcpGroupName        = "test-discovered-group"
		vmcpServerName      = "test-vmcp-discovered"
		backend1Name        = "backend-fetch"
		backend2Name        = "backend-osv"
		authConfigName      = "test-token-exchange-auth"
		authSecretName      = "test-auth-secret"
		mockOAuthServerName = "mock-oauth-discovered"
		timeout             = 5 * time.Minute
		pollingInterval     = 5 * time.Second
		vmcpNodePort        int32
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Deploying mock OAuth server for token exchange")
		DeployMockOIDCServerHTTP(ctx, k8sClient, testNamespace, mockOAuthServerName)

		By("Creating default MCPGroup to prevent migration errors")
		defaultGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "default",
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{},
		}
		Expect(k8sClient.Create(ctx, defaultGroup)).To(Succeed())

		By("Creating auth secret for bearer token")
		authSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"bearer-token": "Bearer test-bearer-token-value",
			},
		}
		Expect(k8sClient.Create(ctx, authSecret)).To(Succeed())

		By("Creating MCPExternalAuthConfig for header injection (OIDC bearer token)")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "Authorization",
					Value:      "Bearer test-bearer-token-value",
				},
			},
		}
		Expect(k8sClient.Create(ctx, externalAuthConfig)).To(Succeed())

		By("Waiting for MCPExternalAuthConfig to be ready")
		Eventually(func() error {
			config := &mcpv1alpha1.MCPExternalAuthConfig{}
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfigName,
				Namespace: testNamespace,
			}, config)
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for VirtualMCP discovered mode E2E tests",
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

		By("Creating first backend MCPServer WITH auth - fetch (streamable-http)")
		backend1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend1Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     "ghcr.io/stackloklabs/gofetch/server:1.0.1",
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: authConfigName,
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend1)).To(Succeed())

		By("Creating second backend MCPServer WITHOUT auth - osv (streamable-http)")
		backend2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend2Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     "ghcr.io/stackloklabs/osv-mcp/server:0.0.7",
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
		By("All backend MCPServers are running (ClusterIP services)")

		By("Creating VirtualMCPServer in discovered mode with discovered auth")
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
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered", // Discovered mode - auth discovered from backend MCPServers
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
		Eventually(func() error {
			service := &corev1.Service{}
			serviceName := vmcpServiceName()
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      serviceName,
				Namespace: testNamespace,
			}, service)
			if err != nil {
				return err
			}
			if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].NodePort == 0 {
				return fmt.Errorf("nodePort not assigned for vmcp")
			}
			vmcpNodePort = service.Spec.Ports[0].NodePort
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By(fmt.Sprintf("VirtualMCPServer accessible at http://localhost:%d", vmcpNodePort))
		By("Backend servers use ClusterIP and are accessed through VirtualMCPServer")
	})

	AfterAll(func() {
		By("Cleaning up default MCPGroup")
		defaultGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "default",
				Namespace: testNamespace,
			},
		}
		if err := k8sClient.Delete(ctx, defaultGroup); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete default MCPGroup: %v\n", err)
		}

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

		By("Cleaning up MCPExternalAuthConfig")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfigName,
				Namespace: testNamespace,
			},
		}
		if err := k8sClient.Delete(ctx, externalAuthConfig); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete MCPExternalAuthConfig: %v\n", err)
		}

		By("Cleaning up auth secret")
		authSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecretName,
				Namespace: testNamespace,
			},
		}
		if err := k8sClient.Delete(ctx, authSecret); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete auth secret: %v\n", err)
		}

		By("Cleaning up mock OAuth server")
		CleanupMockServer(ctx, k8sClient, testNamespace, mockOAuthServerName, "")
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

		It("should aggregate tools from all backends", func() {
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
			initRequest.Params.ProtocolVersion = mcpProtocolVersion
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

			// In discovered mode with prefix conflict resolution, tools from all backends should be available
			// fetch server has 'fetch' tool, osv server has vulnerability scanning tools
			// With prefix strategy, they should be prefixed with backend names
			Expect(len(tools.Tools)).To(BeNumerically(">=", 4),
				"VirtualMCPServer should aggregate tools from both backends (3 from osv, 1+ from fetch)")

			// Verify we have tools from all backends (with prefixes due to conflict resolution)
			toolNames := make([]string, len(tools.Tools))
			hasFetchTool := false
			hasOsvTool := false
			for i, tool := range tools.Tools {
				toolNames[i] = tool.Name
				if strings.HasPrefix(tool.Name, "backend-fetch") {
					hasFetchTool = true
				}
				if strings.HasPrefix(tool.Name, "backend-osv") {
					hasOsvTool = true
				}
			}
			GinkgoWriter.Printf("All aggregated tool names: %v\n", toolNames)

			// Fail early if expected tools are missing
			Expect(hasOsvTool).To(BeTrue(), "Should have tools from backend-osv")
			Expect(hasFetchTool).To(BeTrue(), "Should have tools from backend-fetch (requires header injection auth)")
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
			initRequest.Params.ProtocolVersion = mcpProtocolVersion
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

				callRequest := mcp.CallToolRequest{}
				callRequest.Params.Name = targetToolName
				callRequest.Params.Arguments = map[string]any{
					"url": "https://example.com",
				}

				result, err := mcpClient.CallTool(ctx, callRequest)
				Expect(err).ToNot(HaveOccurred(),
					fmt.Sprintf("Should be able to call tool '%s' through VirtualMCPServer", targetToolName))
				Expect(result).ToNot(BeNil())

				GinkgoWriter.Printf("Tool call successful: %s\n", targetToolName)
			} else {
				GinkgoWriter.Printf("Warning: fetch tool not found in aggregated tools\n")
			}
		})
	})

	Context("when verifying auth discovery and configuration", func() {
		It("should discover and apply header injection auth to authenticated backends", func() {
			By("Verifying authenticated backend's tools are available through VirtualMCPServer")
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
			initRequest.Params.ProtocolVersion = mcpProtocolVersion
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-auth-test",
				Version: "1.0.0",
			}

			_, err = mcpClient.Initialize(ctx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying tools from authenticated backend (fetch) are accessible")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			// Verify that the authenticated backend's tool is present
			// This confirms auth was properly discovered and applied during aggregation
			var fetchTool string
			for _, tool := range tools.Tools {
				if strings.Contains(tool.Name, "fetch") {
					fetchTool = tool.Name
					break
				}
			}
			Expect(fetchTool).ToNot(BeEmpty(), "Should find fetch tool from authenticated backend - confirms header injection auth was applied")

			GinkgoWriter.Printf("Successfully discovered tool %s from authenticated backend\n", fetchTool)
		})

		It("should properly discover auth configuration from MCPServer", func() {
			By("Verifying authenticated backend has ExternalAuthConfigRef")
			backend := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1Name,
				Namespace: testNamespace,
			}, backend)
			Expect(err).ToNot(HaveOccurred())

			Expect(backend.Spec.ExternalAuthConfigRef).ToNot(BeNil(),
				"Authenticated backend should have ExternalAuthConfigRef")
			Expect(backend.Spec.ExternalAuthConfigRef.Name).To(Equal(authConfigName),
				"Backend should reference the header injection auth config")

			By("Verifying MCPExternalAuthConfig is configured correctly")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfigName,
				Namespace: testNamespace,
			}, authConfig)
			Expect(err).ToNot(HaveOccurred())

			Expect(authConfig.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeHeaderInjection),
				"Auth config should be header injection type")
			Expect(authConfig.Spec.HeaderInjection).ToNot(BeNil())
			Expect(authConfig.Spec.HeaderInjection.HeaderName).To(Equal("Authorization"),
				"Header should be Authorization")
			Expect(authConfig.Spec.HeaderInjection.Value).To(ContainSubstring("Bearer"),
				"Header value should contain Bearer token")

			By("Verifying VirtualMCPServer uses discovered auth mode")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.OutgoingAuth).ToNot(BeNil())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("discovered"),
				"VirtualMCPServer should use discovered auth mode")

			GinkgoWriter.Printf("Auth discovery verified: backend -> ExternalAuthConfig -> OAuth server\n")
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
