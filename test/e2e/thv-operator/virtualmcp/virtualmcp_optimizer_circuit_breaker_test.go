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

var _ = Describe("VirtualMCPServer Optimizer with Circuit Breaker", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-opt-cb-group"
		vmcpServerName  = "test-vmcp-opt-cb"
		embeddingName   = "test-opt-cb-embedding"
		stableName      = "backend-opt-cb-stable"
		unstableName    = "backend-opt-cb-unstable"
		timeout         = 5 * time.Minute
		pollingInterval = 2 * time.Second
		vmcpNodePort    int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for optimizer+circuit breaker tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for optimizer+circuit breaker E2E tests", timeout, pollingInterval)

		By("Creating stable backend MCPServer (gofetch - provides 'fetch' tool)")
		CreateMCPServerAndWait(ctx, k8sClient, stableName, testNamespace,
			mcpGroupName, images.GofetchServerImage, timeout, pollingInterval)

		By("Creating unstable backend MCPServer (yardstick - provides 'echo' tool)")
		CreateMCPServerAndWait(ctx, k8sClient, unstableName, testNamespace,
			mcpGroupName, images.YardstickServerImage, timeout, pollingInterval)

		By("Creating EmbeddingServer for optimizer")
		embeddingServer := &mcpv1alpha1.EmbeddingServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      embeddingName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.EmbeddingServerSpec{
				Model: "BAAI/bge-small-en-v1.5",
				Image: images.TextEmbeddingsInferenceImage,
			},
		}
		Expect(k8sClient.Create(ctx, embeddingServer)).To(Succeed())

		By("Creating VirtualMCPServer with optimizer and circuit breaker enabled")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				ServiceType: "NodePort",
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
				},
				EmbeddingServerRef: &mcpv1alpha1.EmbeddingServerRef{
					Name: embeddingName,
				},
				Config: vmcpconfig.Config{
					Group:     mcpGroupName,
					Optimizer: &vmcpconfig.OptimizerConfig{},
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

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting VirtualMCPServer NodePort")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		_, _ = fmt.Fprintf(GinkgoWriter, "VirtualMCPServer is accessible at NodePort: %d\n", vmcpNodePort)
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer); err == nil {
			_ = k8sClient.Delete(ctx, vmcpServer)
		}

		es := &mcpv1alpha1.EmbeddingServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      embeddingName,
			Namespace: testNamespace,
		}, es); err == nil {
			_ = k8sClient.Delete(ctx, es)
		}

		stableBackend := &mcpv1alpha1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      stableName,
			Namespace: testNamespace,
		}, stableBackend); err == nil {
			_ = k8sClient.Delete(ctx, stableBackend)
		}

		unstableBackend := &mcpv1alpha1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      unstableName,
			Namespace: testNamespace,
		}, unstableBackend); err == nil {
			_ = k8sClient.Delete(ctx, unstableBackend)
		}

		mcpGroup := &mcpv1alpha1.MCPGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      mcpGroupName,
			Namespace: testNamespace,
		}, mcpGroup); err == nil {
			_ = k8sClient.Delete(ctx, mcpGroup)
		}
	})

	It("should find tools from all healthy backends via optimizer", func() {
		By("Waiting for both echo and fetch tools to be discoverable via optimizer")
		Eventually(func() error {
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "opt-cb-test-all-healthy", 30*time.Second)
			if err != nil {
				return fmt.Errorf("failed to create MCP client: %w", err)
			}
			defer mcpClient.Close()

			// Check for echo tool from unstable backend
			findResult, err := callFindTool(mcpClient, "echo back a message")
			if err != nil {
				return fmt.Errorf("find_tool for echo failed: %w", err)
			}
			foundTools := getToolNames(findResult)
			hasEcho := false
			for _, name := range foundTools {
				if strings.Contains(name, "echo") {
					hasEcho = true
					break
				}
			}
			if !hasEcho {
				return fmt.Errorf("echo tool not found yet, got tools: %v", foundTools)
			}

			// Check for fetch tool from stable backend
			findResult, err = callFindTool(mcpClient, "fetch content from a URL")
			if err != nil {
				return fmt.Errorf("find_tool for fetch failed: %w", err)
			}
			foundTools = getToolNames(findResult)
			hasFetch := false
			for _, name := range foundTools {
				if strings.Contains(name, "fetch") {
					hasFetch = true
					break
				}
			}
			if !hasFetch {
				return fmt.Errorf("fetch tool not found yet, got tools: %v", foundTools)
			}

			return nil
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Both backends' tools should be discoverable via optimizer")

		_, _ = fmt.Fprintf(GinkgoWriter, "Both backends' tools found via optimizer: echo and fetch\n")
	})

	It("should exclude unhealthy backend tools from optimizer after circuit breaker opens", func() {
		By("Making unstable backend unavailable by changing to non-existent image")
		backend := &mcpv1alpha1.MCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      unstableName,
			Namespace: testNamespace,
		}, backend)).To(Succeed())

		backend.Spec.Image = "nonexistent/image:doesnotexist"
		Expect(k8sClient.Update(ctx, backend)).To(Succeed())

		By("Waiting for backend pods to enter ImagePullBackOff state")
		Eventually(func() bool {
			podList := &corev1.PodList{}
			err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace),
				client.MatchingLabels{"app": unstableName})
			if err != nil || len(podList.Items) == 0 {
				return false
			}
			for _, containerStatus := range podList.Items[0].Status.ContainerStatuses {
				if containerStatus.State.Waiting != nil &&
					(containerStatus.State.Waiting.Reason == "ImagePullBackOff" ||
						containerStatus.State.Waiting.Reason == "ErrImagePull") {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		By("Waiting for circuit breaker to open for unstable backend")
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			for i := range vmcpServer.Status.DiscoveredBackends {
				if vmcpServer.Status.DiscoveredBackends[i].Name == unstableName {
					backend := &vmcpServer.Status.DiscoveredBackends[i]
					if backend.CircuitBreakerState == "open" {
						GinkgoWriter.Printf("Circuit breaker opened (failures: %d)\n",
							backend.ConsecutiveFailures)
						return nil
					}
					return fmt.Errorf("circuit breaker not open yet (state: %s, failures: %d)",
						backend.CircuitBreakerState, backend.ConsecutiveFailures)
				}
			}
			return fmt.Errorf("unstable backend not found in discovered backends")
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating new MCP client (new session triggers filterHealthyBackends)")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "opt-cb-test-unhealthy", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		By("Verifying stable backend fetch tool is still available")
		findResult, err := callFindTool(mcpClient, "fetch content from a URL")
		Expect(err).ToNot(HaveOccurred())
		foundTools := getToolNames(findResult)

		hasFetchTool := false
		for _, name := range foundTools {
			if strings.Contains(name, "fetch") {
				hasFetchTool = true
				break
			}
		}
		Expect(hasFetchTool).To(BeTrue(), "Stable backend fetch tool should still be available, got tools: %v", foundTools)

		By("Verifying unstable backend echo tool is excluded")
		findResult, err = callFindTool(mcpClient, "echo back a message")
		Expect(err).ToNot(HaveOccurred())
		foundTools = getToolNames(findResult)

		for _, name := range foundTools {
			Expect(name).ToNot(ContainSubstring(unstableName+"_"),
				"Tools from unhealthy backend %s should be excluded, but found tool: %s", unstableName, name)
		}

		_, _ = fmt.Fprintf(GinkgoWriter, "Unhealthy backend tools excluded from optimizer results\n")
	})

	It("should restore backend tools in optimizer after circuit breaker recovers", func() {
		By("Restoring unstable backend by fixing the image")
		backend := &mcpv1alpha1.MCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      unstableName,
			Namespace: testNamespace,
		}, backend)).To(Succeed())

		backend.Spec.Image = images.YardstickServerImage
		Expect(k8sClient.Update(ctx, backend)).To(Succeed())

		By("Deleting stuck pods to force recreation with fixed image")
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList,
			client.InNamespace(testNamespace),
			client.MatchingLabels{"app": unstableName},
		)).To(Succeed())
		for i := range podList.Items {
			if podList.Items[i].Status.Phase == corev1.PodPending {
				GinkgoWriter.Printf("Deleting stuck pod %s in phase %s\n",
					podList.Items[i].Name, podList.Items[i].Status.Phase)
				Expect(k8sClient.Delete(ctx, &podList.Items[i])).To(Succeed())
			}
		}

		By("Waiting for backend to become running again")
		Eventually(func() error {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      unstableName,
				Namespace: testNamespace,
			}, server); err != nil {
				return err
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("backend not running yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Waiting for circuit breaker to close after recovery")
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			for i := range vmcpServer.Status.DiscoveredBackends {
				if vmcpServer.Status.DiscoveredBackends[i].Name == unstableName {
					backend := &vmcpServer.Status.DiscoveredBackends[i]
					if backend.CircuitBreakerState == "closed" &&
						(backend.Status == mcpv1alpha1.BackendStatusReady ||
							backend.Status == mcpv1alpha1.BackendStatusDegraded) {
						GinkgoWriter.Printf("Backend recovered: status=%s, circuitState=%s\n",
							backend.Status, backend.CircuitBreakerState)
						return nil
					}
					return fmt.Errorf("backend not recovered yet (status: %s, circuitState: %s)",
						backend.Status, backend.CircuitBreakerState)
				}
			}
			return fmt.Errorf("unstable backend not found in discovered backends")
		}, timeout, pollingInterval).Should(Succeed())

		By("Creating new MCP client to verify tools are restored")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "opt-cb-test-recovered", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		By("Verifying echo tool from recovered backend is available again")
		findResult, err := callFindTool(mcpClient, "echo back a message")
		Expect(err).ToNot(HaveOccurred())
		foundTools := getToolNames(findResult)
		Expect(foundTools).ToNot(BeEmpty(), "find_tool should return results after recovery")

		hasEchoTool := false
		for _, name := range foundTools {
			if strings.Contains(name, "echo") {
				hasEchoTool = true
				break
			}
		}
		Expect(hasEchoTool).To(BeTrue(), "Echo tool should be restored after recovery, got tools: %v", foundTools)

		By("Verifying fetch tool from stable backend is still available")
		findResult, err = callFindTool(mcpClient, "fetch content from a URL")
		Expect(err).ToNot(HaveOccurred())
		foundTools = getToolNames(findResult)

		hasFetchTool := false
		for _, name := range foundTools {
			if strings.Contains(name, "fetch") {
				hasFetchTool = true
				break
			}
		}
		Expect(hasFetchTool).To(BeTrue(), "Fetch tool should still be available, got tools: %v", foundTools)

		_, _ = fmt.Fprintf(GinkgoWriter, "Both backends' tools available after recovery\n")
	})

	It("should not affect stable backend throughout circuit breaker lifecycle", func() {
		By("Verifying stable backend remained healthy throughout the test")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)).To(Succeed())

		var stableBackend *mcpv1alpha1.DiscoveredBackend
		for i := range vmcpServer.Status.DiscoveredBackends {
			if vmcpServer.Status.DiscoveredBackends[i].Name == stableName {
				stableBackend = &vmcpServer.Status.DiscoveredBackends[i]
				break
			}
		}

		Expect(stableBackend).NotTo(BeNil(), "stable backend should be discovered")

		Expect(stableBackend.Status).To(Or(
			Equal(mcpv1alpha1.BackendStatusReady),
			Equal(mcpv1alpha1.BackendStatusDegraded)),
			"stable backend should remain healthy, got status=%s message=%s",
			stableBackend.Status, stableBackend.Message)

		Expect(strings.ToLower(stableBackend.Message)).NotTo(ContainSubstring("circuit breaker open"),
			"stable backend should not have circuit breaker open, message: %s", stableBackend.Message)

		GinkgoWriter.Printf("Stable backend remained healthy: status=%s, circuitState=%s\n",
			stableBackend.Status, stableBackend.CircuitBreakerState)
	})
})
