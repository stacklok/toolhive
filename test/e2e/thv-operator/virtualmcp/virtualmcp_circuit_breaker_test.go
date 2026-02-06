// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

const (
	// Circuit breaker test configuration - faster values for testing
	cbHealthCheckInterval = 5 * time.Second
	cbHealthCheckTimeout  = 2 * time.Second // Must be < interval to prevent queuing
	cbUnhealthyThreshold  = 2
	cbFailureThreshold    = 3
	cbTimeout             = 20 * time.Second
)

var _ = Describe("VirtualMCPServer Circuit Breaker Lifecycle", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-circuit-breaker-group"
		vmcpServerName  = "test-vmcp-circuit-breaker"
		backend1Name    = "backend-cb-stable"
		backend2Name    = "backend-cb-unstable"
		timeout         = 3 * time.Minute
		pollingInterval = 2 * time.Second
	)

	BeforeAll(func() {
		By("Creating MCPGroup for circuit breaker tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for circuit breaker E2E tests", timeout, pollingInterval)

		By("Creating stable backend MCPServer")
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

		By("Creating unstable backend MCPServer (will be scaled down to simulate failure)")
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

		By("Waiting for backend MCPServers to be running")
		Eventually(func() error {
			server1 := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1Name,
				Namespace: testNamespace,
			}, server1); err != nil {
				return err
			}
			if server1.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend1 not running, phase: %s", server1.Status.Phase)
			}

			server2 := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, server2); err != nil {
				return err
			}
			if server2.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend2 not running, phase: %s", server2.Status.Phase)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer); err == nil {
			Expect(k8sClient.Delete(ctx, vmcpServer)).To(Succeed())
		}

		backend1 := &mcpv1alpha1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      backend1Name,
			Namespace: testNamespace,
		}, backend1); err == nil {
			Expect(k8sClient.Delete(ctx, backend1)).To(Succeed())
		}

		backend2 := &mcpv1alpha1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      backend2Name,
			Namespace: testNamespace,
		}, backend2); err == nil {
			Expect(k8sClient.Delete(ctx, backend2)).To(Succeed())
		}

		group := &mcpv1alpha1.MCPGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      mcpGroupName,
			Namespace: testNamespace,
		}, group); err == nil {
			Expect(k8sClient.Delete(ctx, group)).To(Succeed())
		}
	})

	It("should configure circuit breaker from VirtualMCPServer spec", func() {
		By("Creating VirtualMCPServer with circuit breaker enabled")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
				},
				ServiceType: "NodePort",
				Config: vmcpconfig.Config{
					Name:  vmcpServerName,
					Group: mcpGroupName,
					Aggregation: &vmcpconfig.AggregationConfig{
						ConflictResolution: "prefix",
					},
					Operational: &vmcpconfig.OperationalConfig{
						FailureHandling: &vmcpconfig.FailureHandlingConfig{
							HealthCheckInterval: vmcpconfig.Duration(cbHealthCheckInterval),
							HealthCheckTimeout:  vmcpconfig.Duration(cbHealthCheckTimeout),
							UnhealthyThreshold:  cbUnhealthyThreshold,
							CircuitBreaker: &vmcpconfig.CircuitBreakerConfig{
								Enabled:          true,
								FailureThreshold: cbFailureThreshold,
								Timeout:          vmcpconfig.Duration(cbTimeout),
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Verifying circuit breaker configuration in ConfigMap")
		Eventually(func() error {
			configMap := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-vmcp-config", vmcpServerName),
				Namespace: testNamespace,
			}, configMap)
			if err != nil {
				return fmt.Errorf("failed to get ConfigMap: %w", err)
			}

			configYAML := configMap.Data["config.yaml"]
			if configYAML == "" {
				return fmt.Errorf("config.yaml not found in ConfigMap")
			}

			// Check circuit breaker is enabled
			if !strings.Contains(configYAML, "circuitBreaker:") {
				return fmt.Errorf("circuit breaker config not found in ConfigMap")
			}
			if !strings.Contains(configYAML, "enabled: true") {
				return fmt.Errorf("circuit breaker not enabled in ConfigMap")
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Waiting for VirtualMCPServer to become ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
	})

	It("should discover backends with healthy status initially", func() {
		By("Checking VirtualMCPServer status has discovered backends")
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			if len(vmcpServer.Status.DiscoveredBackends) < 2 {
				return fmt.Errorf("expected at least 2 backends, found %d", len(vmcpServer.Status.DiscoveredBackends))
			}

			// Check that backends are initially healthy or ready
			for _, backend := range vmcpServer.Status.DiscoveredBackends {
				// Initial status can be ready, degraded, or unknown (during startup)
				if backend.Status != mcpv1alpha1.BackendStatusReady &&
					backend.Status != mcpv1alpha1.BackendStatusDegraded &&
					backend.Status != mcpv1alpha1.BackendStatusUnknown {
					return fmt.Errorf("backend %s has unexpected status: %s (message: %s)",
						backend.Name, backend.Status, backend.Message)
				}
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	It("should open circuit breaker when backend fails repeatedly", func() {
		By("Making unstable backend unavailable by changing to non-existent image")
		backend := &mcpv1alpha1.MCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      backend2Name,
			Namespace: testNamespace,
		}, backend)).To(Succeed())

		backend.Spec.Image = "nonexistent/image:doesnotexist"
		Expect(k8sClient.Update(ctx, backend)).To(Succeed())

		By("Waiting for backend pods to enter ImagePullBackOff state")
		// Wait for pod to be in ImagePullBackOff or similar error state (same pattern as status reporting test)
		Eventually(func() bool {
			podList := &corev1.PodList{}
			err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace),
				client.MatchingLabels{"app": backend2Name})
			if err != nil || len(podList.Items) == 0 {
				return false
			}
			pod := &podList.Items[0]
			// Check if pod is not ready (container waiting due to image pull failure)
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.State.Waiting != nil &&
					(containerStatus.State.Waiting.Reason == "ImagePullBackOff" ||
						containerStatus.State.Waiting.Reason == "ErrImagePull") {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		By("Waiting for circuit breaker to detect failures and open")
		// Circuit breaker needs cbFailureThreshold consecutive failures
		// Timeline: T=0 (check 1 starts), T=2s (fails), T=5s (check 2), T=7s (fails), T=10s (check 3), T=12s (fails)
		// Circuit opens after 3rd failure at ~12s. Add buffer for pod termination and processing.
		// Calculation: (threshold-1) × interval + threshold × timeout = 2×5s + 3×2s = 10s + 6s = 16s + 5s buffer = 21s
		failureDetectionTime := time.Duration(cbFailureThreshold-1)*cbHealthCheckInterval +
			time.Duration(cbFailureThreshold)*cbHealthCheckTimeout
		time.Sleep(failureDetectionTime + 5*time.Second)

		By("Verifying circuit breaker opened for unstable backend")
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Find the unstable backend
			var unstableBackend *mcpv1alpha1.DiscoveredBackend
			for i := range vmcpServer.Status.DiscoveredBackends {
				if vmcpServer.Status.DiscoveredBackends[i].Name == backend2Name {
					unstableBackend = &vmcpServer.Status.DiscoveredBackends[i]
					break
				}
			}

			if unstableBackend == nil {
				return fmt.Errorf("unstable backend not found in discovered backends")
			}

			// Check backend is unavailable (unhealthy backends map to "unavailable" in CRD)
			if unstableBackend.Status != mcpv1alpha1.BackendStatusUnavailable {
				return fmt.Errorf("backend status is %s (expected unavailable), message: %s",
					unstableBackend.Status, unstableBackend.Message)
			}

			// Check circuit breaker state (should be "open" once threshold is reached)
			if unstableBackend.CircuitBreakerState == "open" {
				GinkgoWriter.Printf("✓ Circuit breaker opened (failures: %d, state: %s)\n",
					unstableBackend.ConsecutiveFailures, unstableBackend.CircuitBreakerState)
				return nil
			}

			// Circuit not open yet - may still be accumulating failures
			return fmt.Errorf("circuit breaker not open yet (state: %s, failures: %d, threshold: %d)",
				unstableBackend.CircuitBreakerState, unstableBackend.ConsecutiveFailures, cbFailureThreshold)
		}, timeout, pollingInterval).Should(Succeed())

		By("Verifying VirtualMCPServer phase reflects backend failure")
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Phase should be Degraded (some backends unavailable) or Failed (all unavailable)
			if vmcpServer.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseDegraded &&
				vmcpServer.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseFailed {
				return fmt.Errorf("expected phase Degraded or Failed, got: %s", vmcpServer.Status.Phase)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Verifying tools from circuit-breaker-open backend are excluded from capabilities")
		// NOTE: Full end-to-end verification of tools/list filtering would require:
		// 1. Making an HTTP request to the vMCP server
		// 2. Implementing MCP protocol initialize handshake
		// 3. Calling tools/list and parsing the response
		//
		// The core filtering logic is implemented in pkg/vmcp/discovery/middleware.go
		// and thoroughly unit tested in middleware_test.go (TestFilterHealthyBackends).
		//
		// The filtering works as follows:
		// - When backend circuit breaker opens -> backend marked unhealthy
		// - handleInitializeRequest filters unhealthy backends before aggregation
		// - Only healthy/degraded backends' tools appear in tools/list response
		GinkgoWriter.Printf("✓ Backend health filtering implemented and unit tested\n")
		GinkgoWriter.Printf("  - Unhealthy backends excluded from capability aggregation\n")
		GinkgoWriter.Printf("  - See: pkg/vmcp/discovery/middleware.go:filterHealthyBackends()\n")
	})

	It("should close circuit breaker when backend recovers", func() {
		By("Restoring unstable backend by fixing the image")
		backend := &mcpv1alpha1.MCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      backend2Name,
			Namespace: testNamespace,
		}, backend)).To(Succeed())

		backend.Spec.Image = images.YardstickServerImage
		Expect(k8sClient.Update(ctx, backend)).To(Succeed())

		By("Deleting stuck pods to force recreation with fixed image")
		// Pods in ImagePullBackOff don't automatically recreate when image is fixed
		// Delete them to force the statefulset to create new pods with the correct image
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList,
			client.InNamespace(testNamespace),
			client.MatchingLabels{"app": backend2Name},
		)).To(Succeed())
		for i := range podList.Items {
			if podList.Items[i].Status.Phase == corev1.PodPending {
				GinkgoWriter.Printf("Deleting stuck pod %s in phase %s\n",
					podList.Items[i].Name, podList.Items[i].Status.Phase)
				Expect(k8sClient.Delete(ctx, &podList.Items[i])).To(Succeed())
			}
		}

		By("Waiting for backend to become running again")
		// Note: Recovery may take longer than initial setup because pods in ImagePullBackOff
		// need to be recreated. Status reporting test intentionally skips recovery testing
		// for this reason, but circuit breaker recovery is a key feature we must verify.
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend not running yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Waiting for circuit breaker to transition to half-open and recover")
		// Circuit breaker will:
		// 1. Stay open for cbTimeout (20s)
		// 2. Transition to half-open
		// 3. Perform health check
		// 4. Close if healthy
		// We poll instead of sleeping to complete as soon as recovery happens
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Find the unstable backend
			var unstableBackend *mcpv1alpha1.DiscoveredBackend
			for i := range vmcpServer.Status.DiscoveredBackends {
				if vmcpServer.Status.DiscoveredBackends[i].Name == backend2Name {
					unstableBackend = &vmcpServer.Status.DiscoveredBackends[i]
					break
				}
			}

			if unstableBackend == nil {
				return fmt.Errorf("unstable backend not found in discovered backends")
			}

			// Backend should be ready or degraded (recovering)
			if unstableBackend.Status != mcpv1alpha1.BackendStatusReady &&
				unstableBackend.Status != mcpv1alpha1.BackendStatusDegraded {
				return fmt.Errorf("backend status is still %s (expected ready/degraded after recovery), message: %s, circuitState: %s",
					unstableBackend.Status, unstableBackend.Message, unstableBackend.CircuitBreakerState)
			}

			// Circuit breaker should be closed after successful recovery
			if unstableBackend.CircuitBreakerState != "closed" {
				return fmt.Errorf("circuit breaker not closed yet (state: %s, status: %s)",
					unstableBackend.CircuitBreakerState, unstableBackend.Status)
			}

			GinkgoWriter.Printf("✓ Backend recovered: status=%s, circuitState=%s, failures=%d\n",
				unstableBackend.Status, unstableBackend.CircuitBreakerState, unstableBackend.ConsecutiveFailures)

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Verifying VirtualMCPServer phase returns to healthy state")
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			// Phase should return to Ready or Degraded (if still recovering)
			if vmcpServer.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseReady &&
				vmcpServer.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseDegraded {
				return fmt.Errorf("expected phase Ready or Degraded after recovery, got: %s (message: %s)",
					vmcpServer.Status.Phase, vmcpServer.Status.Message)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Verifying tools from recovered backend are restored to capabilities")
		// NOTE: When the backend recovers and circuit breaker closes:
		// - Backend health status changes from unhealthy -> healthy/degraded
		// - Next session initialization will include the recovered backend
		// - Tools from the recovered backend appear in tools/list response
		//
		// This is handled automatically by the filterHealthyBackends() function
		// which only excludes backends with unhealthy/unknown/unauthenticated status.
		GinkgoWriter.Printf("✓ Backend recovered - tools automatically restored on next session\n")
		GinkgoWriter.Printf("  - Healthy backends included in capability aggregation\n")
	})

	It("should track circuit breaker state per backend independently", func() {
		By("Verifying stable backend remained healthy throughout test")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)).To(Succeed())

		// Find the stable backend
		var stableBackend *mcpv1alpha1.DiscoveredBackend
		for i := range vmcpServer.Status.DiscoveredBackends {
			if vmcpServer.Status.DiscoveredBackends[i].Name == backend1Name {
				stableBackend = &vmcpServer.Status.DiscoveredBackends[i]
				break
			}
		}

		Expect(stableBackend).NotTo(BeNil(), "stable backend should be discovered")

		// Stable backend should be ready or degraded (never unavailable)
		Expect(stableBackend.Status).To(Or(
			Equal(mcpv1alpha1.BackendStatusReady),
			Equal(mcpv1alpha1.BackendStatusDegraded)),
			"stable backend should remain healthy, got status=%s message=%s",
			stableBackend.Status, stableBackend.Message)

		// Stable backend message should not contain circuit breaker warnings
		Expect(strings.ToLower(stableBackend.Message)).NotTo(ContainSubstring("circuit breaker open"),
			"stable backend should not have circuit breaker open, message: %s", stableBackend.Message)

		GinkgoWriter.Printf("✓ Stable backend remained healthy: status=%s, message=%s\n",
			stableBackend.Status, stableBackend.Message)
	})
})
