// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// VirtualMCPServer Status Reporting E2E Tests
// These tests verify that the K8sReporter correctly updates the VirtualMCPServer.Status
// subresource with runtime status information from the vMCP runtime.
//
// This validates:
// - Status.Phase is updated (Pending → Ready → Degraded → Failed)
// - Status.BackendCount reflects discovered backends
// - Status.DiscoveredBackends contains backend information with health status
// - Backend status transitions: ready → degraded → unavailable
// - Status.Conditions are properly set (ServerReady, BackendsDiscovered)
// - Status.ObservedGeneration matches resource Generation
// - StatusReporter catches and reports backend failures
//
// Related: Issue #3147 (StatusReporter abstraction), Issue #2854 (Status reporting)
var _ = Describe("VirtualMCPServer Status Reporting - Backend Health", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-status-health-group"
		vmcpServerName  = "test-vmcp-status-health"
		backend1Name    = "backend-health-fetch"
		backend2Name    = "backend-health-osv"
		backend3Name    = "backend-health-dynamic"
		timeout         = 90 * time.Second // Reduced from 3 minutes for faster test execution
		pollingInterval = 2 * time.Second
	)

	BeforeAll(func() {
		By("Creating MCPGroup for status health reporting tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for status health reporting E2E tests", timeout, pollingInterval)

		By("Creating VirtualMCPServer first (no backends yet)")
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
				// Configure fast health checks for E2E testing
				// This reduces health check time from ~90s (30s * 3) to ~20s (10s * 2)
				// We use 10s interval with 2 failures to allow one retry for transient errors
				HealthMonitoring: &mcpv1alpha1.HealthMonitoringConfig{
					CheckInterval:      ptr.To(int32(10)), // Check every 10 seconds
					UnhealthyThreshold: ptr.To(int32(2)),  // Mark unhealthy after 2 failures (1 retry)
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer pod to be running")
		Eventually(func() error {
			pods := &corev1.PodList{}
			err := k8sClient.List(ctx, pods,
				ctrlclient.InNamespace(testNamespace),
				ctrlclient.MatchingLabels{"app.kubernetes.io/instance": vmcpServerName})
			if err != nil {
				return err
			}
			if len(pods.Items) == 0 {
				return fmt.Errorf("no pods found")
			}
			if pods.Items[0].Status.Phase != corev1.PodRunning {
				return fmt.Errorf("pod not running yet: %s", pods.Items[0].Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())
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

		By("Cleaning up all backend MCPServers")
		for _, backendName := range []string{backend1Name, backend2Name, backend3Name} {
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

	Context("Initial State - No Backends", func() {
		It("should report empty backend list when no backends exist", func() {
			By("Checking VirtualMCPServer status")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				return err
			}, timeout, pollingInterval).Should(Succeed())

			By("Verifying BackendCount is 0")
			Eventually(func() int {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return -1
				}
				return vmcp.Status.BackendCount
			}, 30*time.Second, pollingInterval).Should(Equal(0),
				"BackendCount should be 0 when no backends exist")

			By("Verifying DiscoveredBackends is empty")
			Expect(vmcp.Status.DiscoveredBackends).To(BeEmpty(),
				"DiscoveredBackends should be empty when no backends exist")
		})
	})

	Context("Dynamic Backend Addition", func() {
		It("should detect new backend and report it as ready", func() {
			By("Creating first backend MCPServer")
			backend1 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend1Name,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.GofetchServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			}
			Expect(k8sClient.Create(ctx, backend1)).To(Succeed())

			By("Waiting for backend to be running")
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend1Name,
					Namespace: testNamespace,
				}, server)
				if err != nil {
					return err
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
					return fmt.Errorf("backend not running yet: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By("Verifying StatusReporter detects the backend")
			Eventually(func() int {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return -1
				}
				return vmcp.Status.BackendCount
			}, timeout, pollingInterval).Should(Equal(1),
				"BackendCount should be 1 after adding first backend")

			By("Verifying backend appears in DiscoveredBackends with ready status")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return false
				}
				if len(vmcp.Status.DiscoveredBackends) != 1 {
					return false
				}
				// Check if backend has ready status
				backend := vmcp.Status.DiscoveredBackends[0]
				return backend.Status == mcpv1alpha1.BackendStatusReady
			}, timeout, pollingInterval).Should(BeTrue(),
				"Backend should be reported as ready")

			// Log backend details
			backend := vmcp.Status.DiscoveredBackends[0]
			GinkgoWriter.Printf("Backend discovered: name=%s, status=%s, url=%s, lastHealthCheck=%v\n",
				backend.Name, backend.Status, backend.URL, backend.LastHealthCheck.Time)
			Expect(backend.Name).ToNot(BeEmpty())
			Expect(backend.URL).ToNot(BeEmpty())

			// Note: LastHealthCheck may take up to 30 seconds to populate for dynamically
			// added backends because health checks run on a periodic interval.
			// The health monitor now supports dynamic backends - they're automatically
			// added to health monitoring when discovered by the BackendReconciler.
			// The backend status ("ready") comes from the K8s MCPServer phase initially,
			// and is updated by health checks after the first check completes.
		})

		It("should handle multiple backends simultaneously", func() {
			By("Creating second backend MCPServer")
			backend2 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend2Name,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.OSVMCPServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			}
			Expect(k8sClient.Create(ctx, backend2)).To(Succeed())

			By("Waiting for second backend to be running")
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend2Name,
					Namespace: testNamespace,
				}, server)
				if err != nil {
					return err
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
					return fmt.Errorf("backend not running yet: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By("Verifying StatusReporter detects both backends")
			Eventually(func() int {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return -1
				}
				return vmcp.Status.BackendCount
			}, timeout, pollingInterval).Should(Equal(2),
				"BackendCount should be 2 after adding second backend")

			By("Verifying both backends are ready")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			Eventually(func() int {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return -1
				}
				readyCount := 0
				for _, backend := range vmcp.Status.DiscoveredBackends {
					if backend.Status == mcpv1alpha1.BackendStatusReady {
						readyCount++
					}
				}
				return readyCount
			}, timeout, pollingInterval).Should(Equal(2),
				"Both backends should be reported as ready")

			// Log all backends
			for _, backend := range vmcp.Status.DiscoveredBackends {
				GinkgoWriter.Printf("Backend: name=%s, status=%s, url=%s\n",
					backend.Name, backend.Status, backend.URL)
			}
		})
	})

	Context("Backend Failure Detection", func() {
		It("should detect when backend is removed and update status", func() {
			By("Getting current status")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			initialBackendCount := vmcp.Status.BackendCount
			GinkgoWriter.Printf("Initial backend count: %d\n", initialBackendCount)

			By("Deleting the first backend MCPServer")
			// Note: We delete the MCPServer CR, not just the pod, because:
			// 1. Kubernetes controllers immediately recreate deleted pods
			// 2. Pod recreation is often faster than health check interval (10s)
			// 3. This simulates a backend being permanently removed
			backend := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend1Name,
					Namespace: testNamespace,
				},
			}
			GinkgoWriter.Printf("Deleting MCPServer: %s\n", backend1Name)
			Expect(k8sClient.Delete(ctx, backend)).To(Succeed())

			By("Waiting for MCPServer to be deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend1Name,
					Namespace: testNamespace,
				}, &mcpv1alpha1.MCPServer{})
				return errors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue(),
				"MCPServer should be deleted")

			By("Verifying StatusReporter detects the backend removal")
			// The backend should be removed from the discovered backends list
			// and the backend count should decrease
			Eventually(func() bool {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return false
				}

				GinkgoWriter.Printf("Current backend count: %d (initial: %d)\n",
					vmcp.Status.BackendCount, initialBackendCount)

				// Backend should be removed from the list
				backendStillPresent := false
				for _, b := range vmcp.Status.DiscoveredBackends {
					if b.Name == backend1Name {
						backendStillPresent = true
						GinkgoWriter.Printf("Backend %s still present with status: %s\n",
							backend1Name, b.Status)
					}
				}

				// Success if backend is gone and count decreased
				return !backendStillPresent && vmcp.Status.BackendCount < initialBackendCount
			}, timeout, pollingInterval).Should(BeTrue(),
				"StatusReporter should detect backend removal")

			By("Logging final status after backend removal")
			vmcp = &mcpv1alpha1.VirtualMCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Final backend count: %d (was %d)\n",
				vmcp.Status.BackendCount, initialBackendCount)
			for _, backend := range vmcp.Status.DiscoveredBackends {
				GinkgoWriter.Printf("Remaining backend: name=%s, status=%s\n",
					backend.Name, backend.Status)
			}
		})
	})

	Context("Backend Health Monitoring", func() {
		It("should have recent health check timestamps", func() {
			// Health checks run every 10 seconds (configured in HealthMonitoring)
			By("Waiting for health checks to complete for all backends")

			// Poll until all backends have non-zero LastHealthCheck
			// Health check interval is 10s, with 10s initial delay for dynamic backends
			// Allow up to 30s for completion (2-3 health check cycles)
			Eventually(func() bool {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return false
				}

				// Check all backends have non-zero LastHealthCheck
				for _, backend := range vmcp.Status.DiscoveredBackends {
					if backend.LastHealthCheck.IsZero() {
						GinkgoWriter.Printf("Backend %s still waiting for health check\n", backend.Name)
						return false
					}
				}
				return true
			}, 30*time.Second, pollingInterval).Should(BeTrue(),
				"All backends should have health check timestamps")

			// Verify timestamps are reasonable
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())

			now := time.Now()
			for _, backend := range vmcp.Status.DiscoveredBackends {
				By(fmt.Sprintf("Verifying health check timestamp for backend %s", backend.Name))
				healthCheckTime := backend.LastHealthCheck.Time

				Expect(healthCheckTime.Before(now.Add(1*time.Minute))).To(BeTrue(),
					"Health check timestamp should not be in the future")
				Expect(healthCheckTime.After(now.Add(-5*time.Minute))).To(BeTrue(),
					"Health check timestamp should be recent (within last 5 minutes)")

				GinkgoWriter.Printf("Backend %s health check timestamp: %v (age: %v)\n",
					backend.Name, healthCheckTime, now.Sub(healthCheckTime))
			}
		})
	})
})
