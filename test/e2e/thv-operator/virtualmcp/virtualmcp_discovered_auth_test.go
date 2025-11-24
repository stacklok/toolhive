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
)

var _ = Describe("VirtualMCPServer Discovered Auth Mode", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-discovered-auth-group"
		vmcpServerName  = "test-vmcp-discovered-auth"
		backend1Name    = "backend-fetch-discovered-auth"
		backend2Name    = "backend-osv-discovered-auth"
		authConfigName  = "test-token-exchange-auth"
		authSecretName  = "test-auth-secret"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Creating auth secret for token exchange")
		authSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"client-secret": "test-client-secret-value",
			},
		}
		Expect(k8sClient.Create(ctx, authSecret)).To(Succeed())

		By("Creating MCPExternalAuthConfig for token exchange")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
					TokenURL: "https://oauth.example.com/token",
					ClientID: "test-client-id",
					ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: authSecretName,
						Key:  "client-secret",
					},
					Audience:         "https://api.example.com",
					Scopes:           []string{"read", "write"},
					SubjectTokenType: "access_token",
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
				Description: "Test MCP Group for VirtualMCP discovered auth E2E tests",
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

		By("Creating first backend MCPServer with ExternalAuthConfigRef - fetch")
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

		By("Creating second backend MCPServer without ExternalAuthConfigRef - osv")
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
				// No ExternalAuthConfigRef - should use unauthenticated
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
				return fmt.Errorf("failed to get backend1: %w", err)
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend1 not ready yet, phase: %s", server.Status.Phase)
			}

			server2 := &mcpv1alpha1.MCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, server2)
			if err != nil {
				return fmt.Errorf("failed to get backend2: %w", err)
			}
			if server2.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend2 not ready yet, phase: %s", server2.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

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
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered", // Discovered mode - auth discovered from backend MCPServers
				},
				Aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: "prefix",
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			if err != nil {
				return false
			}
			return vmcpServer.Status.Phase == mcpv1alpha1.VirtualMCPServerPhaseReady
		}, timeout, pollingInterval).Should(BeTrue())

		By("Getting VirtualMCPServer NodePort")
		vmcpService := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServiceName(),
			Namespace: testNamespace,
		}, vmcpService)).To(Succeed())

		Expect(vmcpService.Spec.Ports).To(HaveLen(1))
		vmcpNodePort = vmcpService.Spec.Ports[0].NodePort
		Expect(vmcpNodePort).NotTo(BeZero())

		By(fmt.Sprintf("VirtualMCPServer available at NodePort: %d", vmcpNodePort))
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
		backend1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend1Name,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, backend1)

		backend2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend2Name,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, backend2)

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpGroup)

		By("Cleaning up MCPExternalAuthConfig")
		externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfigName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, externalAuthConfig)

		By("Cleaning up auth secret")
		authSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecretName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, authSecret)
	})

	It("should discover backends with their auth configurations", func() {
		// Verify VirtualMCPServer status has discovered backends
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)).To(Succeed())

		By("Verifying discovered backends in status")
		Expect(vmcpServer.Status.DiscoveredBackends).To(HaveLen(2))

		// Find the backends in the discovered list
		var backend1Discovered, backend2Discovered *mcpv1alpha1.DiscoveredBackend
		for i := range vmcpServer.Status.DiscoveredBackends {
			backend := &vmcpServer.Status.DiscoveredBackends[i]
			switch backend.Name {
			case backend1Name:
				backend1Discovered = backend
			case backend2Name:
				backend2Discovered = backend
			}
		}

		By("Verifying backend1 has discovered auth config")
		Expect(backend1Discovered).NotTo(BeNil())
		Expect(backend1Discovered.AuthConfigRef).To(Equal(authConfigName))
		Expect(backend1Discovered.AuthType).To(Equal(mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef))

		By("Verifying backend2 has no auth config")
		Expect(backend2Discovered).NotTo(BeNil())
		Expect(backend2Discovered.AuthConfigRef).To(BeEmpty())
	})

	It("should successfully connect to VirtualMCPServer and list aggregated tools", func() {
		vmcpURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", vmcpNodePort)

		By(fmt.Sprintf("Connecting to VirtualMCPServer at %s", vmcpURL))
		mcpClient, err := client.NewStreamableHttpClient(vmcpURL)
		Expect(err).NotTo(HaveOccurred())
		defer mcpClient.Close()

		By("Starting transport and initializing MCP client")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = mcpClient.Start(ctx)
		Expect(err).NotTo(HaveOccurred())

		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcpProtocolVersion
		initRequest.Params.ClientInfo = mcp.Implementation{
			Name:    "test-client",
			Version: "1.0.0",
		}

		_, err = mcpClient.Initialize(ctx, initRequest)
		Expect(err).NotTo(HaveOccurred())

		By("Listing tools from aggregated backends")
		toolsResult, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying tools from both backends are present")
		toolNames := make([]string, 0, len(toolsResult.Tools))
		for _, tool := range toolsResult.Tools {
			toolNames = append(toolNames, tool.Name)
		}

		// Both backends should have their tools aggregated despite different auth configs
		// Backend1 (fetch) should have fetch tools with prefix
		// Backend2 (osv) should have osv tools with prefix
		Expect(toolNames).To(ContainElement(ContainSubstring("fetch")))
		Expect(toolNames).To(ContainElement(ContainSubstring("osv")))

		By("Test completed successfully - discovered auth mode working")
	})

	It("should handle mixed mode with discovered and explicit auth", func() {
		mixedVmcpName := "test-vmcp-mixed-auth"

		By("Creating VirtualMCPServer with mixed auth mode")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mixedVmcpName,
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
					Source: "mixed", // Mixed mode - discover by default, allow overrides
					Backends: map[string]mcpv1alpha1.BackendAuthConfig{
						backend2Name: {
							Type: mcpv1alpha1.BackendAuthTypePassThrough,
						},
					},
				},
				Aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: "prefix",
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for mixed mode VirtualMCPServer to be ready")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mixedVmcpName,
				Namespace: testNamespace,
			}, vmcpServer)
			if err != nil {
				return false
			}
			return vmcpServer.Status.Phase == mcpv1alpha1.VirtualMCPServerPhaseReady
		}, timeout, pollingInterval).Should(BeTrue())

		By("Verifying mixed mode backend configuration")
		// Backend1 should use discovered auth (from ExternalAuthConfigRef)
		// Backend2 should use explicit pass_through override
		Expect(vmcpServer.Status.DiscoveredBackends).To(HaveLen(2))

		By("Cleaning up mixed mode VirtualMCPServer")
		_ = k8sClient.Delete(ctx, vmcpServer)
	})
})
