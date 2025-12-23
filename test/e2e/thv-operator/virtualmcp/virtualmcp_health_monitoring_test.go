package virtualmcp

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Health Monitoring", Ordered, func() {
	var (
		testNamespace               = "default"
		mcpGroupName                = "test-health-group"
		vmcpServerName              = "test-vmcp-health"
		healthyBackend1             = "healthy-backend-1"
		healthyBackend2             = "healthy-backend-2"
		unhealthyBackend            = "unhealthy-backend"
		timeout                     = 3 * time.Minute
		pollingInterval             = 2 * time.Second
		healthCheckInterval         = "5s"             // Fast checks for e2e
		unhealthyThreshold          = 2                // Mark unhealthy after 2 consecutive failures
		healthCheckStabilizeTimeout = 30 * time.Second // Time for health checks to stabilize and detect failures
	)

	BeforeAll(func() {
		By("Creating MCPGroup for health monitoring test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"MCP Group for health monitoring e2e tests", timeout, pollingInterval)

		By("Creating healthy backend MCPServers")
		// Create two healthy backends using yardstick image
		for i, backendName := range []string{healthyBackend1, healthyBackend2} {
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
						{Name: "TOOL_PREFIX", Value: fmt.Sprintf("backend%d", i+1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, backend)).To(Succeed())
		}

		By("Creating unhealthy backend MCPServer (will fail to start)")
		// Create a backend that will fail health checks by using an invalid port
		unhealthyBackendResource := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      unhealthyBackend,
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
					{Name: "TOOL_PREFIX", Value: "unhealthy"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, unhealthyBackendResource)).To(Succeed())

		By("Waiting for healthy backends to be running")
		Eventually(func() error {
			for _, backendName := range []string{healthyBackend1, healthyBackend2} {
				server := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backendName,
					Namespace: testNamespace,
				}, server); err != nil {
					return fmt.Errorf("%s: failed to get server: %w", backendName, err)
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
					return fmt.Errorf("%s not ready yet, phase: %s", backendName, server.Status.Phase)
				}
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed(), "Healthy backends should be running")

		By("Creating VirtualMCPServer with health monitoring enabled")
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
					FailureHandling: &mcpv1alpha1.FailureHandlingConfig{
						HealthCheckInterval: healthCheckInterval,
						UnhealthyThreshold:  unhealthyThreshold,
						PartialFailureMode:  "best_effort", // Continue even with unhealthy backends
					},
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be deployed")
		// Use WaitForVirtualMCPServerDeployed instead of WaitForVirtualMCPServerReady
		// because we intentionally have an unhealthy backend, so Ready will be False
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
		for _, backendName := range []string{healthyBackend1, healthyBackend2, unhealthyBackend} {
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

	It("should discover all backends including the unhealthy one", func() {
		// Wait for health checks to complete and update backend status
		// Health monitoring runs every 5s and marks backends unhealthy after 2 consecutive failures
		// So we need to wait at least 10-15 seconds for the unhealthy backend to be detected
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Should discover all 3 backends
			if len(vmcpServer.Status.DiscoveredBackends) != 3 {
				return fmt.Errorf("expected 3 discovered backends, got %d", len(vmcpServer.Status.DiscoveredBackends))
			}

			// BackendCount should be 2 (only ready backends)
			if vmcpServer.Status.BackendCount != 2 {
				return fmt.Errorf("expected BackendCount=2 (only ready backends), got %d", vmcpServer.Status.BackendCount)
			}

			// Verify unhealthy backend is marked as unavailable/degraded
			unhealthyFound := false
			healthyCount := 0
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == unhealthyBackend {
					if backend.Status != mcpv1alpha1.BackendStatusUnavailable &&
						backend.Status != mcpv1alpha1.BackendStatusDegraded {
						return fmt.Errorf("unhealthy backend %s should be unavailable/degraded but is %s",
							backend.Name, backend.Status)
					}
					unhealthyFound = true
				} else {
					if backend.Status != mcpv1alpha1.BackendStatusReady {
						return fmt.Errorf("healthy backend %s should be ready but is %s",
							backend.Name, backend.Status)
					}
					healthyCount++
				}
			}

			if !unhealthyFound {
				return fmt.Errorf("unhealthy backend not found in discovered backends")
			}

			if healthyCount != 2 {
				return fmt.Errorf("expected 2 healthy backends, found %d", healthyCount)
			}

			return nil
		}, healthCheckStabilizeTimeout, pollingInterval).Should(Succeed(), "Health checks should mark unhealthy backend as unavailable")

		// Verify all backends are present in discovery
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)
		Expect(err).NotTo(HaveOccurred())
		Expect(vmcpServer.Status.DiscoveredBackends).To(HaveLen(3))

		// Check that all backends are present in discovery
		backendNames := make(map[string]bool)
		for _, backend := range vmcpServer.Status.DiscoveredBackends {
			backendNames[backend.Name] = true
		}
		Expect(backendNames).To(HaveKey(healthyBackend1))
		Expect(backendNames).To(HaveKey(healthyBackend2))
		Expect(backendNames).To(HaveKey(unhealthyBackend))
	})

	It("should report healthy status for working backends", func() {
		// Eventually will poll until health checks stabilize
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Find healthy backends and verify their status
			healthyBackends := 0
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == healthyBackend1 || backend.Name == healthyBackend2 {
					if backend.Status != mcpv1alpha1.BackendStatusReady {
						return fmt.Errorf("backend %s should be ready but is %s", backend.Name, backend.Status)
					}
					// Verify LastHealthCheck is recent
					if backend.LastHealthCheck.IsZero() {
						return fmt.Errorf("backend %s has no health check timestamp", backend.Name)
					}
					healthyBackends++
				}
			}

			if healthyBackends != 2 {
				return fmt.Errorf("expected 2 healthy backends, found %d", healthyBackends)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed(), "Healthy backends should report ready status")
	})

	It("should report unhealthy status for failing backend", func() {
		// Eventually will poll until the unhealthy threshold is exceeded
		By(fmt.Sprintf("Waiting for unhealthy backend to fail %d consecutive health checks", unhealthyThreshold))

		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Find unhealthy backend and verify its status
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == unhealthyBackend {
					if backend.Status != mcpv1alpha1.BackendStatusUnavailable &&
						backend.Status != mcpv1alpha1.BackendStatusDegraded {
						return fmt.Errorf("backend %s should be unavailable/degraded but is %s", backend.Name, backend.Status)
					}
					// Verify LastHealthCheck is recent
					if backend.LastHealthCheck.IsZero() {
						return fmt.Errorf("backend %s has no health check timestamp", backend.Name)
					}
					return nil
				}
			}

			return fmt.Errorf("unhealthy backend %s not found in discovered backends", unhealthyBackend)
		}, timeout, pollingInterval).Should(Succeed(), "Unhealthy backend should report unavailable/degraded status")
	})

	It("should update health check timestamps periodically", func() {
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)
		Expect(err).NotTo(HaveOccurred())

		// Record initial timestamps for healthy backends
		initialTimestamps := make(map[string]time.Time)
		for _, backend := range vmcpServer.Status.DiscoveredBackends {
			if backend.Name == healthyBackend1 || backend.Name == healthyBackend2 {
				initialTimestamps[backend.Name] = backend.LastHealthCheck.Time
			}
		}

		// Eventually will poll until timestamps are updated (after at least 2 health check intervals)
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == healthyBackend1 || backend.Name == healthyBackend2 {
					initialTime, exists := initialTimestamps[backend.Name]
					if !exists {
						return fmt.Errorf("no initial timestamp for backend %s", backend.Name)
					}
					if !backend.LastHealthCheck.After(initialTime) {
						return fmt.Errorf("backend %s health check timestamp not updated (initial: %v, current: %v)",
							backend.Name, initialTime, backend.LastHealthCheck.Time)
					}
				}
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed(), "Health check timestamps should be updated periodically")
	})

	It("should handle backend recovery gracefully", func() {
		By("Fixing the unhealthy backend by updating its McpPort")
		unhealthyBackendResource := &mcpv1alpha1.MCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      unhealthyBackend,
			Namespace: testNamespace,
		}, unhealthyBackendResource)
		Expect(err).NotTo(HaveOccurred())

		// Fix the port to make it healthy
		unhealthyBackendResource.Spec.McpPort = 8080
		Expect(k8sClient.Update(ctx, unhealthyBackendResource)).To(Succeed())

		By("Waiting for backend to restart and become running")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      unhealthyBackend,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend not running yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Waiting for health monitoring to detect recovery")
		// Eventually will poll until health monitoring detects the recovery
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Find the recovered backend and verify it's now healthy
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				if backend.Name == unhealthyBackend {
					if backend.Status != mcpv1alpha1.BackendStatusReady {
						return fmt.Errorf("backend %s should have recovered and be ready but is %s",
							backend.Name, backend.Status)
					}
					return nil
				}
			}

			return fmt.Errorf("backend %s not found in discovered backends", unhealthyBackend)
		}, timeout, pollingInterval).Should(Succeed(), "Backend should recover and report healthy status")
	})

	It("should maintain health status for all backends after recovery", func() {
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)
		Expect(err).NotTo(HaveOccurred())

		// All backends should now be healthy
		readyBackends := 0
		for _, backend := range vmcpServer.Status.DiscoveredBackends {
			if backend.Status == mcpv1alpha1.BackendStatusReady {
				readyBackends++
			}
			// Verify all have recent health checks
			Expect(backend.LastHealthCheck.IsZero()).To(BeFalse(),
				"Backend %s should have health check timestamp", backend.Name)
		}

		Expect(readyBackends).To(Equal(3), "All 3 backends should be healthy after recovery")
	})
})
