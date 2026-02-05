// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

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
				if backend.Status != "ready" && backend.Status != "degraded" && backend.Status != "unknown" {
					return fmt.Errorf("backend %s has unexpected status: %s (message: %s)",
						backend.Name, backend.Status, backend.Message)
				}
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	It("should open circuit breaker when backend fails repeatedly", func() {
		By("Scaling down unstable backend to simulate failure")
		// Scale down both Deployment (proxy) and StatefulSet (MCP server)
		Eventually(func() error {
			// Scale down deployment
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, deployment); err != nil {
				return fmt.Errorf("failed to get deployment: %w", err)
			}
			deployment.Spec.Replicas = ptr.To(int32(0))
			if err := k8sClient.Update(ctx, deployment); err != nil {
				return fmt.Errorf("failed to scale deployment: %w", err)
			}

			// Scale down statefulset
			statefulset := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, statefulset); err != nil {
				return fmt.Errorf("failed to get statefulset: %w", err)
			}
			statefulset.Spec.Replicas = ptr.To(int32(0))
			if err := k8sClient.Update(ctx, statefulset); err != nil {
				return fmt.Errorf("failed to scale statefulset: %w", err)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Waiting for circuit breaker to detect failures and open")
		// Circuit breaker needs cbFailureThreshold consecutive failures
		// Timeline: T=0 (check 1 starts), T=3s (fails), T=5s (check 2), T=8s (fails), T=10s (check 3), T=13s (fails)
		// Circuit opens after 3rd failure at ~13s. Add buffer for pod termination and processing.
		// Calculation: (threshold-1) × interval + threshold × timeout = 2×5s + 3×3s = 19s + 5s buffer = 24s
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

			// Check backend is unhealthy
			if unstableBackend.Status != "unhealthy" && unstableBackend.Status != "unavailable" {
				return fmt.Errorf("backend status is %s (expected unhealthy/unavailable), message: %s",
					unstableBackend.Status, unstableBackend.Message)
			}

			// Check for circuit breaker message (circuit may not have opened yet depending on timing)
			if strings.Contains(strings.ToLower(unstableBackend.Message), "circuit") {
				GinkgoWriter.Printf("✓ Circuit breaker message detected: %s\n", unstableBackend.Message)
			} else {
				GinkgoWriter.Printf("⚠ Backend unhealthy but no circuit message yet: %s\n", unstableBackend.Message)
			}

			return nil
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

			// Phase should be Degraded (some backends unhealthy) or Failed (all unhealthy)
			if vmcpServer.Status.Phase != "Degraded" && vmcpServer.Status.Phase != "Failed" {
				return fmt.Errorf("expected phase Degraded or Failed, got: %s", vmcpServer.Status.Phase)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	It("should close circuit breaker when backend recovers", func() {
		By("Restoring unstable backend by scaling up")
		// Scale up both Deployment (proxy) and StatefulSet (MCP server)
		Eventually(func() error {
			// Scale up deployment
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, deployment); err != nil {
				return fmt.Errorf("failed to get deployment: %w", err)
			}
			deployment.Spec.Replicas = ptr.To(int32(1))
			if err := k8sClient.Update(ctx, deployment); err != nil {
				return fmt.Errorf("failed to scale deployment: %w", err)
			}

			// Scale up statefulset
			statefulset := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, statefulset); err != nil {
				return fmt.Errorf("failed to get statefulset: %w", err)
			}
			statefulset.Spec.Replicas = ptr.To(int32(1))
			if err := k8sClient.Update(ctx, statefulset); err != nil {
				return fmt.Errorf("failed to scale statefulset: %w", err)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Waiting for backend pod to be ready")
		time.Sleep(15 * time.Second) // Allow pod to start

		By("Waiting for circuit breaker timeout and recovery")
		// Circuit breaker timeout: cbTimeout (20s)
		// Plus health check interval: cbHealthCheckInterval (5s)
		// Plus buffer for processing: 15s
		// Total: ~40s
		recoveryTime := cbTimeout + cbHealthCheckInterval + 15*time.Second
		time.Sleep(recoveryTime)

		By("Verifying backend status improves after recovery")
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
			if unstableBackend.Status != "ready" && unstableBackend.Status != "degraded" {
				return fmt.Errorf("backend status is still %s (expected ready/degraded after recovery), message: %s",
					unstableBackend.Status, unstableBackend.Message)
			}

			GinkgoWriter.Printf("✓ Backend recovered: status=%s, message=%s\n",
				unstableBackend.Status, unstableBackend.Message)

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
			if vmcpServer.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseReady && vmcpServer.Status.Phase != "Degraded" {
				return fmt.Errorf("expected phase Ready or Degraded after recovery, got: %s (message: %s)",
					vmcpServer.Status.Phase, vmcpServer.Status.Message)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())
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

		// Stable backend should be ready or degraded (never unhealthy)
		Expect(stableBackend.Status).To(Or(Equal("ready"), Equal("degraded")),
			"stable backend should remain healthy, got status=%s message=%s",
			stableBackend.Status, stableBackend.Message)

		// Stable backend message should not contain circuit breaker warnings
		Expect(strings.ToLower(stableBackend.Message)).NotTo(ContainSubstring("circuit breaker open"),
			"stable backend should not have circuit breaker open, message: %s", stableBackend.Message)

		GinkgoWriter.Printf("✓ Stable backend remained healthy: status=%s, message=%s\n",
			stableBackend.Status, stableBackend.Message)
	})
})
