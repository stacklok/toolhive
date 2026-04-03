// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
)

// redisImage is the Redis container image used for session storage in scaling tests.
const redisImage = "redis:7-alpine"

// deployRedis creates a single-replica Redis Deployment and ClusterIP Service.
// Returns after the deployment has at least one ready replica.
func deployRedis(namespace, name string, timeout, pollInterval time.Duration) {
	labels := map[string]string{"app": name}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
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
						Image: redisImage,
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
			Namespace: namespace,
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
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, dep); err != nil {
			return false
		}
		return dep.Status.ReadyReplicas > 0
	}, timeout, pollInterval).Should(gomega.BeTrue(), "Redis should be ready")
}

// cleanupRedis removes the Redis Deployment and Service.
func cleanupRedis(namespace, name string) {
	_ = k8sClient.Delete(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
	_ = k8sClient.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
}

// getReadyMCPServerPods returns all Running+Ready pods for an MCPServer.
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

// waitForMCPServerRunning waits for an MCPServer to reach Running phase.
func waitForMCPServerRunning(name, namespace string, timeout, pollInterval time.Duration) {
	gomega.Eventually(func() error {
		server := &mcpv1alpha1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, server); err != nil {
			return err
		}
		if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
			return fmt.Errorf("MCPServer phase is %s, want Running", server.Status.Phase)
		}
		return nil
	}, timeout, pollInterval).Should(gomega.Succeed())
}

// getMCPServerNodePort creates a NodePort service targeting MCPServer proxy pods
// with SessionAffinity=None so requests round-robin across replicas.
// MCPServer does not expose a ServiceType field, so tests create their own NodePort service.
func getMCPServerNodePort(
	mcpServerName, testSvcName, namespace string,
	proxyPort int32,
	timeout, pollInterval time.Duration,
) int32 {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSvcName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeNodePort,
			Selector: map[string]string{
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": mcpServerName,
			},
			SessionAffinity: corev1.ServiceAffinityNone,
			Ports: []corev1.ServicePort{{
				Port:       proxyPort,
				TargetPort: intstr.FromInt32(proxyPort),
				Protocol:   corev1.ProtocolTCP,
				Name:       "http",
			}},
		},
	}
	gomega.Expect(k8sClient.Create(ctx, svc)).To(gomega.Succeed())

	var nodePort int32
	gomega.Eventually(func() error {
		fetched := &corev1.Service{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: testSvcName, Namespace: namespace}, fetched); err != nil {
			return err
		}
		if len(fetched.Spec.Ports) == 0 || fetched.Spec.Ports[0].NodePort == 0 {
			return fmt.Errorf("nodePort not assigned for service %s", testSvcName)
		}
		nodePort = fetched.Spec.Ports[0].NodePort

		if err := checkPortAccessible(nodePort, 1*time.Second); err != nil {
			return fmt.Errorf("nodePort %d not accessible: %w", nodePort, err)
		}
		if err := checkHTTPHealthReady(nodePort, 2*time.Second); err != nil {
			return fmt.Errorf("nodePort %d accessible but HTTP not ready: %w", nodePort, err)
		}
		return nil
	}, timeout, pollInterval).Should(gomega.Succeed(), "NodePort should be assigned and HTTP server ready")

	return nodePort
}

// sendJSONRPCToPod sends a raw JSON-RPC request to a specific pod IP and returns the response body.
func sendJSONRPCToPod(podIP string, port int32, sessionID, method, params string) ([]byte, int, error) {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","method":"%s","params":%s,"id":1}`, method, params)
	url := fmt.Sprintf("http://%s:%d/mcp", podIP, port)

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, err
}

