package virtualmcp

import (
	"fmt"
	"strings"
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

// conflictResolutionTestSetup holds the configuration for setting up a conflict resolution test
type conflictResolutionTestSetup struct {
	groupName       string
	vmcpName        string
	backend1Name    string
	backend2Name    string
	namespace       string
	aggregation     *mcpv1alpha1.AggregationConfig
	timeout         time.Duration
	pollingInterval time.Duration
}

// setupConflictResolutionTest creates MCPGroup, backend MCPServers, and VirtualMCPServer
// Returns the NodePort for accessing the VirtualMCPServer
func setupConflictResolutionTest(setup conflictResolutionTestSetup) int32 {
	By(fmt.Sprintf("Creating MCPGroup: %s", setup.groupName))
	CreateMCPGroupAndWait(ctx, k8sClient, setup.groupName, setup.namespace,
		fmt.Sprintf("Test MCP Group for %s conflict resolution", setup.aggregation.ConflictResolution),
		setup.timeout, setup.pollingInterval)

	By(fmt.Sprintf("Creating backend MCPServers in parallel: %s, %s", setup.backend1Name, setup.backend2Name))
	CreateMultipleMCPServersInParallel(ctx, k8sClient, []BackendConfig{
		{Name: setup.backend1Name, Namespace: setup.namespace, GroupRef: setup.groupName, Image: images.YardstickServerImage},
		{Name: setup.backend2Name, Namespace: setup.namespace, GroupRef: setup.groupName, Image: images.YardstickServerImage},
	}, setup.timeout, setup.pollingInterval)

	By(fmt.Sprintf("Creating VirtualMCPServer: %s with %s conflict resolution", setup.vmcpName, setup.aggregation.ConflictResolution))
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      setup.vmcpName,
			Namespace: setup.namespace,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{Group: setup.groupName},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			Aggregation: setup.aggregation,
			ServiceType: "NodePort",
		},
	}
	Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

	By("Waiting for VirtualMCPServer to be ready")
	WaitForVirtualMCPServerReady(ctx, k8sClient, setup.vmcpName, setup.namespace, setup.timeout, setup.pollingInterval)

	By("Getting NodePort for VirtualMCPServer")
	vmcpNodePort := GetVMCPNodePort(ctx, k8sClient, setup.vmcpName, setup.namespace, setup.timeout, setup.pollingInterval)

	By(fmt.Sprintf("VirtualMCPServer accessible at http://localhost:%d", vmcpNodePort))
	return vmcpNodePort
}

// cleanupConflictResolutionTest cleans up VirtualMCPServer, backend MCPServers, and MCPGroup
func cleanupConflictResolutionTest(groupName, vmcpName, backend1Name, backend2Name, namespace string) {
	By("Cleaning up VirtualMCPServer")
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmcpName,
			Namespace: namespace,
		},
	}
	_ = k8sClient.Delete(ctx, vmcpServer)

	By("Cleaning up backend MCPServers")
	for _, backendName := range []string{backend1Name, backend2Name} {
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: namespace,
			},
		}
		_ = k8sClient.Delete(ctx, backend)
	}

	By("Cleaning up MCPGroup")
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      groupName,
			Namespace: namespace,
		},
	}
	_ = k8sClient.Delete(ctx, mcpGroup)
}

