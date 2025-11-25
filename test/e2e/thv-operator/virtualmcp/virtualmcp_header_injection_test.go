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

var _ = Describe("VirtualMCPServer Header Injection Auth", Ordered, func() {
	var (
		testNamespace       = "default"
		mcpGroupName        = "test-header-injection-group"
		vmcpServerName      = "test-vmcp-header-injection"
		backend1Name        = "backend-header-injection"
		authConfigName      = "test-header-injection-auth"
		secretName          = "test-api-key-secret"
		instrumentedBackend = "instrumented-backend-header"
		timeout             = 5 * time.Minute
		pollingInterval     = 5 * time.Second
		vmcpNodePort        int32
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Creating API key secret for header injection")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"api-key": "test-secret-api-key-12345",
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		By("Creating MCPExternalAuthConfig with header injection")
		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfigName,
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
		Expect(k8sClient.Create(ctx, authConfig)).To(Succeed())

		By("Deploying instrumented backend that logs custom headers")
		DeployInstrumentedBackendServer(ctx, k8sClient, testNamespace, instrumentedBackend)

		By("Creating MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for VirtualMCP header injection auth",
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
					Source: "discovered", // vMCP will discover header injection from backend
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
		CleanupMockServer(ctx, k8sClient, testNamespace, instrumentedBackend, "")
	})

	Context("when using header injection with discovered mode", func() {
		It("should create MCPExternalAuthConfig with header injection", func() {
			By("Verifying MCPExternalAuthConfig exists with correct configuration")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfigName,
				Namespace: testNamespace,
			}, authConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(authConfig.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeHeaderInjection))
			Expect(authConfig.Spec.HeaderInjection).ToNot(BeNil())
			Expect(authConfig.Spec.HeaderInjection.HeaderName).To(Equal("X-API-Key"))
			Expect(authConfig.Spec.HeaderInjection.ValueSecretRef).ToNot(BeNil())
			Expect(authConfig.Spec.HeaderInjection.ValueSecretRef.Name).To(Equal(secretName))
			Expect(authConfig.Spec.HeaderInjection.ValueSecretRef.Key).To(Equal("api-key"))
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

		It("should be able to connect to vMCP server", func() {
			By("Creating MCP client without authentication (anonymous)")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			err = mcpClient.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("Initializing MCP connection")
			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcpProtocolVersion
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-e2e-test",
				Version: "1.0.0",
			}
			_, err = mcpClient.Initialize(ctx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			GinkgoWriter.Printf("Successfully connected to vMCP with discovered header injection auth\n")
		})

		It("should inject X-API-Key header in backend requests", func() {
			Skip("Skipping until vMCP discovered mode fully implements header injection converter integration")

			By("Making a request through vMCP to trigger header injection")
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

			// List tools to trigger backend request
			listRequest := mcp.ListToolsRequest{}
			_, err = mcpClient.ListTools(ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			By("Checking instrumented backend stats for X-API-Key header")
			Eventually(func() bool {
				stats, err := GetInstrumentedBackendStats(ctx, k8sClient, testNamespace, instrumentedBackend)
				if err != nil {
					GinkgoWriter.Printf("Failed to get backend stats: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("Instrumented backend stats: %+v\n", stats)
				// Check if custom_header_requests (X-API-Key) is present and > 0
				count, ok := stats["custom_header_requests"]
				return ok && count > 0
			}, 2*time.Minute, 10*time.Second).Should(BeTrue(),
				"Backend should receive X-API-Key header in requests")
		})
	})
})

var _ = Describe("VirtualMCPServer Header Injection with Direct Value", Ordered, func() {
	var (
		testNamespace  = "default"
		mcpGroupName   = "test-header-direct-value-group"
		vmcpServerName = "test-vmcp-header-direct"
		backend1Name   = "backend-header-direct"
		authConfigName = "test-header-direct-auth"
		timeout        = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort   int32
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Creating MCPExternalAuthConfig with direct header value")
		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-Custom-Auth",
					Value:      "direct-value-12345", // Direct value instead of secret
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
				Description: "Test MCP Group for header injection with direct value",
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

		By("Creating backend MCPServer with direct value auth config")
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
					Source: "discovered",
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
	})

	Context("when using header injection with direct value", func() {
		It("should create MCPExternalAuthConfig with direct value", func() {
			By("Verifying MCPExternalAuthConfig has direct value configuration")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfigName,
				Namespace: testNamespace,
			}, authConfig)
			Expect(err).ToNot(HaveOccurred())
			Expect(authConfig.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeHeaderInjection))
			Expect(authConfig.Spec.HeaderInjection).ToNot(BeNil())
			Expect(authConfig.Spec.HeaderInjection.HeaderName).To(Equal("X-Custom-Auth"))
			Expect(authConfig.Spec.HeaderInjection.Value).To(Equal("direct-value-12345"))
			Expect(authConfig.Spec.HeaderInjection.ValueSecretRef).To(BeNil())
		})

		It("should be able to connect to vMCP server with direct value auth", func() {
			By("Creating MCP client")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			mcpClient, err := client.NewStreamableHttpClient(serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			err = mcpClient.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("Initializing MCP connection")
			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcpProtocolVersion
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "toolhive-e2e-test",
				Version: "1.0.0",
			}
			_, err = mcpClient.Initialize(ctx, initRequest)
			Expect(err).ToNot(HaveOccurred())

			GinkgoWriter.Printf("Successfully connected to vMCP with direct value header injection\n")
		})
	})
})
