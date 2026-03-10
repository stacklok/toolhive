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

// This test exercises composite tool execution through the optimizer's call_tool.
// Without the fix in injectOptimizerCapabilities, composite tools are registered
// with backend routing handlers (ToSDKTools) instead of workflow execution handlers
// (ToCompositeToolSDKTools), causing call_tool to fail with ErrToolNotFound.
//
// A lightweight fake embedding server replaces the heavyweight TEI image to keep
// test setup fast while satisfying the optimizer's embedding service requirement.
var _ = Describe("VirtualMCPServer Optimizer Composite Tools", Ordered, func() {
	var (
		testNamespace     = "default"
		mcpGroupName      = "test-opt-composite-group"
		vmcpServerName    = "test-vmcp-opt-composite"
		fakeEmbeddingName = "fake-embed-opt-composite"
		backendName       = "backend-opt-composite"
		// vmcpFetchToolName is the renamed fetch tool exposed through aggregation.
		// Renaming lets us verify the optimizer respects the aggregation config.
		vmcpFetchToolName        = "opt_composite_fetch"
		vmcpFetchToolDescription = "Fetches a URL for the optimizer composite test."
		backendFetchToolName     = "fetch"
		compositeToolName        = "double_fetch"
		timeout                  = 5 * time.Minute
		pollingInterval          = 1 * time.Second
		vmcpNodePort             int32
	)

	BeforeAll(func() {
		By("Deploying fake embedding server")
		embeddingURL := DeployFakeEmbeddingServer(ctx, k8sClient,
			fakeEmbeddingName, testNamespace, timeout, pollingInterval)
		_, _ = fmt.Fprintf(GinkgoWriter, "Fake embedding server at: %s\n", embeddingURL)

		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"MCP Group for optimizer composite E2E tests", timeout, pollingInterval)

		By("Creating backend MCPServer - gofetch")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace,
			mcpGroupName, images.GofetchServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer with optimizer + composite tool")

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
				// Use embeddingService directly instead of EmbeddingServerRef
				// to avoid depending on the heavyweight TEI image.
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
					Optimizer: &vmcpconfig.OptimizerConfig{
						EmbeddingService: embeddingURL,
					},
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

		By("Cleaning up fake embedding server")
		CleanupFakeEmbeddingServer(ctx, k8sClient, fakeEmbeddingName, testNamespace)
	})

	It("should only expose find_tool and call_tool", func() {
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "opt-composite-list", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, mcp.ListToolsRequest{})
		Expect(err).ToNot(HaveOccurred())
		Expect(tools.Tools).To(HaveLen(2), "Should only have find_tool and call_tool")

		toolNames := make([]string, len(tools.Tools))
		for i, tool := range tools.Tools {
			toolNames[i] = tool.Name
		}
		Expect(toolNames).To(ConsistOf("find_tool", "call_tool"))
	})

	It("should discover backend tool via find_tool", func() {
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "opt-composite-find-backend", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		findResult, err := callFindTool(mcpClient, vmcpFetchToolName)
		Expect(err).ToNot(HaveOccurred())

		foundTools := getToolNames(findResult)
		Expect(foundTools).ToNot(BeEmpty(), "find_tool should discover backend tools")

		found := false
		for _, name := range foundTools {
			if strings.Contains(name, vmcpFetchToolName) {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "Should find the renamed backend fetch tool")
		_, _ = fmt.Fprintf(GinkgoWriter, "Found backend tool in: %v\n", foundTools)
	})

	It("should discover composite tool via find_tool", func() {
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "opt-composite-find-composite", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		findResult, err := callFindTool(mcpClient, compositeToolName)
		Expect(err).ToNot(HaveOccurred())

		foundTools := getToolNames(findResult)
		Expect(foundTools).ToNot(BeEmpty(), "find_tool should discover composite tools")

		found := false
		for _, name := range foundTools {
			if strings.Contains(name, compositeToolName) {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "Should find the composite tool")
		_, _ = fmt.Fprintf(GinkgoWriter, "Found composite tool in: %v\n", foundTools)
	})

	It("should invoke backend tool via call_tool", func() {
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "opt-composite-call-backend", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		result, err := callToolViaOptimizer(mcpClient, vmcpFetchToolName, map[string]any{
			"url": "https://example.com",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result).ToNot(BeNil())
		Expect(result.Content).ToNot(BeEmpty(), "call_tool should return content from backend tool")
		_, _ = fmt.Fprintf(GinkgoWriter, "Successfully called backend tool via call_tool\n")
	})

	It("should invoke composite tool via call_tool", func() {
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "opt-composite-call-composite", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		result, err := callToolViaOptimizer(mcpClient, compositeToolName, map[string]any{
			"url": "https://example.com",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result).ToNot(BeNil())
		Expect(result.Content).ToNot(BeEmpty(), "call_tool should return content from composite tool")
		_, _ = fmt.Fprintf(GinkgoWriter, "Successfully called composite tool via call_tool\n")
	})
})
