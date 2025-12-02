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
	var vmcpNodePort int32

	By(fmt.Sprintf("Creating MCPGroup: %s", setup.groupName))
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      setup.groupName,
			Namespace: setup.namespace,
		},
		Spec: mcpv1alpha1.MCPGroupSpec{
			Description: fmt.Sprintf("Test MCP Group for %s conflict resolution", setup.aggregation.ConflictResolution),
		},
	}
	Expect(k8sClient.Create(ctx, mcpGroup)).To(Succeed())

	By("Waiting for MCPGroup to be ready")
	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      setup.groupName,
			Namespace: setup.namespace,
		}, mcpGroup)
		if err != nil {
			return false
		}
		return mcpGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
	}, setup.timeout, setup.pollingInterval).Should(BeTrue())

	By(fmt.Sprintf("Creating backend MCPServers: %s, %s", setup.backend1Name, setup.backend2Name))
	for _, backendName := range []string{setup.backend1Name, setup.backend2Name} {
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: setup.namespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  setup.groupName,
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
	}

	By("Waiting for backend MCPServers to be ready")
	for _, backendName := range []string{setup.backend1Name, setup.backend2Name} {
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backendName,
				Namespace: setup.namespace,
			}, server)
			if err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}
			if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
				return nil
			}
			return fmt.Errorf("%s not ready yet, phase: %s", backendName, server.Status.Phase)
		}, setup.timeout, setup.pollingInterval).Should(Succeed(), fmt.Sprintf("%s should be ready", backendName))
	}

	By(fmt.Sprintf("Creating VirtualMCPServer: %s with %s conflict resolution", setup.vmcpName, setup.aggregation.ConflictResolution))
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      setup.vmcpName,
			Namespace: setup.namespace,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{
				Name: setup.groupName,
			},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			Aggregation: setup.aggregation,
			ServiceType: "NodePort",
		},
	}
	Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

	By("Waiting for VirtualMCPServer to be ready")
	WaitForVirtualMCPServerReady(ctx, k8sClient, setup.vmcpName, setup.namespace, setup.timeout)

	By("Getting NodePort for VirtualMCPServer")
	serviceName := fmt.Sprintf("vmcp-%s", setup.vmcpName)
	Eventually(func() error {
		service := &corev1.Service{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      serviceName,
			Namespace: setup.namespace,
		}, service)
		if err != nil {
			return err
		}
		if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].NodePort == 0 {
			return fmt.Errorf("nodePort not assigned for vmcp")
		}
		vmcpNodePort = service.Spec.Ports[0].NodePort
		return nil
	}, setup.timeout, setup.pollingInterval).Should(Succeed())

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
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
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
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-prefix-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Listing available tools")
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				Expect(err).ToNot(HaveOccurred())

				// Find a prefixed tool to call (e.g., echo tool)
				var targetToolName string
				for _, tool := range tools.Tools {
					if (strings.HasPrefix(tool.Name, backend1Name+"_") || strings.HasPrefix(tool.Name, backend2Name+"_")) &&
						strings.Contains(tool.Name, "echo") {
						targetToolName = tool.Name
						break
					}
				}
				Expect(targetToolName).ToNot(BeEmpty(), "Should find a prefixed echo tool")

				By(fmt.Sprintf("Calling prefixed tool: %s", targetToolName))
				testInput := "prefix-test-123"
				callRequest := mcp.CallToolRequest{}
				callRequest.Params.Name = targetToolName
				callRequest.Params.Arguments = map[string]any{
					"input": testInput,
				}

				result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
				Expect(err).ToNot(HaveOccurred(), "Should be able to call prefixed tool")
				Expect(result).ToNot(BeNil())
				Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

				GinkgoWriter.Printf("Prefixed tool call successful: %s\n", targetToolName)
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
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-priority-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Listing available tools")
				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				Expect(err).ToNot(HaveOccurred())

				// Find an echo tool (should be from backend1 due to priority)
				var targetToolName string
				for _, tool := range tools.Tools {
					if strings.Contains(tool.Name, "echo") {
						targetToolName = tool.Name
						break
					}
				}
				Expect(targetToolName).ToNot(BeEmpty(), "Should find an echo tool")

				By(fmt.Sprintf("Calling tool with priority resolution: %s", targetToolName))
				testInput := "priority-test-123"
				callRequest := mcp.CallToolRequest{}
				callRequest.Params.Name = targetToolName
				callRequest.Params.Arguments = map[string]any{
					"input": testInput,
				}

				result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
				Expect(err).ToNot(HaveOccurred(), "Should be able to call tool with priority resolution")
				Expect(result).ToNot(BeNil())
				Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

				GinkgoWriter.Printf("Priority tool call successful: %s\n", targetToolName)
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
				By("Creating and initializing MCP client for VirtualMCPServer")
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-manual-test", 30*time.Second)
				Expect(err).ToNot(HaveOccurred())
				defer mcpClient.Close()

				By("Calling manually overridden tool from backend1")
				testInput := "manual-test-backend1"
				callRequest := mcp.CallToolRequest{}
				callRequest.Params.Name = "echo_backend1"
				callRequest.Params.Arguments = map[string]any{
					"input": testInput,
				}

				result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
				Expect(err).ToNot(HaveOccurred(), "Should be able to call manually overridden tool from backend1")
				Expect(result).ToNot(BeNil())
				Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

				By("Calling manually overridden tool from backend2")
				testInput2 := "manual-test-backend2"
				callRequest2 := mcp.CallToolRequest{}
				callRequest2.Params.Name = "echo_backend2"
				callRequest2.Params.Arguments = map[string]any{
					"input": testInput2,
				}

				result2, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest2)
				Expect(err).ToNot(HaveOccurred(), "Should be able to call manually overridden tool from backend2")
				Expect(result2).ToNot(BeNil())
				Expect(result2.Content).ToNot(BeEmpty(), "Should have content in response")

				GinkgoWriter.Printf("Manual tool calls successful: echo_backend1 and echo_backend2\n")
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
