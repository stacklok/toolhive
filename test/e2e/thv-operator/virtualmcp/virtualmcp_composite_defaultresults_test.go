// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Composite Tool DefaultResults", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-composite-defaults-group"
		vmcpServerName  = "test-vmcp-composite-defaults"
		backendName     = "yardstick-defaults"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32

		// Composite tool name
		compositeToolName = "conditional_echo"
	)

	BeforeAll(func() {
		By("Creating MCPGroup for composite defaultResults test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for composite defaultResults E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer with composite tool using defaultResults")
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
					},
					// Define a composite tool with a conditional step that has defaultResults
					CompositeTools: []vmcpconfig.CompositeToolConfig{
						{
							Name:        compositeToolName,
							Description: "Conditionally echoes input, uses default when skipped",
							Parameters: thvjson.NewMap(map[string]any{
								"type": "object",
								"properties": map[string]any{
									"run_step": map[string]any{
										"type":        "boolean",
										"description": "Whether to run the conditional step",
									},
									"message": map[string]any{
										"type":        "string",
										"description": "Message to echo if step runs",
									},
								},
								"required": []any{"run_step", "message"},
							}),
							Timeout: vmcpconfig.Duration(30 * time.Second),
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									ID:   "conditional_step",
									Type: "tool",
									Tool: fmt.Sprintf("%s_echo", backendName),
									// Only run when run_step=true
									Condition: "{{.params.run_step}}",
									Arguments: thvjson.NewMap(map[string]any{
										"input": "{{ .params.message }}",
									}),
									// When skipped, use this default value
									// Uses "output" key to match yardstick 1.1.1 EchoResponse format
									DefaultResults: thvjson.NewMap(map[string]any{
										"output": "default_value_when_skipped",
									}),
								},
							},
							// Output references the conditional step's output.output
							Output: &vmcpconfig.OutputConfig{
								Properties: map[string]vmcpconfig.OutputProperty{
									"result": {
										Type:        "string",
										Description: "Result from conditional step",
										Value:       "{{.steps.conditional_step.output.output}}",
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

	Context("when conditional step is skipped", func() {
		It("should use defaultResults in the workflow output", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-defaults-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Calling composite tool with run_step=false (step will be skipped)")
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = compositeToolName
			callRequest.Params.Arguments = map[string]any{
				"run_step": false,
				"message":  "this_should_not_appear",
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Composite tool call should succeed")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

			// Extract text content from result
			var resultText string
			for _, content := range result.Content {
				if textContent, ok := mcp.AsTextContent(content); ok {
					resultText = textContent.Text
					break
				}
			}

			GinkgoWriter.Printf("Workflow result when step skipped: %s\n", resultText)

			// The output should contain the default value
			Expect(resultText).To(ContainSubstring("default_value_when_skipped"),
				"Output should contain defaultResults value when step is skipped")

			// The output should NOT contain the message that would be echoed if step ran
			Expect(resultText).ToNot(ContainSubstring("this_should_not_appear"),
				"Output should not contain the message since step was skipped")
		})
	})

	Context("when conditional step runs", func() {
		It("should use actual step output instead of defaultResults", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-defaults-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Calling composite tool with run_step=true (step will run)")
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = compositeToolName
			callRequest.Params.Arguments = map[string]any{
				"run_step": true,
				"message":  "actualstepoutput123", // yardstick requires alphanumeric only
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Composite tool call should succeed")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

			// Extract text content from result
			var resultText string
			for _, content := range result.Content {
				if textContent, ok := mcp.AsTextContent(content); ok {
					resultText = textContent.Text
					break
				}
			}

			GinkgoWriter.Printf("Workflow result when step runs: %s\n", resultText)

			// The output should contain the actual echoed message (yardstick wraps in JSON)
			Expect(resultText).To(ContainSubstring("actualstepoutput123"),
				"Output should contain actual step output when step runs")

			// The output should NOT contain the default value
			Expect(resultText).ToNot(ContainSubstring("default_value_when_skipped"),
				"Output should not contain defaultResults value when step runs")
		})
	})
})
