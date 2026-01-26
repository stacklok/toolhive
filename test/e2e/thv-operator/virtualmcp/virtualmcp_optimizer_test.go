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

var _ = Describe("VirtualMCPServer Optimizer Mode", Ordered, func() {
	var (
		testNamespace  = "default"
		mcpGroupName   = "test-optimizer-group"
		vmcpServerName = "test-vmcp-optimizer"
		backendName    = "backend-optimizer-fetch"
		// vmcpFetchToolName is the name of the fetch tool exposed by the VirtualMCPServer
		// We intentionally specify an aggregation, so we can rename the tool.
		// Renaming the tool allows us to also verify the optimizer respects the aggregation config.
		vmcpFetchToolName        = "rename_fetch_tool"
		vmcpFetchToolDescription = "This is a non-sense description for the fetch tool."
		// backendFetchToolName is the name of the fetch tool exposed by the backend MCPServer
		backendFetchToolName = "fetch"
		compositeToolName    = "double_fetch"
		timeout              = 3 * time.Minute
		pollingInterval      = 1 * time.Second
		vmcpNodePort         int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for optimizer test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for optimizer E2E tests", timeout, pollingInterval)

		By("Creating backend MCPServer - fetch")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace,
			mcpGroupName, images.GofetchServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer with optimizer enabled and a composite tool")

		// Define step arguments that reference the input parameter
		stepArgs := map[string]interface{}{
			"url": "{{.params.url}}",
		}

		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				ServiceType: "NodePort",
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
				},

			Config: vmcpconfig.Config{
				Group: mcpGroupName,
				Optimizer: &vmcpconfig.OptimizerConfig{
					Enabled: true,
					// EmbeddingURL is required for optimizer configuration
					// For in-cluster services, use the full service DNS name with port
					EmbeddingURL: "http://dummy-embedding-service.default.svc.cluster.local:11434",
				},
					// Define a composite tool that calls fetch twice
					CompositeTools: []vmcpconfig.CompositeToolConfig{
						{
							Name:        compositeToolName,
							Description: "Fetches a URL twice in sequence for verification",
							Parameters: thvjson.NewMap(map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"url": map[string]interface{}{
										"type":        "string",
										"description": "URL to fetch twice",
									},
								},
								"required": []string{"url"},
							}),
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									ID:        "first_fetch",
									Type:      "tool",
									Tool:      vmcpFetchToolName,
									Arguments: thvjson.NewMap(stepArgs),
								},
								{
									ID:        "second_fetch",
									Type:      "tool",
									Tool:      vmcpFetchToolName,
									DependsOn: []string{"first_fetch"},
									Arguments: thvjson.NewMap(stepArgs),
								},
							},
						},
					},
					Aggregation: &vmcpconfig.AggregationConfig{
						ConflictResolution: "prefix",
						Tools: []*vmcpconfig.WorkloadToolConfig{
							{
								Workload: backendName,
								Overrides: map[string]*vmcpconfig.ToolOverride{
									backendFetchToolName: {
										Name:        vmcpFetchToolName,
										Description: vmcpFetchToolDescription,
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting VirtualMCPServer NodePort")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		_, _ = fmt.Fprintf(GinkgoWriter, "VirtualMCPServer is accessible at NodePort: %d\n", vmcpNodePort)
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer); err == nil {
			_ = k8sClient.Delete(ctx, vmcpServer)
		}

		By("Cleaning up backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      backendName,
			Namespace: testNamespace,
		}, backend); err == nil {
			_ = k8sClient.Delete(ctx, backend)
		}

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      mcpGroupName,
			Namespace: testNamespace,
		}, mcpGroup); err == nil {
			_ = k8sClient.Delete(ctx, mcpGroup)
		}
	})

	It("should only expose find_tool and call_tool", func() {
		By("Creating and initializing MCP client")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "optimizer-test-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		By("Listing tools from VirtualMCPServer")
		listRequest := mcp.ListToolsRequest{}
		tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying only optimizer tools are exposed")
		Expect(tools.Tools).To(HaveLen(2), "Should only have find_tool and call_tool")

		toolNames := make([]string, len(tools.Tools))
		for i, tool := range tools.Tools {
			toolNames[i] = tool.Name
		}
		Expect(toolNames).To(ConsistOf("find_tool", "call_tool"))

		_, _ = fmt.Fprintf(GinkgoWriter, "✓ Optimizer mode correctly exposes only: %v\n", toolNames)
	})

	testFindAndCall := func(toolName string, params map[string]any) {
		By("Creating and initializing MCP client")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, fmt.Sprintf("optimizer-call-test-%s", toolName), 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		By("Finding the backend tool")
		findResult, err := callFindTool(mcpClient, toolName)
		Expect(err).ToNot(HaveOccurred())

		foundTools := getToolNames(findResult)
		Expect(foundTools).ToNot(BeEmpty())

		foundToolName := func() string {
			for _, tool := range foundTools {
				if strings.Contains(tool, toolName) {
					return tool
				}
			}
			return ""
		}()
		Expect(foundToolName).ToNot(BeEmpty(), "Should find backend tool")

		By(fmt.Sprintf("Calling %s via call_tool", foundToolName))
		result, err := callToolViaOptimizer(mcpClient, foundToolName, params)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).ToNot(BeNil())
		Expect(result.Content).ToNot(BeEmpty(), "call_tool should return content from backend tool")

		_, _ = fmt.Fprintf(GinkgoWriter, "✓ Successfully called %s via call_tool\n", foundToolName)
	}

	It("should find and invoke backend tools via call_tool", func() {
		testFindAndCall(vmcpFetchToolName, map[string]any{
			"url": "https://example.com",
		})
	})

	It("should find and invoke composite tools via optimizer", func() {
		testFindAndCall(compositeToolName, map[string]any{
			"url": "https://example.com",
		})
	})
})

// callFindTool calls find_tool and returns the StructuredContent directly
func callFindTool(mcpClient *InitializedMCPClient, description string) (map[string]any, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = "find_tool"
	req.Params.Arguments = map[string]any{"tool_description": description}

	result, err := mcpClient.Client.CallTool(mcpClient.Ctx, req)
	if err != nil {
		return nil, err
	}
	content, ok := result.StructuredContent.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected map[string]any, got %T", result.StructuredContent)
	}
	return content, nil
}

// getToolNames extracts tool names from find_tool structured content
func getToolNames(content map[string]any) []string {
	tools, ok := content["tools"].([]any)
	if !ok {
		return nil
	}
	var names []string
	for _, t := range tools {
		if tool, ok := t.(map[string]any); ok {
			if name, ok := tool["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// callToolViaOptimizer invokes a tool through call_tool
func callToolViaOptimizer(mcpClient *InitializedMCPClient, toolName string, params map[string]any) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = "call_tool"
	req.Params.Arguments = map[string]any{
		"tool_name":  toolName,
		"parameters": params,
	}
	return mcpClient.Client.CallTool(mcpClient.Ctx, req)
}
