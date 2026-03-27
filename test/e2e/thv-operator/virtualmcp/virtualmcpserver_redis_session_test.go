// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"fmt"
	"time"

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

var _ = ginkgo.Describe("VirtualMCPServer Redis Session Continuity", func() {
	const (
		timeout      = time.Minute * 5
		pollInterval = time.Second * 2
	)

	// -------------------------------------------------------------------------
	// Context: session survives complete pod replacement when Redis is configured
	// -------------------------------------------------------------------------

	ginkgo.Context("When sessions are backed by Redis", ginkgo.Ordered, func() {
		var (
			mcpGroupName string
			backendName  string
			vmcpName     string
			redisName    string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-redis-session-%d", ts)
			backendName = fmt.Sprintf("e2e-redis-backend-%d", ts)
			vmcpName = fmt.Sprintf("e2e-redis-vmcp-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-%d", ts)

			ginkgo.By("Deploying Redis session storage backend")
			DeployRedis(ctx, k8sClient, testNamespace, redisName)

			ginkgo.By("Creating MCPGroup")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
				Spec:       mcpv1alpha1.MCPGroupSpec{Description: "E2E Redis session group"},
			})).To(gomega.Succeed())

			ginkgo.By("Creating backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			replicas := int32(2)
			ginkgo.By("Creating VirtualMCPServer with 2 replicas and Redis session storage")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: testNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:       vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Replicas:     &replicas,
					ServiceType:  "NodePort",
					// None: requests are distributed across pods so the second request
					// is not guaranteed to land on the same pod as the first.
					SessionAffinity: string(corev1.ServiceAffinityNone),
					SessionStorage: &mcpv1alpha1.SessionStorageConfig{
						Provider: mcpv1alpha1.SessionStorageProviderRedis,
						Address:  fmt.Sprintf("%s:6379", redisName),
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for 2 VirtualMCPServer pods to become ready")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: testNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: testNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
			})
			CleanupRedis(ctx, k8sClient, testNamespace, redisName)
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: testNamespace,
				}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should not set SessionStorageWarning when Redis is configured", func() {
			WaitForCondition(ctx, k8sClient, vmcpName, testNamespace,
				mcpv1alpha1.ConditionSessionStorageWarning, "False",
				timeout, pollInterval)
		})

		ginkgo.It("Should configure the Service with SessionAffinity=None", func() {
			svc := &corev1.Service{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx,
					types.NamespacedName{Name: VMCPServiceName(vmcpName), Namespace: testNamespace}, svc)
			}, timeout, pollInterval).Should(gomega.Succeed())
			gomega.Expect(svc.Spec.SessionAffinity).To(gomega.Equal(corev1.ServiceAffinityNone),
				"Service must have SessionAffinity=None so requests are distributed across pods")
		})

		ginkgo.It("Should serve the session on fresh pods after complete pod replacement", func() {
			ginkgo.By("Getting NodePort for VirtualMCPServer")
			nodePort := GetVMCPNodePort(ctx, k8sClient, vmcpName, testNamespace, timeout, pollInterval)

			// Use a long-lived context for the client so it outlasts pod deletion and restart.
			ginkgo.By("Initializing MCP session")
			sessionClient, err := CreateInitializedMCPClient(nodePort, "redis-session-continuity-test", 10*time.Minute)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			defer sessionClient.Close()

			ginkgo.By("Sending tools/list to establish session state on the current pods")
			toolsBefore, err := sessionClient.Client.ListTools(sessionClient.Ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(toolsBefore.Tools).ToNot(gomega.BeEmpty(),
				"tools/list should return tools before pod replacement")

			ginkgo.By("Recording the names of the running vMCP pods")
			podList, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpName, testNamespace)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			var runningPodNames []string
			for _, pod := range podList.Items {
				if pod.Status.Phase == corev1.PodRunning {
					runningPodNames = append(runningPodNames, pod.Name)
				}
			}
			gomega.Expect(runningPodNames).To(gomega.HaveLen(2),
				"expected exactly 2 running vMCP pods before deletion")

			ginkgo.By("Deleting all running vMCP pods to wipe in-memory session state")
			for i := range podList.Items {
				if podList.Items[i].Status.Phase == corev1.PodRunning {
					err := k8sClient.Delete(ctx, &podList.Items[i])
					if err != nil && !apierrors.IsNotFound(err) {
						gomega.Expect(err).ToNot(gomega.HaveOccurred())
					}
				}
			}

			ginkgo.By("Waiting for all original pods to be fully deleted")
			for _, podName := range runningPodNames {
				WaitForPodDeletion(ctx, k8sClient, podName, testNamespace, timeout, pollInterval)
			}

			ginkgo.By("Waiting for 2 replacement pods to become ready")
			gomega.Eventually(func() (int, error) {
				return countReadyPods(vmcpName)
			}, timeout, pollInterval).Should(gomega.Equal(2))

			// The replacement pods have zero in-memory session state.
			// If the vMCP correctly stored the session in Redis during initialization,
			// the next request with the same Mcp-Session-Id header will be served by
			// a fresh pod that restores the session from Redis.
			ginkgo.By("Sending tools/list on the same session — replacement pods must restore it from Redis")
			gomega.Eventually(func() error {
				reqCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, innerErr := sessionClient.Client.ListTools(reqCtx, mcp.ListToolsRequest{})
				return innerErr
			}, timeout, pollInterval).Should(gomega.Succeed(),
				"tools/list must succeed after complete pod replacement: "+
					"session state must have been persisted in Redis, not only in memory")
		})
	})
})
