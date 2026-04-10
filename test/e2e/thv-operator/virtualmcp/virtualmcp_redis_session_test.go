// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"fmt"
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
		timeout          = time.Minute * 5
		pollInterval     = time.Second * 2
		defaultNamespace = "default"
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
			deployRedis(defaultNamespace, redisName, timeout, pollInterval)

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
			cleanupRedis(defaultNamespace, redisName)
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
			deployRedis(defaultNamespace, redisName, timeout, pollInterval)

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
			cleanupRedis(defaultNamespace, redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should allow a session established on pod A to be used on pod B", func() {
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
})
