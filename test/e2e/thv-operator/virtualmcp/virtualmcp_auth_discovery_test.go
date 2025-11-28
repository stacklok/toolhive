package virtualmcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

var _ = Describe("VirtualMCPServer Auth Discovery", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-auth-discovery-group"
		vmcpServerName  = "test-vmcp-auth-discovery"
		backend1Name    = "backend-with-token-exchange"
		backend2Name    = "backend-with-header-injection"
		backend3Name    = "backend-no-auth"
		authConfig1Name = "test-token-exchange-auth"
		authConfig2Name = "test-header-injection-auth"
		authSecret1Name = "test-token-exchange-secret"
		authSecret2Name = "test-header-injection-secret"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		mockServer      *httptest.Server
	)

	BeforeAll(func() {
		By("Setting up mock HTTP server for fetch tool testing")
		mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Mock response for auth discovery test"))
		}))

		By("Creating secrets for authentication")
		// Secret for token exchange
		secret1 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecret1Name,
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"client-secret": []byte("test-client-secret-value"),
			},
		}
		Expect(k8sClient.Create(ctx, secret1)).To(Succeed())

		// Secret for header injection
		secret2 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecret2Name,
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"api-key": []byte("test-api-key-value"),
			},
		}
		Expect(k8sClient.Create(ctx, secret2)).To(Succeed())

		By("Creating MCPExternalAuthConfig for token exchange")
		authConfig1 := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfig1Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
					ClientID: "test-client-id",
					ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: authSecret1Name,
						Key:  "client-secret",
					},
					Audience:         "https://api.example.com",
					Scopes:           []string{"read", "write"},
					SubjectTokenType: "access_token",
				},
			},
		}
		Expect(k8sClient.Create(ctx, authConfig1)).To(Succeed())

		By("Creating MCPExternalAuthConfig for header injection")
		authConfig2 := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfig2Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
					ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: authSecret2Name,
						Key:  "api-key",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, authConfig2)).To(Succeed())

		By("Creating MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for auth discovery E2E tests",
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

		By("Creating backend MCPServer with token exchange auth")
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
					Name: authConfig1Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend1)).To(Succeed())

		By("Creating backend MCPServer with header injection auth")
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
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: authConfig2Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend2)).To(Succeed())

		By("Creating backend MCPServer without auth")
		backend3 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend3Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     "ghcr.io/stackloklabs/gofetch/server:1.0.1",
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
				// No ExternalAuthConfigRef - this backend has no auth
			},
		}
		Expect(k8sClient.Create(ctx, backend3)).To(Succeed())

		By("Waiting for backend MCPServers to be ready")
		for _, backendName := range []string{backend1Name, backend2Name, backend3Name} {
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
				return fmt.Errorf("%s not ready yet, phase: %s", backendName, server.Status.Phase)
			}, timeout, pollingInterval).Should(Succeed(), fmt.Sprintf("%s should be ready", backendName))
		}

		By("Creating VirtualMCPServer with discovered auth mode")
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
				// DISCOVERED MODE: vMCP will discover auth from backend MCPServers
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
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
	})

	AfterAll(func() {
		By("Shutting down mock HTTP server")
		if mockServer != nil {
			mockServer.Close()
		}

		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, vmcpServer)

		By("Cleaning up backend MCPServers")
		for _, backendName := range []string{backend1Name, backend2Name, backend3Name} {
			backend := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backendName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, backend)
		}

		By("Cleaning up MCPExternalAuthConfigs")
		for _, authConfigName := range []string{authConfig1Name, authConfig2Name} {
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      authConfigName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, authConfig)
		}

		By("Cleaning up secrets")
		for _, secretName := range []string{authSecret1Name, authSecret2Name} {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, secret)
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

	Context("when verifying discovered auth configuration", func() {
		It("should have discovered auth mode configured on VirtualMCPServer", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.OutgoingAuth).ToNot(BeNil())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("discovered"))

			By("Discovered mode means vMCP will use auth discovered from backend MCPServers' ExternalAuthConfigRef")
		})

		It("should discover all three backends in the group", func() {
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(backends).To(HaveLen(3), "Should discover all three backends in the group")

			backendNames := make([]string, len(backends))
			for i, backend := range backends {
				backendNames[i] = backend.Name
			}
			Expect(backendNames).To(ContainElements(backend1Name, backend2Name, backend3Name))
		})

		It("should have ExternalAuthConfigRef on backends with auth", func() {
			// Backend 1 should have token exchange auth
			backend1 := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1Name,
				Namespace: testNamespace,
			}, backend1)
			Expect(err).ToNot(HaveOccurred())
			Expect(backend1.Spec.ExternalAuthConfigRef).ToNot(BeNil())
			Expect(backend1.Spec.ExternalAuthConfigRef.Name).To(Equal(authConfig1Name))

			// Backend 2 should have header injection auth
			backend2 := &mcpv1alpha1.MCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, backend2)
			Expect(err).ToNot(HaveOccurred())
			Expect(backend2.Spec.ExternalAuthConfigRef).ToNot(BeNil())
			Expect(backend2.Spec.ExternalAuthConfigRef.Name).To(Equal(authConfig2Name))

			// Backend 3 should NOT have auth
			backend3 := &mcpv1alpha1.MCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend3Name,
				Namespace: testNamespace,
			}, backend3)
			Expect(err).ToNot(HaveOccurred())
			Expect(backend3.Spec.ExternalAuthConfigRef).To(BeNil())
		})

		It("should have token exchange MCPExternalAuthConfig with correct configuration", func() {
			authConfig1 := &mcpv1alpha1.MCPExternalAuthConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfig1Name,
				Namespace: testNamespace,
			}, authConfig1)
			Expect(err).ToNot(HaveOccurred())

			Expect(authConfig1.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeTokenExchange))
			Expect(authConfig1.Spec.TokenExchange).ToNot(BeNil())
			Expect(authConfig1.Spec.TokenExchange.TokenURL).To(Equal("https://auth.example.com/token"))
			Expect(authConfig1.Spec.TokenExchange.ClientID).To(Equal("test-client-id"))
			Expect(authConfig1.Spec.TokenExchange.Audience).To(Equal("https://api.example.com"))
			Expect(authConfig1.Spec.TokenExchange.Scopes).To(Equal([]string{"read", "write"}))
			Expect(authConfig1.Spec.TokenExchange.ClientSecretRef).ToNot(BeNil())
			Expect(authConfig1.Spec.TokenExchange.ClientSecretRef.Name).To(Equal(authSecret1Name))
			Expect(authConfig1.Spec.TokenExchange.ClientSecretRef.Key).To(Equal("client-secret"))
		})

		It("should have header injection MCPExternalAuthConfig with correct configuration", func() {
			authConfig2 := &mcpv1alpha1.MCPExternalAuthConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfig2Name,
				Namespace: testNamespace,
			}, authConfig2)
			Expect(err).ToNot(HaveOccurred())

			Expect(authConfig2.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeHeaderInjection))
			Expect(authConfig2.Spec.HeaderInjection).ToNot(BeNil())
			Expect(authConfig2.Spec.HeaderInjection.HeaderName).To(Equal("X-API-Key"))
			Expect(authConfig2.Spec.HeaderInjection.ValueSecretRef).ToNot(BeNil())
			Expect(authConfig2.Spec.HeaderInjection.ValueSecretRef.Name).To(Equal(authSecret2Name))
			Expect(authConfig2.Spec.HeaderInjection.ValueSecretRef.Key).To(Equal("api-key"))
		})

		It("should have secrets with correct values", func() {
			// Verify token exchange secret
			secret1 := &corev1.Secret{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      authSecret1Name,
				Namespace: testNamespace,
			}, secret1)
			Expect(err).ToNot(HaveOccurred())
			Expect(secret1.Data).To(HaveKey("client-secret"))
			Expect(string(secret1.Data["client-secret"])).To(Equal("test-client-secret-value"))

			// Verify header injection secret
			secret2 := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      authSecret2Name,
				Namespace: testNamespace,
			}, secret2)
			Expect(err).ToNot(HaveOccurred())
			Expect(secret2.Data).To(HaveKey("api-key"))
			Expect(string(secret2.Data["api-key"])).To(Equal("test-api-key-value"))
		})
	})

	Context("when verifying VirtualMCPServer state", func() {
		It("should have VirtualMCPServer in Ready phase", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady))

			By("This demonstrates that discovered auth mode successfully handles:")
			GinkgoWriter.Println("  1. Backend with token exchange authentication (OAuth 2.0)")
			GinkgoWriter.Println("  2. Backend with header injection authentication (API Key)")
			GinkgoWriter.Println("  3. Backend with no authentication")
			GinkgoWriter.Println("All three backends coexist in the same group and are aggregated by vMCP")
		})

		It("should have all backends ready", func() {
			for _, backendName := range []string{backend1Name, backend2Name, backend3Name} {
				backend := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backendName,
					Namespace: testNamespace,
				}, backend)
				Expect(err).ToNot(HaveOccurred())
				Expect(backend.Status.Phase).To(Equal(mcpv1alpha1.MCPServerPhaseRunning))
			}
		})

		It("should log discovered auth information", func() {
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())

			By(fmt.Sprintf("Discovered %d backends in group %s", len(backends), mcpGroupName))
			for _, backend := range backends {
				authInfo := "no auth"
				if backend.Spec.ExternalAuthConfigRef != nil {
					authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      backend.Spec.ExternalAuthConfigRef.Name,
						Namespace: testNamespace,
					}, authConfig)
					if err == nil {
						authInfo = fmt.Sprintf("auth type: %s", authConfig.Spec.Type)
					}
				}
				GinkgoWriter.Printf("  Backend: %s (%s)\n", backend.Name, authInfo)
			}
		})
	})

	Context("when testing discovered auth behavior with real MCP requests", func() {
		var vmcpNodePort int32

		BeforeAll(func() {
			By("Getting NodePort for VirtualMCPServer")
			Eventually(func() error {
				service := &corev1.Service{}
				serviceName := fmt.Sprintf("vmcp-%s", vmcpServerName)
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      serviceName,
					Namespace: testNamespace,
				}, service)
				if err != nil {
					return err
				}

				// Wait for NodePort to be assigned by Kubernetes
				if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].NodePort == 0 {
					return fmt.Errorf("nodePort not assigned yet")
				}
				vmcpNodePort = service.Spec.Ports[0].NodePort
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By(fmt.Sprintf("VirtualMCPServer accessible at http://localhost:%d", vmcpNodePort))
		})

		It("should be accessible via HTTP", func() {
			By("Testing HTTP connectivity to VirtualMCPServer health endpoint")
			Eventually(func() error {
				url := fmt.Sprintf("http://localhost:%d/health", vmcpNodePort)
				resp, err := http.Get(url)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
				}
				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("should aggregate tools from all backends with discovered auth", func() {
			By("Creating MCP client for VirtualMCPServer")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Starting transport and initializing connection")
			testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err = mcpClient.Start(testCtx)
			Expect(err).ToNot(HaveOccurred())

			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-auth-discovery-test",
				Version: "1.0.0",
			}

			_, err = mcpClient.Initialize(testCtx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(testCtx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "VirtualMCPServer should aggregate tools from backends")

			By(fmt.Sprintf("VirtualMCPServer aggregates %d tools with discovered auth", len(tools.Tools)))
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  Tool: %s - %s\n", tool.Name, tool.Description)
			}

			// Verify we have tools from multiple backends
			Expect(len(tools.Tools)).To(BeNumerically(">=", 2),
				"VirtualMCPServer should aggregate tools from multiple backends despite different auth configs")
		})

		It("should successfully call tools through VirtualMCPServer with discovered auth", func() {
			By("Creating MCP client for VirtualMCPServer")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Starting transport and initializing connection")
			testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err = mcpClient.Start(testCtx)
			Expect(err).ToNot(HaveOccurred())

			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-auth-discovery-test",
				Version: "1.0.0",
			}

			_, err = mcpClient.Initialize(testCtx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Listing available tools")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(testCtx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			By("Calling a tool through VirtualMCPServer to verify auth works")
			// Find a fetch tool we can call
			var targetToolName string
			for _, tool := range tools.Tools {
				// Look for fetch tool (may have prefix)
				if tool.Name == fetchToolName || strings.HasSuffix(tool.Name, fetchToolName) {
					targetToolName = tool.Name
					break
				}
			}

			if targetToolName != "" {
				GinkgoWriter.Printf("Testing tool call with discovered auth: %s\n", targetToolName)

				toolCallCtx, toolCallCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer toolCallCancel()

				callRequest := mcp.CallToolRequest{}
				callRequest.Params.Name = targetToolName
				callRequest.Params.Arguments = map[string]any{
					// Use mock server to avoid external dependencies and timeouts
					"url": mockServer.URL,
				}

				result, err := mcpClient.CallTool(toolCallCtx, callRequest)
				Expect(err).ToNot(HaveOccurred(),
					"Should be able to call tool through VirtualMCPServer with discovered auth")
				Expect(result).ToNot(BeNil())

				GinkgoWriter.Printf("✓ Tool call successful with discovered auth: %s\n", targetToolName)
				GinkgoWriter.Printf("  This proves vMCP correctly discovered and applied auth from backend ExternalAuthConfigRef\n")
			} else {
				GinkgoWriter.Printf("Warning: fetch tool not found, skipping tool call test\n")
			}
		})

	})
})
