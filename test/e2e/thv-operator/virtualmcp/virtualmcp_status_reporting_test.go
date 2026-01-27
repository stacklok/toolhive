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

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
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
		By("Creating VirtualMCPServer with discovered mode")
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
		By("Deleting backend pod to simulate temporary failure")
		// Delete the StatefulSet pod for backend1
		// The StatefulSet controller will recreate it, but there will be a gap during which
		// the backend is unavailable and health checks should fail
		backend1PodName := backend1Name + "-0" // StatefulSet pod naming convention
		backend1Pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      backend1PodName,
			Namespace: testNamespace,
		}, backend1Pod)).To(Succeed())

		// Delete the pod
		Expect(k8sClient.Delete(ctx, backend1Pod)).To(Succeed())

		By("Waiting for vMCP runtime to detect backend failure and update status")
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			// Phase should transition to Degraded (some backends unhealthy)
			if server.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseDegraded {
				return fmt.Errorf("expected phase Degraded, got %s", server.Status.Phase)
			}

			// BackendCount should decrease to 1 (only healthy backends counted)
			if server.Status.BackendCount != 1 {
				return fmt.Errorf("expected backendCount=1, got %d", server.Status.BackendCount)
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

		By("Waiting for backend pod to be recreated by StatefulSet controller")
		// The StatefulSet controller will automatically recreate the deleted pod
		// Wait for the pod to be running again
		Eventually(func() error {
			pod := &corev1.Pod{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend1PodName,
				Namespace: testNamespace,
			}, pod); err != nil {
				return err
			}
			if pod.Status.Phase != corev1.PodRunning {
				return fmt.Errorf("pod not running yet: %s", pod.Status.Phase)
			}
			// Check if pod is ready
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					return nil
				}
			}
			return fmt.Errorf("pod not ready yet")
		}, timeout, pollingInterval).Should(Succeed())

		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			// Phase should return to Ready
			if server.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseReady {
				return fmt.Errorf("expected phase Ready, got %s", server.Status.Phase)
			}

			// BackendCount should return to 2
			if server.Status.BackendCount != 2 {
				return fmt.Errorf("expected backendCount=2, got %d", server.Status.BackendCount)
			}

			// Check that backend1 is back to ready
			backend1Found := false
			for _, backend := range server.Status.DiscoveredBackends {
				if backend.Name == backend1Name {
					backend1Found = true
					if backend.Status != mcpv1alpha1.BackendStatusReady {
						return fmt.Errorf("backend1 should be ready, got %s", backend.Status)
					}
				}
			}
			if !backend1Found {
				return fmt.Errorf("backend1 not found in discovered backends after restoration")
			}

			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})
})
