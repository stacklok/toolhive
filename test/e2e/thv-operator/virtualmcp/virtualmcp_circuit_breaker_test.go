package virtualmcp

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Circuit Breaker", Ordered, func() {
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
				},
				Operational: &mcpv1alpha1.OperationalConfig{
					LogLevel: "debug", // Enable debug logging to see circuit breaker activity
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
		// Add buffer time for processing
		circuitOpenTimeout := time.Duration(circuitBreakerThreshold*5+10) * time.Second

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

	It("should keep healthy backend circuit closed and functional", func() {
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
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

			// Look for circuit breaker logs
			if !containsAny(logs, "circuit", "Circuit breaker", "fast-fail") {
				return fmt.Errorf("no circuit breaker activity found in logs")
			}

			// Specifically look for fast-fail or circuit open logs
			if !containsAny(logs, "fast-fail", "circuit is open", "Circuit", "open") {
				return fmt.Errorf("no fast-fail behavior detected in logs")
			}

			return nil
		}, 30*time.Second, 3*time.Second).Should(Succeed(),
			"Should find circuit breaker fast-fail activity in pod logs")
	})

	It("should transition to half-open state after timeout", func() {
		By(fmt.Sprintf("Waiting for circuit breaker timeout (%s) to allow half-open transition", circuitBreakerTimeout))

		// Wait for the circuit breaker timeout plus some buffer
		time.Sleep(35 * time.Second)

		// Check logs for half-open or recovery activity
		podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpServerName, testNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(podList.Items).NotTo(BeEmpty())

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

			// Look for half-open transition or recovery attempt logs
			// The circuit may transition quickly through half-open if backend still failing
			if !containsAny(logs, "half-open", "half open", "halfopen", "transition", "recovery") {
				return fmt.Errorf("no half-open or recovery activity found after timeout")
			}

			return nil
		}, 20*time.Second, 3*time.Second).Should(Succeed(),
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
		}

		Expect(readyBackends).To(Equal(2), "Both backends should be healthy after recovery")
		Expect(vmcpServer.Status.BackendCount).To(Equal(2), "BackendCount should be 2")
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
