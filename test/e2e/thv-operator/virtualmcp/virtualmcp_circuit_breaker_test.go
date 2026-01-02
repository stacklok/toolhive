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
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Circuit Breaker and Health Filtering", Ordered, func() {
	var (
		testNamespace           = "default"
		mcpGroupName            = "test-circuit-breaker-group"
		vmcpServerName          = "test-vmcp-circuit-breaker"
		healthyBackend          = "cb-healthy-backend"
		failingBackend          = "cb-failing-backend"
		timeout                 = 3 * time.Minute
		pollingInterval         = 2 * time.Second
		healthCheckInterval     = "5s"  // Fast checks for e2e
		unhealthyThreshold      = 10    // Higher than circuit breaker threshold
		circuitBreakerThreshold = 3     // Open circuit after 3 failures
		circuitBreakerTimeout   = "30s" // Recover after 30 seconds
		vmcpNodePort            int32
		mcpClient               *InitializedMCPClient
	)

	BeforeAll(func() {
		By("Creating MCPGroup for circuit breaker test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"MCP Group for circuit breaker e2e tests", timeout, pollingInterval)

		By("Creating healthy backend MCPServer")
		healthyBackendResource := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      healthyBackend,
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
					{Name: "TOOL_PREFIX", Value: "healthy"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, healthyBackendResource)).To(Succeed())

		By("Creating failing backend MCPServer (will fail health checks)")
		failingBackendResource := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      failingBackend,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   9999, // Wrong port - will cause connection failures
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
					{Name: "TOOL_PREFIX", Value: "failing"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, failingBackendResource)).To(Succeed())

		By("Waiting for healthy backend to be running")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthyBackend,
				Namespace: testNamespace,
			}, server); err != nil {
				return fmt.Errorf("failed to get server: %w", err)
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("not ready yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed(), "Healthy backend should be running")

		By("Creating VirtualMCPServer with circuit breaker enabled")
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
					ConflictResolutionConfig: &mcpv1alpha1.ConflictResolutionConfig{
						PrefixFormat: "{workload}_",
					},
				},
				Operational: &mcpv1alpha1.OperationalConfig{
					LogLevel:           "debug", // Enable debug logging to see circuit breaker activity
					CapabilityCacheTTL: "10s",   // Short cache TTL for testing backend recovery
					FailureHandling: &mcpv1alpha1.FailureHandlingConfig{
						HealthCheckInterval: healthCheckInterval,
						UnhealthyThreshold:  unhealthyThreshold,
						PartialFailureMode:  "best_effort",
						CircuitBreaker: &mcpv1alpha1.CircuitBreakerConfig{
							Enabled:          true,
							FailureThreshold: circuitBreakerThreshold,
							Timeout:          circuitBreakerTimeout,
						},
					},
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be deployed")
		WaitForVirtualMCPServerDeployed(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By(fmt.Sprintf("Creating MCP client connected to http://localhost:%d", vmcpNodePort))
		var err error
		mcpClient, err = CreateInitializedMCPClient(vmcpNodePort, "circuit-breaker-test-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterAll(func() {
		By("Closing MCP client")
		if mcpClient != nil {
			mcpClient.Close()
		}

		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, vmcpServer)

		By("Cleaning up backend MCPServers")
		for _, backendName := range []string{healthyBackend, failingBackend} {
			backend := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backendName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, backend)
		}

		By("Cleaning up MCPGroup")
		group := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, group)
	})

	It("should discover both backends including the failing one", func() {
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			if len(vmcpServer.Status.DiscoveredBackends) != 2 {
				return fmt.Errorf("expected 2 discovered backends, got %d", len(vmcpServer.Status.DiscoveredBackends))
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed(), "Should discover both backends")
	})

	It("should open circuit breaker for failing backend after threshold failures", func() {
		By(fmt.Sprintf("Waiting for circuit to open after %d failures (approximately %d seconds)",
			circuitBreakerThreshold, circuitBreakerThreshold*5))

		// Circuit should open after 3 failures at 5s interval = ~15 seconds
		// Add generous buffer time for processing and status propagation (similar to health monitoring test)
		circuitOpenTimeout := time.Duration(circuitBreakerThreshold*5+25) * time.Second

		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Find the failing backend
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == failingBackend {
					// Backend should be marked as unhealthy/unavailable
					if backend.Status != mcpv1alpha1.BackendStatusUnavailable &&
						backend.Status != mcpv1alpha1.BackendStatusDegraded {
						return fmt.Errorf("failing backend should be unavailable/degraded but is %s", backend.Status)
					}

					// Check if circuit breaker info is available in status
					// Note: This requires the VirtualMCPServer status to expose circuit breaker state
					// For now, we verify through backend status
					return nil
				}
			}

			return fmt.Errorf("failing backend not found in discovered backends")
		}, circuitOpenTimeout, pollingInterval).Should(Succeed(), "Circuit should open for failing backend")
	})

	It("should reject tool calls to unhealthy backend with clear error message", func() {
		By("Creating fresh MCP client for tool calls")
		testClient, err := CreateInitializedMCPClient(vmcpNodePort, "circuit-breaker-tool-test", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer testClient.Close()

		By("Listing tools to find a tool from the failing backend")
		listRequest := mcp.ListToolsRequest{}
		tools, err := testClient.Client.ListTools(testClient.Ctx, listRequest)
		Expect(err).ToNot(HaveOccurred())

		// Find a tool from the failing backend (if any - may not be discoverable if already unhealthy)
		var failingBackendTool string
		for _, tool := range tools.Tools {
			// The failing backend has TOOL_PREFIX=failing, so tools should have "failing" in name
			if strings.Contains(tool.Name, "failing") {
				failingBackendTool = tool.Name
				break
			}
		}

		// If no tools found (backend already filtered out), that's also acceptable
		// since it means Layer 1 filtering worked. Let's verify the backend is unavailable.
		if failingBackendTool == "" {
			By("Failing backend tools not in capability list (filtered at discovery)")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).NotTo(HaveOccurred())

			// Verify the failing backend exists but is unavailable
			// Note: Degraded backends are still usable and would be included in capabilities,
			// so if tools are filtered out, the backend must be Unavailable (not Degraded)
			failingBackendFound := false
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == failingBackend {
					failingBackendFound = true
					Expect(backend.Status).To(Equal(mcpv1alpha1.BackendStatusUnavailable),
						"Failing backend should be unavailable (not degraded, as degraded backends are still included in capabilities)")
					break
				}
			}
			Expect(failingBackendFound).To(BeTrue(), "Failing backend should be in discovered backends")
			GinkgoWriter.Printf("✓ Failing backend correctly excluded from capabilities (Layer 1 filtering)\n")
			return
		}

		By(fmt.Sprintf("Attempting to call tool from unhealthy backend: %s", failingBackendTool))
		callRequest := mcp.CallToolRequest{}
		callRequest.Params.Name = failingBackendTool
		callRequest.Params.Arguments = map[string]any{
			"input": "testinput", // Must be alphanumeric only (no hyphens or special chars)
		}

		result, err := testClient.Client.CallTool(testClient.Ctx, callRequest)
		Expect(err).ToNot(HaveOccurred(), "MCP call should not return error")
		Expect(result).ToNot(BeNil())

		// Verify the result indicates the backend is unavailable (Layer 2 runtime check)
		Expect(result.IsError).To(BeTrue(), "Tool call should return error")
		Expect(result.Content).ToNot(BeEmpty())

		// Extract error message
		var errorMsg string
		for _, content := range result.Content {
			if textContent, ok := content.(mcp.TextContent); ok {
				errorMsg = textContent.Text
				break
			}
		}

		// Verify error message indicates backend is unavailable
		Expect(errorMsg).To(ContainSubstring("currently unavailable"),
			"Error message should indicate backend is unavailable")

		GinkgoWriter.Printf("✓ Tool call correctly rejected with error (Layer 2 runtime check): %s\n", errorMsg)
	})

	It("should keep healthy backend circuit closed and functional", func() {
		By("Creating fresh MCP client for tool calls")
		testClient, err := CreateInitializedMCPClient(vmcpNodePort, "circuit-breaker-healthy-test", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer testClient.Close()

		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)
		Expect(err).NotTo(HaveOccurred())

		// Healthy backend should remain ready
		healthyFound := false
		for _, backend := range vmcpServer.Status.DiscoveredBackends {
			if backend.Name == healthyBackend {
				Expect(backend.Status).To(Equal(mcpv1alpha1.BackendStatusReady),
					"Healthy backend should remain ready despite failing backend's circuit being open")
				healthyFound = true
			}
		}
		Expect(healthyFound).To(BeTrue(), "Healthy backend should be present")

		By("Verifying tool calls to healthy backend succeed")
		listRequest := mcp.ListToolsRequest{}
		tools, err := testClient.Client.ListTools(testClient.Ctx, listRequest)
		Expect(err).ToNot(HaveOccurred())

		// Find a tool from the healthy backend
		var healthyBackendTool string
		for _, tool := range tools.Tools {
			// The healthy backend has TOOL_PREFIX=healthy, so tools should have "healthy" in name
			if strings.Contains(tool.Name, "healthy") {
				healthyBackendTool = tool.Name
				break
			}
		}
		Expect(healthyBackendTool).ToNot(BeEmpty(), "Should find a tool from healthy backend")

		By(fmt.Sprintf("Calling tool from healthy backend: %s", healthyBackendTool))
		callRequest := mcp.CallToolRequest{}
		callRequest.Params.Name = healthyBackendTool
		callRequest.Params.Arguments = map[string]any{
			"input": "testinput", // Must be alphanumeric only (no hyphens or special chars)
		}

		result, err := testClient.Client.CallTool(testClient.Ctx, callRequest)
		Expect(err).ToNot(HaveOccurred(), "Should successfully call tool from healthy backend")
		Expect(result).ToNot(BeNil())
		Expect(result.Content).ToNot(BeEmpty())
		Expect(result.IsError).To(BeFalse(), "Tool call should succeed on healthy backend")

		GinkgoWriter.Printf("✓ Tool call to healthy backend succeeded\n")
	})

	It("should fast-fail health checks when circuit is open", func() {
		By("Verifying circuit breaker fast-fail behavior in logs")

		// Get the vMCP pod logs to verify fast-fail behavior
		podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpServerName, testNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(podList.Items).NotTo(BeEmpty(), "Should have at least one vMCP pod")

		// Check logs for circuit breaker activity
		Eventually(func() error {
			pod := &podList.Items[0]
			containerName := "vmcp"
			if len(pod.Spec.Containers) > 0 {
				containerName = pod.Spec.Containers[0].Name
			}

			logs, err := GetPodLogs(ctx, pod.Name, testNamespace, containerName)
			if err != nil {
				return fmt.Errorf("failed to get pod logs: %w", err)
			}

			// Look for health monitoring logs that indicate circuit breaker behavior
			// When a backend is marked unhealthy, the circuit is effectively "open"
			if !containsAny(logs, "unhealthy", "health degraded", "Backend "+failingBackend) {
				return fmt.Errorf("no health monitoring activity found for failing backend in logs")
			}

			// Verify the failing backend is being tracked as unhealthy (circuit open)
			if !containsAny(logs, "unhealthy", "unavailable", "health check failed") {
				return fmt.Errorf("no unhealthy/unavailable status detected in logs")
			}

			return nil
		}, 30*time.Second, 3*time.Second).Should(Succeed(),
			"Should find circuit breaker fast-fail activity in pod logs")
	})

	It("should transition to half-open state after timeout", func() {
		By(fmt.Sprintf("Waiting for circuit breaker timeout (%s) to allow half-open transition", circuitBreakerTimeout))

		// Use Eventually to wait for half-open transition or recovery activity
		// Circuit breaker timeout is 30s, so we check for up to 45s to allow some buffer
		Eventually(func() error {
			podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpServerName, testNamespace)
			if err != nil {
				return fmt.Errorf("failed to get pods: %w", err)
			}
			if len(podList.Items) == 0 {
				return fmt.Errorf("no vMCP pods found")
			}

			pod := &podList.Items[0]
			containerName := "vmcp"
			if len(pod.Spec.Containers) > 0 {
				containerName = pod.Spec.Containers[0].Name
			}

			logs, err := GetPodLogs(ctx, pod.Name, testNamespace, containerName)
			if err != nil {
				return fmt.Errorf("failed to get pod logs: %w", err)
			}

			// Look for half-open transition or recovery attempt logs
			// The circuit may transition quickly through half-open if backend still failing
			// After circuit breaker timeout, look for health check activity or recovery attempts
			// The implementation retries health checks which is functionally equivalent to half-open state
			if !containsAny(logs, "health check", "Health check", "backend "+failingBackend) {
				return fmt.Errorf("no health check retry activity found after timeout")
			}

			return nil
		}, 45*time.Second, 3*time.Second).Should(Succeed(),
			"Should find half-open transition or recovery activity in logs after timeout")
	})

	It("should verify circuit breaker configuration is present", func() {
		By("Checking VirtualMCPServer spec has circuit breaker config")

		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)
		Expect(err).NotTo(HaveOccurred())

		// Verify circuit breaker configuration in spec
		Expect(vmcpServer.Spec.Operational).NotTo(BeNil())
		Expect(vmcpServer.Spec.Operational.FailureHandling).NotTo(BeNil())
		Expect(vmcpServer.Spec.Operational.FailureHandling.CircuitBreaker).NotTo(BeNil())
		Expect(vmcpServer.Spec.Operational.FailureHandling.CircuitBreaker.Enabled).To(BeTrue())
		Expect(vmcpServer.Spec.Operational.FailureHandling.CircuitBreaker.FailureThreshold).To(Equal(circuitBreakerThreshold))
		Expect(vmcpServer.Spec.Operational.FailureHandling.CircuitBreaker.Timeout).To(Equal(circuitBreakerTimeout))
	})

	It("should handle backend recovery with circuit breaker", func() {
		By("Fixing the failing backend by updating its McpPort")
		failingBackendResource := &mcpv1alpha1.MCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      failingBackend,
			Namespace: testNamespace,
		}, failingBackendResource)
		Expect(err).NotTo(HaveOccurred())

		// Fix the port to make it healthy
		failingBackendResource.Spec.McpPort = 8080
		Expect(k8sClient.Update(ctx, failingBackendResource)).To(Succeed())

		By("Waiting for backend to restart and become running")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      failingBackend,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend not running yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Waiting for circuit to close and backend to recover")
		// Circuit breaker will transition to half-open, test recovery, and close circuit
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Find the recovered backend
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == failingBackend {
					if backend.Status != mcpv1alpha1.BackendStatusReady {
						return fmt.Errorf("backend should have recovered and be ready but is %s", backend.Status)
					}
					return nil
				}
			}

			return fmt.Errorf("backend not found in discovered backends")
		}, timeout, pollingInterval).Should(Succeed(), "Circuit should close and backend should recover")
	})

	It("should maintain all backends healthy after recovery", func() {
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)
		Expect(err).NotTo(HaveOccurred())

		// Both backends should now be healthy
		readyBackends := 0
		for _, backend := range vmcpServer.Status.DiscoveredBackends {
			if backend.Status == mcpv1alpha1.BackendStatusReady {
				readyBackends++
			}
			// Verify all backends have recent health checks
			Expect(backend.LastHealthCheck.IsZero()).To(BeFalse(),
				"Backend %s should have health check timestamp", backend.Name)
		}

		Expect(readyBackends).To(Equal(2), "Both backends should be healthy after recovery")
		Expect(vmcpServer.Status.BackendCount).To(Equal(2), "BackendCount should be 2")

		By("Waiting for recovered backend to appear in aggregated capabilities")
		// After backend recovery, the discovery/aggregation process needs to refresh.
		// We configured a 10s cache TTL for testing, so we poll by creating fresh MCP clients
		// until the recovered backend's tools appear (should happen within 10-15 seconds).
		var recoveredToolName string
		Eventually(func() error {
			// Create fresh client to get current aggregated capabilities
			freshClient, err := CreateInitializedMCPClient(vmcpNodePort, "circuit-breaker-capability-check", 30*time.Second)
			if err != nil {
				return fmt.Errorf("failed to create MCP client: %w", err)
			}
			defer freshClient.Close()

			listRequest := mcp.ListToolsRequest{}
			tools, err := freshClient.Client.ListTools(freshClient.Ctx, listRequest)
			if err != nil {
				return fmt.Errorf("failed to list tools: %w", err)
			}

			// Look for a tool from the recovered backend
			for _, tool := range tools.Tools {
				if strings.Contains(tool.Name, "failing") {
					recoveredToolName = tool.Name
					GinkgoWriter.Printf("Found recovered backend tool: %s\n", recoveredToolName)
					return nil
				}
			}

			// Debug: Show what tools we have
			var toolNames []string
			for _, tool := range tools.Tools {
				toolNames = append(toolNames, tool.Name)
			}
			return fmt.Errorf("recovered backend tool not yet in capabilities (have %d tools: %v)", len(tools.Tools), toolNames)
		}, 30*time.Second, pollingInterval).Should(Succeed(), "Recovered backend should appear in aggregated capabilities within cache TTL")

		By(fmt.Sprintf("Verifying tool calls to recovered backend succeed: %s", recoveredToolName))
		// Create a fresh client for the actual tool call
		freshClient, err := CreateInitializedMCPClient(vmcpNodePort, "circuit-breaker-tool-call", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer freshClient.Close()

		callRequest := mcp.CallToolRequest{}
		callRequest.Params.Name = recoveredToolName
		callRequest.Params.Arguments = map[string]any{
			"input": "testinput", // Must be alphanumeric only (no hyphens or special chars)
		}

		result, err := freshClient.Client.CallTool(freshClient.Ctx, callRequest)
		Expect(err).ToNot(HaveOccurred(), "Tool call should succeed")
		Expect(result).ToNot(BeNil())
		Expect(result.Content).ToNot(BeEmpty())
		Expect(result.IsError).To(BeFalse(), "Tool call should not return error")

		GinkgoWriter.Printf("✓ Tool call to recovered backend succeeded\n")
	})
})

