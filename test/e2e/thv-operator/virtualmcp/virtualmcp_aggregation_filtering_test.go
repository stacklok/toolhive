package virtualmcp

import (
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// Compile-time check to ensure corev1 is used (for Service type)
var _ = corev1.ServiceSpec{}

var _ = Describe("VirtualMCPServer Aggregation Filtering", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-filtering-group"
		vmcpServerName  = "test-vmcp-filtering"
		backend1Name    = "yardstick-filter-a"
		backend2Name    = "yardstick-filter-b"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Creating MCPGroup for filtering test")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for tool filtering E2E tests",
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

		By("Creating first yardstick backend MCPServer")
		backend1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend1Name,
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
			},
		}
		Expect(k8sClient.Create(ctx, backend1)).To(Succeed())

		By("Creating second yardstick backend MCPServer")
		backend2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend2Name,
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
			},
		}
		Expect(k8sClient.Create(ctx, backend2)).To(Succeed())

		By("Waiting for backend MCPServers to be ready")
		for _, backendName := range []string{backend1Name, backend2Name} {
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

		By("Creating VirtualMCPServer with tool filtering - only expose tools from backend1")
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
				Aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: "prefix",
					// Tool filtering: only allow echo from backend1, nothing from backend2
					// TODO(#2779): Currently there's no way to exclude all tools from a backend.
					// Using a non-matching filter as a workaround until excludeAll is implemented.
					// See: https://github.com/stacklok/toolhive/issues/2779
					Tools: []mcpv1alpha1.WorkloadToolConfig{
						{
							Workload: backend1Name,
							Filter:   []string{"echo"}, // Only expose echo tool
						},
						{
							Workload: backend2Name,
							Filter:   []string{"nonexistent_tool"}, // Filter out all tools (workaround)
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
		for _, backendName := range []string{backend1Name, backend2Name} {
			backend := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backendName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, backend)
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

	Context("when tool filtering is configured", func() {
		It("should only expose filtered tools from backend1", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-filtering-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			By(fmt.Sprintf("VirtualMCPServer exposes %d tools after filtering", len(tools.Tools)))
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  Exposed tool: %s - %s\n", tool.Name, tool.Description)
			}

			// Verify filtering: should only have echo tool from backend1
			toolNames := make([]string, len(tools.Tools))
			for i, tool := range tools.Tools {
				toolNames[i] = tool.Name
			}

			// Should have tool from backend1
			hasBackend1Tool := false
			for _, name := range toolNames {
				if strings.Contains(name, backend1Name) && strings.Contains(name, "echo") {
					hasBackend1Tool = true
				}
			}
			Expect(hasBackend1Tool).To(BeTrue(), "Should have echo tool from backend1")

			// Should NOT have any tool from backend2 (filtered with non-matching filter)
			// TODO(#2779): Once excludeAll is implemented, update this test to use it
			hasBackend2Tool := false
			for _, name := range toolNames {
				if strings.Contains(name, backend2Name) {
					hasBackend2Tool = true
				}
			}
			Expect(hasBackend2Tool).To(BeFalse(), "Should NOT have any tool from backend2 (filtered out via non-matching filter)")
		})

		It("should still allow calling filtered tools", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-filtering-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing available tools")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			// Find the backend1 echo tool
			var targetToolName string
			for _, tool := range tools.Tools {
				if strings.Contains(tool.Name, backend1Name) && strings.Contains(tool.Name, "echo") {
					targetToolName = tool.Name
					break
				}
			}
			Expect(targetToolName).ToNot(BeEmpty(), "Should find echo tool from backend1")

			By(fmt.Sprintf("Calling filtered echo tool: %s", targetToolName))
			testInput := "filtered123"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = targetToolName
			callRequest.Params.Arguments = map[string]any{
				"input": testInput,
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Should be able to call filtered tool")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

			GinkgoWriter.Printf("Filtered tool call successful: %s\n", targetToolName)
		})
	})

	Context("when verifying filtering configuration", func() {
		It("should have correct aggregation configuration with tool filters", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.Aggregation).ToNot(BeNil())
			Expect(vmcpServer.Spec.Aggregation.Tools).To(HaveLen(2))

			// Verify backend1 filter allows echo
			var backend1Config *mcpv1alpha1.WorkloadToolConfig
			var backend2Config *mcpv1alpha1.WorkloadToolConfig
			for i := range vmcpServer.Spec.Aggregation.Tools {
				if vmcpServer.Spec.Aggregation.Tools[i].Workload == backend1Name {
					backend1Config = &vmcpServer.Spec.Aggregation.Tools[i]
				}
				if vmcpServer.Spec.Aggregation.Tools[i].Workload == backend2Name {
					backend2Config = &vmcpServer.Spec.Aggregation.Tools[i]
				}
			}

			Expect(backend1Config).ToNot(BeNil())
			Expect(backend1Config.Filter).To(ContainElement("echo"))

			Expect(backend2Config).ToNot(BeNil())
			// TODO(#2779): Once excludeAll is implemented, update this to use excludeAll: true
			Expect(backend2Config.Filter).To(ContainElement("nonexistent_tool"), "Backend2 should have non-matching filter as workaround")
		})
	})
})
