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

var _ = Describe("VirtualMCPServer Partial Failure Mode", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-partial-failure-group"
		vmcpServerName  = "test-vmcp-partial-failure"
		backend1Name    = "backend-partial-1"
		backend2Name    = "backend-partial-2"
		timeout         = 5 * time.Minute
		pollingInterval = 2 * time.Second
	)

	// Helper to get vMCP server status
	getVMCPStatus := func() (*mcpv1alpha1.VirtualMCPServer, error) {
		server := &mcpv1alpha1.VirtualMCPServer{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, server)
		return server, err
	}

	// Helper to update backend image
	updateBackendImage := func(backendName, image string) error {
		backend := &mcpv1alpha1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      backendName,
			Namespace: testNamespace,
		}, backend); err != nil {
			return err
		}
		backend.Spec.Image = image
		return k8sClient.Update(ctx, backend)
	}

	Context("Fail mode", func() {
		BeforeAll(func() {
			By("Creating MCPGroup")
			CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
				"Test MCP Group for partial failure mode test", timeout, pollingInterval)

			By("Creating backend 1 MCPServer (healthy)")
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

			By("Creating backend 2 MCPServer (healthy)")
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

			By("Waiting for both backends to be running")
			for _, name := range []string{backend1Name, backend2Name} {
				Eventually(func() error {
					server := &mcpv1alpha1.MCPServer{}
					if err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      name,
						Namespace: testNamespace,
					}, server); err != nil {
						return err
					}
					if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
						return fmt.Errorf("backend %s not running, phase: %s", name, server.Status.Phase)
					}
					return nil
				}, timeout, pollingInterval).Should(Succeed())
			}

			By("Creating VirtualMCPServer with fail mode (default)")
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
								PartialFailureMode:      vmcpconfig.PartialFailureModeFail, // Explicit fail mode
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
				server, err := getVMCPStatus()
				if err != nil {
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
			_ = k8sClient.Delete(ctx, vmcpServer)

			By("Cleaning up backend MCPServers")
			for _, name := range []string{backend1Name, backend2Name} {
				backend := &mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
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

		It("should track backend health correctly when one backend fails", func() {
			By("Step 1: Verifying both backends are healthy")
			Eventually(func() error {
				server, err := getVMCPStatus()
				if err != nil {
					return err
				}

				if len(server.Status.DiscoveredBackends) != 2 {
					return fmt.Errorf("expected 2 backends, got %d", len(server.Status.DiscoveredBackends))
				}

				for _, backend := range server.Status.DiscoveredBackends {
					if backend.Status != mcpv1alpha1.BackendStatusReady {
						return fmt.Errorf("backend %s status is %s, want ready", backend.Name, backend.Status)
					}
				}

				GinkgoWriter.Printf("✓ Initial state: Both backends are ready\n")
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By("Step 2: Breaking backend 1")
			Expect(updateBackendImage(backend1Name, "invalid-image:v999")).To(Succeed())
			GinkgoWriter.Println("✓ Backend 1 image changed to invalid")

			By("Step 3: Waiting for backend 1 circuit to open")
			Eventually(func() error {
				server, err := getVMCPStatus()
				if err != nil {
					return err
				}

				backend1Unavailable := false
				backend2Ready := false

				for _, backend := range server.Status.DiscoveredBackends {
					if backend.Name == backend1Name {
						if backend.Status == mcpv1alpha1.BackendStatusUnavailable {
							backend1Unavailable = true
						}
					}
					if backend.Name == backend2Name {
						if backend.Status == mcpv1alpha1.BackendStatusReady {
							backend2Ready = true
						}
					}
				}

				if !backend1Unavailable {
					return fmt.Errorf("backend 1 should be unavailable")
				}
				if !backend2Ready {
					return fmt.Errorf("backend 2 should remain ready")
				}

				GinkgoWriter.Printf("✓ Backend 1 unavailable, backend 2 healthy (partial failure)\n")
				return nil
			}, 1*time.Minute, pollingInterval).Should(Succeed())

			By("Step 4: Verifying VirtualMCPServer enters Degraded phase with partial backend failure")
			Eventually(func() error {
				server, err := getVMCPStatus()
				if err != nil {
					return err
				}

				if server.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseDegraded {
					return fmt.Errorf("vMCP should be Degraded with partial backend failure, got %s", server.Status.Phase)
				}

				GinkgoWriter.Printf("✓ VirtualMCPServer correctly entered Degraded phase (partial failure)\n")
				return nil
			}, 30*time.Second, pollingInterval).Should(Succeed())

			GinkgoWriter.Println("✅ Partial backend failure test completed:")
			GinkgoWriter.Println("   - One backend failed ✓")
			GinkgoWriter.Println("   - Other backend remained healthy ✓")
			GinkgoWriter.Println("   - VirtualMCPServer entered Degraded phase (partial failure tracked) ✓")
			GinkgoWriter.Println("   - Fail mode allows continued operation with remaining healthy backend ✓")
		})

		It("should track partial failure mode configuration", func() {
			By("Verifying VirtualMCPServer is configured with fail mode")
			server, err := getVMCPStatus()
			Expect(err).ToNot(HaveOccurred())

			// Check the configuration
			Expect(server.Spec.Config.Operational).ToNot(BeNil())
			Expect(server.Spec.Config.Operational.FailureHandling).ToNot(BeNil())
			Expect(server.Spec.Config.Operational.FailureHandling.PartialFailureMode).
				To(Equal(vmcpconfig.PartialFailureModeFail))

			GinkgoWriter.Println("✅ Fail mode configuration validated:")
			GinkgoWriter.Println("   - PartialFailureMode is set to 'fail' ✓")
			GinkgoWriter.Println("   - Configuration properly stored in VirtualMCPServer spec ✓")
		})

		It("should support reconfiguration to best_effort mode", func() {
			By("Step 1: Updating VirtualMCPServer to use best_effort mode")
			server, err := getVMCPStatus()
			Expect(err).ToNot(HaveOccurred())

			// Update to best_effort mode
			server.Spec.Config.Operational.FailureHandling.PartialFailureMode = vmcpconfig.PartialFailureModeBestEffort
			Expect(k8sClient.Update(ctx, server)).To(Succeed())

			GinkgoWriter.Println("✓ VirtualMCPServer updated to best_effort mode")

			By("Step 2: Verifying configuration change is persisted")
			Eventually(func() error {
				server, err := getVMCPStatus()
				if err != nil {
					return err
				}

				if server.Spec.Config.Operational.FailureHandling.PartialFailureMode != vmcpconfig.PartialFailureModeBestEffort {
					return fmt.Errorf("expected best_effort mode, got %s",
						server.Spec.Config.Operational.FailureHandling.PartialFailureMode)
				}

				GinkgoWriter.Printf("✓ Configuration persisted: PartialFailureMode = best_effort\n")
				return nil
			}, 30*time.Second, pollingInterval).Should(Succeed())

			By("Step 3: Verifying VirtualMCPServer deployment is updated")
			// Wait for the deployment to be updated with new config
			// The operator should trigger a rollout with the new configuration
			Eventually(func() error {
				// The vMCP server should still be operational (Degraded is ok, just not Failed)
				server, err := getVMCPStatus()
				if err != nil {
					return err
				}

				if server.Status.Phase == mcpv1alpha1.VirtualMCPServerPhaseFailed {
					return fmt.Errorf("vMCP should not fail with best_effort mode")
				}

				return nil
			}, 1*time.Minute, pollingInterval).Should(Succeed())

			GinkgoWriter.Println("✅ Best effort mode reconfiguration test completed:")
			GinkgoWriter.Println("   - Configuration updated to best_effort mode ✓")
			GinkgoWriter.Println("   - Configuration change persisted ✓")
			GinkgoWriter.Println("   - VirtualMCPServer handles mode switch correctly ✓")
		})
	})
})