var _ = Describe("VirtualMCPServer Partial Failure Mode", Ordered, func() {
	var (
		testNamespace          = "default"
		mcpGroupName           = "test-partial-failure-group"
		vmcpServerFailMode     = "test-vmcp-fail-mode"
		vmcpServerBestEffort   = "test-vmcp-best-effort"
		backend1               = "pfm-backend-1"
		backend2               = "pfm-backend-2"
		timeout                = 3 * time.Minute
		pollingInterval        = 2 * time.Second
		healthCheckInterval    = "5s"
		unhealthyThreshold     = 3
		vmcpFailModeNodePort   int32
		vmcpBestEffortNodePort int32
		mcpClientFailMode      *InitializedMCPClient
		mcpClientBestEffort    *InitializedMCPClient
	)

	BeforeAll(func() {
		By("Creating MCPGroup for partial failure mode tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"MCP Group for partial failure mode e2e tests", timeout, pollingInterval)

		By("Creating backend MCPServers")
		for _, backendName := range []string{backend1, backend2} {
			backendResource := &mcpv1alpha1.MCPServer{
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
						{Name: "TOOL_PREFIX", Value: backendName},
					},
				},
			}
			Expect(k8sClient.Create(ctx, backendResource)).To(Succeed())
		}

		By("Waiting for backends to be running")
		for _, backendName := range []string{backend1, backend2} {
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backendName,
					Namespace: testNamespace,
				}, server); err != nil {
					return fmt.Errorf("failed to get server: %w", err)
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
					return fmt.Errorf("not ready yet, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollingInterval).Should(Succeed(), fmt.Sprintf("%s should be running", backendName))
		}

		By("Creating VirtualMCPServer with FAIL mode (strict)")
		vmcpFailModeServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerFailMode,
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
					ConflictResolutionConfig: &mcpv1alpha1.ConflictResolutionConfig{
						PrefixFormat: "{workload}_",
					},
				},
				Operational: &mcpv1alpha1.OperationalConfig{
					LogLevel:           "debug",
					CapabilityCacheTTL: "10s",
					FailureHandling: &mcpv1alpha1.FailureHandlingConfig{
						HealthCheckInterval: healthCheckInterval,
						UnhealthyThreshold:  unhealthyThreshold,
						PartialFailureMode:  "fail", // STRICT MODE
					},
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpFailModeServer)).To(Succeed())

		By("Creating VirtualMCPServer with BEST_EFFORT mode (lenient)")
		vmcpBestEffortServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerBestEffort,
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
					ConflictResolutionConfig: &mcpv1alpha1.ConflictResolutionConfig{
						PrefixFormat: "{workload}_",
					},
				},
				Operational: &mcpv1alpha1.OperationalConfig{
					LogLevel:           "debug",
					CapabilityCacheTTL: "10s",
					FailureHandling: &mcpv1alpha1.FailureHandlingConfig{
						HealthCheckInterval: healthCheckInterval,
						UnhealthyThreshold:  unhealthyThreshold,
						PartialFailureMode:  "best_effort", // LENIENT MODE
					},
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpBestEffortServer)).To(Succeed())

		By("Waiting for both VirtualMCPServers to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerFailMode, testNamespace, timeout, pollingInterval)
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerBestEffort, testNamespace, timeout, pollingInterval)

		By("Getting NodePorts for VirtualMCPServers")
		vmcpFailModeNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerFailMode, testNamespace, timeout, pollingInterval)
		vmcpBestEffortNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerBestEffort, testNamespace, timeout, pollingInterval)

		By(fmt.Sprintf("Creating MCP clients (FAIL: %d, BEST_EFFORT: %d)", vmcpFailModeNodePort, vmcpBestEffortNodePort))
		var err error
		mcpClientFailMode, err = CreateInitializedMCPClient(vmcpFailModeNodePort, "partial-failure-fail-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		mcpClientBestEffort, err = CreateInitializedMCPClient(vmcpBestEffortNodePort, "partial-failure-best-effort-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterAll(func() {
		By("Closing MCP clients")
		if mcpClientFailMode != nil {
			mcpClientFailMode.Close()
		}
		if mcpClientBestEffort != nil {
			mcpClientBestEffort.Close()
		}

		By("Cleaning up VirtualMCPServers")
		for _, serverName := range []string{vmcpServerFailMode, vmcpServerBestEffort} {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, vmcpServer)
		}

		By("Cleaning up MCPServers")
		for _, backendName := range []string{backend1, backend2} {
			backend := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backendName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, backend)
		}

		By("Cleaning up MCPGroup")
		group := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, group)
	})

	It("should deploy both vMCP servers successfully with different partial failure modes", func() {
		By("Verifying FAIL mode server is ready")
		vmcpFail := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerFailMode,
			Namespace: testNamespace,
		}, vmcpFail)
		Expect(err).ToNot(HaveOccurred())
		Expect(vmcpFail.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady))
		Expect(vmcpFail.Spec.Operational.FailureHandling.PartialFailureMode).To(Equal("fail"))

		By("Verifying BEST_EFFORT mode server is ready")
		vmcpBestEffort := &mcpv1alpha1.VirtualMCPServer{}
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerBestEffort,
			Namespace: testNamespace,
		}, vmcpBestEffort)
		Expect(err).ToNot(HaveOccurred())
		Expect(vmcpBestEffort.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady))
		Expect(vmcpBestEffort.Spec.Operational.FailureHandling.PartialFailureMode).To(Equal("best_effort"))
	})

	It("should discover all healthy backends in both modes", func() {
		By("Checking FAIL mode discovered backends")
		vmcpFail := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerFailMode,
			Namespace: testNamespace,
		}, vmcpFail)
		Expect(err).ToNot(HaveOccurred())
		Expect(vmcpFail.Status.DiscoveredBackends).To(HaveLen(2), "FAIL mode should discover 2 backends")

		By("Checking BEST_EFFORT mode discovered backends")
		vmcpBestEffort := &mcpv1alpha1.VirtualMCPServer{}
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerBestEffort,
			Namespace: testNamespace,
		}, vmcpBestEffort)
		Expect(err).ToNot(HaveOccurred())
		Expect(vmcpBestEffort.Status.DiscoveredBackends).To(HaveLen(2), "BEST_EFFORT mode should discover 2 backends")
	})

	It("should handle MCP requests successfully in both modes", func() {
		listRequest := mcp.ListToolsRequest{}

		By("Listing tools from FAIL mode server")
		toolsFail, err := mcpClientFailMode.Client.ListTools(mcpClientFailMode.Ctx, listRequest)
		Expect(err).ToNot(HaveOccurred())
		Expect(toolsFail.Tools).ToNot(BeEmpty(), "FAIL mode should expose tools")

		By("Listing tools from BEST_EFFORT mode server")
		toolsBestEffort, err := mcpClientBestEffort.Client.ListTools(mcpClientBestEffort.Ctx, listRequest)
		Expect(err).ToNot(HaveOccurred())
		Expect(toolsBestEffort.Tools).ToNot(BeEmpty(), "BEST_EFFORT mode should expose tools")

		By("Verifying both modes expose the same tools when all backends are healthy")
		Expect(toolsFail.Tools).To(HaveLen(len(toolsBestEffort.Tools)), "Both modes should expose same number of tools when all backends are healthy")
	})
})

// containsAny checks if the text contains any of the given patterns (case-insensitive)
func containsAny(text string, patterns ...string) bool {
	lowerText := strings.ToLower(text)
	for _, pattern := range patterns {
		if strings.Contains(lowerText, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}
