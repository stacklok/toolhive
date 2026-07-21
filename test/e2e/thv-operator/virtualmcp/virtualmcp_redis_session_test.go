// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	mcpclient "github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
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
			gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1beta1.MCPServerSpec{
					GroupRef:  &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					MCPPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backendName,
					Namespace: defaultNamespace,
				}, server); err != nil {
					return fmt.Errorf("failed to get MCPServer: %w", err)
				}
				if server.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready yet, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(), "backend MCPServer should be ready")

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating VirtualMCPServer with replicas=2 and Redis")
			gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
				v1beta1test.WithVMCPGroupRef(mcpGroupName),
				v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
				v1beta1test.WithVMCPReplicas(replicas),
				v1beta1test.WithVMCPSessionStorage(&mcpv1beta1.SessionStorageConfig{
					Provider:  mcpv1beta1.SessionStorageProviderRedis,
					Address:   redisAddr,
					KeyPrefix: "thv:vmcp:e2e:",
				}),
				v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
					v.Spec.SessionAffinity = "None"
				}),
			))).To(gomega.Succeed())

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Waiting for VirtualMCPServer to report Ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace))
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should set SessionStorageWarning=False when Redis is configured", func() {
			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1beta1.ConditionSessionStorageWarning, "False",
				timeout, pollInterval)
		})

		ginkgo.It("Should report Ready=True when Redis is configured", func() {
			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1beta1.ConditionTypeVirtualMCPServerReady, "True",
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
			gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1beta1.MCPServerSpec{
					GroupRef:  &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					MCPPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      backendName,
					Namespace: defaultNamespace,
				}, server); err != nil {
					return fmt.Errorf("failed to get MCPServer: %w", err)
				}
				if server.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready yet, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(), "backend MCPServer should be ready")

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating VirtualMCPServer with replicas=2 and Redis")
			gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
				v1beta1test.WithVMCPGroupRef(mcpGroupName),
				v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
				v1beta1test.WithVMCPReplicas(replicas),
				v1beta1test.WithVMCPSessionStorage(&mcpv1beta1.SessionStorageConfig{
					Provider:  mcpv1beta1.SessionStorageProviderRedis,
					Address:   redisAddr,
					KeyPrefix: "thv:vmcp:e2e:",
				}),
				v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
					v.Spec.SessionAffinity = "None"
				}),
			))).To(gomega.Succeed())

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Waiting for VirtualMCPServer to report Ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace))
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{})
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
			localPortA, cleanupA, err := portForwardToPod(podA.Name, vmcpPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupA()

			ginkgo.By(fmt.Sprintf("Port-forwarding to pod B (%s)", podB.Name))
			localPortB, cleanupB, err := portForwardToPod(podB.Name, vmcpPort)
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

			ginkgo.By("Verifying pod A stored backend session IDs in Redis")
			backendIDsBeforeRestore, err := readRedisSessionBackendIDs(redisName, "thv:vmcp:e2e:", sessionID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(backendIDsBeforeRestore).NotTo(gomega.BeEmpty(),
				"pod A must have written per-backend session IDs to Redis so pod B can use them as hints")

			ginkgo.By(fmt.Sprintf("Connecting to pod B (%s) with the same session ID", podB.Name))
			serverURLB := fmt.Sprintf("http://localhost:%d/mcp", localPortB)
			clientB, err := mcpclient.NewStreamableHttpClient(serverURLB, transport.WithSession(sessionID))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer func() { _ = clientB.Close() }()

			startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer startCancel()
			gomega.Expect(clientB.Start(startCtx)).To(gomega.Succeed())

			// Cross-pod reconstruction is eventually consistent (see the lazy-eviction
			// test): retry until pod B serves the reconstructed session rather than
			// asserting on a single immediate ListTools, which flakes under parallel CI load.
			ginkgo.By("Listing tools on pod B using the session from pod A")
			var toolsB *mcp.ListToolsResult
			gomega.Eventually(func() error {
				listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer listCancel()
				result, listErr := clientB.ListTools(listCtx, mcp.ListToolsRequest{})
				if listErr != nil {
					return listErr
				}
				if len(result.Tools) == 0 {
					return fmt.Errorf("pod B returned no tools via Redis-reconstructed session yet")
				}
				toolsB = result
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(), "pod B must return tools via Redis-reconstructed session")

			ginkgo.By("Verifying backend session IDs in Redis are the same hints pod B received")
			backendIDsAfterRestore, err := readRedisSessionBackendIDs(redisName, "thv:vmcp:e2e:", sessionID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(backendIDsAfterRestore).To(gomega.Equal(backendIDsBeforeRestore),
				"RestoreSession must not overwrite the backend session IDs stored by pod A — "+
					"pod B used them as hints and the IDs must be stable")

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
			gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1beta1.MCPServerSpec{
					GroupRef:  &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					MCPPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: backendName, Namespace: defaultNamespace}, server); err != nil {
					return err
				}
				if server.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed())

			replicas := int32(1)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating VirtualMCPServer with replicas=1, Redis, and NodePort")
			gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
				v1beta1test.WithVMCPGroupRef(mcpGroupName),
				v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
				v1beta1test.WithVMCPReplicas(replicas),
				v1beta1test.WithVMCPSessionStorage(&mcpv1beta1.SessionStorageConfig{
					Provider:  mcpv1beta1.SessionStorageProviderRedis,
					Address:   redisAddr,
					KeyPrefix: "thv:vmcp:e2e:",
				}),
				v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
					v.Spec.ServiceType = "NodePort"
					v.Spec.SessionAffinity = "None"
				}),
			))).To(gomega.Succeed())

			ginkgo.By("Waiting for VirtualMCPServer to be ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace))
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{})
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
			// We intentionally skip Client.Close() here because Close() sends a
			// DELETE /mcp request that would terminate the session in Redis before
			// the pod is restarted — defeating the purpose of this test.
			// The transport's background goroutine (started by Start()) selects on
			// ctx.Done(), so Cancel() is sufficient to stop it without leaking.
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
			gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1beta1.MCPServerSpec{
					GroupRef:  &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					MCPPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: backendName, Namespace: defaultNamespace}, server); err != nil {
					return err
				}
				if server.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed())

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating VirtualMCPServer with replicas=2, Redis, and SessionAffinity=None")
			gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
				v1beta1test.WithVMCPGroupRef(mcpGroupName),
				v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
				v1beta1test.WithVMCPReplicas(replicas),
				v1beta1test.WithVMCPSessionStorage(&mcpv1beta1.SessionStorageConfig{
					Provider:  mcpv1beta1.SessionStorageProviderRedis,
					Address:   redisAddr,
					KeyPrefix: "thv:vmcp:e2e:",
				}),
				v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
					v.Spec.SessionAffinity = "None"
				}),
			))).To(gomega.Succeed())

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Waiting for VirtualMCPServer to report Ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace))
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{})
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
			localPortA, cleanupA, err := portForwardToPod(podA.Name, vmcpPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupA()

			ginkgo.By(fmt.Sprintf("Port-forwarding to pod B (%s)", podB.Name))
			localPortB, cleanupB, err := portForwardToPod(podB.Name, vmcpPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupB()

			ginkgo.By("Initializing a session on pod A")
			var clientA *InitializedMCPClient
			gomega.Eventually(func() error {
				if clientA != nil {
					clientA.Cancel()
					clientA = nil
				}
				var initErr error
				clientA, initErr = CreateInitializedMCPClient(int32(localPortA), "e2e-redis-term-test", 30*time.Second)
				return initErr
			}, timeout, pollInterval).Should(gomega.Succeed(),
				"pod A should accept session initialization once backend routing is ready")
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
			defer startCancel()
			gomega.Expect(clientB.Start(startCtx)).To(gomega.Succeed())

			// Cross-pod reconstruction is eventually consistent: pod B must reconstruct
			// the session from Redis and warm its per-identity capability view, so retry
			// rather than asserting on a single immediate ListTools (which flakes under
			// parallel CI load). A genuine inability to serve still fails when the poll
			// times out.
			ginkgo.By("Verifying pod B can serve the reconstructed session")
			gomega.Eventually(func() error {
				listCtxB, listCancelB := context.WithTimeout(context.Background(), 30*time.Second)
				defer listCancelB()
				toolsB, listErr := clientB.ListTools(listCtxB, mcp.ListToolsRequest{})
				if listErr != nil {
					return listErr
				}
				if len(toolsB.Tools) == 0 {
					return fmt.Errorf("pod B returned no tools for the reconstructed session yet")
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(),
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
				if !errors.Is(listErr, transport.ErrSessionTerminated) {
					return fmt.Errorf("expected ErrSessionTerminated (404), got: %w", listErr)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(),
				"pod B should reject the session after it is terminated on pod A")
		})
	})

	// -------------------------------------------------------------------------
	// Context 5: cross-pod session restore with upstreamInject outgoing auth
	// Exercises the fix from #5650: context.WithoutCancel in loadSession
	// ensures the incoming request's identity (with UpstreamTokens) reaches
	// RestoreSession, allowing upstreamInject to authenticate backend requests
	// after cross-pod restore.
	// -------------------------------------------------------------------------

	ginkgo.Context("When cross-pod session restore preserves upstreamInject identity", ginkgo.Ordered, func() {
		const (
			timeout      = 5 * time.Minute
			pollInterval = 2 * time.Second
		)

		var (
			mcpGroupName            string
			backendName             string
			vmcpName                string
			redisName               string
			dexName                 string
			externalAuthConfigName  string
			oidcConfigName          string
			signingKeySecretName    string
			hmacSecretName          string
			redisPasswordSecretName string
			dexClientSecretName     string
			dexInfo                 *DexInfo
			cleanupDexFn            func()
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-oidc-group-%d", ts)
			backendName = fmt.Sprintf("e2e-oidc-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-oidc-vmcp-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-oidc-%d", ts)
			dexName = fmt.Sprintf("e2e-dex-%d", ts)
			externalAuthConfigName = fmt.Sprintf("e2e-ui-config-%d", ts)
			oidcConfigName = fmt.Sprintf("e2e-oidc-cfg-%d", ts)
			signingKeySecretName = fmt.Sprintf("e2e-as-key-%d", ts)
			hmacSecretName = fmt.Sprintf("e2e-as-hmac-%d", ts)
			redisPasswordSecretName = fmt.Sprintf("e2e-redis-pw-%d", ts)
			dexClientSecretName = fmt.Sprintf("e2e-dex-secret-%d", ts)

			// The embedded AS issuer is the vMCP service URL (http://vmcp-<name>.ns.svc:4483).
			// The operator derives AllowedAudiences from oidcRef.ResourceURL when set;
			// we set it explicitly so the audience in issued JWTs matches the OIDC config.
			// Validation: rc.Issuer == oidc.Issuer AND oidc.Audience ∈ rc.AllowedAudiences.
			vmcpServiceHost := fmt.Sprintf("vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)
			embeddedASIssuerURL := fmt.Sprintf("http://%s", vmcpServiceHost)
			vmcpCallbackURL := fmt.Sprintf("http://%s/oauth/callback", vmcpServiceHost)
			// resourceURL = audience in JWTs = AllowedAudiences entry = vMCP service URL
			vmcpResourceURL := embeddedASIssuerURL

			// Use a real Redis password so the secretKeyRef env var is non-empty
			// (Kubernetes does not inject secretKeyRef env vars whose Secret key
			// value is empty, which would prevent the embedded AS from starting).
			const redisPassword = "e2e-test-redis-password"

			ginkgo.By("Deploying Redis (shared: session storage + embedded AS token store)")
			deployRedisWithPassword(redisName, redisPassword)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating embedded AS RSA signing key Secret")
			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			privateKeyPEM := pem.EncodeToMemory(&pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
			})
			gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: signingKeySecretName, Namespace: defaultNamespace},
				Data:       map[string][]byte{"private-key": privateKeyPEM},
			})).To(gomega.Succeed())

			ginkgo.By("Creating embedded AS HMAC secret")
			hmacBytes := make([]byte, 32)
			_, err = rand.Read(hmacBytes)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: hmacSecretName, Namespace: defaultNamespace},
				Data:       map[string][]byte{"hmac": hmacBytes},
			})).To(gomega.Succeed())

			ginkgo.By("Creating Redis password Secret for embedded AS token storage")
			gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: redisPasswordSecretName, Namespace: defaultNamespace},
				StringData: map[string]string{"password": redisPassword},
			})).To(gomega.Succeed())

			ginkgo.By("Creating Dex client secret for embedded AS")
			gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: dexClientSecretName, Namespace: defaultNamespace},
				StringData: map[string]string{"client-secret": "authserver-secret"},
			})).To(gomega.Succeed())

			ginkgo.By("Creating MCPExternalAuthConfig with upstreamInject type")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: externalAuthConfigName, Namespace: defaultNamespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type:           mcpv1beta1.ExternalAuthTypeUpstreamInject,
					UpstreamInject: &mcpv1beta1.UpstreamInjectSpec{ProviderName: "dex"},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Creating MCPGroup")
			CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, defaultNamespace,
				"E2E upstreamInject restore group", timeout, pollInterval)

			ginkgo.By("Creating MCPOIDCConfig pointing at embedded AS issuer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer:             embeddedASIssuerURL,
						InsecureAllowHTTP:  true,
						JWKSAllowPrivateIP: true,
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Deploying Dex with mockCallback connector")
			var dexCleanup func()
			dexInfo, dexCleanup = deployDex(ctx, k8sClient, dexName, defaultNamespace,
				vmcpCallbackURL, timeout, pollInterval)
			cleanupDexFn = dexCleanup

			ginkgo.By("Creating instrumented MCP backend MCPServer")
			// The proxy runner creates the inner container using:
			//   command = patch.Containers[mcp].Command  (preserved)
			//   args    = MCPServer.Spec.Args            (configureContainer calls WithArgs(Spec.Args))
			// So the patch sets the entrypoint override (sh -c) and Spec.Args carries the script.
			// The proxy runner applies ReadOnlyRootFilesystem to the inner mcp container
			// via the platform-aware security context. pip install needs a writable /tmp,
			// so mount a writable emptyDir there.
			mcpPodPatch := corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "mcp",
						Command: []string{"sh", "-c"},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "tmp",
							MountPath: "/tmp",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "tmp",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			}
			mcpPodPatchRaw, err := json.Marshal(mcpPodPatch)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewMCPServer(
				backendName, defaultNamespace,
				v1beta1test.WithImage(images.PythonImage),
				v1beta1test.WithTransport("streamable-http"),
				v1beta1test.WithProxyPort(8080),
				v1beta1test.WithMCPPort(8080),
				v1beta1test.WithMCPGroupRef(mcpGroupName),
				v1beta1test.WithExternalAuthConfigRef(externalAuthConfigName),
				v1beta1test.WithPodTemplateSpec(&runtime.RawExtension{Raw: mcpPodPatchRaw}),
				v1beta1test.WithArgs(InstrumentedMCPBackendScript),
			))).To(gomega.Succeed())

			ginkgo.By("Waiting for backend MCPServer to be ready")
			gomega.Eventually(func() error {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: backendName, Namespace: defaultNamespace,
				}, server); err != nil {
					return fmt.Errorf("failed to get MCPServer: %w", err)
				}
				if server.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
					return fmt.Errorf("MCPServer not ready, phase: %s", server.Status.Phase)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(), "backend MCPServer should be ready")

			replicas := int32(2)
			dexInClusterBaseURL := dexInfo.InClusterBaseURL

			ginkgo.By("Creating VirtualMCPServer with embedded AS, OIDC auth, upstreamInject, and Redis")
			gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
				v1beta1test.WithVMCPGroupRef(mcpGroupName),
				v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
					Type: "oidc",
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
						Name:        oidcConfigName,
						Audience:    vmcpResourceURL,
						ResourceURL: vmcpResourceURL,
					},
				}),
				v1beta1test.WithVMCPOutgoingAuth(&mcpv1beta1.OutgoingAuthConfig{
					Source: "discovered",
				}),
				v1beta1test.WithVMCPReplicas(replicas),
				v1beta1test.WithVMCPSessionStorage(&mcpv1beta1.SessionStorageConfig{
					Provider:  mcpv1beta1.SessionStorageProviderRedis,
					Address:   redisAddr,
					KeyPrefix: "thv:vmcp:e2e:auth:",
					PasswordRef: &mcpv1beta1.SecretKeyRef{
						Name: redisPasswordSecretName,
						Key:  "password",
					},
				}),
				v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer:            embeddedASIssuerURL,
					InsecureAllowHTTP: true,
					SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{
						Name: signingKeySecretName,
						Key:  "private-key",
					}},
					HMACSecretRefs: []mcpv1beta1.SecretKeyRef{{
						Name: hmacSecretName,
						Key:  "hmac",
					}},
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
						Name: "dex",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: dexInClusterBaseURL + "/auth",
							TokenEndpoint:         dexInClusterBaseURL + "/token",
							ClientID:              "vmcp-authserver",
							Scopes:                []string{"openid", "profile", "email", "offline_access"},
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: dexClientSecretName,
								Key:  "client-secret",
							},
						},
					}},
					Storage: &mcpv1beta1.AuthServerStorageConfig{
						Type: mcpv1beta1.AuthServerStorageTypeRedis,
						Redis: &mcpv1beta1.RedisStorageConfig{
							Addr: redisAddr,
							ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
								PasswordSecretRef: &mcpv1beta1.SecretKeyRef{
									Name: redisPasswordSecretName,
									Key:  "password",
								},
							},
						},
					},
				}),
				v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
					v.Spec.SessionAffinity = "None"
				}),
			))).To(gomega.Succeed())

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Waiting for VirtualMCPServer to report Ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)

			ginkgo.By("Waiting for AuthServerConfig to be validated")
			WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace,
				mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace))
			_ = k8sClient.Delete(ctx, v1beta1test.NewMCPServer(backendName, defaultNamespace))
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: externalAuthConfigName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			})
			for _, name := range []string{signingKeySecretName, hmacSecretName, redisPasswordSecretName, dexClientSecretName} {
				_ = k8sClient.Delete(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
				})
			}
			if cleanupDexFn != nil {
				cleanupDexFn()
			}
			cleanupRedis(redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace},
					&mcpv1beta1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should inject upstream tokens into backend requests after cross-pod restore", func() {
			vmcpServiceHost := fmt.Sprintf("vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)
			dexInClusterHost := fmt.Sprintf("%s.%s.svc.cluster.local:5556", dexName, defaultNamespace)
			// These must match what BeforeAll configured: issuer = service URL, audience = resource URL.
			embeddedASIssuerURL := fmt.Sprintf("http://%s", vmcpServiceHost)
			vmcpResourceURL := embeddedASIssuerURL
			backendServiceName := fmt.Sprintf("mcp-%s-proxy", backendName)

			ginkgo.By("Port-forwarding to backend /stats endpoint")
			backendStatsPort, cleanupBackendFwd, err := portForwardToService(backendServiceName, 8080)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupBackendFwd()
			backendStatsURL := fmt.Sprintf("http://localhost:%d/stats", backendStatsPort)

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
			gomega.Expect(podA.Name).NotTo(gomega.Equal(podB.Name))

			ginkgo.By(fmt.Sprintf("Port-forwarding to pod A (%s)", podA.Name))
			localPortA, cleanupA, err := portForwardToPod(podA.Name, vmcpPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupA()

			ginkgo.By(fmt.Sprintf("Port-forwarding to pod B (%s)", podB.Name))
			localPortB, cleanupB, err := portForwardToPod(podB.Name, vmcpPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupB()

			// Obtain an embedded AS JWT via the OAuth2 PKCE flow. This JWT has a
			// "tsid" claim that links to the Dex upstream tokens stored in Redis by
			// the embedded AS during the auth code exchange.
			ginkgo.By("Obtaining embedded AS JWT via OAuth2 PKCE flow against pod A")
			vmcpLocalURL := fmt.Sprintf("http://localhost:%d", localPortA)
			asToken, err := getEmbeddedASToken(
				vmcpLocalURL,
				dexInfo.LocalURL,
				dexInClusterHost,
				vmcpServiceHost,
				vmcpResourceURL,
			)
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "OAuth2 PKCE flow should succeed")
			gomega.Expect(asToken).NotTo(gomega.BeEmpty())

			// Authorization header value carrying the embedded AS JWT. Both pod A and
			// pod B clients share this token; OIDC validation in each vMCP pod reads
			// upstream tokens from the shared Redis token store (keyed by tsid).
			authHeader := map[string]string{
				"Authorization": fmt.Sprintf("Bearer %s", asToken),
			}

			ginkgo.By("Initializing MCP session on pod A with embedded AS token")
			serverURLPodA := fmt.Sprintf("http://localhost:%d/mcp", localPortA)
			clientA, err := mcpclient.NewStreamableHttpClient(serverURLPodA,
				transport.WithHTTPHeaders(authHeader))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer func() { _ = clientA.Close() }()

			initCtxA, initCancelA := context.WithTimeout(context.Background(), 60*time.Second)
			defer initCancelA()
			gomega.Expect(clientA.Start(initCtxA)).To(gomega.Succeed(),
				"MCP client should start on pod A — check embedded AS JWT validity and OIDC config")

			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
			initRequest.Params.ClientInfo = mcp.Implementation{
				Name:    "e2e-upstreamInject-test",
				Version: "1.0.0",
			}
			_, err = clientA.Initialize(initCtxA, initRequest)
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Initialize on pod A should succeed")

			sessionID := clientA.GetSessionId()
			gomega.Expect(sessionID).NotTo(gomega.BeEmpty(), "session ID must be assigned after Initialize")

			ginkgo.By("Listing tools on pod A to verify the session is functional")
			toolsCtxA, toolsCancelA := context.WithTimeout(context.Background(), 30*time.Second)
			defer toolsCancelA()
			toolsA, err := clientA.ListTools(toolsCtxA, mcp.ListToolsRequest{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(toolsA.Tools).NotTo(gomega.BeEmpty(),
				"pod A must return tools after Initialize")

			ginkgo.By("Verifying backend received a Bearer token on pod A's Initialize (upstreamInject fired)")
			// The embedded AS stored the Dex access token in Redis (keyed by tsid).
			// The OIDC middleware on pod A reads it and places it in Identity.UpstreamTokens["dex"].
			// upstreamInject injects this token into the backend Initialize request.
			// InitializeCalls is only incremented on the "initialize" JSON-RPC method,
			// making this assertion precise: it is not inflated by ListTools or CallTool traffic.
			gomega.Eventually(func() (int, error) {
				stats, statsErr := GetInstrumentedMCPBackendStatsFromURL(backendStatsURL)
				if statsErr != nil {
					return 0, statsErr
				}
				return stats.InitializeCalls, nil
			}, 30*time.Second, 2*time.Second).Should(gomega.BeNumerically(">=", 1),
				"backend must have received at least one Initialize call from pod A")

			ginkgo.By(fmt.Sprintf("Connecting to pod B (%s) with the same session ID (triggers RestoreSession)", podB.Name))
			serverURLPodB := fmt.Sprintf("http://localhost:%d/mcp", localPortB)
			clientB, err := mcpclient.NewStreamableHttpClient(serverURLPodB,
				transport.WithSession(sessionID),
				transport.WithHTTPHeaders(authHeader))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer func() { _ = clientB.Close() }()

			startCtxB, startCancelB := context.WithTimeout(context.Background(), 60*time.Second)
			defer startCancelB()
			gomega.Expect(clientB.Start(startCtxB)).To(gomega.Succeed())

			ginkgo.By("Listing tools on pod B using the restored session")
			listCtxB, listCancelB := context.WithTimeout(context.Background(), 30*time.Second)
			defer listCancelB()
			toolsB, err := clientB.ListTools(listCtxB, mcp.ListToolsRequest{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred(),
				"pod B must not return an error — with the context fix, the identity "+
					"(including UpstreamTokens) is propagated to RestoreSession so the "+
					"backend Initialize is authenticated; without the fix this 401s")
			gomega.Expect(toolsB.Tools).NotTo(gomega.BeEmpty(),
				"pod B must return tools via the Redis-reconstructed session")

			ginkgo.By("Verifying backend received a second Initialize call from pod B's RestoreSession")
			// This assertion fails if context.WithoutCancel in loadSession is reverted
			// to context.Background(): without the fix, RestoreSession would call
			// Initialize with no identity, upstreamInject would fail to find
			// UpstreamTokens["dex"], and the backend Initialize would fail — keeping
			// initialize_calls at 1.  InitializeCalls is only incremented on the
			// "initialize" JSON-RPC method so ListTools and CallTool traffic cannot
			// spuriously satisfy this threshold.
			gomega.Eventually(func() (int, error) {
				stats, statsErr := GetInstrumentedMCPBackendStatsFromURL(backendStatsURL)
				if statsErr != nil {
					return 0, statsErr
				}
				return stats.InitializeCalls, nil
			}, 30*time.Second, 2*time.Second).Should(gomega.BeNumerically(">=", 2),
				"backend must have received two Initialize calls (pod A + pod B RestoreSession); "+
					"this assertion fails if context.WithoutCancel in loadSession is reverted to context.Background()")

			ginkgo.By("Verifying upstreamInject injected a Bearer token into both Initialize calls")
			// InitializeCalls >= 2 proves RestoreSession ran; BearerTokenRequests >= 2
			// proves upstreamInject actually fired on both calls. The two assertions
			// together fully satisfy the acceptance criteria: the restored session on
			// pod B must authenticate the backend Initialize, not just reach it.
			gomega.Eventually(func() (int, error) {
				stats, statsErr := GetInstrumentedMCPBackendStatsFromURL(backendStatsURL)
				if statsErr != nil {
					return 0, statsErr
				}
				return stats.BearerTokenRequests, nil
			}, 30*time.Second, 2*time.Second).Should(gomega.BeNumerically(">=", 2),
				"upstreamInject must have injected a Bearer token into both Initialize calls "+
					"(pod A + pod B RestoreSession)")
		})
	})
})
