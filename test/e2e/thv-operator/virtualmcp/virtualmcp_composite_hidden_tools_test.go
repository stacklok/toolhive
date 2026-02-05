// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This test verifies that composite tools can use backend tools that are hidden
// from direct MCP client access via both ExcludeAll and Filter configurations.
//
// Test setup:
// - Backend A (yardstick-hidden-a): Uses ExcludeAll to hide all tools
// - Backend B (yardstick-hidden-b): Uses Filter to selectively hide tools
// - A composite tool that calls tools from BOTH backends
//
// This validates the fix for issue #3636:
// https://github.com/stacklok/toolhive/issues/3636

var _ = Describe("VirtualMCPServer Composite with Hidden Backend Tools", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-composite-hidden-group"
		vmcpServerName  = "test-vmcp-composite-hidden"
		backendAName    = "yardstick-hidden-a" // Uses ExcludeAll
		backendBName    = "yardstick-hidden-b" // Uses Filter
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32

		// Composite tool that chains tools from both backends
		compositeToolName = "dual_backend_echo"
	)

	BeforeAll(func() {
		By("Creating MCPGroup for composite with hidden tools test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test group for composite tool with hidden backend tools", timeout, pollingInterval)

		By("Creating backend A (ExcludeAll) - yardstick MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backendAName, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating backend B (Filter) - yardstick MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backendBName, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer with mixed ExcludeAll and Filter configuration")
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
						Tools: []*vmcpconfig.WorkloadToolConfig{
							{
								// Backend A: Hide ALL tools using ExcludeAll
								Workload:   backendAName,
								ExcludeAll: true,
							},
							{
								// Backend B: Hide tools using Filter (only expose non-existent tool name)
								// This effectively hides all backend tools while keeping them in routing table
								Workload: backendBName,
								Filter:   []string{"nonexistent_tool_for_filter_test"},
							},
						},
					},
					// Define a composite tool that uses tools from BOTH hidden backends
					CompositeTools: []vmcpconfig.CompositeToolConfig{
						{
							Name:        compositeToolName,
							Description: "A composite tool that echoes via both hidden backends",
							Parameters: thvjson.NewMap(map[string]any{
								"type": "object",
								"properties": map[string]any{
									"message": map[string]any{
										"type":        "string",
										"description": "The message to echo through both backends",
									},
								},
								"required": []any{"message"},
							}),
							Timeout: vmcpconfig.Duration(30 * time.Second),
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									// Step 1: Echo through Backend A (ExcludeAll)
									ID:   "echo_backend_a",
									Type: "tool",
									Tool: fmt.Sprintf("%s_echo", backendAName),
									Arguments: thvjson.NewMap(map[string]any{
										"input": "{{ .params.message }}",
									}),
								},
								{
									// Step 2: Echo through Backend B (Filter)
									ID:   "echo_backend_b",
									Type: "tool",
									Tool: fmt.Sprintf("%s_echo", backendBName),
									Arguments: thvjson.NewMap(map[string]any{
										"input": "{{ .params.message }}",
									}),
									DependsOn: []string{"echo_backend_a"},
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

		By("Cleaning up backend A MCPServer")
		backendA := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendAName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, backendA)

		By("Cleaning up backend B MCPServer")
		backendB := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendBName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, backendB)

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpGroup)
	})

	Context("when backends use ExcludeAll and Filter to hide tools", func() {
		It("should only expose the composite tool (no backend tools visible)", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-hidden-tools-list-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, mcp.ListToolsRequest{})
			Expect(err).ToNot(HaveOccurred())

			GinkgoWriter.Printf("VirtualMCPServer exposes %d tools\n", len(tools.Tools))
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  Tool: %s - %s\n", tool.Name, tool.Description)
			}

			// Only the composite tool should be exposed
			Expect(len(tools.Tools)).To(Equal(1),
				"Should expose exactly 1 tool (the composite), no backend tools")
			Expect(tools.Tools[0].Name).To(Equal(compositeToolName))

			// Verify NO backend tools are exposed
			backendAToolPrefix := fmt.Sprintf("%s_", backendAName)
			backendBToolPrefix := fmt.Sprintf("%s_", backendBName)
			for _, tool := range tools.Tools {
				Expect(strings.HasPrefix(tool.Name, backendAToolPrefix)).To(BeFalse(),
					"Backend A tools should be hidden via ExcludeAll")
				Expect(strings.HasPrefix(tool.Name, backendBToolPrefix)).To(BeFalse(),
					"Backend B tools should be hidden via Filter")
			}
		})

		It("should successfully execute composite tool using hidden backend tools from both backends", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-hidden-tools-exec-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Calling composite tool with test message")
			// Note: yardstick echo tool requires alphanumeric only
			testMessage := "helloFromDualBackendTest"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = compositeToolName
			callRequest.Params.Arguments = map[string]any{
				"message": testMessage,
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "MCP call should succeed at transport level")
			Expect(result).ToNot(BeNil())

			GinkgoWriter.Printf("Composite tool result: %+v\n", result.Content)

			// Composite tool should succeed - both ExcludeAll and Filter preserve routing table
			Expect(result.IsError).To(BeFalse(),
				"Composite tool should succeed using hidden backend tools")

			// Verify we got a response (not an error message)
			Expect(result.Content).ToNot(BeEmpty(), "Should have result content")

			textContent, ok := result.Content[0].(mcp.TextContent)
			Expect(ok).To(BeTrue(), "Result should be TextContent")

			// The response should NOT contain error messages
			Expect(textContent.Text).ToNot(ContainSubstring("tool not found"),
				"Should not have 'tool not found' error")
			Expect(textContent.Text).ToNot(ContainSubstring("Workflow execution failed"),
				"Should not have workflow execution error")

			// The response should contain our test message (from backend B's echo)
			Expect(strings.Contains(textContent.Text, testMessage)).To(BeTrue(),
				"Response should contain the echoed message from backend B")

			GinkgoWriter.Printf("SUCCESS: Composite tool executed using both hidden backends\n")
		})
	})

	Context("when verifying configuration", func() {
		It("should have correct ExcludeAll and Filter configuration", func() {
			var vmcpServer mcpv1alpha1.VirtualMCPServer
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, &vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			// Verify aggregation configuration
			Expect(vmcpServer.Spec.Config.Aggregation).ToNot(BeNil())
			Expect(vmcpServer.Spec.Config.Aggregation.Tools).To(HaveLen(2))

			// Find and verify Backend A config (ExcludeAll)
			var backendAConfig, backendBConfig *vmcpconfig.WorkloadToolConfig
			for _, toolConfig := range vmcpServer.Spec.Config.Aggregation.Tools {
				if toolConfig.Workload == backendAName {
					backendAConfig = toolConfig
				} else if toolConfig.Workload == backendBName {
					backendBConfig = toolConfig
				}
			}

			Expect(backendAConfig).ToNot(BeNil(), "Backend A config should exist")
			Expect(backendAConfig.ExcludeAll).To(BeTrue(), "Backend A should use ExcludeAll")

			Expect(backendBConfig).ToNot(BeNil(), "Backend B config should exist")
			Expect(backendBConfig.Filter).ToNot(BeEmpty(), "Backend B should use Filter")

			// Verify composite tool configuration
			Expect(vmcpServer.Spec.Config.CompositeTools).To(HaveLen(1))
			compositeTool := vmcpServer.Spec.Config.CompositeTools[0]
			Expect(compositeTool.Name).To(Equal(compositeToolName))
			Expect(compositeTool.Steps).To(HaveLen(2))

			// Verify step references to both backends
			step1 := compositeTool.Steps[0]
			step2 := compositeTool.Steps[1]
			Expect(step1.Tool).To(Equal(fmt.Sprintf("%s_echo", backendAName)),
				"Step 1 should reference Backend A's echo tool")
			Expect(step2.Tool).To(Equal(fmt.Sprintf("%s_echo", backendBName)),
				"Step 2 should reference Backend B's echo tool")
		})
	})
})
