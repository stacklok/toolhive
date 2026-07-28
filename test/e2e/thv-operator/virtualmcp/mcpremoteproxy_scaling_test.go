// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpclient "github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

// getReadyMCPRemoteProxyPods returns all Running+Ready proxy pods for an MCPRemoteProxy.
//
//nolint:unparam // namespace kept as parameter to mirror getReadyMCPServerPods
func getReadyMCPRemoteProxyPods(proxyName, namespace string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := k8sClient.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"app.kubernetes.io/name":     "mcpremoteproxy",
			"app.kubernetes.io/instance": proxyName,
		}); err != nil {
		return nil, err
	}
	var ready []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				ready = append(ready, pod)
				break
			}
		}
	}
	return ready, nil
}

var _ = ginkgo.Describe("MCPRemoteProxy Cross-Replica Session Routing with Redis", func() {

	// MCPRemoteProxy differs from MCPServer/VirtualMCPServer in that it targets a
	// remote MCP server by URL rather than a deployed container. The fixture stays
	// close to the sibling tests by pointing the proxy at a backend MCPServer
	// already running in the cluster, reachable by its in-cluster Service URL.
	ginkgo.Context("When MCPRemoteProxy has replicas=2 with Redis session storage", ginkgo.Ordered, func() {
		var (
			proxyName   string
			backendName string
			redisName   string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			proxyName = fmt.Sprintf("e2e-remoteproxy-scale-%d", ts)
			backendName = fmt.Sprintf("e2e-remoteproxy-backend-%d", ts)
			redisName = fmt.Sprintf("e2e-remoteproxy-redis-%d", ts)

			ginkgo.By("Deploying Redis for session storage")
			deployRedis(redisName)

			ginkgo.By("Creating the backend MCPServer that the proxy targets")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: proxyPort,
					MCPPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for the backend MCPServer to be Running")
			testutil.WaitForMCPServerRunning(ctx, k8sClient, backendName, defaultNamespace, e2eTimeout, e2ePollInterval)

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)
			// The backend MCPServer's proxy Service is reachable in-cluster at
			// mcp-<name>-proxy.<ns>.svc, serving MCP at /mcp. We deliberately use the
			// ".svc" short form rather than the ".svc.cluster.local" FQDN: the operator's
			// ValidateRemoteURL rejects "*.cluster.local" as SSRF protection (production
			// MCPRemoteProxy targets external servers, never in-cluster ones). The ".svc"
			// form is not on the blocklist and still resolves via the pod's DNS search
			// domains, letting the test exercise the proxy against an in-cluster backend.
			backendURL := fmt.Sprintf("http://mcp-%s-proxy.%s.svc:%d/mcp",
				backendName, defaultNamespace, proxyPort)

			ginkgo.By("Creating MCPRemoteProxy with replicas=2, Redis session storage, and sessionAffinity=None")
			gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewMCPRemoteProxy(proxyName, defaultNamespace,
				v1beta1test.WithRemoteProxyURL(backendURL),
				v1beta1test.WithRemoteProxyTransport("streamable-http"),
				v1beta1test.WithRemoteProxyPort(proxyPort),
				v1beta1test.WithRemoteProxyReplicas(replicas),
				v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
					p.Spec.SessionAffinity = "None"
				}),
				v1beta1test.WithRemoteProxySessionStorage(&mcpv1beta1.SessionStorageConfig{
					Provider:  mcpv1beta1.SessionStorageProviderRedis,
					Address:   redisAddr,
					KeyPrefix: "thv:remoteproxy:e2e:",
				}),
			))).To(gomega.Succeed())

			ginkgo.By("Waiting for MCPRemoteProxy to reach the Ready phase")
			gomega.Eventually(func() error {
				proxy := &mcpv1beta1.MCPRemoteProxy{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: proxyName, Namespace: defaultNamespace}, proxy); err != nil {
					return err
				}
				if proxy.Status.Phase == mcpv1beta1.MCPRemoteProxyPhaseFailed {
					return gomega.StopTrying(fmt.Sprintf("MCPRemoteProxy %s failed: %s", proxyName, proxy.Status.Message))
				}
				if proxy.Status.Phase != mcpv1beta1.MCPRemoteProxyPhaseReady {
					return fmt.Errorf("MCPRemoteProxy %s not ready, phase: %s", proxyName, proxy.Status.Phase)
				}
				return nil
			}, e2eTimeout, e2ePollInterval).Should(gomega.Succeed())

			ginkgo.By("Waiting for 2 ready proxy pods")
			gomega.Eventually(func() (int, error) {
				pods, err := getReadyMCPRemoteProxyPods(proxyName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				return len(pods), nil
			}, e2eTimeout, e2ePollInterval).Should(gomega.Equal(2))
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, v1beta1test.NewMCPRemoteProxy(proxyName, defaultNamespace))
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)

			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: proxyName, Namespace: defaultNamespace}, &mcpv1beta1.MCPRemoteProxy{})
				return apierrors.IsNotFound(err)
			}, e2eTimeout, e2ePollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should have SessionStorageWarning=False since Redis is configured", func() {
			gomega.Eventually(func() error {
				proxy := &mcpv1beta1.MCPRemoteProxy{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: proxyName, Namespace: defaultNamespace}, proxy); err != nil {
					return err
				}
				for _, cond := range proxy.Status.Conditions {
					if cond.Type == mcpv1beta1.ConditionSessionStorageWarning {
						if string(cond.Status) == "False" {
							return nil
						}
						return fmt.Errorf("SessionStorageWarning is %s (reason: %s), want False",
							cond.Status, cond.Reason)
					}
				}
				return fmt.Errorf("SessionStorageWarning condition not found")
			}, e2eTimeout, e2ePollInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should route a session established on proxy A through proxy B via Redis-shared state", func() {
			ginkgo.By("Getting the two ready proxy pods")
			var pods []corev1.Pod
			gomega.Eventually(func() (int, error) {
				var err error
				pods, err = getReadyMCPRemoteProxyPods(proxyName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				return len(pods), nil
			}, e2eTimeout, e2ePollInterval).Should(gomega.Equal(2))

			podA := pods[0]
			podB := pods[1]
			gomega.Expect(podA.Name).NotTo(gomega.Equal(podB.Name),
				"The two proxy pods must be distinct")

			ginkgo.By(fmt.Sprintf("Setting up port-forward to proxy A (%s)", podA.Name))
			localPortA, cleanupA, err := portForwardToPod(podA.Name, proxyPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupA()

			ginkgo.By(fmt.Sprintf("Setting up port-forward to proxy B (%s)", podB.Name))
			localPortB, cleanupB, err := portForwardToPod(podB.Name, proxyPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupB()

			ginkgo.By("Initializing a session on proxy A")
			clientA, err := CreateInitializedMCPClient(int32(localPortA), "e2e-remoteproxy-xpod-test", 30*time.Second)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer clientA.Close()

			sessionID := clientA.Client.GetSessionId()
			gomega.Expect(sessionID).NotTo(gomega.BeEmpty(), "session ID must be assigned after Initialize")

			ginkgo.By(fmt.Sprintf("Listing tools on proxy A (%s)", podA.Name))
			toolsA, err := clientA.Client.ListTools(clientA.Ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(toolsA.Tools).NotTo(gomega.BeEmpty(),
				"proxy A should return tools for this session")

			ginkgo.By(fmt.Sprintf("Creating a new client to proxy B (%s) with the SAME session ID", podB.Name))
			serverURLB := fmt.Sprintf("http://localhost:%d/mcp", localPortB)
			clientB, err := mcpclient.NewStreamableHttpClient(serverURLB, transport.WithSession(sessionID))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer func() { _ = clientB.Close() }()

			startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer startCancel()
			gomega.Expect(clientB.Start(startCtx)).To(gomega.Succeed())

			// Proxy B's transparent proxy validates the Mcp-Session-Id against the
			// shared store and rewrites the client-facing session ID to the backend
			// session ID on every hop. Without Redis, proxy B has no record of the
			// session created on proxy A and rejects the request ("Session not
			// found"). Five consecutive successes give confidence the cross-pod
			// lookup and per-hop rewrite are working via shared Redis state.
			ginkgo.By("Sending 5 requests on proxy B to verify the session resolves via shared Redis state")
			for i := range 5 {
				listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
				toolsB, listErr := clientB.ListTools(listCtx, mcp.ListToolsRequest{})
				listCancel()
				gomega.Expect(listErr).NotTo(gomega.HaveOccurred(),
					"Request %d/5 on proxy B should succeed — the session created on proxy A must resolve via Redis", i+1)
				gomega.Expect(toolsB.Tools).To(gomega.HaveLen(len(toolsA.Tools)),
					"Request %d/5 on proxy B should return the same tools as proxy A", i+1)
			}
		})
	})
})
