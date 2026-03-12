// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// healthCheckAuthInterval is a short interval used in health-check auth tests so
// the monitor runs at least a few times within the test timeout.
const healthCheckAuthInterval = 5 * time.Second

var _ = Describe("VirtualMCPServer Unauthenticated Backend Auth", Ordered, func() {
	var (
		testNamespace          = "default"
		mcpGroupName           = "test-unauthenticated-auth-group"
		vmcpServerName         = "test-vmcp-unauthenticated"
		backendName            = "backend-fetch-unauthenticated"
		externalAuthConfigName = "test-unauthenticated-auth-config"
		timeout                = 3 * time.Minute
		pollingInterval        = 1 * time.Second
		vmcpNodePort           int32
		mockHTTPServer         *MockHTTPServerInfo
	)

	BeforeAll(func() {
		By("Creating mock HTTP server for fetch tool testing")
		mockHTTPServer = CreateMockHTTPServer(ctx, k8sClient, "mock-http-unauth", testNamespace, timeout, pollingInterval)

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
				Config: vmcpconfig.Config{Group: mcpGroupName},
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
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		By("Waiting for VirtualMCPServer to stabilize")
		time.Sleep(5 * time.Second)
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

		By("Cleaning up mock HTTP server")
		CleanupMockHTTPServer(ctx, k8sClient, "mock-http-unauth", testNamespace)
	})

	Context("when using unauthenticated backend auth", func() {
		It("should discover, validate, and successfully use unauthenticated backend auth", func() {
			By("Verifying VirtualMCPServer discovered unauthenticated auth")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpServerName, Namespace: testNamespace}, vmcpServer)).To(Succeed())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("discovered"))
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

			By("Validating MCPExternalAuthConfig")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: externalAuthConfigName, Namespace: testNamespace}, authConfig)).To(Succeed())
			Expect(authConfig.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeUnauthenticated))
			Expect(authConfig.Spec.TokenExchange).To(BeNil())
			Expect(authConfig.Spec.HeaderInjection).To(BeNil())

			By("Creating MCP client and connecting")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient := InitializeMCPClientWithRetries(serverURL, 2*time.Minute, WithHttpLoggerOption())
			defer mcpClient.Close()

			testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			By("Listing and calling tools")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(testCtx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			var fetchTool *mcp.Tool
			for _, tool := range tools.Tools {
				if tool.Name == fetchToolName || tool.Name == "backend-fetch-unauthenticated_fetch" {
					t := tool
					fetchTool = &t
					break
				}
			}
			Expect(fetchTool).ToNot(BeNil(), "fetch tool should be available")

			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = fetchTool.Name
			callRequest.Params.Arguments = map[string]interface{}{"url": mockHTTPServer.URL}

			result, err := mcpClient.CallTool(testCtx, callRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Content).ToNot(BeEmpty())
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
		timeout                = 3 * time.Minute
		pollingInterval        = 1 * time.Second
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
				Config: vmcpconfig.Config{Group: mcpGroupName},
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
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		By("Waiting for VirtualMCPServer to stabilize")
		time.Sleep(5 * time.Second)
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
		It("should configure and successfully use inline unauthenticated backend auth", func() {
			By("Verifying VirtualMCPServer has inline auth configured")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpServerName, Namespace: testNamespace}, vmcpServer)).To(Succeed())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("inline"))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends).To(HaveKey(backendName))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends[backendName].Type).To(Equal("external_auth_config_ref"))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends[backendName].ExternalAuthConfigRef.Name).To(Equal(externalAuthConfigName))

			By("Creating MCP client and listing tools")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient := InitializeMCPClientWithRetries(serverURL, 2*time.Minute)
			defer mcpClient.Close()
			testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(testCtx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

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
		timeout                = 3 * time.Minute
		pollingInterval        = 1 * time.Second
		vmcpNodePort           int32
		mockHTTPServer         *MockHTTPServerInfo
	)

	BeforeAll(func() {
		By("Creating mock HTTP server for fetch tool testing")
		mockHTTPServer = CreateMockHTTPServer(ctx, k8sClient, "mock-http-header", testNamespace, timeout, pollingInterval)

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
				Config: vmcpconfig.Config{Group: mcpGroupName},
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
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		By("Waiting for VirtualMCPServer to stabilize")
		time.Sleep(5 * time.Second)
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
		By("Cleaning up mock HTTP server")
		CleanupMockHTTPServer(ctx, k8sClient, "mock-http-header", testNamespace)
	})

	Context("when using headerInjection backend auth", func() {
		It("should discover, validate, and successfully use headerInjection backend auth", func() {
			By("Verifying VirtualMCPServer discovered headerInjection auth")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpServerName, Namespace: testNamespace}, vmcpServer)).To(Succeed())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("discovered"))
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

			By("Validating MCPExternalAuthConfig")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: externalAuthConfigName, Namespace: testNamespace}, authConfig)).To(Succeed())
			Expect(authConfig.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeHeaderInjection))
			Expect(authConfig.Spec.TokenExchange).To(BeNil())
			Expect(authConfig.Spec.HeaderInjection).ToNot(BeNil())
			Expect(authConfig.Spec.HeaderInjection.HeaderName).To(Equal("X-API-Key"))
			Expect(authConfig.Spec.HeaderInjection.ValueSecretRef.Name).To(Equal(secretName))
			Expect(authConfig.Spec.HeaderInjection.ValueSecretRef.Key).To(Equal("api-key"))

			By("Creating MCP client and connecting")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient := InitializeMCPClientWithRetries(serverURL, 2*time.Minute)
			defer mcpClient.Close()
			testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			By("Listing and calling tools")
			var tools *mcp.ListToolsResult
			var fetchTool *mcp.Tool

			// Retry ListTools to handle transient connection issues
			Eventually(func() error {
				listRequest := mcp.ListToolsRequest{}
				var err error
				tools, err = mcpClient.ListTools(testCtx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				if len(tools.Tools) == 0 {
					return fmt.Errorf("no tools returned")
				}
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed(), "Should be able to list tools")

			// Find the fetch tool
			for _, tool := range tools.Tools {
				if tool.Name == fetchToolName || tool.Name == "backend-fetch-headerinjection_fetch" {
					t := tool
					fetchTool = &t
					break
				}
			}
			Expect(fetchTool).ToNot(BeNil(), "fetch tool should be available")

			// Retry CallTool to handle transient connection issues
			var result *mcp.CallToolResult
			Eventually(func() error {
				callRequest := mcp.CallToolRequest{}
				callRequest.Params.Name = fetchTool.Name
				callRequest.Params.Arguments = map[string]interface{}{"url": mockHTTPServer.URL}

				var err error
				result, err = mcpClient.CallTool(testCtx, callRequest)
				if err != nil {
					return fmt.Errorf("failed to call tool: %w", err)
				}
				if len(result.Content) == 0 {
					return fmt.Errorf("tool returned empty content")
				}
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed(), "Should be able to call tool")

			Expect(result.Content).ToNot(BeEmpty())
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
		timeout                = 3 * time.Minute
		pollingInterval        = 1 * time.Second
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
				Config: vmcpconfig.Config{Group: mcpGroupName},
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
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		By("Waiting for VirtualMCPServer to stabilize")
		time.Sleep(5 * time.Second)
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
		It("should configure and successfully use inline headerInjection backend auth", func() {
			By("Verifying VirtualMCPServer has inline auth configured")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpServerName, Namespace: testNamespace}, vmcpServer)).To(Succeed())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("inline"))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends).To(HaveKey(backendName))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends[backendName].Type).To(Equal("external_auth_config_ref"))
			Expect(vmcpServer.Spec.OutgoingAuth.Backends[backendName].ExternalAuthConfigRef.Name).To(Equal(externalAuthConfigName))

			By("Creating MCP client and listing tools")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient := InitializeMCPClientWithRetries(serverURL, 2*time.Minute)
			defer mcpClient.Close()
			testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(testCtx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

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

// VirtualMCPServer Health Check with HeaderInjection Auth
//
// This suite validates the fix for https://github.com/stacklok/toolhive/issues/4101:
// health checks must apply outgoing auth credentials (header_injection) when probing
// backend MCPServers, exactly as real requests do.
//
// Before the fix, HeaderInjectionStrategy.Authenticate() returned early for health-check
// contexts, meaning the header was never injected. After the fix it always injects the
// header because static headers do not depend on user identity.
//
// Because the standard test backends (yardstick) do not enforce API-key validation this
// test validates the directly observable effects:
//   - The backend is reported as BackendStatusReady (not BackendStatusUnavailable/Unknown)
//     even while health monitoring is running continuously.
//   - Tools remain accessible through the vMCP (proving health checks passed and the
//     backend was not prematurely marked unhealthy).
var _ = Describe("VirtualMCPServer Health Check with HeaderInjection Auth", Ordered, func() {
	var (
		testNamespace          = "default"
		mcpGroupName           = "test-hc-headerinjection-group"
		vmcpServerName         = "test-vmcp-hc-headerinjection"
		backendName            = "backend-hc-headerinjection"
		externalAuthConfigName = "test-hc-headerinjection-config"
		secretName             = "test-hc-headerinjection-secret"
		timeout                = 3 * time.Minute
		pollingInterval        = 1 * time.Second
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
				"api-key": "test-hc-api-key-value",
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
			"Test group for health check headerInjection auth", timeout, pollingInterval)

		By("Creating backend MCPServer with headerInjection auth ref")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
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
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: externalAuthConfigName,
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend)).To(Succeed())

		By("Waiting for backend MCPServer to be ready")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backendName,
				Namespace: testNamespace,
			}, server); err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}
			if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
				return nil
			}
			return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer with discovered auth and health monitoring enabled")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
					Operational: &vmcpconfig.OperationalConfig{
						FailureHandling: &vmcpconfig.FailureHandlingConfig{
							// Short interval so several health checks run within the test timeout.
							HealthCheckInterval: vmcpconfig.Duration(healthCheckAuthInterval),
							HealthCheckTimeout:  vmcpconfig.Duration(2 * time.Second),
							UnhealthyThreshold:  3,
						},
					},
				},
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
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

	Context("when health monitoring is running alongside header injection auth", func() {
		It("should keep the backend healthy — not mark it unauthenticated", func() {
			// Core regression check for the fix in HeaderInjectionStrategy.Authenticate():
			// health checks must inject the static header just like real requests do.
			// We use Consistently to observe the backend status over several health check
			// cycles: if the fix were absent and the backend enforced the header, it would
			// accumulate probe failures and flip to BackendStatusUnavailable during
			// this window.
			By("Verifying backend status never becomes unavailable over several health check cycles")
			Consistently(func() bool {
				server := &mcpv1alpha1.VirtualMCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, server); err != nil {
					return false // cannot read status — treat as failure
				}
				for _, b := range server.Status.DiscoveredBackends {
					if b.Name == backendName {
						return b.Status == mcpv1alpha1.BackendStatusReady || b.Status == mcpv1alpha1.BackendStatusDegraded
					}
				}
				// BackendsDiscovered=True was confirmed in BeforeAll, so the backend
				// must appear in DiscoveredBackends — absence is unexpected.
				return false
			}, 3*healthCheckAuthInterval, pollingInterval).Should(BeTrue(),
				"backend must remain ready/degraded during health check cycles; "+
					"without the fix, health checks send unauthenticated probes which accumulate "+
					"failures and flip the backend to unavailable/unknown")
		})

		It("should serve tools — proving health checks did not prematurely mark the backend unhealthy", func() {
			By("Connecting to vMCP and listing tools")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient := InitializeMCPClientWithRetries(serverURL, 2*time.Minute)
			defer mcpClient.Close()

			testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			Eventually(func() error {
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.ListTools(testCtx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				if len(tools.Tools) == 0 {
					return fmt.Errorf("no tools returned — backend may have been excluded due to failed health checks")
				}
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed(),
				"tools must be accessible; if the backend was marked unhealthy by the health monitor "+
					"it would be excluded by the discovery middleware and no tools would be returned")
		})
	})
})

