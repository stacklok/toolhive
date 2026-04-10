// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/script"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Script Middleware", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-script-group"
		vmcpServerName  = "test-vmcp-script"
		backendName     = "yardstick-script"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for script middleware test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for script middleware", timeout, pollingInterval)

		By("Creating yardstick backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
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
		Expect(k8sClient.Create(ctx, backend)).To(Succeed())

		By("Waiting for backend MCPServer to be running")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backendName,
				Namespace: testNamespace,
			}, server); err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseReady {
				return fmt.Errorf("not ready yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
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
		GinkgoWriter.Printf("VirtualMCPServer accessible at http://localhost:%d\n", vmcpNodePort)
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: vmcpServerName, Namespace: testNamespace},
		})

		By("Cleaning up backend MCPServer")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
		})

		By("Cleaning up MCPGroup")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
		})
	})

	It("should include execute_tool_script in tools/list with dynamic description", func() {
		tools := WaitForExpectedTools(vmcpNodePort, "script-test-client",
			func(toolsList []mcp.Tool) error {
				return ToolsContainAll(toolsList, script.ExecuteToolScriptName)
			}, timeout)

		// Find the script tool and verify its description mentions backend tools
		var scriptTool *mcp.Tool
		for i := range tools.Tools {
			if tools.Tools[i].Name == script.ExecuteToolScriptName {
				scriptTool = &tools.Tools[i]
				break
			}
		}
		Expect(scriptTool).ToNot(BeNil(), "execute_tool_script should be in tool list")
		Expect(scriptTool.Description).To(ContainSubstring("echo"),
			"dynamic description should mention yardstick's echo tool")

		GinkgoWriter.Printf("Script tool description:\n%s\n", scriptTool.Description)
	})

	It("should execute a script that calls a backend tool", func() {
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "script-exec-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		callRequest := mcp.CallToolRequest{}
		callRequest.Params.Name = script.ExecuteToolScriptName
		callRequest.Params.Arguments = map[string]any{
			"script": `result = echo(input=message)
return {"echoed": result}`,
			"data": map[string]any{
				"message": "hello from script",
			},
		}

		result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
		Expect(err).ToNot(HaveOccurred(), "Script tool call should succeed")
		Expect(result).ToNot(BeNil())
		Expect(result.Content).ToNot(BeEmpty(), "Should have content in response")

		// Parse the result text
		textContent, ok := result.Content[0].(mcp.TextContent)
		Expect(ok).To(BeTrue(), "First content should be text")
		GinkgoWriter.Printf("Script result: %s\n", textContent.Text)

		// The result should be valid JSON containing the echoed value
		var resultMap map[string]any
		Expect(json.Unmarshal([]byte(textContent.Text), &resultMap)).To(Succeed())
		Expect(resultMap).To(HaveKey("echoed"))
	})

	It("should return error for invalid script", func() {
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "script-error-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		callRequest := mcp.CallToolRequest{}
		callRequest.Params.Name = script.ExecuteToolScriptName
		callRequest.Params.Arguments = map[string]any{
			"script": "return !!!",
		}

		// The call may return an error or an isError result depending on how the
		// JSON-RPC error is surfaced through the mcp-go client
		_, err = mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
		Expect(err).To(HaveOccurred(), "Invalid script should produce an error")
	})
})
