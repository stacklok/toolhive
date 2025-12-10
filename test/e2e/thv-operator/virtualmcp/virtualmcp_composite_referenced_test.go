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

var _ = Describe("VirtualMCPServer Composite Referenced Workflow", Ordered, func() {
	var (
		testNamespace        = "default"
		mcpGroupName         = "test-composite-ref-group"
		vmcpServerName       = "test-vmcp-composite-ref"
		backendName          = "yardstick-composite-ref"
		compositeToolDefName = "echo-twice-definition"
		timeout              = 5 * time.Minute
		pollingInterval      = 5 * time.Second
		vmcpNodePort         int32

		// Composite tool name
		compositeToolName = "echo_twice_ref"
	)

	BeforeAll(func() {
		By("Creating MCPGroup for composite referenced test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for composite referenced E2E tests", timeout, pollingInterval)

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

		By("Creating VirtualMCPCompositeToolDefinition")
		compositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      compositeToolDefName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
				Name:        compositeToolName,
				Description: "Echoes the input message twice in sequence (referenced)",
				Parameters: &runtime.RawExtension{
					Raw: paramSchemaBytes,
				},
				Steps: []mcpv1alpha1.WorkflowStep{
					{
						ID:   "first_echo",
						Type: "tool",
						// Use dot notation for tool references: backend.toolname
						Tool: fmt.Sprintf("%s.echo", backendName),
						Arguments: map[string]string{
							// Template expansion: use input parameter
							"input": "{{ .params.message }}",
						},
					},
					{
						ID:   "second_echo",
						Type: "tool",
						// Use dot notation for tool references: backend.toolname
						Tool:      fmt.Sprintf("%s.echo", backendName),
						DependsOn: []string{"first_echo"},
						Arguments: map[string]string{
							// Template expansion: use output from previous step
							"input": "{{ .steps.first_echo.result }}",
						},
					},
				},
				Timeout: "30s",
			},
		}
		Expect(k8sClient.Create(ctx, compositeToolDef)).To(Succeed())

		By("Verifying VirtualMCPCompositeToolDefinition was created")
		// If creation succeeded, the webhook validation passed (no controller sets status)
		Eventually(func() bool {
			def := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      compositeToolDefName,
				Namespace: testNamespace,
			}, def)
			return err == nil
		}, 30*time.Second, pollingInterval).Should(BeTrue(), "VirtualMCPCompositeToolDefinition should exist")

		By("Creating VirtualMCPServer with referenced composite tool")
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
				// Reference the composite tool definition instead of defining inline
				CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
					{
						Name: compositeToolDefName,
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

		By("Cleaning up VirtualMCPCompositeToolDefinition")
		compositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      compositeToolDefName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, compositeToolDef)

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

	Context("when composite tools are referenced", func() {
		It("should expose the referenced composite tool in tool listing", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-composite-ref-test", 30*time.Second)
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

			// Should find the referenced composite tool
			var foundComposite bool
			for _, tool := range tools.Tools {
				if tool.Name == compositeToolName {
					foundComposite = true
					Expect(tool.Description).To(Equal("Echoes the input message twice in sequence (referenced)"))
					break
				}
			}
			Expect(foundComposite).To(BeTrue(), "Should find referenced composite tool: %s", compositeToolName)

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

		It("should execute referenced composite tool with sequential workflow", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-composite-ref-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Calling referenced composite tool with test message")
			testMessage := "hello_referenced_test"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = compositeToolName
			callRequest.Params.Arguments = map[string]any{
				"message": testMessage,
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Referenced composite tool call should succeed")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

			// The result should reflect the sequential execution
			// First echo: echoes testMessage
			// Second echo: echoes the result of first echo
			GinkgoWriter.Printf("Referenced composite tool result: %+v\n", result.Content)
		})
	})

	Context("when verifying referenced composite tool configuration", func() {
		It("should have correct CompositeToolRefs in VirtualMCPServer", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			// Should use CompositeToolRefs, not inline CompositeTools
			Expect(vmcpServer.Spec.CompositeTools).To(BeEmpty(), "Should not have inline composite tools")
			Expect(vmcpServer.Spec.CompositeToolRefs).To(HaveLen(1), "Should have one composite tool reference")

			ref := vmcpServer.Spec.CompositeToolRefs[0]
			Expect(ref.Name).To(Equal(compositeToolDefName))
		})

		It("should have correct composite tool definition stored", func() {
			compositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      compositeToolDefName,
				Namespace: testNamespace,
			}, compositeToolDef)
			Expect(err).ToNot(HaveOccurred())

			// Verify the definition spec
			Expect(compositeToolDef.Spec.Name).To(Equal(compositeToolName))
			Expect(compositeToolDef.Spec.Steps).To(HaveLen(2))

			// Verify step dependencies
			step1 := compositeToolDef.Spec.Steps[0]
			Expect(step1.ID).To(Equal("first_echo"))
			Expect(step1.DependsOn).To(BeEmpty())

			step2 := compositeToolDef.Spec.Steps[1]
			Expect(step2.ID).To(Equal("second_echo"))
			Expect(step2.DependsOn).To(ContainElement("first_echo"))

			// Verify template usage in arguments
			Expect(step1.Arguments["input"]).To(ContainSubstring(".params.message"))
			Expect(step2.Arguments["input"]).To(ContainSubstring(".steps.first_echo"))

			// Note: ValidationStatus is not set because there's no controller for VirtualMCPCompositeToolDefinition
			// If the resource exists, it means webhook validation passed
		})

		It("should reflect referenced tool in VirtualMCPServer status", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			// Check that VirtualMCPServer is in Ready phase
			Expect(vmcpServer.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady),
				"VirtualMCPServer should be in Ready phase when using valid CompositeToolRefs")

			// Check for CompositeToolRefsValidated condition (if it exists)
			// Note: This condition might not always be set immediately
			for _, condition := range vmcpServer.Status.Conditions {
				if condition.Type == mcpv1alpha1.ConditionTypeCompositeToolRefsValidated {
					Expect(condition.Status).To(Equal(metav1.ConditionTrue),
						"CompositeToolRefs should be validated")
					Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonCompositeToolRefsValid))
					GinkgoWriter.Printf("Found CompositeToolRefsValidated condition: %s\n", condition.Message)
					break
				}
			}
		})
	})
})
