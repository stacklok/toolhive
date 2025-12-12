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
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Tool Filtering via MCPToolConfig", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-toolconfig-group"
		vmcpServerName  = "test-vmcp-toolconfig"
		toolConfigName  = "test-tool-config"
		backend1Name    = "gofetch-toolconfig-a"
		backend2Name    = "gofetch-toolconfig-b"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for ToolConfig test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for MCPToolConfig E2E tests", timeout, pollingInterval)

		By("Creating gofetch backend MCPServers in parallel")
		CreateMultipleMCPServersInParallel(ctx, k8sClient, []BackendConfig{
			{Name: backend1Name, Namespace: testNamespace, GroupRef: mcpGroupName, Image: images.GofetchServerImage},
			{Name: backend2Name, Namespace: testNamespace, GroupRef: mcpGroupName, Image: images.GofetchServerImage},
		}, timeout, pollingInterval)

		By("Creating MCPToolConfig for filtering and overriding tools")
		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      toolConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPToolConfigSpec{
				// Filter on the overridden name (renamed_fetch), not the backend name (fetch).
				// This is because filtering happens AFTER override is applied.
				ToolsFilter: []string{"renamed_fetch"},
				// Override the fetch tool name and description
				ToolsOverride: map[string]mcpv1alpha1.ToolOverride{
					"fetch": {
						Name:        "renamed_fetch",
						Description: "This fetch tool has been renamed via MCPToolConfig",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, toolConfig)).To(Succeed())

		By("Creating VirtualMCPServer with MCPToolConfig reference for backend1")
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
					Tools: []mcpv1alpha1.WorkloadToolConfig{
						{
							Workload: backend1Name,
							// Reference MCPToolConfig instead of inline Filter
							ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
								Name: toolConfigName,
							},
						},
						{
							Workload: backend2Name,
							// Use inline filter to exclude all tools from backend2
							Filter: []string{"nonexistent_tool"},
						},
					},
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

		By("Cleaning up MCPToolConfig")
		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      toolConfigName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, toolConfig)

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

	Context("when MCPToolConfig is used for filtering", func() {
		It("should only expose filtered tools from backend1", func() {
			By("Waiting for VirtualMCPServer to discover backends and expose tools")
			var tools *mcp.ListToolsResult
			Eventually(func() error {
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-toolconfig-test", 30*time.Second)
				if err != nil {
					return fmt.Errorf("failed to create MCP client: %w", err)
				}
				defer mcpClient.Close()

				listRequest := mcp.ListToolsRequest{}
				tools, err = mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				if len(tools.Tools) == 0 {
					return fmt.Errorf("no tools available yet (backends may still be connecting)")
				}
				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "VirtualMCPServer should expose tools once backends are connected")

			By(fmt.Sprintf("VirtualMCPServer exposes %d tools after MCPToolConfig filtering", len(tools.Tools)))
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  Exposed tool: %s - %s\n", tool.Name, tool.Description)
			}

			// Verify filtering: should only have fetch tool from backend1 (renamed to renamed_fetch)
			toolNames := make([]string, len(tools.Tools))
			for i, tool := range tools.Tools {
				toolNames[i] = tool.Name
			}

			// Should have tool from backend1 with renamed name
			hasBackend1Tool := false
			for _, name := range toolNames {
				if strings.Contains(name, backend1Name) && strings.Contains(name, "renamed_fetch") {
					hasBackend1Tool = true
				}
			}
			Expect(hasBackend1Tool).To(BeTrue(), "Should have renamed_fetch tool from backend1")

			// Should NOT have the original 'fetch' name
			hasOriginalFetch := false
			for _, name := range toolNames {
				if strings.Contains(name, backend1Name) && strings.Contains(name, "fetch") && !strings.Contains(name, "renamed_fetch") {
					hasOriginalFetch = true
				}
			}
			Expect(hasOriginalFetch).To(BeFalse(), "Should NOT have original 'fetch' name (it should be renamed)")

			// Should NOT have any other gofetch tools from backend1 (filtered out)
			hasOtherBackend1Tools := false
			for _, name := range toolNames {
				if strings.Contains(name, backend1Name) && !strings.Contains(name, "renamed_fetch") {
					hasOtherBackend1Tools = true
					GinkgoWriter.Printf("  Unexpected tool from backend1: %s\n", name)
				}
			}
			Expect(hasOtherBackend1Tools).To(BeFalse(), "Should NOT have other tools from backend1 (filtered out)")

			// Should NOT have any tool from backend2 (filtered with non-matching filter)
			hasBackend2Tool := false
			for _, name := range toolNames {
				if strings.Contains(name, backend2Name) {
					hasBackend2Tool = true
				}
			}
			Expect(hasBackend2Tool).To(BeFalse(), "Should NOT have any tool from backend2 (filtered out)")
		})

		It("should apply tool overrides from MCPToolConfig", func() {
			By("Waiting for tools to be available")
			var tools *mcp.ListToolsResult
			Eventually(func() error {
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-toolconfig-test", 30*time.Second)
				if err != nil {
					return fmt.Errorf("failed to create MCP client: %w", err)
				}
				defer mcpClient.Close()

				listRequest := mcp.ListToolsRequest{}
				tools, err = mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				if len(tools.Tools) == 0 {
					return fmt.Errorf("no tools available yet")
				}
				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Should have tools available")

			// Find the renamed tool
			var renamedTool *mcp.Tool
			for i := range tools.Tools {
				if strings.Contains(tools.Tools[i].Name, backend1Name) && strings.Contains(tools.Tools[i].Name, "renamed_fetch") {
					renamedTool = &tools.Tools[i]
					break
				}
			}

			Expect(renamedTool).ToNot(BeNil(), "Should find renamed_fetch tool")
			Expect(renamedTool.Description).To(ContainSubstring("renamed via MCPToolConfig"),
				"Tool description should be overridden by MCPToolConfig")
		})

		It("should still allow calling the renamed tool", func() {
			By("Waiting for tools to be available")
			var tools *mcp.ListToolsResult
			Eventually(func() error {
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-toolconfig-test", 30*time.Second)
				if err != nil {
					return fmt.Errorf("failed to create MCP client: %w", err)
				}
				defer mcpClient.Close()

				listRequest := mcp.ListToolsRequest{}
				tools, err = mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				if err != nil {
					return fmt.Errorf("failed to list tools: %w", err)
				}
				if len(tools.Tools) == 0 {
					return fmt.Errorf("no tools available yet")
				}
				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Should have tools available")

			// Find the renamed tool
			var targetToolName string
			for _, tool := range tools.Tools {
				if strings.Contains(tool.Name, backend1Name) && strings.Contains(tool.Name, "renamed_fetch") {
					targetToolName = tool.Name
					break
				}
			}
			Expect(targetToolName).ToNot(BeEmpty(), "Should find renamed_fetch tool from backend1")

			By(fmt.Sprintf("Calling renamed tool: %s", targetToolName))
			// Create a new client for calling the tool
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-toolconfig-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			testURL := "https://example.com"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = targetToolName
			callRequest.Params.Arguments = map[string]any{
				"url": testURL,
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Should be able to call renamed tool")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

			GinkgoWriter.Printf("Renamed tool call successful: %s\n", targetToolName)
		})
	})

	Context("when verifying MCPToolConfig configuration", func() {
		It("should have correct ToolConfigRef in VirtualMCPServer spec", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.Aggregation).ToNot(BeNil())
			Expect(vmcpServer.Spec.Aggregation.Tools).To(HaveLen(2))

			// Verify backend1 has ToolConfigRef
			var backend1Config *mcpv1alpha1.WorkloadToolConfig
			for i := range vmcpServer.Spec.Aggregation.Tools {
				if vmcpServer.Spec.Aggregation.Tools[i].Workload == backend1Name {
					backend1Config = &vmcpServer.Spec.Aggregation.Tools[i]
					break
				}
			}

			Expect(backend1Config).ToNot(BeNil())
			Expect(backend1Config.ToolConfigRef).ToNot(BeNil())
			Expect(backend1Config.ToolConfigRef.Name).To(Equal(toolConfigName))
		})

		It("should have MCPToolConfig with correct filter and overrides", func() {
			toolConfig := &mcpv1alpha1.MCPToolConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      toolConfigName,
				Namespace: testNamespace,
			}, toolConfig)
			Expect(err).ToNot(HaveOccurred())

			// Verify filter contains the renamed tool name (filtering happens after override)
			Expect(toolConfig.Spec.ToolsFilter).To(ContainElement("renamed_fetch"))
			Expect(toolConfig.Spec.ToolsFilter).To(HaveLen(1))

			// Verify overrides
			Expect(toolConfig.Spec.ToolsOverride).To(HaveKey("fetch"))
			override := toolConfig.Spec.ToolsOverride["fetch"]
			Expect(override.Name).To(Equal("renamed_fetch"))
			Expect(override.Description).To(ContainSubstring("renamed via MCPToolConfig"))
		})
	})
})

