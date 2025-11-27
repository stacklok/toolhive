package virtualmcp

import (
	"context"
	"fmt"
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

var _ = Describe("VirtualMCPServer Inline Auth with Anonymous Incoming", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-inline-auth-anon-group"
		vmcpServerName  = "test-vmcp-inline-auth-anon"
		backend1Name    = "backend-fetch-inline-anon"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Creating MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for VirtualMCP inline auth with anonymous incoming",
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

		By("Creating backend MCPServer")
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

		By("Creating VirtualMCPServer with anonymous incoming and inline outgoing auth")
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
					// No Default specified - will use unauthenticated
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
	})

	Context("when using anonymous incoming with inline outgoing auth", func() {
		It("should configure inline outgoing auth with anonymous incoming", func() {
			By("Verifying VirtualMCPServer has inline auth configuration")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Spec.IncomingAuth.Type).To(Equal("anonymous"))
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("inline"))
		})

		It("should proxy tool calls with inline auth configuration", func() {
			By("Creating MCP client with anonymous auth")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

			By("Listing and calling backend tool through inline auth proxy")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			var targetToolName string
			for _, tool := range tools.Tools {
				if tool.Name == fetchToolName || strings.HasSuffix(tool.Name, fetchToolName) {
					targetToolName = tool.Name
					break
				}
			}
			Expect(targetToolName).ToNot(BeEmpty())

			GinkgoWriter.Printf("Calling tool '%s' with anonymous incoming and inline outgoing auth\n", targetToolName)

			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = targetToolName
			callRequest.Params.Arguments = map[string]any{
				"url": "https://example.com",
			}

			result, err := mcpClient.CallTool(ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Tool call should succeed with inline auth")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty())

			GinkgoWriter.Printf("Anonymous auth with inline outgoing: tool call succeeded\n")
		})
	})
})

var _ = Describe("VirtualMCPServer Inline Auth with OIDC Incoming", Ordered, func() {
	var (
		testNamespace       = "default"
		mcpGroupName        = "test-inline-auth-oidc-group"
		vmcpServerName      = "test-vmcp-inline-auth-oidc"
		backend1Name        = "backend-fetch-inline-oidc"
		secretName          = "vmcp-oidc-secret"
		mockOIDCServerName  = "mock-oidc-http"
		instrumentedBackend = "instrumented-backend-oidc"
		timeout             = 5 * time.Minute
		pollingInterval     = 5 * time.Second
		vmcpNodePort        int32
		mockOIDCIssuerURL   string
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Deploying mock OIDC server with HTTP")
		DeployMockOIDCServerHTTP(ctx, k8sClient, testNamespace, mockOIDCServerName)
		mockOIDCIssuerURL = fmt.Sprintf("http://%s.%s.svc.cluster.local", mockOIDCServerName, testNamespace)

		By("Deploying instrumented backend that logs Bearer tokens")
		DeployInstrumentedBackendServer(ctx, k8sClient, testNamespace, instrumentedBackend)

		By("Creating OIDC client secret")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"clientSecret": "test-secret-value",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		By("Creating MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for VirtualMCP inline auth with OIDC incoming",
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

		By("Creating backend MCPServer")
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

		By("Creating VirtualMCPServer with OIDC incoming and inline outgoing auth")
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
					Type: "oidc",
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: "inline",
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:                          mockOIDCIssuerURL,
							ClientID:                        "test-client-id",
							Audience:                        "test-audience",
							JWKSAllowPrivateIP:              true, // Allow private IP for JWKS endpoint
							ProtectedResourceAllowPrivateIP: true,
							InsecureAllowHTTP:               true, // Allow HTTP OIDC for testing
							ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: secretName,
								Key:  "clientSecret",
							},
						},
					},
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "inline",
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
		// Dump vmcp pod logs before cleanup
		By("Capturing vmcp pod logs before cleanup")
		podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpServerName, testNamespace)
		if err == nil && len(podList.Items) > 0 {
			for _, pod := range podList.Items {
				fmt.Printf("=== Capturing logs for pod %s before cleanup ===\n", pod.Name)
				for _, containerStatus := range pod.Status.ContainerStatuses {
					previous := containerStatus.RestartCount > 0
					logs, logErr := getPodLogs(ctx, testNamespace, pod.Name, containerStatus.Name, previous)
					if logErr != nil {
						fmt.Printf("Failed to get logs for container %s: %v\n", containerStatus.Name, logErr)
					} else if logs != "" {
						fmt.Printf("Container %s logs:\n%s\n", containerStatus.Name, logs)
					}
				}
			}
		}

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
		k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: testNamespace},
		})
		CleanupMockServer(ctx, k8sClient, testNamespace, mockOIDCServerName, "")
		CleanupMockServer(ctx, k8sClient, testNamespace, instrumentedBackend, "")
	})

	Context("when using OIDC incoming with inline outgoing auth", func() {
		It("should configure inline outgoing auth with OIDC incoming", func() {
			By("Verifying VirtualMCPServer has OIDC incoming and inline outgoing auth")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Spec.IncomingAuth.Type).To(Equal("oidc"))
			Expect(vmcpServer.Spec.IncomingAuth.OIDCConfig).ToNot(BeNil())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("inline"))
		})

		It("should reject unauthenticated requests when OIDC is required", func() {
			By("Attempting to connect without OIDC token")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err = mcpClient.Start(ctx)
			if err == nil {
				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcpProtocolVersion
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-e2e-test",
					Version: "1.0.0",
				}
				_, err = mcpClient.Initialize(ctx, initRequest)
			}

			Expect(err).To(HaveOccurred(), "Should reject request without valid OIDC token")
			GinkgoWriter.Printf("OIDC incoming auth correctly rejected unauthenticated request\n")
		})

		It("should call mock OIDC server for discovery", func() {
			By("Checking mock OIDC server stats for discovery requests")
			Eventually(func() bool {
				stats, err := GetMockOIDCStats(ctx, k8sClient, testNamespace, mockOIDCServerName)
				if err != nil {
					GinkgoWriter.Printf("Failed to get OIDC stats: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("Mock OIDC server stats: %+v\n", stats)
				// Check if discovery_requests is present and > 0
				count, ok := stats["discovery_requests"]
				return ok && count > 0
			}, 2*time.Minute, 10*time.Second).Should(BeTrue(), "Mock OIDC server should receive discovery requests")
		})

		It("should generate and pass Bearer tokens to backends", func() {
			By("Making an MCP tools/list request through vmcp to trigger token generation")
			// When vmcp receives a request, it should automatically:
			// 1. Get a token from the OIDC server using inline outgoing auth
			// 2. Pass that token to the backend in the Authorization header

			// For now, just wait and check if the backend has received any Bearer tokens
			// The vmcp may make discovery or health check requests to backends automatically
			// which would trigger the token flow

			By("Checking instrumented backend stats for Bearer token requests")
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
			}, 2*time.Minute, 10*time.Second).Should(BeTrue(), "Backend should receive Bearer tokens in Authorization header")
		})
	})
})
