// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = ginkgo.Describe("VirtualMCPServer Redis-Backed Session Sharing", func() {
	const (
		timeout      = time.Minute * 5
		pollInterval = time.Second * 2
	)

	// -------------------------------------------------------------------------
	// Context 1: replicas=2 + Redis → SessionStorageWarning is False
	// -------------------------------------------------------------------------

	ginkgo.Context("When VirtualMCPServer has replicas=2 with Redis configured", ginkgo.Ordered, func() {
		var (
			mcpGroupName string
			backendName  string
			vmcpName     string
			redisName    string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-redis-group-%d", ts)
			backendName = fmt.Sprintf("e2e-redis-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-redis-vmcp-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-%d", ts)

			ginkgo.By("Deploying Redis")
			deployRedis(redisName)

			ginkgo.By("Creating MCPGroup")
			CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, defaultNamespace,
				"E2E Redis session group", timeout, pollInterval)

			ginkgo.By("Creating backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backendName,
					Namespace: defaultNamespace,
				}, server); err != nil {
					return fmt.Errorf("failed to get MCPServer: %w", err)
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready yet, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(), "backend MCPServer should be ready")

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating VirtualMCPServer with replicas=2 and Redis")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:          vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth:    &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Replicas:        &replicas,
					SessionAffinity: "None",
					SessionStorage: &mcpv1alpha1.SessionStorageConfig{
						Provider:  mcpv1alpha1.SessionStorageProviderRedis,
						Address:   redisAddr,
						KeyPrefix: "thv:vmcp:e2e:",
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Waiting for VirtualMCPServer to report Ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should set SessionStorageWarning=False when Redis is configured", func() {
			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "False",
				timeout, pollInterval)
		})

		ginkgo.It("Should report Ready=True when Redis is configured", func() {
			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1alpha1.ConditionTypeVirtualMCPServerReady, "True",
				timeout, pollInterval)
		})
	})

	// -------------------------------------------------------------------------
	// Context 2: cross-pod session reconstruction with Redis
	// -------------------------------------------------------------------------

	ginkgo.Context("When cross-pod session reconstruction with Redis", ginkgo.Ordered, func() {
		var (
			mcpGroupName string
			backendName  string
			vmcpName     string
			redisName    string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-redis-xpod-group-%d", ts)
			backendName = fmt.Sprintf("e2e-redis-xpod-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-redis-xpod-vmcp-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-xpod-%d", ts)

			ginkgo.By("Deploying Redis")
			deployRedis(redisName)

			ginkgo.By("Creating MCPGroup")
			CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, defaultNamespace,
				"E2E Redis cross-pod session group", timeout, pollInterval)

			ginkgo.By("Creating backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backendName,
					Namespace: defaultNamespace,
				}, server); err != nil {
					return fmt.Errorf("failed to get MCPServer: %w", err)
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready yet, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(), "backend MCPServer should be ready")

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating VirtualMCPServer with replicas=2 and Redis")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:          vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth:    &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Replicas:        &replicas,
					SessionAffinity: "None",
					SessionStorage: &mcpv1alpha1.SessionStorageConfig{
						Provider:  mcpv1alpha1.SessionStorageProviderRedis,
						Address:   redisAddr,
						KeyPrefix: "thv:vmcp:e2e:",
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Waiting for VirtualMCPServer to report Ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should allow a session established on pod A to be reconstructed on pod B", func() {
			ginkgo.By("Getting the two ready pods")
			var pods []corev1.Pod
			gomega.Eventually(func() (int, error) {
				podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				var ready []corev1.Pod
				for _, pod := range podList.Items {
					if pod.Status.Phase != corev1.PodRunning {
						continue
					}
					for _, c := range pod.Status.Conditions {
						if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
							ready = append(ready, pod)
						}
					}
				}
				pods = ready
				return len(ready), nil
			}, timeout, pollInterval).Should(gomega.Equal(2))

			podA := pods[0]
			podB := pods[1]
			gomega.Expect(podA.Name).NotTo(gomega.Equal(podB.Name), "The two pods must be distinct")

			ginkgo.By(fmt.Sprintf("Port-forwarding to pod A (%s)", podA.Name))
			localPortA, cleanupA, err := portForwardToPod(podA.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupA()

			ginkgo.By(fmt.Sprintf("Port-forwarding to pod B (%s)", podB.Name))
			localPortB, cleanupB, err := portForwardToPod(podB.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupB()

			ginkgo.By("Initializing session on pod A")
			clientA, err := CreateInitializedMCPClient(int32(localPortA), "e2e-redis-test", 30*time.Second)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer clientA.Close()

			sessionID := clientA.Client.GetSessionId()
			gomega.Expect(sessionID).NotTo(gomega.BeEmpty(), "session ID must be assigned after Initialize")

			ginkgo.By("Listing tools on pod A")
			toolsA, err := clientA.Client.ListTools(clientA.Ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(toolsA.Tools).NotTo(gomega.BeEmpty(), "pod A must return tools")

			ginkgo.By(fmt.Sprintf("Connecting to pod B (%s) with the same session ID", podB.Name))
			serverURLB := fmt.Sprintf("http://localhost:%d/mcp", localPortB)
			clientB, err := mcpclient.NewStreamableHttpClient(serverURLB, transport.WithSession(sessionID))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer func() { _ = clientB.Close() }()

			startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer startCancel()
			gomega.Expect(clientB.Start(startCtx)).To(gomega.Succeed())

			ginkgo.By("Listing tools on pod B using the session from pod A")
			listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer listCancel()
			toolsB, err := clientB.ListTools(listCtx, mcp.ListToolsRequest{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(toolsB.Tools).NotTo(gomega.BeEmpty(), "pod B must return tools via Redis-reconstructed session")

			ginkgo.By("Verifying both pods return the same tool count")
			gomega.Expect(toolsB.Tools).To(gomega.HaveLen(len(toolsA.Tools)),
				"pod B must see same session state as pod A")
		})
	})

	// -------------------------------------------------------------------------
	// Context 3: VirtualMCPServer pod restart — session survives in Redis
	// -------------------------------------------------------------------------

	ginkgo.Context("When a VirtualMCPServer pod restarts with Redis configured", ginkgo.Ordered, func() {
		var (
			mcpGroupName string
			backendName  string
			vmcpName     string
			redisName    string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-redis-restart-group-%d", ts)
			backendName = fmt.Sprintf("e2e-redis-restart-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-redis-restart-vmcp-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-restart-%d", ts)

			ginkgo.By("Deploying Redis")
			deployRedis(redisName)

			ginkgo.By("Creating MCPGroup")
			CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, defaultNamespace,
				"E2E Redis pod restart group", timeout, pollInterval)

			ginkgo.By("Creating backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: backendName, Namespace: defaultNamespace}, server); err != nil {
					return err
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed())

			replicas := int32(1)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating VirtualMCPServer with replicas=1, Redis, and NodePort")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:          vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth:    &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Replicas:        &replicas,
					ServiceType:     "NodePort",
					SessionAffinity: "None",
					SessionStorage: &mcpv1alpha1.SessionStorageConfig{
						Provider:  mcpv1alpha1.SessionStorageProviderRedis,
						Address:   redisAddr,
						KeyPrefix: "thv:vmcp:e2e:",
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for VirtualMCPServer to be ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should recover the session on the new pod after the original pod is deleted", func() {
			ginkgo.By("Getting the NodePort for the VirtualMCPServer")
			vmcpNodePort := GetVMCPNodePort(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)

			ginkgo.By("Initializing an MCP session")
			mcpClientA, err := CreateInitializedMCPClient(vmcpNodePort, "e2e-redis-restart-test", 30*time.Second)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			sessionID := mcpClientA.Client.GetSessionId()
			gomega.Expect(sessionID).NotTo(gomega.BeEmpty(), "session ID must be assigned after Initialize")

			ginkgo.By("Verifying tools are available before pod restart")
			toolsBefore, err := mcpClientA.Client.ListTools(mcpClientA.Ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(toolsBefore.Tools).NotTo(gomega.BeEmpty())

			// Cancel context to stop in-flight requests without sending DELETE.
			// This simulates the pod being killed, not a clean client disconnect.
			mcpClientA.Cancel()

			ginkgo.By("Getting the running pod name before restart")
			var pods []corev1.Pod
			gomega.Eventually(func() (int, error) {
				podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				var ready []corev1.Pod
				for _, pod := range podList.Items {
					if pod.Status.Phase != corev1.PodRunning {
						continue
					}
					for _, c := range pod.Status.Conditions {
						if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
							ready = append(ready, pod)
						}
					}
				}
				pods = ready
				return len(ready), nil
			}, timeout, pollInterval).Should(gomega.Equal(1))
			oldPodName := pods[0].Name

			ginkgo.By(fmt.Sprintf("Deleting pod %s (Deployment will recreate it)", oldPodName))
			gomega.Expect(k8sClient.Delete(ctx, &pods[0])).To(gomega.Succeed())

			ginkgo.By("Waiting for a new pod to be Running+Ready")
			gomega.Eventually(func() (string, error) {
				podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpName, defaultNamespace)
				if err != nil {
					return "", err
				}
				for _, pod := range podList.Items {
					if pod.Name == oldPodName || pod.Status.Phase != corev1.PodRunning {
						continue
					}
					for _, c := range pod.Status.Conditions {
						if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
							return pod.Name, nil
						}
					}
				}
				return "", fmt.Errorf("waiting for new pod")
			}, timeout, pollInterval).ShouldNot(gomega.BeEmpty())

			ginkgo.By("Waiting for the NodePort to be serving HTTP again")
			gomega.Eventually(func() error {
				return checkHTTPHealthReady(vmcpNodePort)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Creating a new client with the SAME session ID")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			newClient, err := mcpclient.NewStreamableHttpClient(serverURL, transport.WithSession(sessionID))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer func() { _ = newClient.Close() }()

			startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer startCancel()
			gomega.Expect(newClient.Start(startCtx)).To(gomega.Succeed())

			// Send 5 requests to give confidence the fix holds: without Redis-backed
			// session reconstruction, each request would fail because the new pod's
			// in-memory cache is cold.
			ginkgo.By("Sending 5 requests to verify the session is recovered from Redis on the new pod")
			for i := range 5 {
				listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
				toolsAfter, listErr := newClient.ListTools(listCtx, mcp.ListToolsRequest{})
				listCancel()
				gomega.Expect(listErr).NotTo(gomega.HaveOccurred(),
					"Request %d/5 should succeed after pod restart — session must be recovered from Redis", i+1)
				gomega.Expect(toolsAfter.Tools).To(gomega.HaveLen(len(toolsBefore.Tools)),
					"Request %d/5 should return the same tools as before restart", i+1)
			}
		})
	})

	// -------------------------------------------------------------------------
	// Context 4: Terminated session rejected by pod B via lazy eviction (#4731)
	// -------------------------------------------------------------------------

	ginkgo.Context("When a session is terminated on pod A, pod B rejects it via lazy eviction", ginkgo.Ordered, func() {
		var (
			mcpGroupName string
			backendName  string
			vmcpName     string
			redisName    string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-redis-term-group-%d", ts)
			backendName = fmt.Sprintf("e2e-redis-term-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-redis-term-vmcp-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-term-%d", ts)

			ginkgo.By("Deploying Redis")
			deployRedis(redisName)

			ginkgo.By("Creating MCPGroup")
			CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, defaultNamespace,
				"E2E Redis terminated session group", timeout, pollInterval)

			ginkgo.By("Creating backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: backendName, Namespace: defaultNamespace}, server); err != nil {
					return err
				}
				if server.Status.Phase != mcpv1alpha1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed())

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating VirtualMCPServer with replicas=2, Redis, and SessionAffinity=None")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:          vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth:    &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Replicas:        &replicas,
					SessionAffinity: "None",
					SessionStorage: &mcpv1alpha1.SessionStorageConfig{
						Provider:  mcpv1alpha1.SessionStorageProviderRedis,
						Address:   redisAddr,
						KeyPrefix: "thv:vmcp:e2e:",
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Waiting for VirtualMCPServer to report Ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should reject the session on pod B after it is terminated on pod A", func() {
			ginkgo.By("Getting the two ready pods")
			var pods []corev1.Pod
			gomega.Eventually(func() (int, error) {
				podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				var ready []corev1.Pod
				for _, pod := range podList.Items {
					if pod.Status.Phase != corev1.PodRunning {
						continue
					}
					for _, c := range pod.Status.Conditions {
						if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
							ready = append(ready, pod)
						}
					}
				}
				pods = ready
				return len(ready), nil
			}, timeout, pollInterval).Should(gomega.Equal(2))

			podA := pods[0]
			podB := pods[1]
			gomega.Expect(podA.Name).NotTo(gomega.Equal(podB.Name), "The two pods must be distinct")

			ginkgo.By(fmt.Sprintf("Port-forwarding to pod A (%s)", podA.Name))
			localPortA, cleanupA, err := portForwardToPod(podA.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupA()

			ginkgo.By(fmt.Sprintf("Port-forwarding to pod B (%s)", podB.Name))
			localPortB, cleanupB, err := portForwardToPod(podB.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupB()

			ginkgo.By("Initializing a session on pod A")
			clientA, err := CreateInitializedMCPClient(int32(localPortA), "e2e-redis-term-test", 30*time.Second)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			sessionID := clientA.Client.GetSessionId()
			gomega.Expect(sessionID).NotTo(gomega.BeEmpty(), "session ID must be assigned after Initialize")

			ginkgo.By("Verifying the session is usable on pod A")
			toolsA, err := clientA.Client.ListTools(clientA.Ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(toolsA.Tools).NotTo(gomega.BeEmpty())

			ginkgo.By(fmt.Sprintf("Reconstructing the session on pod B (%s) via Redis", podB.Name))
			serverURLB := fmt.Sprintf("http://localhost:%d/mcp", localPortB)
			clientB, err := mcpclient.NewStreamableHttpClient(serverURLB, transport.WithSession(sessionID))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer func() { _ = clientB.Close() }()

			startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
			gomega.Expect(clientB.Start(startCtx)).To(gomega.Succeed())
			startCancel()

			listCtxB, listCancelB := context.WithTimeout(context.Background(), 30*time.Second)
			toolsB, err := clientB.ListTools(listCtxB, mcp.ListToolsRequest{})
			listCancelB()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(toolsB.Tools).NotTo(gomega.BeEmpty(),
				"pod B should serve the session before termination")

			// Terminate the session on pod A by sending DELETE /mcp directly.
			// We do this via raw HTTP rather than clientA.Close() to avoid the
			// context-cancellation ordering in InitializedMCPClient.Close().
			ginkgo.By("Terminating the session on pod A via DELETE /mcp")
			deleteURL := fmt.Sprintf("http://localhost:%d/mcp", localPortA)
			deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer deleteCancel()
			req, err := http.NewRequestWithContext(deleteCtx, http.MethodDelete, deleteURL, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			req.Header.Set("Mcp-Session-Id", sessionID)
			resp, err := http.DefaultClient.Do(req)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_ = resp.Body.Close()
			gomega.Expect(resp.StatusCode).To(gomega.BeElementOf(http.StatusOK, http.StatusNoContent),
				"DELETE /mcp should return 200 or 204")
			clientA.Cancel()

			// Pod B's in-memory cache still holds the session, but the ValidatingCache's
			// checkSession callback will find the key absent in Redis (deleted by the
			// Terminate call above) and return ErrExpired, triggering lazy eviction.
			// The next request from pod B should therefore fail with a session-not-found error.
			ginkgo.By("Verifying pod B rejects subsequent requests for the terminated session")
			gomega.Eventually(func() error {
				listCtx, listCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer listCancel()
				_, listErr := clientB.ListTools(listCtx, mcp.ListToolsRequest{})
				if listErr == nil {
					return fmt.Errorf("expected pod B to reject the terminated session, but request succeeded")
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(),
				"pod B should reject the session after it is terminated on pod A")
		})
	})
})
