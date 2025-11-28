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

var _ = Describe("VirtualMCPServer Token Exchange Auth", Ordered, func() {
	var (
		testNamespace       = "default"
		mcpGroupName        = "test-tokenexchange-group"
		vmcpServerName      = "test-vmcp-tokenexchange"
		backend1Name        = "backend-tokenexchange"
		authConfigName      = "test-tokenexchange-auth"
		secretName          = "oauth-client-secret"
		mockOIDCServerName  = "mock-oidc-tokenexchange"
		instrumentedBackend = "instrumented-backend-tokenexchange"
		timeout             = 5 * time.Minute
		pollingInterval     = 5 * time.Second
		vmcpNodePort        int32
		mockOIDCIssuerURL   string
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Deploying mock OIDC server for token exchange")
		DeployMockOIDCServerHTTP(ctx, k8sClient, testNamespace, mockOIDCServerName)
		mockOIDCIssuerURL = fmt.Sprintf("http://%s.%s.svc.cluster.local", mockOIDCServerName, testNamespace)

		By("Deploying instrumented backend that logs Bearer tokens")
		DeployInstrumentedBackendServer(ctx, k8sClient, testNamespace, instrumentedBackend)

		By("Creating OAuth client secret")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"client-secret": "test-client-secret-12345",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		By("Creating MCPExternalAuthConfig with token exchange")
		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
					TokenURL: fmt.Sprintf("%s/token", mockOIDCIssuerURL),
					ClientID: "toolhive-test-client",
					ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: secretName,
						Key:  "client-secret",
					},
					Audience: "mcp-backend",
					Scopes:   []string{"openid", "profile"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, authConfig)).To(Succeed())

		By("Creating MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for VirtualMCP token exchange auth",
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

		By("Creating backend MCPServer with ExternalAuthConfigRef")
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

		By("Waiting for backend MCPServer to be ready")
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
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer with discovered outgoing auth")
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
					Source: "discovered", // vMCP will discover token exchange from backend
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
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: vmcpServerName, Namespace: testNamespace},
		})
		k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backend1Name, Namespace: testNamespace},
		})
		k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
		})
		k8sClient.Delete(ctx, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: testNamespace},
		})
		k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: testNamespace},
		})
		CleanupMockServer(ctx, k8sClient, testNamespace, mockOIDCServerName, "")
		CleanupMockServer(ctx, k8sClient, testNamespace, instrumentedBackend, "")
	})

	Context("when using token exchange with discovered mode", func() {
		It("should create MCPExternalAuthConfig with token exchange", func() {
			By("Verifying MCPExternalAuthConfig exists with correct configuration")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfigName,
				Namespace: testNamespace,
			}, authConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(authConfig.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeTokenExchange))
			Expect(authConfig.Spec.TokenExchange).ToNot(BeNil())
			Expect(authConfig.Spec.TokenExchange.TokenURL).To(ContainSubstring("/token"))
			Expect(authConfig.Spec.TokenExchange.ClientID).To(Equal("toolhive-test-client"))
			Expect(authConfig.Spec.TokenExchange.ClientSecretRef).ToNot(BeNil())
			Expect(authConfig.Spec.TokenExchange.ClientSecretRef.Name).To(Equal(secretName))
			Expect(authConfig.Spec.TokenExchange.ClientSecretRef.Key).To(Equal("client-secret"))
			Expect(authConfig.Spec.TokenExchange.Audience).To(Equal("mcp-backend"))
		})

		It("should create MCPServer with ExternalAuthConfigRef", func() {
			By("Verifying MCPServer references the auth config")
			server := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1Name,
				Namespace: testNamespace,
			}, server)
			Expect(err).ToNot(HaveOccurred())
			Expect(server.Spec.ExternalAuthConfigRef).ToNot(BeNil())
			Expect(server.Spec.ExternalAuthConfigRef.Name).To(Equal(authConfigName))
		})

		It("should create VirtualMCPServer with discovered outgoing auth", func() {
			By("Verifying VirtualMCPServer has discovered auth configuration")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Spec.IncomingAuth.Type).To(Equal("anonymous"))
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("discovered"))
		})

		It("should have vMCP server ready with token exchange configuration", func() {
			By("Verifying VirtualMCPServer is in Ready phase")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			// Check that vMCP has Ready condition
			hasReadyCondition := false
			for _, condition := range vmcpServer.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "True" {
					hasReadyCondition = true
					break
				}
			}
			Expect(hasReadyCondition).To(BeTrue(), "VirtualMCPServer should have Ready condition")

			GinkgoWriter.Printf("VirtualMCPServer is ready with discovered token exchange auth\n")
		})

		It("should exchange tokens and inject Bearer token in backend requests", func() {
			// TODO: This test requires full discovered mode auth integration to be implemented.
			// The ResolveSecrets implementation for token exchange is complete and tested,
			// but it needs to be integrated into the backend discovery flow in pkg/vmcp/workloads/k8s.go
			// to fetch and resolve MCPExternalAuthConfig CRDs when backends are discovered.
			// See: pkg/vmcp/auth/converters/token_exchange.go for the working implementation.
			Skip("Discovered mode auth integration pending - ResolveSecrets implemented but not yet integrated into discovery flow")

			By("Making a request through vMCP to trigger token exchange")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			testCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			err = mcpClient.Start(testCtx)
			Expect(err).ToNot(HaveOccurred())

			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-e2e-test",
				Version: "1.0.0",
			}
			_, err = mcpClient.Initialize(testCtx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			// List tools to trigger backend request with token exchange
			listRequest := mcp.ListToolsRequest{}
			_, err = mcpClient.ListTools(testCtx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Checking mock OIDC server received token exchange request")
			Eventually(func() bool {
				stats, err := GetMockOIDCStats(ctx, k8sClient, testNamespace, mockOIDCServerName)
				if err != nil {
					GinkgoWriter.Printf("Failed to get OIDC stats: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("Mock OIDC stats: %+v\n", stats)
				// Check if token_requests is present and > 0
				count, ok := stats["token_requests"]
				return ok && count > 0
			}, 2*time.Minute, 10*time.Second).Should(BeTrue(),
				"OIDC server should receive token exchange requests")

			By("Checking instrumented backend received Bearer token")
			Eventually(func() bool {
				stats, err := GetInstrumentedBackendStats(ctx, k8sClient, testNamespace, instrumentedBackend)
				if err != nil {
					GinkgoWriter.Printf("Failed to get backend stats: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("Instrumented backend stats: %+v\n", stats)
				// Check if bearer_token_requests is present and > 0
				count, ok := stats["bearer_token_requests"]
				return ok && count > 0
			}, 2*time.Minute, 10*time.Second).Should(BeTrue(),
				"Backend should receive Bearer token in requests")
		})
	})
})
