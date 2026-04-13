// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

const (
	proxyPort = int32(8080) // MCPServer proxy container port
	vmcpPort  = int32(4483) // VirtualMCPServer container port
)

// deployRedis creates a single-replica Redis Deployment and ClusterIP Service.
// Returns after the deployment has at least one ready replica.
func deployRedis(name string) {
	labels := map[string]string{"app": name}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "redis",
						Image: images.RedisImage,
						Ports: []corev1.ContainerPort{{ContainerPort: 6379, Name: "redis"}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								TCPSocket: &corev1.TCPSocketAction{
									Port: intstr.FromInt32(6379),
								},
							},
							InitialDelaySeconds: 2,
							PeriodSeconds:       3,
						},
					}},
				},
			},
		},
	}
	gomega.Expect(k8sClient.Create(ctx, deployment)).To(gomega.Succeed())

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Port:       6379,
				TargetPort: intstr.FromInt32(6379),
				Protocol:   corev1.ProtocolTCP,
				Name:       "redis",
			}},
		},
	}
	gomega.Expect(k8sClient.Create(ctx, service)).To(gomega.Succeed())

	ginkgo.By("Waiting for Redis to become ready")
	gomega.Eventually(func() bool {
		dep := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: defaultNamespace}, dep); err != nil {
			return false
		}
		return dep.Status.ReadyReplicas > 0
	}, e2eTimeout, e2ePollInterval).Should(gomega.BeTrue(), "Redis should be ready")
}

// cleanupRedis removes the Redis Deployment and Service.
func cleanupRedis(name string) {
	_ = k8sClient.Delete(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
	})
	_ = k8sClient.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
	})
}

