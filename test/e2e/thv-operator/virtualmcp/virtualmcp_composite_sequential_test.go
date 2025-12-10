package virtualmcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Composite Sequential Workflow", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-composite-seq-group"
		vmcpServerName  = "test-vmcp-composite-seq"
		backendName     = "yardstick-composite-seq"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32

		// Composite tool names
		compositeToolName = "echo_twice"
	)

	BeforeAll(func() {
		By("Creating MCPGroup for composite sequential test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for composite sequential E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		// JSON Schema for composite tool parameters
		// Per MCP spec, inputSchema should be a JSON Schema object
		parameterSchema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"message": map[string]interface{}{
					"type":        "string",
					"description": "The message to echo twice",
				},
			},
			"required": []string{"message"},
		}
		paramSchemaBytes, err := json.Marshal(parameterSchema)
		Expect(err).ToNot(HaveOccurred())

		// Build arguments as JSON for runtime.RawExtension
		firstEchoArgs, err := json.Marshal(map[string]any{
			"input": "{{ .params.message }}",
		})
		Expect(err).ToNot(HaveOccurred())

		secondEchoArgs, err := json.Marshal(map[string]any{
			"input": "{{ .steps.first_echo.result }}",
		})
		Expect(err).ToNot(HaveOccurred())

		By("Creating VirtualMCPServer with composite sequential workflow")
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
				},
				// Define a composite tool that echoes input, then echoes the result again
				CompositeTools: []mcpv1alpha1.CompositeToolSpec{
					{
						Name:        compositeToolName,
						Description: "Echoes the input message twice in sequence",
						Parameters: &runtime.RawExtension{
							Raw: paramSchemaBytes,
						},
						Steps: []mcpv1alpha1.WorkflowStep{
							{
								ID:   "first_echo",
								Type: "tool",
								Tool: fmt.Sprintf("%s.echo", backendName),
								Arguments: &runtime.RawExtension{
									Raw: firstEchoArgs,
								},
							},
							{
								ID:        "second_echo",
								Type:      "tool",
								Tool:      fmt.Sprintf("%s.echo", backendName),
								DependsOn: []string{"first_echo"},
								Arguments: &runtime.RawExtension{
									Raw: secondEchoArgs,
								},
							},
						},
						Timeout: "30s",
					},
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout)

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
			Expect(result.Content).ToNot(ContainSubstring("<no value>"))
			Expect(result.Content).To(ContainSubstring(testMessage))

			// The result should reflect the sequential execution
			// First echo: echoes testMessage
			// Second echo: echoes the result of first echo
			GinkgoWriter.Printf("Composite tool result: %+v\n", result.Content)
		})
	})

	Context("when verifying composite tool configuration", func() {
		It("should have correct composite tool spec stored", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.CompositeTools).To(HaveLen(1))

			compositeTool := vmcpServer.Spec.CompositeTools[0]
			Expect(compositeTool.Name).To(Equal(compositeToolName))
			Expect(compositeTool.Steps).To(HaveLen(2))

			// Verify step dependencies
			step1 := compositeTool.Steps[0]
			Expect(step1.ID).To(Equal("first_echo"))
			Expect(step1.DependsOn).To(BeEmpty())

			step2 := compositeTool.Steps[1]
			Expect(step2.ID).To(Equal("second_echo"))
			Expect(step2.DependsOn).To(ContainElement("first_echo"))

			// Verify template usage in arguments (unmarshal from RawExtension)
			var step1Args map[string]any
			Expect(json.Unmarshal(step1.Arguments.Raw, &step1Args)).To(Succeed())
			Expect(step1Args["input"]).To(ContainSubstring(".params.message"))

			var step2Args map[string]any
			Expect(json.Unmarshal(step2.Arguments.Raw, &step2Args)).To(Succeed())
			Expect(step2Args["input"]).To(ContainSubstring(".steps.first_echo"))
		})
	})
})
