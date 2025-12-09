package virtualmcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Unauthenticated Backend Auth", Ordered, func() {
	var (
		testNamespace          = "default"
		mcpGroupName           = "test-unauthenticated-auth-group"
		vmcpServerName         = "test-vmcp-unauthenticated"
		backendName            = "backend-fetch-unauthenticated"
		externalAuthConfigName = "test-unauthenticated-auth-config"
		timeout                = 5 * time.Minute
		pollingInterval        = 5 * time.Second
		vmcpNodePort           int32
	)

	BeforeAll(func() {
		By("Creating MCPExternalAuthConfig with unauthenticated type")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      externalAuthConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
				// No TokenExchange or HeaderInjection fields needed
			},
		}
		Expect(k8sClient.Create(ctx, externalAuthConfig)).To(Succeed())

		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for VirtualMCP unauthenticated auth", timeout, pollingInterval)

		By("Creating backend MCPServer without auth (localhost, trusted)")
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
				// Reference the unauthenticated external auth config
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: externalAuthConfigName,
				},
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
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer with discovered auth mode (should use unauthenticated)")
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
					Source: "discovered", // Will discover unauthenticated from backend
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: vmcpServerName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: externalAuthConfigName, Namespace: testNamespace},
		})
	})

	Context("when using unauthenticated backend auth", func() {
		It("should discover unauthenticated auth from backend MCPServer", func() {
			By("Verifying VirtualMCPServer discovered unauthenticated auth")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("discovered"))

			// Check that backend was discovered with auth config
			Expect(vmcpServer.Status.DiscoveredBackends).ToNot(BeEmpty())
			found := false
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == backendName {
					found = true
					Expect(backend.AuthConfigRef).To(Equal(externalAuthConfigName))
					break
				}
			}
			Expect(found).To(BeTrue(), "Backend should be discovered with auth config reference")
		})

		It("should successfully connect and call tools with unauthenticated backend", func() {
			By("Creating MCP client")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			By("Starting MCP client")
			err = mcpClient.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("Initializing MCP session")
			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-e2e-test",
				Version: "1.0.0",
			}
			_, err = mcpClient.Initialize(ctx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Listing tools from unauthenticated backend")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			By("Calling a tool from unauthenticated backend")
			// Find the fetch tool
			var fetchTool *mcp.Tool
			for _, tool := range tools.Tools {
				if tool.Name == fetchToolName || tool.Name == "backend-fetch-unauthenticated_fetch" {
					t := tool
					fetchTool = &t
					break
				}
			}
			Expect(fetchTool).ToNot(BeNil(), "fetch tool should be available")

			// Call the fetch tool
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = fetchTool.Name
			callRequest.Params.Arguments = map[string]interface{}{
				"url": "https://example.com",
			}

			result, err := mcpClient.CallTool(ctx, callRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Content).ToNot(BeEmpty())
		})

		It("should validate MCPExternalAuthConfig with unauthenticated type", func() {
			By("Verifying MCPExternalAuthConfig exists and is valid")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      externalAuthConfigName,
				Namespace: testNamespace,
			}, authConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(authConfig.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeUnauthenticated))
			Expect(authConfig.Spec.TokenExchange).To(BeNil())
			Expect(authConfig.Spec.HeaderInjection).To(BeNil())
		})
	})
})

var _ = Describe("VirtualMCPServer Inline Unauthenticated Backend Auth", Ordered, func() {
	var (
		testNamespace          = "default"
		mcpGroupName           = "test-inline-unauth-group"
		vmcpServerName         = "test-vmcp-inline-unauth"
		backendName            = "backend-inline-unauth"
		externalAuthConfigName = "test-inline-unauth-config"
		timeout                = 5 * time.Minute
		pollingInterval        = 5 * time.Second
		vmcpNodePort           int32
	)

	BeforeAll(func() {
		By("Creating MCPExternalAuthConfig with unauthenticated type for inline mode")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      externalAuthConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
			},
		}
		Expect(k8sClient.Create(ctx, externalAuthConfig)).To(Succeed())

		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for inline unauthenticated auth", timeout, pollingInterval)

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
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer with inline unauthenticated auth")
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
					Source: "inline",
					// Explicitly configure unauthenticated for specific backend
					Backends: map[string]mcpv1alpha1.BackendAuthConfig{
						backendName: {
							Type: "external_auth_config_ref",
							ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
								Name: externalAuthConfigName,
							},
						},
					},
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: vmcpServerName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: externalAuthConfigName, Namespace: testNamespace},
		})
	})

	Context("when using inline unauthenticated backend auth", func() {
		It("should configure inline unauthenticated auth for specific backend", func() {
			By("Verifying VirtualMCPServer has inline auth configured")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("inline"))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends).To(HaveKey(backendName))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends[backendName].Type).To(Equal("external_auth_config_ref"))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends[backendName].ExternalAuthConfigRef.Name).To(Equal(externalAuthConfigName))
		})

		It("should successfully proxy tool calls with inline unauthenticated auth", func() {
			By("Creating MCP client")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			By("Starting and initializing MCP client")
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

			By("Listing and calling tools through inline unauthenticated proxy")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			// Verify tools are accessible
			var fetchTool *mcp.Tool
			for _, tool := range tools.Tools {
				if tool.Name == fetchToolName || tool.Name == "backend-inline-unauth_fetch" {
					t := tool
					fetchTool = &t
					break
				}
			}
			Expect(fetchTool).ToNot(BeNil(), "fetch tool should be available")
		})
	})
})

