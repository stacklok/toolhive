// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
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
	// Test configuration constants for fast failure detection in E2E tests
	testHealthCheckInterval     = 5 * time.Second
	testStatusReportingInterval = 5 * time.Second
	testUnhealthyThreshold      = 1
)

var _ = Describe("VirtualMCPServer Status Reporting", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-status-reporting-group"
		vmcpServerName  = "test-vmcp-status"
		backend1Name    = "backend-status-1"
		backend2Name    = "backend-status-2"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
	)

	BeforeAll(func() {
		By("Creating MCPGroup for status reporting tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for status reporting E2E tests", timeout, pollingInterval)

		By("Creating backend MCPServers")
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

		By("Creating VirtualMCPServer with discovered mode and fast health checks")
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
					Name:  "test-vmcp-status",
					Group: mcpGroupName,
					Operational: &vmcpconfig.OperationalConfig{
						FailureHandling: &vmcpconfig.FailureHandlingConfig{
							HealthCheckInterval:     vmcpconfig.Duration(testHealthCheckInterval),
							StatusReportingInterval: vmcpconfig.Duration(testStatusReportingInterval),
							UnhealthyThreshold:      testUnhealthyThreshold,
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to reach Ready phase")
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			// Check Phase
			if server.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseReady {
				return fmt.Errorf("phase is %s, want Ready", server.Status.Phase)
			}

			// Check Ready condition
			readyCondition := false
			for _, cond := range server.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
					readyCondition = true
					break
				}
			}
			if !readyCondition {
				return fmt.Errorf("Ready condition not found or not True")
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

		mcpGroup := &mcpv1alpha1.MCPGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      mcpGroupName,
			Namespace: testNamespace,
		}, mcpGroup); err == nil {
			Expect(k8sClient.Delete(ctx, mcpGroup)).To(Succeed())
		}
	})

	It("should report backend discovery and health status", func() {
		By("Verifying backend discovery status is populated by vMCP runtime")
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			// Check BackendsDiscovered condition (set by vMCP runtime)
			backendsDiscovered := false
			for _, cond := range server.Status.Conditions {
				if cond.Type == "BackendsDiscovered" && cond.Status == metav1.ConditionTrue {
					backendsDiscovered = true
					break
				}
			}
			if !backendsDiscovered {
				return fmt.Errorf("BackendsDiscovered condition not found or not True")
			}

			// Check discoveredBackends field (populated by vMCP runtime)
			if len(server.Status.DiscoveredBackends) != 2 {
				return fmt.Errorf("expected 2 discovered backends, got %d", len(server.Status.DiscoveredBackends))
			}

			// Verify backend names
			backendNames := make(map[string]bool)
			for _, backend := range server.Status.DiscoveredBackends {
				backendNames[backend.Name] = true
			}
			if !backendNames[backend1Name] {
				return fmt.Errorf("backend1 not found in discovered backends")
			}
			if !backendNames[backend2Name] {
				return fmt.Errorf("backend2 not found in discovered backends")
			}

			// Check backendCount (should be 2 for healthy backends)
			if server.Status.BackendCount != 2 {
				return fmt.Errorf("expected backendCount=2, got %d", server.Status.BackendCount)
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Verifying backend health status is tracked by vMCP runtime")
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			// Check that backends have status and LastHealthCheck timestamp
			for _, backend := range server.Status.DiscoveredBackends {
				if backend.Status == "" {
					return fmt.Errorf("backend %s has empty status", backend.Name)
				}
				if backend.LastHealthCheck.IsZero() {
					return fmt.Errorf("backend %s has zero LastHealthCheck timestamp", backend.Name)
				}
				// Backends should be ready or degraded (not unknown)
				if backend.Status != mcpv1alpha1.BackendStatusReady &&
					backend.Status != mcpv1alpha1.BackendStatusDegraded {
					return fmt.Errorf("backend %s has unexpected status: %s", backend.Name, backend.Status)
				}
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	It("should handle backend failure and update status accordingly", func() {
		By("Changing backend1 image to non-existent to simulate backend failure")
		// Change the MCPServer's image to a non-existent one to make it unavailable
		// This keeps the MCPServer CR in the group (so it stays in discoveredBackends)
		// but makes the backend unreachable, which should trigger Degraded phase
		backend1Server := &mcpv1alpha1.MCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      backend1Name,
			Namespace: testNamespace,
		}, backend1Server)).To(Succeed())

		// Change to a non-existent image
		backend1Server.Spec.Image = "nonexistent/image:doesnotexist"
		Expect(k8sClient.Update(ctx, backend1Server)).To(Succeed())

		// Wait for pod to be in ImagePullBackOff or similar error state
		Eventually(func() bool {
			podList := &corev1.PodList{}
			err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace),
				client.MatchingLabels{"app": backend1Name})
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

		By("Waiting for vMCP runtime to detect backend failure and update status")
		// Status reports run every 5 seconds, and health checks run every 5 seconds
		// We need to wait long enough for: health check to detect failure + status report to run
		// Use a 3-minute timeout to allow for multiple status reporting cycles
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			// Phase should transition to Degraded (some backends healthy, some unhealthy)
			// Backend1 is still in the group but unreachable, so we have 1 healthy + 1 unhealthy
			if server.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseDegraded {
				return fmt.Errorf("expected phase Degraded, got %s", server.Status.Phase)
			}

			// BackendCount should decrease to 1 (only healthy backends counted)
			if server.Status.BackendCount != 1 {
				return fmt.Errorf("expected backendCount=1, got %d", server.Status.BackendCount)
			}

			// Both backends should still be in discoveredBackends (backend1 is still in the group)
			if len(server.Status.DiscoveredBackends) != 2 {
				return fmt.Errorf("expected 2 discovered backends, got %d", len(server.Status.DiscoveredBackends))
			}

			// Check that backend1 is marked as unavailable
			backend1Found := false
			for _, backend := range server.Status.DiscoveredBackends {
				if backend.Name == backend1Name {
					backend1Found = true
					if backend.Status != mcpv1alpha1.BackendStatusUnavailable {
						return fmt.Errorf("backend1 status should be unavailable, got %s", backend.Status)
					}
				}
			}
			if !backend1Found {
				return fmt.Errorf("backend1 not found in discovered backends")
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())

		// Note: Recovery testing (restoring backend and verifying return to Ready phase) is
		// intentionally skipped because StatefulSet pod recreation takes too long and causes
		// test flakiness. The key functionality (failure detection and Degraded phase transition)
		// has been verified above.
	})
})
