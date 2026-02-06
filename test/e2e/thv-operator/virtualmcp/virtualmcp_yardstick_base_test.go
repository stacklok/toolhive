// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
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

var _ = Describe("VirtualMCPServer Yardstick Base", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-yardstick-group"
		vmcpServerName  = "test-vmcp-yardstick"
		backend1Name    = "yardstick-a"
		backend2Name    = "yardstick-b"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for yardstick backends")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for yardstick-based E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServers in parallel")
		// Create both MCPServer resources without waiting
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

		// Wait for both backends to be running in parallel
		By("Waiting for both backend MCPServers to be running")
		Eventually(func() error {
			server1 := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1Name,
				Namespace: testNamespace,
			}, server1); err != nil {
				return fmt.Errorf("backend1: failed to get server: %w", err)
			}
			if server1.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend1 not ready yet, phase: %s", server1.Status.Phase)
			}

			server2 := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, server2); err != nil {
				return fmt.Errorf("backend2: failed to get server: %w", err)
			}
			if server2.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend2 not ready yet, phase: %s", server2.Status.Phase)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed(), "Both MCPServers should be running")

		By("Creating VirtualMCPServer with prefix conflict resolution")
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

	Context("when testing basic yardstick aggregation", func() {
		It("should be accessible via NodePort", func() {
			By("Testing HTTP connectivity to VirtualMCPServer")
			httpClient := &http.Client{Timeout: 10 * time.Second}
			url := fmt.Sprintf("http://localhost:%d/health", vmcpNodePort)

			Eventually(func() error {
				resp, err := httpClient.Get(url)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
				}
				return nil
			}, 2*time.Minute, pollingInterval).Should(Succeed())
		})

		It("should aggregate echo tools from both yardstick backends", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-yardstick-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "VirtualMCPServer should aggregate tools from backends")

			By(fmt.Sprintf("VirtualMCPServer aggregates %d tools", len(tools.Tools)))
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  Aggregated tool: %s - %s\n", tool.Name, tool.Description)
			}

			// With prefix conflict resolution, both yardstick backends should expose "echo" tool
			// prefixed with their workload name: yardstick-a_echo, yardstick-b_echo
			Expect(len(tools.Tools)).To(BeNumerically(">=", 2),
				"VirtualMCPServer should aggregate echo tools from both backends")

			// Verify we have prefixed tools from both backends
			toolNames := make([]string, len(tools.Tools))
			for i, tool := range tools.Tools {
				toolNames[i] = tool.Name
			}
			GinkgoWriter.Printf("All aggregated tool names: %v\n", toolNames)

			// Check that we have tools from both backends (with prefixes)
			hasBackend1Tool := false
			hasBackend2Tool := false
			for _, name := range toolNames {
				if strings.Contains(name, backend1Name) {
					hasBackend1Tool = true
				}
				if strings.Contains(name, backend2Name) {
					hasBackend2Tool = true
				}
			}
			Expect(hasBackend1Tool).To(BeTrue(), "Should have tool from backend 1")
			Expect(hasBackend2Tool).To(BeTrue(), "Should have tool from backend 2")
		})

		It("should successfully call echo tool through VirtualMCPServer", func() {
			// Use shared helper to test tool listing and calling
			TestToolListingAndCall(vmcpNodePort, "toolhive-yardstick-test", "echo", "hello123")
		})

		It("should preserve metadata when calling tools through vMCP", func() {
			By("Creating and initializing MCP client")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-metadata-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			// Find an echo tool to call
			var toolToCall string
			for _, tool := range tools.Tools {
				if strings.Contains(tool.Name, "echo") {
					toolToCall = tool.Name
					break
				}
			}
			Expect(toolToCall).ToNot(BeEmpty(), "Should find an echo tool")

			By(fmt.Sprintf("Calling tool: %s with metadata", toolToCall))
			// Yardstick server echoes back metadata from requests to responses
			// This tests the full round-trip: client → vMCP → backend → vMCP → client
			testTraceID := "test-trace-123"
			testRequestID := "req-456"
			callRequest := mcp.CallToolRequest{}
			callRequest.Params.Name = toolToCall
			callRequest.Params.Arguments = map[string]interface{}{
				"input": "testmetadatapreservation",
			}
			callRequest.Params.Meta = &mcp.Meta{
				AdditionalFields: map[string]interface{}{
					"traceId":   testTraceID,
					"requestId": testRequestID,
				},
			}

			result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
			Expect(err).ToNot(HaveOccurred(), "Tool call should succeed")
			Expect(result).ToNot(BeNil())

			By("Verifying metadata preservation through vMCP")
			// Yardstick echoes back _meta fields from the request
			// This validates the full metadata preservation path:
			// 1. vMCP accepts _meta in client requests
			// 2. vMCP forwards _meta to backend (yardstick)
			// 3. Backend echoes _meta in response
			// 4. vMCP preserves _meta from backend response back to client

			GinkgoWriter.Printf("Tool call result - IsError: %v\n", result.IsError)

			if result.Meta == nil {
				GinkgoWriter.Printf("[DEBUG] Result.Meta is nil - metadata was not preserved\n")
				GinkgoWriter.Printf("[DEBUG] This could indicate:\n")
				GinkgoWriter.Printf("[DEBUG]   - Metadata not forwarded from vMCP to backend\n")
				GinkgoWriter.Printf("[DEBUG]   - Backend not returning metadata (check yardstick logs)\n")
				GinkgoWriter.Printf("[DEBUG]   - Metadata not preserved from backend response to client\n")
			}

			Expect(result.Meta).ToNot(BeNil(),
				"Yardstick should echo back metadata from request. "+
					"Check: 1) vMCP forwarding _meta to backend, 2) backend echoing _meta, 3) vMCP preserving _meta from response")

			GinkgoWriter.Printf("Metadata preserved through vMCP:\n")
			if result.Meta.ProgressToken != nil {
				GinkgoWriter.Printf("  progressToken: %v\n", result.Meta.ProgressToken)
			}

			Expect(result.Meta.AdditionalFields).ToNot(BeEmpty(),
				"Yardstick should preserve additional metadata fields from request")

			// Verify the custom fields we sent are echoed back
			traceID, hasTraceID := result.Meta.AdditionalFields["traceId"]
			Expect(hasTraceID).To(BeTrue(), "Should preserve traceId field")
			Expect(traceID).To(Equal(testTraceID), "TraceId value should match what was sent")

			requestID, hasRequestID := result.Meta.AdditionalFields["requestId"]
			Expect(hasRequestID).To(BeTrue(), "Should preserve requestId field")
			Expect(requestID).To(Equal(testRequestID), "RequestId value should match what was sent")

			for key, value := range result.Meta.AdditionalFields {
				GinkgoWriter.Printf("  %s: %v\n", key, value)
			}

			GinkgoWriter.Printf("[PASS] vMCP correctly preserves metadata end-to-end\n")
		})
	})

	Context("when verifying VirtualMCPServer status", func() {
		It("should have correct aggregation configuration", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcpServer.Spec.Config.Aggregation).ToNot(BeNil())
			Expect(string(vmcpServer.Spec.Config.Aggregation.ConflictResolution)).To(Equal("prefix"))
		})

		It("should discover both yardstick backends in the group", func() {
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(backends).To(HaveLen(2), "Should discover both yardstick backends in the group")

			backendNames := make([]string, len(backends))
			for i, backend := range backends {
				backendNames[i] = backend.Name
			}
			Expect(backendNames).To(ContainElements(backend1Name, backend2Name))
		})

		It("should have VirtualMCPServer in Ready phase", func() {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady))
		})

	})

	Context("when testing group membership changes trigger reconciliation", func() {
		backend3Name := "yardstick-c"

		It("should have two discovered backends initially", func() {
			status, err := GetVirtualMCPServerStatus(ctx, k8sClient, vmcpServerName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(status.BackendCount).To(Equal(2), "Should have 2 initial backends")
			Expect(status.DiscoveredBackends).To(HaveLen(2), "Should have 2 discovered backends")

			backendNames := make([]string, len(status.DiscoveredBackends))
			for i, backend := range status.DiscoveredBackends {
				backendNames[i] = backend.Name
			}
			Expect(backendNames).To(ContainElements(backend1Name, backend2Name))

			By(fmt.Sprintf("Initial backends: %v", backendNames))
		})

		It("should discover a new backend when added to the group", func() {
			By("Creating a new yardstick backend MCPServer and adding to the group")
			CreateMCPServerAndWait(ctx, k8sClient, backend3Name, testNamespace,
				mcpGroupName, images.YardstickServerImage, timeout, pollingInterval)

			By("Waiting for VirtualMCPServer to reconcile and discover the new backend")
			Eventually(func() error {
				status, err := GetVirtualMCPServerStatus(ctx, k8sClient, vmcpServerName, testNamespace)
				if err != nil {
					return err
				}

				// Check DiscoveredBackends first (this includes all backends regardless of health)
				if len(status.DiscoveredBackends) != 3 {
					return fmt.Errorf("expected 3 discovered backends, got %d", len(status.DiscoveredBackends))
				}

				backendNames := make([]string, len(status.DiscoveredBackends))
				for i, backend := range status.DiscoveredBackends {
					backendNames[i] = backend.Name
				}

				if !slices.Contains(backendNames, backend3Name) {
					return fmt.Errorf("new backend %s not found in discovered backends: %v", backend3Name, backendNames)
				}

				// BackendCount only includes healthy backends, so check this separately
				// We expect all backends to eventually become healthy
				if status.BackendCount != 3 {
					return fmt.Errorf("expected 3 healthy backends, got %d (discovered: %v)", status.BackendCount, backendNames)
				}

				return nil
			}, timeout, pollingInterval).Should(Succeed(), "VirtualMCPServer should discover the new backend")

		})

		// Note: Backend failure, recovery, and status phase transitions are tested
		// comprehensively in virtualmcp_circuit_breaker_test.go with fast intervals (5s)
		// for quick testing. That test provides thorough coverage of health monitoring
		// and phase transitions (Ready→Degraded→Ready), avoiding duplication here.

		AfterAll(func() {
			By("Cleaning up additional backends from membership test")
			backend3 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend3Name,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, backend3)
		})
	})
})