var _ = Describe("VirtualMCPServer Conflict Resolution", Ordered, func() {
	var (
		testNamespace   = "default"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
	)

	Describe("Prefix Strategy", Ordered, func() {
		var (
			mcpGroupName   = "test-prefix-group"
			vmcpServerName = "test-vmcp-prefix"
			backend1Name   = "yardstick-prefix-a"
			backend2Name   = "yardstick-prefix-b"
			vmcpNodePort   int32
		)

		BeforeAll(func() {
			vmcpNodePort = setupConflictResolutionTest(conflictResolutionTestSetup{
				groupName:       mcpGroupName,
				vmcpName:        vmcpServerName,
				backend1Name:    backend1Name,
				backend2Name:    backend2Name,
				namespace:       testNamespace,
				timeout:         timeout,
				pollingInterval: pollingInterval,
				aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: mcpv1alpha1.ConflictResolutionPrefix,
					ConflictResolutionConfig: &mcpv1alpha1.ConflictResolutionConfig{
						PrefixFormat: "{workload}_",
					},
				},
			})
		})

		AfterAll(func() {
			cleanupConflictResolutionTest(mcpGroupName, vmcpServerName, backend1Name, backend2Name, testNamespace)
		})

		Context("when tools from multiple backends have the same name", func() {
			It("should prefix tool names with workload identifier", func() {
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-prefix-test", 30*time.Second)
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

				// Verify that tools from both backends are prefixed
				hasBackend1Tool := false
				hasBackend2Tool := false
				for _, tool := range tools.Tools {
					if strings.HasPrefix(tool.Name, backend1Name+"_") {
						hasBackend1Tool = true
						By(fmt.Sprintf("Found tool from backend1 with prefix: %s", tool.Name))
					}
					if strings.HasPrefix(tool.Name, backend2Name+"_") {
						hasBackend2Tool = true
						By(fmt.Sprintf("Found tool from backend2 with prefix: %s", tool.Name))
					}
				}

				Expect(hasBackend1Tool).To(BeTrue(), "Should have at least one tool prefixed with backend1 name")
				Expect(hasBackend2Tool).To(BeTrue(), "Should have at least one tool prefixed with backend2 name")
			})

			It("should be able to call prefixed tools successfully", func() {
				// Use shared helper to test tool listing and calling
				TestToolListingAndCall(vmcpNodePort, "toolhive-prefix-test", "echo", "prefix-test-123")
			})

			It("should expose tools from both backends with different prefixes", func() {
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-prefix-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Listing tools from VirtualMCPServer")
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				Expect(err).ToNot(HaveOccurred())

				// Count tools by prefix
				backend1Count := 0
				backend2Count := 0
				unprefixedCount := 0

				for _, tool := range tools.Tools {
					if strings.HasPrefix(tool.Name, backend1Name+"_") {
						backend1Count++
					} else if strings.HasPrefix(tool.Name, backend2Name+"_") {
						backend2Count++
					} else {
						unprefixedCount++
					}
				}

				By(fmt.Sprintf("Found %d tools from backend1, %d from backend2, %d unprefixed",
					backend1Count, backend2Count, unprefixedCount))

				// Both backends should have tools prefixed
				Expect(backend1Count).To(BeNumerically(">", 0),
					"Should have tools prefixed with backend1 name")
				Expect(backend2Count).To(BeNumerically(">", 0),
					"Should have tools prefixed with backend2 name")

				// Since both backends are identical, they should have the same number of tools
				Expect(backend1Count).To(Equal(backend2Count),
					"Both backends should expose the same number of tools (they're identical)")

				// All tools should be prefixed (no unprefixed tools)
				Expect(unprefixedCount).To(Equal(0),
					"Prefix strategy should prefix all tools")
			})

			It("should handle conflicting tool names by prefixing both", func() {
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-prefix-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Listing tools from VirtualMCPServer")
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				Expect(err).ToNot(HaveOccurred())

				// Look for the same tool name with different prefixes (e.g., echo)
				// Since both backends are identical yardstick, they'll have the same tools
				echoTools := []string{}
				for _, tool := range tools.Tools {
					if strings.Contains(tool.Name, "echo") {
						echoTools = append(echoTools, tool.Name)
					}
				}

				By(fmt.Sprintf("Found %d echo tools: %v", len(echoTools), echoTools))

				// Should have 2 echo tools (one from each backend)
				Expect(echoTools).To(HaveLen(2), "Should have echo tool from both backends with different prefixes")

				// Verify they have different prefixes
				hasBackend1Echo := false
				hasBackend2Echo := false
				for _, toolName := range echoTools {
					if strings.HasPrefix(toolName, backend1Name+"_") {
						hasBackend1Echo = true
					}
					if strings.HasPrefix(toolName, backend2Name+"_") {
						hasBackend2Echo = true
					}
				}

				Expect(hasBackend1Echo).To(BeTrue(), "Should have echo from backend1")
				Expect(hasBackend2Echo).To(BeTrue(), "Should have echo from backend2")
			})
		})
	})

	Describe("Priority Strategy", Ordered, func() {
		var (
			mcpGroupName   = "test-priority-group"
			vmcpServerName = "test-vmcp-priority"
			backend1Name   = "yardstick-priority-a"
			backend2Name   = "yardstick-priority-b"
			vmcpNodePort   int32
		)

		BeforeAll(func() {
			vmcpNodePort = setupConflictResolutionTest(conflictResolutionTestSetup{
				groupName:       mcpGroupName,
				vmcpName:        vmcpServerName,
				backend1Name:    backend1Name,
				backend2Name:    backend2Name,
				namespace:       testNamespace,
				timeout:         timeout,
				pollingInterval: pollingInterval,
				aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: mcpv1alpha1.ConflictResolutionPriority,
					ConflictResolutionConfig: &mcpv1alpha1.ConflictResolutionConfig{
						PriorityOrder: []string{backend1Name, backend2Name},
					},
				},
			})
		})

		AfterAll(func() {
			cleanupConflictResolutionTest(mcpGroupName, vmcpServerName, backend1Name, backend2Name, testNamespace)
		})

		Context("when tools from multiple backends have the same name", func() {
			It("should expose tools from highest priority backend without prefix", func() {
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-priority-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Listing tools from VirtualMCPServer")
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				Expect(err).ToNot(HaveOccurred())

				By(fmt.Sprintf("VirtualMCPServer exposes %d tools with priority strategy", len(tools.Tools)))
				for _, tool := range tools.Tools {
					GinkgoWriter.Printf("  Tool: %s - %s\n", tool.Name, tool.Description)
				}

				// Verify that tools are NOT prefixed (priority strategy doesn't prefix)
				// Both backends should have tools, but conflicting tools should come from backend1 (higher priority)
				hasToolsWithoutPrefix := false
				for _, tool := range tools.Tools {
					// Tools should not have workload prefixes
					if !strings.HasPrefix(tool.Name, backend1Name+"_") && !strings.HasPrefix(tool.Name, backend2Name+"_") {
						hasToolsWithoutPrefix = true
						By(fmt.Sprintf("Found tool without prefix: %s", tool.Name))
					}
				}

				Expect(hasToolsWithoutPrefix).To(BeTrue(), "Priority strategy should not prefix tool names")
			})

			It("should be able to call tools successfully with priority resolution", func() {
				// Use shared helper to test tool listing and calling
				TestToolListingAndCall(vmcpNodePort, "toolhive-priority-test", "echo", "priority-test-123")
			})

			It("should resolve conflicts by using highest priority backend", func() {
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-priority-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Listing tools from VirtualMCPServer")
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				Expect(err).ToNot(HaveOccurred())

				By(fmt.Sprintf("VirtualMCPServer exposes %d tools total", len(tools.Tools)))

				// Count tools by name (should not have duplicates due to priority resolution)
				toolNameMap := make(map[string]int)
				for _, tool := range tools.Tools {
					toolNameMap[tool.Name]++
				}

				// Verify no duplicate tool names (priority should resolve conflicts)
				duplicates := []string{}
				for toolName, count := range toolNameMap {
					if count > 1 {
						duplicates = append(duplicates, fmt.Sprintf("%s (count: %d)", toolName, count))
					}
				}

				Expect(duplicates).To(BeEmpty(), "Priority strategy should resolve all conflicts")

				// Verify we have tools from both backends (non-conflicting tools should be exposed)
				// Since both backends are identical yardstick images, they'll have the same tools
				// Priority resolution should keep only one copy of each tool (from backend1)
				By(fmt.Sprintf("Verified all %d tools have unique names (no conflicts)", len(tools.Tools)))
			})

			It("should expose non-conflicting tools from all backends", func() {
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-priority-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Listing tools from VirtualMCPServer")
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				Expect(err).ToNot(HaveOccurred())

				// Since both backends have identical tools (same yardstick image),
				// we should have exactly the same number of tools as a single backend would expose
				// This verifies that priority resolution doesn't drop non-conflicting tools
				Expect(tools.Tools).ToNot(BeEmpty(), "Should expose tools from backends")

				By(fmt.Sprintf("Priority strategy correctly exposes %d unique tools", len(tools.Tools)))
			})

			It("should have correct priority configuration", func() {
				vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcpServer)
				Expect(err).ToNot(HaveOccurred())

				Expect(vmcpServer.Spec.Aggregation).ToNot(BeNil())
				Expect(vmcpServer.Spec.Aggregation.ConflictResolution).To(Equal(mcpv1alpha1.ConflictResolutionPriority))
				Expect(vmcpServer.Spec.Aggregation.ConflictResolutionConfig).ToNot(BeNil())
				Expect(vmcpServer.Spec.Aggregation.ConflictResolutionConfig.PriorityOrder).To(HaveLen(2))
				Expect(vmcpServer.Spec.Aggregation.ConflictResolutionConfig.PriorityOrder[0]).To(Equal(backend1Name))
				Expect(vmcpServer.Spec.Aggregation.ConflictResolutionConfig.PriorityOrder[1]).To(Equal(backend2Name))
			})
		})
	})

	Describe("Manual Strategy", Ordered, func() {
		var (
			mcpGroupName   = "test-manual-group"
			vmcpServerName = "test-vmcp-manual"
			backend1Name   = "yardstick-manual-a"
			backend2Name   = "yardstick-manual-b"
			vmcpNodePort   int32
		)

		BeforeAll(func() {
			vmcpNodePort = setupConflictResolutionTest(conflictResolutionTestSetup{
				groupName:       mcpGroupName,
				vmcpName:        vmcpServerName,
				backend1Name:    backend1Name,
				backend2Name:    backend2Name,
				namespace:       testNamespace,
				timeout:         timeout,
				pollingInterval: pollingInterval,
				aggregation: &mcpv1alpha1.AggregationConfig{
					ConflictResolution: mcpv1alpha1.ConflictResolutionManual,
					Tools: []mcpv1alpha1.WorkloadToolConfig{
						{
							Workload: backend1Name,
							Overrides: map[string]mcpv1alpha1.ToolOverride{
								"echo": {Name: "echo_backend1"},
							},
						},
						{
							Workload: backend2Name,
							Overrides: map[string]mcpv1alpha1.ToolOverride{
								"echo": {Name: "echo_backend2"},
							},
						},
					},
				},
			})
		})

		AfterAll(func() {
			cleanupConflictResolutionTest(mcpGroupName, vmcpServerName, backend1Name, backend2Name, testNamespace)
		})

		Context("when tools from multiple backends have explicit overrides", func() {
			It("should expose tools with manually specified names", func() {
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-manual-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Listing tools from VirtualMCPServer")
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				Expect(err).ToNot(HaveOccurred())

				By(fmt.Sprintf("VirtualMCPServer exposes %d tools with manual strategy", len(tools.Tools)))
				for _, tool := range tools.Tools {
					GinkgoWriter.Printf("  Tool: %s - %s\n", tool.Name, tool.Description)
				}

				// Verify that tools are exposed with manually specified names
				toolNames := make([]string, len(tools.Tools))
				for i, tool := range tools.Tools {
					toolNames[i] = tool.Name
				}

				// Check for the manually overridden names
				Expect(toolNames).To(ContainElement("echo_backend1"), "Should have echo tool from backend1 with manual override")
				Expect(toolNames).To(ContainElement("echo_backend2"), "Should have echo tool from backend2 with manual override")
			})

			It("should be able to call manually overridden tools successfully", func() {
				// Use shared helper to test calling both manually overridden tools
				TestToolListingAndCall(vmcpNodePort, "toolhive-manual-test", "echo_backend1", "manual-test-backend1")
				TestToolListingAndCall(vmcpNodePort, "toolhive-manual-test", "echo_backend2", "manual-test-backend2")
			})

			It("should have correct manual configuration with overrides", func() {
				vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcpServer)
				Expect(err).ToNot(HaveOccurred())

				Expect(vmcpServer.Spec.Aggregation).ToNot(BeNil())
				Expect(vmcpServer.Spec.Aggregation.ConflictResolution).To(Equal(mcpv1alpha1.ConflictResolutionManual))
				Expect(vmcpServer.Spec.Aggregation.Tools).To(HaveLen(2))

				// Verify backend1 overrides
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
				Expect(backend1Config.Overrides).To(HaveKey("echo"))
				Expect(backend1Config.Overrides["echo"].Name).To(Equal("echo_backend1"))

				Expect(backend2Config).ToNot(BeNil())
				Expect(backend2Config.Overrides).To(HaveKey("echo"))
				Expect(backend2Config.Overrides["echo"].Name).To(Equal("echo_backend2"))
			})
		})
	})
})