// getReadyMCPServerPods returns all Running+Ready pods for an MCPServer.
//
//nolint:unparam // namespace kept as parameter for reusability across test contexts
func getReadyMCPServerPods(mcpServerName, namespace string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := k8sClient.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"app.kubernetes.io/name":     "mcpserver",
			"app.kubernetes.io/instance": mcpServerName,
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

// portForwardToPod starts a kubectl port-forward to a specific pod's containerPort and
// returns the local port and a cleanup function. The caller must call cleanup to stop
// the port-forward.
func portForwardToPod(podName string, containerPort int32) (int, func(), error) {
	// Find a free local port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, nil, fmt.Errorf("failed to find free port: %w", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	// Close immediately so kubectl can bind to it
	_ = listener.Close()

	kubeconfigArg := fmt.Sprintf("--kubeconfig=%s", kubeconfig)
	//nolint:gosec // kubeconfig, podName, and ports are test-controlled values
	cmd := exec.Command("kubectl", kubeconfigArg,
		"-n", defaultNamespace, "port-forward",
		fmt.Sprintf("pod/%s", podName),
		fmt.Sprintf("%d:%d", localPort, containerPort))
	if err := cmd.Start(); err != nil {
		return 0, nil, fmt.Errorf("failed to start port-forward to %s: %w", podName, err)
	}

	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}

	// Wait for the port-forward to be ready
	for range 30 {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", localPort), 500*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return localPort, cleanup, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	cleanup()
	return 0, nil, fmt.Errorf("port-forward to %s never became ready on localhost:%d", podName, localPort)
}

var _ = ginkgo.Describe("MCPServer Cross-Replica Session Routing with Redis", func() {

	ginkgo.Context("When MCPServer has replicas=2 and backendReplicas=2", ginkgo.Ordered, func() {
		var (
			mcpServerName string
			redisName     string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpServerName = fmt.Sprintf("e2e-backend-scale-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-be-%d", ts)

			ginkgo.By("Deploying Redis for session storage")
			deployRedis(redisName)

			replicas := int32(2)
			backendReplicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating MCPServer with replicas=2, backendReplicas=2, Redis session storage")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:           images.YardstickServerImage,
					Transport:       "streamable-http",
					ProxyPort:       proxyPort,
					MCPPort:         8080,
					Replicas:        &replicas,
					BackendReplicas: &backendReplicas,
					SessionAffinity: "None",
					SessionStorage: &mcpv1alpha1.SessionStorageConfig{
						Provider: mcpv1alpha1.SessionStorageProviderRedis,
						Address:  redisAddr,
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for MCPServer to be Running")
			testutil.WaitForMCPServerRunning(ctx, k8sClient, mcpServerName, defaultNamespace, e2eTimeout, e2ePollInterval)

			ginkgo.By("Waiting for 2 ready proxy runner pods")
			gomega.Eventually(func() (int, error) {
				pods, err := getReadyMCPServerPods(mcpServerName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				return len(pods), nil
			}, e2eTimeout, e2ePollInterval).Should(gomega.Equal(2))
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)

			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mcpServerName, Namespace: defaultNamespace}, &mcpv1alpha1.MCPServer{})
				return apierrors.IsNotFound(err)
			}, e2eTimeout, e2ePollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should route session from proxy A to proxy B via Redis-shared state", func() {
			ginkgo.By("Getting the two ready proxy pods")
			var pods []corev1.Pod
			gomega.Eventually(func() (int, error) {
				var err error
				pods, err = getReadyMCPServerPods(mcpServerName, defaultNamespace)
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
			clientA, err := CreateInitializedMCPClient(int32(localPortA), "e2e-cross-proxy-test", 30*time.Second)
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

			// Proxy B must route the session's backend requests to the correct
			// backend pod (the one that handled initialize on proxy A). With 2
			// backends and random ClusterIP routing, P(all 5 hit the right pod
			// by chance) ≈ 3%, so 5 consecutive successes give high confidence
			// that Redis-backed session routing is working.
			ginkgo.By("Sending 5 requests on proxy B to verify consistent backend routing")
			for i := range 5 {
				listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
				toolsB, listErr := clientB.ListTools(listCtx, mcp.ListToolsRequest{})
				listCancel()
				gomega.Expect(listErr).NotTo(gomega.HaveOccurred(),
					"Request %d/5 on proxy B should succeed — session should route to the correct backend", i+1)
				gomega.Expect(toolsB.Tools).To(gomega.HaveLen(len(toolsA.Tools)),
					"Request %d/5 on proxy B should return the same tools as proxy A", i+1)
			}
		})
	})

	ginkgo.Context("When MCPServer has replicas=2 with Redis session storage", ginkgo.Ordered, func() {
		var (
			mcpServerName string
			redisName     string
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpServerName = fmt.Sprintf("e2e-scale-redis-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-%d", ts)

			ginkgo.By("Deploying Redis for session storage")
			deployRedis(redisName)

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating MCPServer with replicas=2, Redis session storage, and sessionAffinity=None")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:           images.YardstickServerImage,
					Transport:       "streamable-http",
					ProxyPort:       proxyPort,
					MCPPort:         8080,
					Replicas:        &replicas,
					SessionAffinity: "None",
					SessionStorage: &mcpv1alpha1.SessionStorageConfig{
						Provider: mcpv1alpha1.SessionStorageProviderRedis,
						Address:  redisAddr,
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for MCPServer to be Running")
			testutil.WaitForMCPServerRunning(ctx, k8sClient, mcpServerName, defaultNamespace, e2eTimeout, e2ePollInterval)

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				pods, err := getReadyMCPServerPods(mcpServerName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				return len(pods), nil
			}, e2eTimeout, e2ePollInterval).Should(gomega.Equal(2))
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: defaultNamespace},
			})
			cleanupRedis(redisName)

			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mcpServerName, Namespace: defaultNamespace}, &mcpv1alpha1.MCPServer{})
				return apierrors.IsNotFound(err)
			}, e2eTimeout, e2ePollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should have SessionStorageWarning=False since Redis is configured", func() {
			gomega.Eventually(func() error {
				server := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: mcpServerName, Namespace: defaultNamespace}, server); err != nil {
					return err
				}
				for _, cond := range server.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionSessionStorageWarning {
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

		ginkgo.It("Should allow a session established on pod A to be used on pod B", func() {
			ginkgo.By("Getting the two ready pods")
			var pods []corev1.Pod
			gomega.Eventually(func() (int, error) {
				var err error
				pods, err = getReadyMCPServerPods(mcpServerName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				return len(pods), nil
			}, e2eTimeout, e2ePollInterval).Should(gomega.Equal(2))

			podA := pods[0]
			podB := pods[1]
			gomega.Expect(podA.Name).NotTo(gomega.Equal(podB.Name),
				"The two pods must be distinct")

			ginkgo.By(fmt.Sprintf("Setting up port-forward to pod A (%s)", podA.Name))
			localPortA, cleanupA, err := portForwardToPod(podA.Name, proxyPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupA()

			ginkgo.By(fmt.Sprintf("Setting up port-forward to pod B (%s)", podB.Name))
			localPortB, cleanupB, err := portForwardToPod(podB.Name, proxyPort)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer cleanupB()

			ginkgo.By("Initializing a session on pod A")
			clientA, err := CreateInitializedMCPClient(int32(localPortA), "e2e-cross-pod-test", 30*time.Second)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer clientA.Close()

			sessionID := clientA.Client.GetSessionId()
			gomega.Expect(sessionID).NotTo(gomega.BeEmpty(), "session ID must be assigned after Initialize")

			ginkgo.By(fmt.Sprintf("Listing tools on pod A (%s)", podA.Name))
			toolsA, err := clientA.Client.ListTools(clientA.Ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(toolsA.Tools).NotTo(gomega.BeEmpty(),
				"pod A should return tools for this session")

			ginkgo.By(fmt.Sprintf("Creating a new client to pod B (%s) with the SAME session ID", podB.Name))
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
			gomega.Expect(toolsB.Tools).NotTo(gomega.BeEmpty(),
				"pod B should return tools via Redis-shared session")

			ginkgo.By("Verifying both pods returned the same tool count")
			gomega.Expect(toolsB.Tools).To(gomega.HaveLen(len(toolsA.Tools)),
				"Both replicas should see the same session state and return identical tools")
		})
	})
})