// VirtualMCPServer Health Check with TokenExchange Auth verifies that health checks
// apply a client_credentials grant when TokenExchange auth is configured with
// client_id + client_secret (fix for issue #4101 — token-exchange path).
var _ = Describe("VirtualMCPServer Health Check with TokenExchange Auth", Ordered, func() {
	const (
		testNamespace          = "default"
		mcpGroupName           = "hc-te-group"
		vmcpServerName         = "hc-te-vmcp"
		backendName            = "hc-te-backend"
		externalAuthConfigName = "hc-te-auth-config"
		secretName             = "hc-te-client-secret"
		oauth2ServerName       = "hc-te-oauth2"
		timeout                = 3 * time.Minute
		pollingInterval        = 1 * time.Second
	)

	var (
		oauth2TokenURL string
		cleanupOAuth2  func()
	)

	BeforeAll(func() {
		By("Deploying mock OAuth2 server")
		oauth2TokenURL, cleanupOAuth2 = DeployMockOAuth2Server(
			ctx, k8sClient, oauth2ServerName, testNamespace, timeout, pollingInterval,
		)

		By("Creating Secret with OAuth2 client secret")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"client-secret": []byte("test-secret"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		By("Creating MCPExternalAuthConfig with tokenExchange type")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      externalAuthConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
					TokenURL: oauth2TokenURL,
					ClientID: "test-client",
					ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: secretName,
						Key:  "client-secret",
					},
					Audience: "test-backend",
				},
			},
		}
		Expect(k8sClient.Create(ctx, externalAuthConfig)).To(Succeed())

		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test group for health check tokenExchange auth", timeout, pollingInterval)

		By("Creating backend MCPServer with tokenExchange auth ref")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
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
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: externalAuthConfigName,
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend)).To(Succeed())

		By("Waiting for backend MCPServer to be ready")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backendName,
				Namespace: testNamespace,
			}, server); err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}
			if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
				return nil
			}
			return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer with discovered auth and health monitoring enabled")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
					Operational: &vmcpconfig.OperationalConfig{
						FailureHandling: &vmcpconfig.FailureHandlingConfig{
							HealthCheckInterval: vmcpconfig.Duration(healthCheckAuthInterval),
							HealthCheckTimeout:  vmcpconfig.Duration(2 * time.Second),
							UnhealthyThreshold:  3,
						},
					},
				},
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)
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
		if cleanupOAuth2 != nil {
			cleanupOAuth2()
		}
	})

	Context("when health monitoring is running alongside token exchange auth", func() {
		It("should call the token endpoint with client_credentials during health checks", func() {
			// Core regression check for the fix in TokenExchangeStrategy.Authenticate():
			// health checks must perform a client_credentials grant when client_id +
			// client_secret are configured, rather than skipping auth entirely.
			// GetMockOAuth2Stats uses a curl pod to query /stats over in-cluster DNS,
			// which works reliably in CI without requiring NodePort reachability.
			By("Querying mock OAuth2 server /stats to verify health checks called the token endpoint")
			Eventually(func() (int, error) {
				return GetMockOAuth2Stats(ctx, k8sClient, testNamespace, oauth2ServerName)
			}, 2*time.Minute, 15*time.Second).Should(BeNumerically(">", 0),
				"mock OAuth2 server must record at least one client_credentials grant; "+
					"without the fix, health checks skip auth and never call the token endpoint")
		})

		It("should keep the backend healthy — not mark it unavailable", func() {
			// The first It spec (client_credentials stats check) already waits via
			// Eventually until at least one token request has been made, so by the
			// time this spec runs health checks have definitely fired. We then use
			// Consistently to confirm the backend stays healthy over further cycles.
			By("Verifying backend status never becomes unavailable over several health check cycles")
			Consistently(func() bool {
				server := &mcpv1alpha1.VirtualMCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, server); err != nil {
					return false // cannot read status — treat as failure
				}
				for _, b := range server.Status.DiscoveredBackends {
					if b.Name == backendName {
						return b.Status == mcpv1alpha1.BackendStatusReady || b.Status == mcpv1alpha1.BackendStatusDegraded
					}
				}
				// BackendsDiscovered=True was confirmed in BeforeAll, so the backend
				// must appear in DiscoveredBackends — absence is unexpected.
				return false
			}, 3*healthCheckAuthInterval, pollingInterval).Should(BeTrue(),
				"backend must remain ready/degraded during health check cycles; "+
					"without the fix, health checks skip auth, the backend accumulates "+
					"probe failures, and its status flips to unavailable/unknown")
		})

	})
})
