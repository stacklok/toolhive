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
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Global ExcludeAllTools", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-excludeall-global-group"
		vmcpServerName  = "test-vmcp-excludeall-global"
		backend1Name    = "yardstick-excludeall-a"
		backend2Name    = "yardstick-excludeall-b"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for global excludeAllTools test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for global excludeAllTools E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServers in parallel")
		CreateMultipleMCPServersInParallel(ctx, k8sClient, []BackendConfig{
			{Name: backend1Name, Namespace: testNamespace, GroupRef: mcpGroupName, Image: images.YardstickServerImage},
			{Name: backend2Name, Namespace: testNamespace, GroupRef: mcpGroupName, Image: images.YardstickServerImage},
		}, timeout, pollingInterval)

		By("Creating VirtualMCPServer with global excludeAllTools: true")
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
						// Global flag to exclude all tools from all backends
						ExcludeAllTools: true,
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

	Context("when global excludeAllTools is enabled", func() {
		It("should return empty tools list from all backends", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-excludeall-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())

			By(fmt.Sprintf("VirtualMCPServer returns %d tools with excludeAllTools: true", len(tools.Tools)))

			// Verify tools list is empty due to global excludeAllTools
			Expect(tools.Tools).To(BeEmpty(), "Should have no tools when excludeAllTools is true globally")
		})

		It("should still respond to MCP protocol requests", func() {
			By("Creating and initializing MCP client")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-excludeall-protocol-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Verifying server responds to tools/list even when empty")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred(), "Server should respond to tools/list request")
			Expect(tools).ToNot(BeNil(), "Response should not be nil")

			// The response should be valid but with empty tools
			Expect(tools.Tools).To(BeEmpty())
		})
	})

	Context("when verifying excludeAllTools configuration", func() {
		It("should have correct aggregation configuration with excludeAllTools", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.Config.Aggregation).ToNot(BeNil())
			Expect(vmcpServer.Spec.Config.Aggregation.ExcludeAllTools).To(BeTrue(),
				"Global excludeAllTools should be true")
		})

		It("should have backends discovered but tools excluded", func() {
			// Verify backends are in the group
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(backends).To(HaveLen(2), "Should have 2 backends in the group")

			// Verify each backend is running
			for _, backend := range backends {
				Expect(backend.Status.Phase).To(Equal(mcpv1alpha1.MCPServerPhaseRunning),
					fmt.Sprintf("Backend %s should be running", backend.Name))
			}

			// Even though backends are running, tools should be excluded
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-backend-verify-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).To(BeEmpty(), "Tools should be excluded despite backends being available")
		})
	})
})