var _ = Describe("VirtualMCPServer MCPToolConfig Dynamic Updates", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-toolconfig-update-group"
		vmcpServerName  = "test-vmcp-toolconfig-update"
		toolConfigName  = "test-tool-config-update"
		backendName     = "gofetch-toolconfig-update"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for ToolConfig update test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for MCPToolConfig update E2E tests", timeout, pollingInterval)

		By("Creating gofetch backend MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace, mcpGroupName,
			images.GofetchServerImage, timeout, pollingInterval)

		By("Creating MCPToolConfig with initial filter")
		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      toolConfigName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPToolConfigSpec{
				// Initially only allow 'fetch' tool
				ToolsFilter: []string{"fetch"},
			},
		}
		Expect(k8sClient.Create(ctx, toolConfig)).To(Succeed())

		By("Creating VirtualMCPServer with MCPToolConfig reference")
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
					Tools: []mcpv1alpha1.WorkloadToolConfig{
						{
							Workload: backendName,
							ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
								Name: toolConfigName,
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

		By("Cleaning up MCPToolConfig")
		toolConfig := &mcpv1alpha1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      toolConfigName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, toolConfig)

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

	Context("when MCPToolConfig is updated", func() {
		It("should initially only expose the fetch tool", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-toolconfig-update-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			// Should only have fetch tool
			Expect(tools.Tools).ToNot(BeEmpty())
			for _, tool := range tools.Tools {
				Expect(tool.Name).To(ContainSubstring("fetch"), "Should only have fetch tool")
			}
		})

		It("should reflect updated overrides when MCPToolConfig is modified", func() {
			By("Updating MCPToolConfig to change the tool override name")
			toolConfig := &mcpv1alpha1.MCPToolConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      toolConfigName,
				Namespace: testNamespace,
			}, toolConfig)
			Expect(err).ToNot(HaveOccurred())

			// Update the override to rename fetch to "updated_fetch" instead of "renamed_fetch"
			// Also update the filter to match the new overridden name
			toolConfig.Spec.ToolsFilter = []string{"updated_fetch"}
			toolConfig.Spec.ToolsOverride = map[string]mcpv1alpha1.ToolOverride{
				"fetch": {
					Name:        "updated_fetch",
					Description: "This fetch tool name was updated via MCPToolConfig",
				},
			}
			Expect(k8sClient.Update(ctx, toolConfig)).To(Succeed())

			By("Waiting for VirtualMCPServer to reconcile and reflect new override")
			// Use Eventually to wait for the updated tool name (up to 2 minutes)
			Eventually(func() bool {
				mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-toolconfig-update-test", 30*time.Second)
				if err != nil {
					GinkgoWriter.Printf("Failed to create MCP client: %v\n", err)
					return false
				}
				defer mcpClient.Close()

				listRequest := mcp.ListToolsRequest{}
				tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
				if err != nil {
					GinkgoWriter.Printf("Failed to list tools: %v\n", err)
					return false
				}

				hasUpdatedFetch := false
				for _, tool := range tools.Tools {
					if strings.Contains(tool.Name, "updated_fetch") {
						hasUpdatedFetch = true
						GinkgoWriter.Printf("Found updated tool: %s - %s\n", tool.Name, tool.Description)
					}
				}

				GinkgoWriter.Printf("Current tools: updated_fetch=%v (total: %d)\n", hasUpdatedFetch, len(tools.Tools))
				return hasUpdatedFetch
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Should have updated_fetch tool after MCPToolConfig override update")
		})
	})
})
