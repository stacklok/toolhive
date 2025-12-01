package virtualmcp

import (
	"fmt"
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

var _ = Describe("VirtualMCPServer Tool Overrides", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-overrides-group"
		vmcpServerName  = "test-vmcp-overrides"
		backendName     = "yardstick-override"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32

		// The original and renamed tool names
		originalToolName = "echo"
		renamedToolName  = "custom_echo_tool"
		newDescription   = "A renamed echo tool with custom description"
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Creating MCPGroup for overrides test")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for tool overrides E2E tests",
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

		By("Creating yardstick backend MCPServer")
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
			return fmt.Errorf("%s not ready yet, phase: %s", backendName, server.Status.Phase)
		}, timeout, pollingInterval).Should(Succeed(), "Backend should be ready")

		By("Creating VirtualMCPServer with tool overrides")
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
					// Tool overrides: rename echo to custom_echo_tool with new description
					Tools: []mcpv1alpha1.WorkloadToolConfig{
						{
							Workload: backendName,
							Filter:   []string{originalToolName}, // Only expose echo
							Overrides: map[string]mcpv1alpha1.ToolOverride{
								originalToolName: {
									Name:        renamedToolName,
									Description: newDescription,
								},
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

		By("Cleaning up backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, backend)

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpGroup)
	})

	Context("when tool overrides are configured", func() {
		It("should expose tools with renamed names", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-overrides-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			By(fmt.Sprintf("VirtualMCPServer exposes %d tools", len(tools.Tools)))
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  Tool: %s - %s\n", tool.Name, tool.Description)
			}

			// Should have the renamed tool
			var foundTool *mcp.Tool
			for i := range tools.Tools {
				tool := &tools.Tools[i]
				// Tool name will be prefixed with workload name due to prefix conflict resolution
				// Format: {workload}_{original_or_renamed_tool}
				if tool.Name == fmt.Sprintf("%s_%s", backendName, renamedToolName) {
					foundTool = tool
					break
				}
			}

			Expect(foundTool).ToNot(BeNil(), "Should find renamed tool: %s_%s", backendName, renamedToolName)
			Expect(foundTool.Description).To(Equal(newDescription), "Tool should have the custom description")
		})

		It("should NOT expose the original tool name", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-overrides-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			// Should NOT have the original tool name
			for _, tool := range tools.Tools {
				originalWithPrefix := fmt.Sprintf("%s_%s", backendName, originalToolName)
				Expect(tool.Name).ToNot(Equal(originalWithPrefix),
					"Original tool name should not be exposed when renamed")
			}
		})

		It("should allow calling the renamed tool", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-overrides-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			renamedToolFullName := fmt.Sprintf("%s_%s", backendName, renamedToolName)
			By(fmt.Sprintf("Calling renamed tool: %s", renamedToolFullName))

			testInput := "override_test_123"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = renamedToolFullName
			callRequest.Params.Arguments = map[string]any{
				"input": testInput,
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Should be able to call renamed tool")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

			// Yardstick echo tool echoes back the input
			GinkgoWriter.Printf("Renamed tool call result: %+v\n", result.Content)
		})
	})

	Context("when verifying override configuration", func() {
		It("should have correct aggregation configuration with tool overrides", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.Aggregation).ToNot(BeNil())
			Expect(vmcpServer.Spec.Aggregation.Tools).To(HaveLen(1))

			// Verify backend config has overrides
			backendConfig := vmcpServer.Spec.Aggregation.Tools[0]
			Expect(backendConfig.Workload).To(Equal(backendName))
			Expect(backendConfig.Overrides).To(HaveLen(1))

			override, exists := backendConfig.Overrides[originalToolName]
			Expect(exists).To(BeTrue(), "Should have override for original tool name")
			Expect(override.Name).To(Equal(renamedToolName))
			Expect(override.Description).To(Equal(newDescription))
		})
	})
})
