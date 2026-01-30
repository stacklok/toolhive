// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// Circuit breaker test configuration shared across tests
var (
	cbHealthCheckInterval = 5 * time.Second
	cbFailureThreshold    = 3
	cbTimeout             = 20 * time.Second
	cbUnhealthyThreshold  = 3
)

var _ = Describe("VirtualMCPServer Circuit Breaker - Degradation Path", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-cb-degradation-group"
		vmcpServerName  = "test-vmcp-cb-degradation"
		backendName     = "backend-cb-degradation"
		timeout         = 5 * time.Minute
		pollingInterval = 2 * time.Second
	)

	BeforeAll(func() {
		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for circuit breaker degradation test", timeout, pollingInterval)

		By("Creating backend MCPServer with valid image (healthy)")
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
				},
			},
		}
		Expect(k8sClient.Create(ctx, backend)).To(Succeed())

		By("Waiting for backend MCPServer to be running")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backendName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend not running, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

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
					Operational: &vmcpconfig.OperationalConfig{
						FailureHandling: &vmcpconfig.FailureHandlingConfig{
							HealthCheckInterval:     vmcpconfig.Duration(cbHealthCheckInterval),
							StatusReportingInterval: vmcpconfig.Duration(5 * time.Second),
							UnhealthyThreshold:      cbUnhealthyThreshold,
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

		By("Waiting for VirtualMCPServer to reach Ready phase")
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			if server.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseReady {
				return fmt.Errorf("phase is %s, want Ready", server.Status.Phase)
			}

			// Check for Ready condition
			readyCondition := false
			for _, cond := range server.Status.Conditions {
				if cond.Type == ConditionTypeReady && cond.Status == metav1.ConditionTrue {
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
		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
		}
		Expect(k8sClient.Delete(ctx, vmcpServer)).To(Succeed())

		By("Cleaning up backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: testNamespace,
			},
		}
		Expect(k8sClient.Delete(ctx, backend)).To(Succeed())

		By("Cleaning up MCPGroup")
		group := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		Expect(k8sClient.Delete(ctx, group)).To(Succeed())
	})

	It("should open circuit breaker when backend becomes unhealthy", func() {
		By("Step 1: Verifying initial healthy state (circuit closed)")
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			if len(server.Status.DiscoveredBackends) == 0 {
				return fmt.Errorf("no discovered backends")
			}

			backend := server.Status.DiscoveredBackends[0]
			if backend.Status != mcpv1alpha1.BackendStatusReady {
				return fmt.Errorf("backend status is %s, want ready", backend.Status)
			}

			GinkgoWriter.Printf("✓ Initial state: Backend %s is ready (circuit closed)\n", backend.Name)
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Step 2: Breaking backend by changing to invalid image")
		backend := &mcpv1alpha1.MCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      backendName,
			Namespace: testNamespace,
		}, backend)
		Expect(err).ToNot(HaveOccurred())

		backend.Spec.Image = "invalid-image-does-not-exist:v999"
		Expect(k8sClient.Update(ctx, backend)).To(Succeed())

		GinkgoWriter.Println("✓ Backend image changed to invalid")

		By("Step 3: Waiting for circuit breaker to open")
		// Circuit should open after:
		// - failureThreshold (3) consecutive failures
		// - With healthCheckInterval (5s) between checks
		// - Should take ~15-20 seconds
		Eventually(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			if len(server.Status.DiscoveredBackends) == 0 {
				return fmt.Errorf("no discovered backends")
			}

			backend := server.Status.DiscoveredBackends[0]
			if backend.Status != mcpv1alpha1.BackendStatusUnavailable {
				return fmt.Errorf("backend status is %s, want unavailable", backend.Status)
			}

			GinkgoWriter.Printf("✓ Circuit breaker OPENED: Backend %s marked unavailable\n", backend.Name)
			return nil
		}, 1*time.Minute, pollingInterval).Should(Succeed())

		By("Step 4: Verifying circuit remains open")
		Consistently(func() error {
			server := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}

			if len(server.Status.DiscoveredBackends) == 0 {
				return fmt.Errorf("no discovered backends")
			}

			backend := server.Status.DiscoveredBackends[0]
			if backend.Status != mcpv1alpha1.BackendStatusUnavailable {
				return fmt.Errorf("backend should remain unavailable, got %s", backend.Status)
			}

			return nil
		}, 10*time.Second, pollingInterval).Should(Succeed())

		GinkgoWriter.Println("✅ Degradation test completed:")
		GinkgoWriter.Println("   - Backend transitioned from healthy to unhealthy ✓")
		GinkgoWriter.Println("   - Circuit breaker opened after threshold failures ✓")
		GinkgoWriter.Println("   - Backend marked unavailable ✓")
	})
})
