package virtualmcp

import (
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Tool Overrides", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-overrides-group"
		vmcpServerName  = "test-vmcp-overrides"
		backendName     = "yardstick-override"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32

		// The original and renamed tool names
		originalToolName = "echo"
		renamedToolName  = "custom_echo_tool"
		newDescription   = "A renamed echo tool with custom description"
	)

	BeforeAll(func() {
		By("Creating MCPGroup for overrides test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for tool overrides E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer with tool overrides")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
					Aggregation: &vmcpconfig.AggregationConfig{
						ConflictResolution: "prefix",
						// Tool overrides: rename echo to custom_echo_tool with new description
						// Note: Filter uses the user-facing name (after override), so we filter by
						// the renamed tool name, not the original name.
						Tools: []*vmcpconfig.WorkloadToolConfig{
							{
								Workload: backendName,
								Filter:   []string{renamedToolName}, // Filter by user-facing name (after override)
								Overrides: map[string]*vmcpconfig.ToolOverride{
									originalToolName: {
										Name:        renamedToolName,
										Description: newDescription,
									},
								},
							},
						},
					},
				},
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

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
			// Use shared helper to test tool listing and calling
			TestToolListingAndCall(vmcpNodePort, "toolhive-overrides-test", renamedToolName, "override_test_123")
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

			Expect(vmcpServer.Spec.Config.Aggregation).ToNot(BeNil())
			Expect(vmcpServer.Spec.Config.Aggregation.Tools).To(HaveLen(1))

			// Verify backend config has overrides
			backendConfig := vmcpServer.Spec.Config.Aggregation.Tools[0]
			Expect(backendConfig.Workload).To(Equal(backendName))
			Expect(backendConfig.Overrides).To(HaveLen(1))

			// Filter should contain the user-facing name (after override)
			Expect(backendConfig.Filter).To(ContainElement(renamedToolName),
				"Filter should contain the renamed tool name (user-facing name)")

			override, exists := backendConfig.Overrides[originalToolName]
			Expect(exists).To(BeTrue(), "Should have override for original tool name")
			Expect(override.Name).To(Equal(renamedToolName))
			Expect(override.Description).To(Equal(newDescription))
		})
	})
})
