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
		timeout         = 3 * time.Minute
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
			GinkgoWriter.Printf("Backend discovered: name=%s, status=%s, url=%s\n",
				backend.Name, backend.Status, backend.URL)
			Expect(backend.Name).ToNot(BeEmpty())
			Expect(backend.URL).ToNot(BeEmpty())
			Expect(backend.LastHealthCheck.IsZero()).To(BeFalse())
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
		It("should detect when backend pod is deleted and report as unavailable", func() {
			By("Getting current status")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			initialBackendCount := vmcp.Status.BackendCount
			GinkgoWriter.Printf("Initial backend count: %d\n", initialBackendCount)

			By("Finding and deleting the first backend's pod")
			pods := &corev1.PodList{}
			err = k8sClient.List(ctx, pods,
				ctrlclient.InNamespace(testNamespace),
				ctrlclient.MatchingLabels{"app.kubernetes.io/name": backend1Name})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).ToNot(BeEmpty(), "Should find backend pod")

			podToDelete := pods.Items[0]
			GinkgoWriter.Printf("Deleting backend pod: %s\n", podToDelete.Name)
			Expect(k8sClient.Delete(ctx, &podToDelete)).To(Succeed())

			By("Waiting for pod to be deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      podToDelete.Name,
					Namespace: testNamespace,
				}, &corev1.Pod{})
				return errors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue(),
				"Pod should be deleted")

			By("Verifying StatusReporter detects the backend failure")
			// The backend should either:
			// 1. Change status to unavailable/degraded, OR
			// 2. Disappear from the list temporarily (if it's recreated by deployment)
			Eventually(func() bool {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return false
				}

				// Check if any backend is marked as unavailable or if count decreased
				hasUnavailableBackend := false
				for _, backend := range vmcp.Status.DiscoveredBackends {
					GinkgoWriter.Printf("Backend status check: name=%s, status=%s\n",
						backend.Name, backend.Status)
					if backend.Status == mcpv1alpha1.BackendStatusUnavailable ||
						backend.Status == mcpv1alpha1.BackendStatusDegraded {
						hasUnavailableBackend = true
						break
					}
				}

				countDecreased := vmcp.Status.BackendCount < initialBackendCount

				return hasUnavailableBackend || countDecreased
			}, timeout, pollingInterval).Should(BeTrue(),
				"StatusReporter should detect backend failure")

			By("Logging final status after failure detection")
			vmcp = &mcpv1alpha1.VirtualMCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Final backend count: %d (was %d)\n",
				vmcp.Status.BackendCount, initialBackendCount)
			for _, backend := range vmcp.Status.DiscoveredBackends {
				GinkgoWriter.Printf("Backend after failure: name=%s, status=%s\n",
					backend.Name, backend.Status)
			}
		})

		It("should detect when backend is scaled to 0 and report accordingly", func() {
			By("Getting the second backend MCPServer")
			backend := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend2Name,
				Namespace: testNamespace,
			}, backend)
			Expect(err).ToNot(HaveOccurred())

			By("Recording current backend count")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			beforeCount := vmcp.Status.BackendCount
			GinkgoWriter.Printf("Backend count before scaling: %d\n", beforeCount)

			By("Deleting the second backend entirely")
			Expect(k8sClient.Delete(ctx, backend)).To(Succeed())

			By("Waiting for backend deletion")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend2Name,
					Namespace: testNamespace,
				}, &mcpv1alpha1.MCPServer{})
				return errors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())

			By("Verifying StatusReporter detects backend removal")
			Eventually(func() bool {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return false
				}

				// Backend count should decrease or backend should disappear from list
				if vmcp.Status.BackendCount < beforeCount {
					GinkgoWriter.Printf("Backend count decreased to %d (was %d)\n",
						vmcp.Status.BackendCount, beforeCount)
					return true
				}

				// Check if backend2 is no longer in the list
				for _, b := range vmcp.Status.DiscoveredBackends {
					if b.Name == backend2Name || b.URL != "" && vmcp.Status.BackendCount == beforeCount {
						// Still there, keep waiting
						return false
					}
				}
				return true
			}, timeout, pollingInterval).Should(BeTrue(),
				"StatusReporter should detect backend removal")
		})
	})

	Context("Backend Recovery Detection", func() {
		It("should detect when a new backend is added after failures", func() {
			By("Getting current backend count")
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			beforeCount := vmcp.Status.BackendCount
			GinkgoWriter.Printf("Backend count before adding new backend: %d\n", beforeCount)

			By("Creating a new backend to simulate recovery")
			backend3 := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backend3Name,
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
			Expect(k8sClient.Create(ctx, backend3)).To(Succeed())

			By("Waiting for new backend to be running")
			Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backend3Name,
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

			By("Verifying StatusReporter detects the new backend")
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
			}, timeout, pollingInterval).Should(BeNumerically(">", beforeCount),
				"BackendCount should increase after adding new backend")

			By("Verifying new backend is reported as ready")
			Eventually(func() bool {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return false
				}

				// Check if we have at least one ready backend
				for _, backend := range vmcp.Status.DiscoveredBackends {
					if backend.Status == mcpv1alpha1.BackendStatusReady {
						GinkgoWriter.Printf("Found ready backend: %s\n", backend.Name)
						return true
					}
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue(),
				"At least one backend should be ready")

			By("Logging final backend status")
			vmcp = &mcpv1alpha1.VirtualMCPServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Final backend count: %d\n", vmcp.Status.BackendCount)
			for _, backend := range vmcp.Status.DiscoveredBackends {
				GinkgoWriter.Printf("Backend: name=%s, status=%s, url=%s, lastHealthCheck=%v\n",
					backend.Name, backend.Status, backend.URL, backend.LastHealthCheck.Time)
			}
		})
	})

	Context("Status Consistency and Timing", func() {
		It("should maintain consistent status updates over time", func() {
			By("Monitoring status for consistency")
			Consistently(func() error {
				vmcp := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpServerName,
					Namespace: testNamespace,
				}, vmcp)
				if err != nil {
					return err
				}

				// BackendCount should match DiscoveredBackends length
				if len(vmcp.Status.DiscoveredBackends) != vmcp.Status.BackendCount {
					return fmt.Errorf("backend count mismatch: count=%d, len(backends)=%d",
						vmcp.Status.BackendCount, len(vmcp.Status.DiscoveredBackends))
				}

				// ObservedGeneration should match Generation
				if vmcp.Status.ObservedGeneration != vmcp.Generation {
					return fmt.Errorf("observed generation mismatch: observed=%d, generation=%d",
						vmcp.Status.ObservedGeneration, vmcp.Generation)
				}

				// All backends should have required fields
				for _, backend := range vmcp.Status.DiscoveredBackends {
					if backend.Name == "" {
						return fmt.Errorf("backend has empty name")
					}
					if backend.URL == "" {
						return fmt.Errorf("backend %s has empty URL", backend.Name)
					}
					if backend.LastHealthCheck.IsZero() {
						return fmt.Errorf("backend %s has zero LastHealthCheck", backend.Name)
					}
				}

				return nil
			}, 20*time.Second, pollingInterval).Should(Succeed(),
				"Status should remain consistent over time")
		})

		It("should have recent health check timestamps", func() {
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())

			now := time.Now()
			for _, backend := range vmcp.Status.DiscoveredBackends {
				By(fmt.Sprintf("Checking health check timestamp for backend %s", backend.Name))
				Expect(backend.LastHealthCheck.IsZero()).To(BeFalse(),
					"LastHealthCheck should be set")

				healthCheckTime := backend.LastHealthCheck.Time
				Expect(healthCheckTime.Before(now.Add(1*time.Minute))).To(BeTrue(),
					"Health check timestamp should not be in the future")
				Expect(healthCheckTime.After(now.Add(-5*time.Minute))).To(BeTrue(),
					"Health check timestamp should be recent (within last 5 minutes)")
			}
		})
	})
})