var _ = ginkgo.Describe("MCPServer Cross-Replica Session Routing with Redis", func() {
	const (
		timeout          = time.Minute * 5
		pollInterval     = time.Second * 2
		defaultNamespace = "default"
		proxyPort        = int32(8080)
	)

	ginkgo.Context("When MCPServer has replicas=2 with Redis session storage", ginkgo.Ordered, func() {
		var (
			mcpServerName string
			redisName     string
			testSvcName   string
			nodePort      int32
		)

		ginkgo.BeforeAll(func() {
			ts := time.Now().UnixNano()
			mcpServerName = fmt.Sprintf("e2e-scale-redis-%d", ts)
			redisName = fmt.Sprintf("e2e-redis-%d", ts)
			testSvcName = fmt.Sprintf("e2e-scale-redis-np-%d", ts)

			ginkgo.By("Deploying Redis for session storage")
			deployRedis(defaultNamespace, redisName, timeout, pollInterval)

			replicas := int32(2)
			redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

			ginkgo.By("Creating MCPServer with replicas=2, Redis session storage, and sessionAffinity=None")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:           images.YardstickServerImage,
					Transport:       "streamable-http",
					ProxyPort:       proxyPort,
					McpPort:         8080,
					Replicas:        &replicas,
					SessionAffinity: "None",
					SessionStorage: &mcpv1alpha1.SessionStorageConfig{
						Provider: mcpv1alpha1.SessionStorageProviderRedis,
						Address:  redisAddr,
					},
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for MCPServer to be Running")
			waitForMCPServerRunning(mcpServerName, defaultNamespace, timeout, pollInterval)

			ginkgo.By("Waiting for 2 ready pods")
			gomega.Eventually(func() (int, error) {
				pods, err := getReadyMCPServerPods(mcpServerName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				return len(pods), nil
			}, timeout, pollInterval).Should(gomega.Equal(2))

			ginkgo.By("Creating NodePort service for external access")
			nodePort = getMCPServerNodePort(mcpServerName, testSvcName, defaultNamespace, proxyPort, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: testSvcName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: defaultNamespace},
			})
			cleanupRedis(defaultNamespace, redisName)

			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mcpServerName, Namespace: defaultNamespace}, &mcpv1alpha1.MCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
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
			}, timeout, pollInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should allow a session established on pod A to be used on pod B", func() {
			ginkgo.By("Getting the two ready pod IPs")
			var pods []corev1.Pod
			gomega.Eventually(func() (int, error) {
				var err error
				pods, err = getReadyMCPServerPods(mcpServerName, defaultNamespace)
				if err != nil {
					return 0, err
				}
				return len(pods), nil
			}, timeout, pollInterval).Should(gomega.Equal(2))

			podA := pods[0]
			podB := pods[1]
			gomega.Expect(podA.Status.PodIP).NotTo(gomega.BeEmpty())
			gomega.Expect(podB.Status.PodIP).NotTo(gomega.BeEmpty())
			gomega.Expect(podA.Status.PodIP).NotTo(gomega.Equal(podB.Status.PodIP),
				"The two pods must have distinct IPs")

			ginkgo.By("Initializing a session on pod A via the NodePort service")
			mcpClient, err := CreateInitializedMCPClient(nodePort, "e2e-cross-pod-test", 30*time.Second)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			sessionID := mcpClient.Client.GetSessionId()
			gomega.Expect(sessionID).NotTo(gomega.BeEmpty(), "session ID must be assigned after Initialize")
			mcpClient.Close()

			ginkgo.By(fmt.Sprintf("Sending tools/list directly to pod A (%s) with the session ID", podA.Name))
			bodyA, statusA, err := sendJSONRPCToPod(podA.Status.PodIP, proxyPort, sessionID, "tools/list", "{}")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(statusA).To(gomega.Equal(http.StatusOK),
				"pod A should accept the session; got body: %s", string(bodyA))

			var rpcRespA struct {
				Result struct {
					Tools []json.RawMessage `json:"tools"`
				} `json:"result"`
			}
			gomega.Expect(json.Unmarshal(bodyA, &rpcRespA)).To(gomega.Succeed())
			gomega.Expect(rpcRespA.Result.Tools).NotTo(gomega.BeEmpty(),
				"pod A should return tools for this session")

			ginkgo.By(fmt.Sprintf("Sending tools/list directly to pod B (%s) with the SAME session ID", podB.Name))
			bodyB, statusB, err := sendJSONRPCToPod(podB.Status.PodIP, proxyPort, sessionID, "tools/list", "{}")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(statusB).To(gomega.Equal(http.StatusOK),
				"pod B should accept the session via Redis; got body: %s", string(bodyB))

			var rpcRespB struct {
				Result struct {
					Tools []json.RawMessage `json:"tools"`
				} `json:"result"`
			}
			gomega.Expect(json.Unmarshal(bodyB, &rpcRespB)).To(gomega.Succeed())
			gomega.Expect(rpcRespB.Result.Tools).NotTo(gomega.BeEmpty(),
				"pod B should return the same tools for this session")

			ginkgo.By("Verifying both pods returned the same tool count")
			gomega.Expect(len(rpcRespB.Result.Tools)).To(gomega.Equal(len(rpcRespA.Result.Tools)),
				"Both replicas should see the same session state and return identical tools")
		})
	})
})
