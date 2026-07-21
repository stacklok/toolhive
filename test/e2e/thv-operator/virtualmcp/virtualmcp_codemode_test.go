// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This suite exercises the code mode CRD path end to end: spec.config.codeMode on a
// VirtualMCPServer must flow through the operator reconcile into a running pod that
// advertises execute_tool_script and runs submitted Starlark scripts, whose inner tool
// calls reach the backend through the proxy.
var _ = Describe("VirtualMCPServer Code Mode", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-codemode-group"
		vmcpServerName  = "test-vmcp-codemode"
		backendName     = "codemode-yardstick"
		timeout         = 5 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for code mode test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for code mode E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServer (deterministic, offline echo tool)")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace,
			mcpGroupName, images.YardstickServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer with code mode enabled")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, testNamespace,
			v1beta1test.WithVMCPGroupRef(mcpGroupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
			}),
			v1beta1test.WithVMCPOutgoingAuth(&mcpv1beta1.OutgoingAuthConfig{
				Source: "discovered",
			}),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{
				Group:    mcpGroupName,
				CodeMode: &vmcpconfig.CodeModeConfig{Enabled: true},
			}),
			v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
				v.Spec.ServiceType = "NodePort"
			}),
		)
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting VirtualMCPServer NodePort")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		_, _ = fmt.Fprintf(GinkgoWriter, "VirtualMCPServer is accessible at NodePort: %d\n", vmcpNodePort)
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1beta1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer); err == nil {
			_ = k8sClient.Delete(ctx, vmcpServer)
		}

		By("Cleaning up backend MCPServer")
		backend := &mcpv1beta1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      backendName,
			Namespace: testNamespace,
		}, backend); err == nil {
			_ = k8sClient.Delete(ctx, backend)
		}

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1beta1.MCPGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      mcpGroupName,
			Namespace: testNamespace,
		}, mcpGroup); err == nil {
			_ = k8sClient.Delete(ctx, mcpGroup)
		}
	})

	It("should advertise execute_tool_script alongside the backend tools", func() {
		By("Creating and initializing MCP client")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "codemode-list-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		By("Listing tools from VirtualMCPServer")
		tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, mcp.ListToolsRequest{})
		Expect(err).ToNot(HaveOccurred())

		toolNames := make([]string, len(tools.Tools))
		for i, tool := range tools.Tools {
			toolNames[i] = tool.Name
		}

		By("Verifying execute_tool_script and the backend echo tool are both present")
		Expect(toolNames).To(ContainElement("execute_tool_script"),
			"code mode must advertise the execute_tool_script virtual tool")
		Expect(toolNames).To(ContainElement(ContainSubstring("echo")),
			"the backend echo tool must still be advertised alongside the virtual tool")

		_, _ = fmt.Fprintf(GinkgoWriter, "✓ Code mode advertises: %v\n", toolNames)
	})

	It("should execute a Starlark script that fans out to a backend tool and aggregates", func() {
		By("Creating and initializing MCP client")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "codemode-exec-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		By("Resolving the advertised echo tool name")
		tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, mcp.ListToolsRequest{})
		Expect(err).ToNot(HaveOccurred())
		echoTool := ""
		for _, tool := range tools.Tools {
			if strings.Contains(tool.Name, "echo") {
				echoTool = tool.Name
				break
			}
		}
		Expect(echoTool).ToNot(BeEmpty(), "should find an echo tool to call from the script")

		By("Submitting a script that calls the backend tool twice in parallel")
		// parallel() fans out two real backend calls server-side; the script returns one
		// aggregated result. If either inner call failed (auth, transport, missing tool),
		// the engine surfaces it and IsError would be true — so a clean count of 2 proves
		// both calls reached the backend through the proxy and came back.
		// Input values must be alphanumeric: the yardstick echo tool validates `input`
		// against ^[a-zA-Z0-9]+$, so hyphens/spaces would be rejected by the backend.
		script := fmt.Sprintf(`
results = parallel([
    lambda: call_tool(%q, input="codemodealpha"),
    lambda: call_tool(%q, input="codemodebeta"),
])
return {"count": len(results), "results": results}
`, echoTool, echoTool)

		req := mcp.CallToolRequest{}
		req.Params.Name = "execute_tool_script"
		req.Params.Arguments = map[string]any{"script": script}

		result, err := mcpClient.Client.CallTool(mcpClient.Ctx, req)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).ToNot(BeNil())
		Expect(result.IsError).To(BeFalse(), "script (and both inner backend calls) must succeed: %s", toolResultText(result))

		By("Verifying the aggregated result")
		var out struct {
			Count int `json:"count"`
		}
		Expect(json.Unmarshal([]byte(toolResultText(result)), &out)).To(Succeed())
		Expect(out.Count).To(Equal(2), "the script must aggregate both parallel backend calls into one result")

		_, _ = fmt.Fprintf(GinkgoWriter, "✓ Code mode executed a 2-way parallel script end to end\n")
	})
})

// toolResultText returns the first text content of a tool call result, or "" if none.
func toolResultText(result *mcp.CallToolResult) string {
	for _, c := range result.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			return tc.Text
		}
	}
	return ""
}
