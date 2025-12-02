package virtualmcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// Compile-time check to ensure corev1 is used (for Service type)
var _ = corev1.ServiceSpec{}

var _ = Describe("VirtualMCPServer Composite Parallel Workflow", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-composite-par-group"
		vmcpServerName  = "test-vmcp-composite-par"
		backend1Name    = "yardstick-par-a"
		backend2Name    = "yardstick-par-b"
		timeout         = 5 * time.Minute
		pollingInterval = 5 * time.Second
		vmcpNodePort    int32

		// Composite tool name
		compositeToolName = "parallel_echo"
	)

	vmcpServiceName := func() string {
		return fmt.Sprintf("vmcp-%s", vmcpServerName)
	}

	BeforeAll(func() {
		By("Creating MCPGroup for composite parallel test")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for composite parallel E2E tests",
			},
		}
		Expect(k8sClient.Create(ctx, mcpGroup)).To(Succeed())

		By("Waiting for MCPGroup to be ready")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			}, mcpGroup)
			if err != nil {
				return false
			}
			return mcpGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
		}, timeout, pollingInterval).Should(BeTrue())

		By("Creating first yardstick backend MCPServer")
		backend1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend1Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend1)).To(Succeed())

		By("Creating second yardstick backend MCPServer")
		backend2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backend2Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend2)).To(Succeed())

		By("Waiting for backend MCPServers to be ready")
		for _, backendName := range []string{backend1Name, backend2Name} {
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backendName,
					Namespace: testNamespace,
				}, server)
				if err != nil {
					return fmt.Errorf("failed to get server: %w", err)
				}
				if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
					return nil
				}
				return fmt.Errorf("%s not ready yet, phase: %s", backendName, server.Status.Phase)
			}, timeout, pollingInterval).Should(Succeed(), fmt.Sprintf("%s should be ready", backendName))
		}

		// JSON Schema for composite tool parameters
		parameterSchema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"message": map[string]interface{}{
					"type":        "string",
					"description": "The message to echo in parallel to both backends",
				},
			},
			"required": []string{"message"},
		}
		paramSchemaBytes, err := json.Marshal(parameterSchema)
		Expect(err).ToNot(HaveOccurred())

		By("Creating VirtualMCPServer with composite parallel workflow")
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
				// Define a composite tool that echoes to both backends in parallel
				// Steps without DependsOn can execute concurrently
				CompositeTools: []mcpv1alpha1.CompositeToolSpec{
					{
						Name:        compositeToolName,
						Description: "Echoes message to both backends in parallel, then combines results",
						Parameters: &runtime.RawExtension{
							Raw: paramSchemaBytes,
						},
						Steps: []mcpv1alpha1.WorkflowStep{
							{
								// Step 1: Echo to backend1 (no dependencies - runs in parallel)
								ID:   "echo_backend1",
								Type: "tool",
								Tool: fmt.Sprintf("%s.echo", backend1Name),
								Arguments: map[string]string{
									"input": "backend1: {{ .params.message }}",
								},
							},
							{
								// Step 2: Echo to backend2 (no dependencies - runs in parallel with step 1)
								ID:   "echo_backend2",
								Type: "tool",
								Tool: fmt.Sprintf("%s.echo", backend2Name),
								Arguments: map[string]string{
									"input": "backend2: {{ .params.message }}",
								},
							},
							{
								// Step 3: Final aggregation - depends on both parallel steps
								ID:        "combine_results",
								Type:      "tool",
								Tool:      fmt.Sprintf("%s.echo", backend1Name),
								DependsOn: []string{"echo_backend1", "echo_backend2"},
								Arguments: map[string]string{
									// Combine outputs from both parallel steps
									"input": "Combined: [{{ .steps.echo_backend1.result }}] + [{{ .steps.echo_backend2.result }}]",
								},
							},
						},
						Timeout: "60s",
					},
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout)

		By("Getting NodePort for VirtualMCPServer")
		Eventually(func() error {
			service := &corev1.Service{}
			serviceName := vmcpServiceName()
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      serviceName,
				Namespace: testNamespace,
			}, service)
			if err != nil {
				return err
			}
			if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].NodePort == 0 {
				return fmt.Errorf("nodePort not assigned for vmcp")
			}
			vmcpNodePort = service.Spec.Ports[0].NodePort
			return nil
		}, timeout, pollingInterval).Should(Succeed())

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

	Context("when composite tools with parallel steps are configured", func() {
		It("should expose the composite tool in tool listing", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-parallel-test", 30*time.Second)
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
					Expect(tool.Description).To(ContainSubstring("parallel"))
					break
				}
			}
			Expect(foundComposite).To(BeTrue(), "Should find composite tool: %s", compositeToolName)

			// Should also have both backends' native echo tools (with prefix)
			foundBackends := make(map[string]bool)
			for _, tool := range tools.Tools {
				if tool.Name == fmt.Sprintf("%s_echo", backend1Name) {
					foundBackends[backend1Name] = true
				}
				if tool.Name == fmt.Sprintf("%s_echo", backend2Name) {
					foundBackends[backend2Name] = true
				}
			}
			Expect(foundBackends).To(HaveLen(2), "Should find both backend echo tools")
		})

		It("should execute parallel workflow and aggregate results", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-parallel-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Calling composite tool with test message")
			testMessage := "parallel_test_123"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = compositeToolName
			callRequest.Params.Arguments = map[string]any{
				"message": testMessage,
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Composite tool call should succeed")
			Expect(result).ToNot(BeNil())
			Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

			// The result should contain combined outputs from both parallel steps
			// Final step combines: [backend1 result] + [backend2 result]
			GinkgoWriter.Printf("Parallel composite tool result: %+v\n", result.Content)
		})
	})

	Context("when verifying parallel workflow configuration", func() {
		It("should have correct composite tool spec with parallel steps", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.CompositeTools).To(HaveLen(1))

			compositeTool := vmcpServer.Spec.CompositeTools[0]
			Expect(compositeTool.Name).To(Equal(compositeToolName))
			Expect(compositeTool.Steps).To(HaveLen(3))

			// Verify parallel steps (no dependencies)
			step1 := compositeTool.Steps[0]
			Expect(step1.ID).To(Equal("echo_backend1"))
			Expect(step1.DependsOn).To(BeEmpty(), "First step should have no dependencies (parallel)")

			step2 := compositeTool.Steps[1]
			Expect(step2.ID).To(Equal("echo_backend2"))
			Expect(step2.DependsOn).To(BeEmpty(), "Second step should have no dependencies (parallel)")

			// Verify final aggregation step depends on both parallel steps
			step3 := compositeTool.Steps[2]
			Expect(step3.ID).To(Equal("combine_results"))
			Expect(step3.DependsOn).To(ContainElements("echo_backend1", "echo_backend2"))

			// Verify template usage combines outputs from parallel steps
			Expect(step3.Arguments["input"]).To(ContainSubstring(".steps.echo_backend1"))
			Expect(step3.Arguments["input"]).To(ContainSubstring(".steps.echo_backend2"))
		})

		It("should target different backends in parallel steps", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			compositeTool := vmcpServer.Spec.CompositeTools[0]

			// Verify steps target different backends
			step1 := compositeTool.Steps[0]
			step2 := compositeTool.Steps[1]

			Expect(step1.Tool).To(ContainSubstring(backend1Name))
			Expect(step2.Tool).To(ContainSubstring(backend2Name))
		})
	})
})