var _ = Describe("VirtualMCPServer HeaderInjection Backend Auth", Ordered, func() {
	var (
		testNamespace          = "default"
		mcpGroupName           = "test-headerinjection-auth-group"
		vmcpServerName         = "test-vmcp-headerinjection"
		backendName            = "backend-fetch-headerinjection"
		externalAuthConfigName = "test-headerinjection-auth-config"
		secretName             = "test-headerinjection-secret"
		timeout                = 5 * time.Minute
		pollingInterval        = 5 * time.Second
		vmcpNodePort           int32
	)

	BeforeAll(func() {
		By("Creating Secret for header injection")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"api-key": "test-api-key-value",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		By("Creating MCPExternalAuthConfig with headerInjection type")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      externalAuthConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
					ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: secretName,
						Key:  "api-key",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, externalAuthConfig)).To(Succeed())

		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for VirtualMCP headerInjection auth", timeout, pollingInterval)

		By("Creating backend MCPServer with headerInjection auth")
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
				// Reference the headerInjection external auth config
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: externalAuthConfigName,
				},
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
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer with discovered auth mode (should use headerInjection)")
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
					Source: "discovered", // Will discover headerInjection from backend
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: vmcpServerName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: externalAuthConfigName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: testNamespace},
		})
	})

	Context("when using headerInjection backend auth", func() {
		It("should discover headerInjection auth from backend MCPServer", func() {
			By("Verifying VirtualMCPServer discovered headerInjection auth")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("discovered"))

			// Check that backend was discovered with auth config
			Expect(vmcpServer.Status.DiscoveredBackends).ToNot(BeEmpty())
			found := false
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == backendName {
					found = true
					Expect(backend.AuthConfigRef).To(Equal(externalAuthConfigName))
					break
				}
			}
			Expect(found).To(BeTrue(), "Backend should be discovered with auth config reference")
		})

		It("should successfully connect and call tools with headerInjection backend", func() {
			By("Creating MCP client")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			By("Starting MCP client")
			err = mcpClient.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("Initializing MCP session")
			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-e2e-test",
				Version: "1.0.0",
			}
			_, err = mcpClient.Initialize(ctx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Listing tools from headerInjection backend")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			By("Calling a tool from headerInjection backend")
			// Find the fetch tool
			var fetchTool *mcp.Tool
			for _, tool := range tools.Tools {
				if tool.Name == fetchToolName || tool.Name == "backend-fetch-headerinjection_fetch" {
					t := tool
					fetchTool = &t
					break
				}
			}
			Expect(fetchTool).ToNot(BeNil(), "fetch tool should be available")

			// Call the fetch tool
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = fetchTool.Name
			callRequest.Params.Arguments = map[string]interface{}{
				"url": "https://example.com",
			}

			result, err := mcpClient.CallTool(ctx, callRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Content).ToNot(BeEmpty())
		})

		It("should validate MCPExternalAuthConfig with headerInjection type", func() {
			By("Verifying MCPExternalAuthConfig exists and is valid")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      externalAuthConfigName,
				Namespace: testNamespace,
			}, authConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(authConfig.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeHeaderInjection))
			Expect(authConfig.Spec.TokenExchange).To(BeNil())
			Expect(authConfig.Spec.HeaderInjection).ToNot(BeNil())
			Expect(authConfig.Spec.HeaderInjection.HeaderName).To(Equal("X-API-Key"))
			Expect(authConfig.Spec.HeaderInjection.ValueSecretRef.Name).To(Equal(secretName))
			Expect(authConfig.Spec.HeaderInjection.ValueSecretRef.Key).To(Equal("api-key"))
		})
	})
})

var _ = Describe("VirtualMCPServer Inline HeaderInjection Backend Auth", Ordered, func() {
	var (
		testNamespace          = "default"
		mcpGroupName           = "test-inline-headerinjection-group"
		vmcpServerName         = "test-vmcp-inline-headerinjection"
		backendName            = "backend-inline-headerinjection"
		externalAuthConfigName = "test-inline-headerinjection-config"
		secretName             = "test-inline-headerinjection-secret"
		timeout                = 5 * time.Minute
		pollingInterval        = 5 * time.Second
		vmcpNodePort           int32
	)

	BeforeAll(func() {
		By("Creating Secret for inline header injection")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"api-key": "test-inline-api-key-value",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		By("Creating MCPExternalAuthConfig with headerInjection type for inline mode")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      externalAuthConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-Custom-Auth",
					ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: secretName,
						Key:  "api-key",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, externalAuthConfig)).To(Succeed())

		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for inline headerInjection auth", timeout, pollingInterval)

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
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer with inline headerInjection auth")
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
					Source: "inline",
					// Explicitly configure headerInjection for specific backend
					Backends: map[string]mcpv1alpha1.BackendAuthConfig{
						backendName: {
							Type: "external_auth_config_ref",
							ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
								Name: externalAuthConfigName,
							},
						},
					},
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: vmcpServerName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: externalAuthConfigName, Namespace: testNamespace},
		})
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: testNamespace},
		})
	})

	Context("when using inline headerInjection backend auth", func() {
		It("should configure inline headerInjection auth for specific backend", func() {
			By("Verifying VirtualMCPServer has inline auth configured")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("inline"))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends).To(HaveKey(backendName))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends[backendName].Type).To(Equal("external_auth_config_ref"))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends[backendName].ExternalAuthConfigRef.Name).To(Equal(externalAuthConfigName))
		})

		It("should successfully proxy tool calls with inline headerInjection auth", func() {
			By("Creating MCP client")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			By("Starting and initializing MCP client")
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

			By("Listing and calling tools through inline headerInjection proxy")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			// Verify tools are accessible
			var fetchTool *mcp.Tool
			for _, tool := range tools.Tools {
				if tool.Name == fetchToolName || tool.Name == "backend-inline-headerinjection_fetch" {
					t := tool
					fetchTool = &t
					break
				}
			}
			Expect(fetchTool).ToNot(BeNil(), "fetch tool should be available")
		})
	})
})
