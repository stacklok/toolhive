// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Composite Sequential Workflow", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-composite-seq-group"
		vmcpServerName  = "test-vmcp-composite-seq"
		backendName     = "yardstick-composite-seq"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32

		// Composite tool names
		compositeToolName     = "echo_twice"
		annotatedToolName     = "echo_annotated"
		contradictingToolName = "echo_contradicting"
	)

	BeforeAll(func() {
		By("Creating MCPGroup for composite sequential test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for composite sequential E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer with composite sequential workflow")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, testNamespace,
			v1beta1test.WithVMCPGroupRef(mcpGroupName),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{
				Group: mcpGroupName,
				Aggregation: &vmcpconfig.AggregationConfig{
					ConflictResolution: "prefix",
				},
				// Define a composite tool that echoes input, then echoes the result again
				CompositeTools: []vmcpconfig.CompositeToolConfig{
					{
						Name:        compositeToolName,
						Description: "Echoes the input message twice in sequence",
						Parameters: thvjson.NewMap(map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{
									"type":        "string",
									"description": "The message to echo twice",
								},
							},
							"required": []any{"message"},
						}),
						Timeout: vmcpconfig.Duration(30 * time.Second),
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "first_echo",
								Type: "tool",
								Tool: fmt.Sprintf("%s.echo", backendName),
								Arguments: thvjson.NewMap(map[string]any{
									"input": "{{ .params.message }}",
								}),
							},
							{
								ID:        "second_echo",
								Type:      "tool",
								Tool:      fmt.Sprintf("%s.echo", backendName),
								DependsOn: []string{"first_echo"},
								Arguments: thvjson.NewMap(map[string]any{
									"input": "{{ .steps.first_echo.result }}",
								}),
							},
						},
					},
					// Composite tool with explicit annotations. An explicit hint may be
					// MORE conservative than the derived floor, so this passes through.
					{
						Name:        annotatedToolName,
						Description: "Echoes the input with explicit conservative annotations",
						Parameters: thvjson.NewMap(map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{
									"type":        "string",
									"description": "The message to echo",
								},
							},
							"required": []any{"message"},
						}),
						Annotations: &vmcpconfig.ToolAnnotationsOverride{
							Title:           stringPtr("Annotated Echo"),
							ReadOnlyHint:    boolPtr(false),
							DestructiveHint: boolPtr(true),
							IdempotentHint:  boolPtr(true),
						},
						Timeout: vmcpconfig.Duration(30 * time.Second),
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "echo",
								Type: "tool",
								Tool: fmt.Sprintf("%s.echo", backendName),
								Arguments: thvjson.NewMap(map[string]any{
									"input": "{{ .params.message }}",
								}),
							},
						},
					},
					// Composite tool whose explicit annotations CONTRADICT the derived
					// safety floor: readOnlyHint=true claims the tool does not modify
					// its environment, but the yardstick echo tool does not declare
					// readOnlyHint, so the floor is not read-only. The guardrail drops
					// this tool from tools/list at runtime while the server stays Ready.
					{
						Name:        contradictingToolName,
						Description: "Contradicting annotations: dropped by the safety-floor guardrail",
						Parameters: thvjson.NewMap(map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{
									"type":        "string",
									"description": "The message to echo",
								},
							},
							"required": []any{"message"},
						}),
						Annotations: &vmcpconfig.ToolAnnotationsOverride{
							ReadOnlyHint: boolPtr(true),
						},
						Timeout: vmcpconfig.Duration(30 * time.Second),
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "echo",
								Type: "tool",
								Tool: fmt.Sprintf("%s.echo", backendName),
								Arguments: thvjson.NewMap(map[string]any{
									"input": "{{ .params.message }}",
								}),
							},
						},
					},
				},
			}),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
			}),
			v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
				v.Spec.ServiceType = "NodePort"
			}),
		)
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By(fmt.Sprintf("VirtualMCPServer accessible at http://localhost:%d", vmcpNodePort))
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, testNamespace)
		_ = k8sClient.Delete(ctx, vmcpServer)

		By("Cleaning up backend MCPServer")
		backend := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, backend)

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpGroup)
	})

	Context("when composite tools are configured", func() {
		It("should expose the composite tool in tool listing", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-composite-test", 30*time.Second)
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

			// Should find the composite tool
			var foundComposite bool
			for _, tool := range tools.Tools {
				if tool.Name == compositeToolName {
					foundComposite = true
					Expect(tool.Description).To(Equal("Echoes the input message twice in sequence"))
					break
				}
			}
			Expect(foundComposite).To(BeTrue(), "Should find composite tool: %s", compositeToolName)

			// Should also have the backend's native echo tool (with prefix)
			var foundBackendTool bool
			expectedBackendTool := fmt.Sprintf("%s_echo", backendName)
			for _, tool := range tools.Tools {
				if tool.Name == expectedBackendTool {
					foundBackendTool = true
					break
				}
			}
			Expect(foundBackendTool).To(BeTrue(), "Should find backend native tool: %s", expectedBackendTool)
		})

		It("should execute sequential workflow with template expansion", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-composite-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Calling composite tool with test message")
			testMessage := "hello_sequential_test"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = compositeToolName
			callRequest.Params.Arguments = map[string]any{
				"message": testMessage,
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Composite tool call should succeed")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

			// The result should reflect the sequential execution
			// First echo: echoes testMessage
			// Second echo: echoes the result of first echo
			GinkgoWriter.Printf("Composite tool result: %+v\n", result.Content)
		})

		It("should advertise annotations on composite tools", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-composite-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			toolAnnotations := make(map[string]mcp.ToolAnnotation, len(tools.Tools))
			for _, tool := range tools.Tools {
				toolAnnotations[tool.Name] = tool.Annotations
				GinkgoWriter.Printf("  Tool: %s annotations=%+v\n", tool.Name, tool.Annotations)
			}

			By("Verifying derived annotations on the composite tool")
			// The yardstick echo backend declares NO annotations, so derivation is
			// fail-closed: echo_twice (no explicit annotations) advertises the
			// conservative floor — readOnlyHint=false, destructiveHint=true,
			// openWorldHint=true.
			compositeAnn, found := toolAnnotations[compositeToolName]
			Expect(found).To(BeTrue(), "Should find composite tool: %s", compositeToolName)
			Expect(compositeAnn.ReadOnlyHint).ToNot(BeNil(), "Composite tool should advertise a derived readOnlyHint")
			Expect(*compositeAnn.ReadOnlyHint).To(BeFalse(),
				"Derived readOnlyHint should be false: the yardstick echo tool does not declare itself read-only")
			Expect(compositeAnn.DestructiveHint).ToNot(BeNil(), "Composite tool should advertise a derived destructiveHint")
			Expect(*compositeAnn.DestructiveHint).To(BeTrue(),
				"Derived destructiveHint should be true: a step whose annotations are unknown taints the floor")
			Expect(compositeAnn.OpenWorldHint).ToNot(BeNil(), "Composite tool should advertise a derived openWorldHint")
			Expect(*compositeAnn.OpenWorldHint).To(BeTrue(),
				"Derived openWorldHint should be true: a step whose annotations are unknown taints the floor")

			By("Verifying explicit annotations pass through when more conservative than the floor")
			annotatedAnn, found := toolAnnotations[annotatedToolName]
			Expect(found).To(BeTrue(), "Should find explicitly annotated composite tool: %s", annotatedToolName)
			Expect(annotatedAnn.Title).To(Equal("Annotated Echo"))
			Expect(annotatedAnn.ReadOnlyHint).ToNot(BeNil())
			Expect(*annotatedAnn.ReadOnlyHint).To(BeFalse())
			Expect(annotatedAnn.DestructiveHint).ToNot(BeNil())
			Expect(*annotatedAnn.DestructiveHint).To(BeTrue())
			Expect(annotatedAnn.IdempotentHint).ToNot(BeNil())
			Expect(*annotatedAnn.IdempotentHint).To(BeTrue())

			By("Verifying the contradicting composite tool is dropped")
			_, found = toolAnnotations[contradictingToolName]
			Expect(found).To(BeFalse(),
				"Composite tool with annotations contradicting the safety floor should be dropped: %s", contradictingToolName)
		})

		It("should stay Ready while dropping the contradicting composite tool", func() {
			vmcpServer := &mcpv1beta1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Status.Phase).To(Equal(mcpv1beta1.VirtualMCPServerPhaseReady),
				"VirtualMCPServer should stay Ready when the annotation guardrail drops a composite tool")
		})
	})

	Context("when verifying composite tool configuration", func() {
		It("should have correct composite tool spec stored", func() {
			vmcpServer := &mcpv1beta1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.Config.CompositeTools).ToNot(BeEmpty())

			compositeTool := vmcpServer.Spec.Config.CompositeTools[0]
			Expect(compositeTool.Name).To(Equal(compositeToolName))
			Expect(compositeTool.Steps).To(HaveLen(2))

			// Verify step dependencies
			step1 := compositeTool.Steps[0]
			Expect(step1.ID).To(Equal("first_echo"))
			Expect(step1.DependsOn).To(BeEmpty())

			step2 := compositeTool.Steps[1]
			Expect(step2.ID).To(Equal("second_echo"))
			Expect(step2.DependsOn).To(ContainElement("first_echo"))

			// Verify template usage in arguments
			step1Args := step1.Arguments.Value
			Expect(step1Args["input"]).To(ContainSubstring(".params.message"))

			step2Args := step2.Arguments.Value
			Expect(step2Args["input"]).To(ContainSubstring(".steps.first_echo"))
		})
	})
})

// boolPtr returns a pointer to b. Used for optional *bool annotation fields.
func boolPtr(b bool) *bool { return &b }

// stringPtr returns a pointer to s. Used for optional *string annotation fields.
func stringPtr(s string) *string { return &s }
