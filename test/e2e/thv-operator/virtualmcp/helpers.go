// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp provides helper functions for VirtualMCP E2E tests.
package virtualmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// WaitForVirtualMCPServerReady waits for a VirtualMCPServer to reach Ready status
// and ensures at least one associated pod is actually running and ready.
// This is used when waiting for a single expected pod (e.g., one replica deployment).
func WaitForVirtualMCPServerReady(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	timeout time.Duration,
	pollingInterval time.Duration,
) {
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{}

	gomega.Eventually(func() error {
		if err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, vmcpServer); err != nil {
			return err
		}

		for _, condition := range vmcpServer.Status.Conditions {
			if condition.Type == mcpv1alpha1.ConditionTypeVirtualMCPServerReady {
				if condition.Status == "True" {
					// Also check that at least one pod is actually running and ready
					labels := map[string]string{
						"app.kubernetes.io/name":     "virtualmcpserver",
						"app.kubernetes.io/instance": name,
					}
					if err := checkPodsReady(ctx, c, namespace, labels); err != nil {
						return fmt.Errorf("VirtualMCPServer ready but pods not ready: %w", err)
					}
					return nil
				}
				return fmt.Errorf("ready condition is %s: %s", condition.Status, condition.Message)
			}
		}
		return fmt.Errorf("ready condition not found")
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// checkPodsReady waits for at least one pod matching the given labels to be ready.
// This is used when checking for a single expected pod (e.g., one replica deployment).
// Pods not in Running phase are skipped (e.g., Succeeded, Failed from previous deployments).
func checkPodsReady(ctx context.Context, c client.Client, namespace string, labels map[string]string) error {
	podList := &corev1.PodList{}
	if err := c.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels(labels)); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return fmt.Errorf("no pods found with labels %v", labels)
	}

	for _, pod := range podList.Items {
		// Skip pods that are not running (e.g., Succeeded, Failed from old deployments)
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		containerReady := false
		podReady := false

		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.ContainersReady {
				containerReady = condition.Status == corev1.ConditionTrue
			}

			if condition.Type == corev1.PodReady {
				podReady = condition.Status == corev1.ConditionTrue
			}
		}

		if !containerReady {
			return fmt.Errorf("pod %s containers not ready", pod.Name)
		}

		if !podReady {
			return fmt.Errorf("pod %s not ready", pod.Name)
		}
	}

	// After filtering, ensure we found at least one running pod
	runningPods := 0
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			runningPods++
		}
	}
	if runningPods == 0 {
		return fmt.Errorf("no running pods found with labels %v", labels)
	}
	return nil
}

// InitializedMCPClient holds an initialized MCP client with its associated context
type InitializedMCPClient struct {
	Client *mcpclient.Client
	Ctx    context.Context
	Cancel context.CancelFunc
}

// Close cleans up the MCP client resources
func (c *InitializedMCPClient) Close() {
	if c.Cancel != nil {
		c.Cancel()
	}
	if c.Client != nil {
		_ = c.Client.Close()
	}
}

// CreateInitializedMCPClient creates an MCP client, starts the transport, and initializes
// the connection with the given client name. Returns an InitializedMCPClient that should
// be closed when done using defer client.Close().
func CreateInitializedMCPClient(nodePort int32, clientName string, timeout time.Duration) (*InitializedMCPClient, error) {
	serverURL := fmt.Sprintf("http://localhost:%d/mcp", nodePort)
	mcpClient, err := mcpclient.NewStreamableHttpClient(serverURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	if err := mcpClient.Start(ctx); err != nil {
		cancel()
		_ = mcpClient.Close()
		return nil, fmt.Errorf("failed to start MCP client: %w", err)
	}

	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.Capabilities = mcp.ClientCapabilities{}
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    clientName,
		Version: "1.0.0",
	}

	if _, err := mcpClient.Initialize(ctx, initRequest); err != nil {
		cancel()
		_ = mcpClient.Close()
		return nil, fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	return &InitializedMCPClient{
		Client: mcpClient,
		Ctx:    ctx,
		Cancel: cancel,
	}, nil
}

// getPodLogs retrieves logs from a specific pod container
func getPodLogs(ctx context.Context, namespace, podName, containerName string, previous bool) (string, error) {
	// Get the rest config - try in-cluster first, then fall back to kubeconfig
	config, err := rest.InClusterConfig()
	if err != nil {
		// If not in cluster, try to load from kubeconfig file (from KUBECONFIG env or default location)
		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			kubeconfigPath = clientcmd.RecommendedHomeFile
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return "", fmt.Errorf("failed to get rest config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", fmt.Errorf("failed to create clientset: %w", err)
	}

	// Set up log options
	logOptions := &corev1.PodLogOptions{
		Container: containerName,
		Previous:  previous,
		TailLines: func(i int64) *int64 { return &i }(50), // Last 50 lines
	}

	// Get the logs
	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, logOptions)
	podLogs, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get log stream: %w", err)
	}
	defer func() {
		// Error ignored in test cleanup
		_ = podLogs.Close()
	}()

	// Read logs
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		return "", fmt.Errorf("failed to read logs: %w", err)
	}

	return buf.String(), nil
}

// GetVirtualMCPServerPods returns all pods for a VirtualMCPServer
func GetVirtualMCPServerPods(ctx context.Context, c client.Client, vmcpServerName, namespace string) (*corev1.PodList, error) {
	podList := &corev1.PodList{}
	err := c.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"app.kubernetes.io/name":     "virtualmcpserver",
			"app.kubernetes.io/instance": vmcpServerName,
		})
	return podList, err
}

// WaitForPodsReady waits for at least one pod matching labels to be ready.
// This is used when waiting for a single expected pod to be ready (e.g., one replica deployment).
func WaitForPodsReady(
	ctx context.Context,
	c client.Client,
	namespace string,
	labels map[string]string,
	timeout time.Duration,
	pollingInterval time.Duration,
) {
	gomega.Eventually(func() error {
		return checkPodsReady(ctx, c, namespace, labels)
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// GetMCPGroupBackends returns the list of backend MCPServers in an MCPGroup
// Note: MCPGroup status contains the list of servers in the group
func GetMCPGroupBackends(ctx context.Context, c client.Client, groupName, namespace string) ([]mcpv1alpha1.MCPServer, error) {
	mcpGroup := &mcpv1alpha1.MCPGroup{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      groupName,
		Namespace: namespace,
	}, mcpGroup); err != nil {
		return nil, err
	}

	// Get all MCPServers in the namespace
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	if err := c.List(ctx, mcpServerList,
		client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	// Filter MCPServers that reference this group
	var backends []mcpv1alpha1.MCPServer
	for _, mcpServer := range mcpServerList.Items {
		if mcpServer.Spec.GroupRef == groupName {
			backends = append(backends, mcpServer)
		}
	}

	return backends, nil
}

// GetVirtualMCPServerStatus returns the current status of a VirtualMCPServer
func GetVirtualMCPServerStatus(
	ctx context.Context,
	c client.Client,
	name, namespace string,
) (*mcpv1alpha1.VirtualMCPServerStatus, error) {
	vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, vmcpServer); err != nil {
		return nil, err
	}
	return &vmcpServer.Status, nil
}

// HasCondition checks if a VirtualMCPServer has a specific condition type with expected status
func HasCondition(vmcpServer *mcpv1alpha1.VirtualMCPServer, conditionType string, expectedStatus string) bool {
	for _, condition := range vmcpServer.Status.Conditions {
		if condition.Type == conditionType && string(condition.Status) == expectedStatus {
			return true
		}
	}
	return false
}

// WaitForCondition waits for a VirtualMCPServer to have a specific condition
func WaitForCondition(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	conditionType string,
	expectedStatus string,
	timeout time.Duration,
	pollingInterval time.Duration,
) {
	gomega.Eventually(func() error {
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		if err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, vmcpServer); err != nil {
			return err
		}

		if HasCondition(vmcpServer, conditionType, expectedStatus) {
			return nil
		}

		return fmt.Errorf("condition %s not found with status %s", conditionType, expectedStatus)
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// OIDC Testing Helpers

// DeployMockOIDCServerHTTP deploys a mock OIDC server with HTTP (for testing)
func DeployMockOIDCServerHTTP(ctx context.Context, c client.Client, namespace, serverName string) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serverName,
			Namespace: namespace,
			Labels:    map[string]string{"app": serverName},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": serverName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": serverName},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "mock-oidc",
							Image:   images.PythonImage,
							Command: []string{"sh", "-c"},
							Args:    []string{MockOIDCServerHTTPScript},
							Ports: []corev1.ContainerPort{
								{ContainerPort: 80, Name: "http"},
							},
						},
					},
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, deployment)).To(gomega.Succeed())

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serverName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": serverName},
			Ports: []corev1.ServicePort{
				{
					Port:     80,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, service)).To(gomega.Succeed())

	gomega.Eventually(func() bool {
		dep := &appsv1.Deployment{}
		err := c.Get(ctx, types.NamespacedName{Name: serverName, Namespace: namespace}, dep)
		return err == nil && dep.Status.ReadyReplicas > 0
	}, 3*time.Minute, 1*time.Second).Should(gomega.BeTrue(), "Mock OIDC server should be ready")
}

// DeployInstrumentedBackendServer deploys a backend server that logs all headers
func DeployInstrumentedBackendServer(ctx context.Context, c client.Client, namespace, serverName string) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serverName,
			Namespace: namespace,
			Labels:    map[string]string{"app": serverName},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": serverName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": serverName},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "instrumented-backend",
							Image:   images.PythonImage,
							Command: []string{"sh", "-c"},
							Args:    []string{InstrumentedBackendScript},
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8080, Name: "http"},
							},
						},
					},
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, deployment)).To(gomega.Succeed())

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serverName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": serverName},
			Ports: []corev1.ServicePort{
				{
					Port:     8080,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, service)).To(gomega.Succeed())

	gomega.Eventually(func() bool {
		dep := &appsv1.Deployment{}
		err := c.Get(ctx, types.NamespacedName{Name: serverName, Namespace: namespace}, dep)
		return err == nil && dep.Status.ReadyReplicas > 0
	}, 3*time.Minute, 1*time.Second).Should(gomega.BeTrue(), "Instrumented backend should be ready")
}

// CleanupMockServer cleans up a mock server deployment, service, and optionally its TLS secret
func CleanupMockServer(ctx context.Context, c client.Client, namespace, serverName, tlsSecretName string) {
	_ = c.Delete(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
	})
	_ = c.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
	})
	if tlsSecretName != "" {
		_ = c.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: tlsSecretName, Namespace: namespace},
		})
	}
}

// GetPodLogsForDeployment returns logs from pods for a deployment (for debugging)
func GetPodLogsForDeployment(ctx context.Context, c client.Client, namespace, deploymentName string) string {
	pods := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{"app": deploymentName},
	}

	err := c.List(ctx, pods, listOpts...)
	if err != nil || len(pods.Items) == 0 {
		return fmt.Sprintf("No pods found for deployment %s", deploymentName)
	}

	pod := pods.Items[0]
	if len(pod.Spec.Containers) == 0 {
		return fmt.Sprintf("No containers found in pod %s", pod.Name)
	}

	// Get logs from the first container
	containerName := pod.Spec.Containers[0].Name
	logs, err := getPodLogs(ctx, namespace, pod.Name, containerName, false)
	if err != nil {
		return fmt.Sprintf("Failed to get logs for pod %s: %v", pod.Name, err)
	}

	return logs
}

// GetPodLogs returns logs from a specific pod and container
func GetPodLogs(ctx context.Context, podName, namespace, containerName string) (string, error) {
	logs, err := getPodLogs(ctx, namespace, podName, containerName, false)
	if err != nil {
		return "", fmt.Errorf("failed to get logs for pod %s container %s: %w", podName, containerName, err)
	}
	return logs, nil
}

func int32Ptr(i int32) *int32 {
	return &i
}

// GetMCPServerDeployment retrieves the deployment for an MCPServer by name.
// MCPServer deployments use the same name as the MCPServer resource.
func GetMCPServerDeployment(ctx context.Context, c client.Client, serverName, namespace string) *appsv1.Deployment {
	deployment := &appsv1.Deployment{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      serverName,
		Namespace: namespace,
	}, deployment)
	if err != nil {
		return nil
	}
	return deployment
}

// GetMCPServerStatefulSet retrieves the StatefulSet for an MCPServer by name.
// MCPServer StatefulSets use the same name as the MCPServer resource for the workload pods.
func GetMCPServerStatefulSet(ctx context.Context, c client.Client, serverName, namespace string) *appsv1.StatefulSet {
	statefulset := &appsv1.StatefulSet{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      serverName,
		Namespace: namespace,
	}, statefulset)
	if err != nil {
		return nil
	}
	return statefulset
}

// WaitForPodDeletion waits for a pod to be fully deleted from the cluster.
// This is useful in AfterAll cleanup to ensure pods are gone before tests repeat.
func WaitForPodDeletion(ctx context.Context, c client.Client, name, namespace string, timeout, pollingInterval time.Duration) {
	gomega.Eventually(func() bool {
		pod := &corev1.Pod{}
		err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, pod)
		// Pod is deleted when we get a NotFound error
		return client.IgnoreNotFound(err) == nil && err != nil
	}, timeout, pollingInterval).Should(gomega.BeTrue(), "Pod %s should be deleted", name)
}

// GetServiceStats queries the /stats endpoint of a service and returns the stats
func GetServiceStats(ctx context.Context, c client.Client, namespace, serviceName string, port int) (string, error) {
	// Create a unique pod name to avoid conflicts
	curlPodName := fmt.Sprintf("stats-checker-%s-%d", serviceName, time.Now().Unix())
	curlPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      curlPodName,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "curl",
					Image:   images.CurlImage,
					Command: []string{"curl", "-s", fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/stats", serviceName, namespace, port)},
				},
			},
		},
	}

	// Create the pod
	if err := c.Create(ctx, curlPod); err != nil {
		return "", fmt.Errorf("failed to create curl pod: %w", err)
	}

	// Wait for pod to complete
	gomega.Eventually(func() bool {
		pod := &corev1.Pod{}
		_ = c.Get(ctx, types.NamespacedName{Name: curlPodName, Namespace: namespace}, pod)
		return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
	}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue())

	// Get logs from the pod (which contain the curl output)
	logs, err := getPodLogs(ctx, namespace, curlPodName, "curl", false)
	if err != nil {
		_ = c.Delete(ctx, curlPod)
		return "", fmt.Errorf("failed to get curl logs: %w", err)
	}

	// Clean up the curl pod
	_ = c.Delete(ctx, curlPod)

	return logs, nil
}

// GetMockOIDCStats queries the /stats endpoint of the mock OIDC server
func GetMockOIDCStats(ctx context.Context, c client.Client, namespace, serviceName string) (map[string]int, error) {
	logs, err := GetServiceStats(ctx, c, namespace, serviceName, 80)
	if err != nil {
		return nil, err
	}

	// Parse JSON response - check if discovery_requests field exists
	stats := make(map[string]int)
	if len(logs) > 0 && bytes.Contains([]byte(logs), []byte("discovery_requests")) {
		stats["discovery_requests"] = 1 // Simplified - just check if field exists
	}
	return stats, nil
}

// GetInstrumentedBackendStats queries the /stats endpoint of the instrumented backend
func GetInstrumentedBackendStats(ctx context.Context, c client.Client, namespace, serviceName string) (map[string]int, error) {
	logs, err := GetServiceStats(ctx, c, namespace, serviceName, 8080)
	if err != nil {
		return nil, err
	}

	// Parse JSON response - check if bearer_token_requests field exists
	stats := make(map[string]int)
	if len(logs) > 0 && bytes.Contains([]byte(logs), []byte("bearer_token_requests")) {
		stats["bearer_token_requests"] = 1 // Simplified - just check if field exists and > 0
	}
	return stats, nil
}

// GetMockOAuth2Stats queries the /stats endpoint of the mock OAuth2 server (port 8080)
// and returns the number of client_credentials grant requests recorded so far.
func GetMockOAuth2Stats(ctx context.Context, c client.Client, namespace, serviceName string) (int, error) {
	logs, err := GetServiceStats(ctx, c, namespace, serviceName, 8080)
	if err != nil {
		return 0, err
	}
	var stats struct {
		ClientCredentialsRequests int `json:"client_credentials_requests"`
	}
	if err := json.Unmarshal([]byte(logs), &stats); err != nil {
		return 0, fmt.Errorf("failed to parse OAuth2 stats JSON %q: %w", logs, err)
	}
	return stats.ClientCredentialsRequests, nil
}

// MockOIDCServerHTTPScript is a mock OIDC server script with HTTP (for testing with private IPs)
const MockOIDCServerHTTPScript = `
pip install --quiet flask && python3 - <<'PYTHON_SCRIPT'
from flask import Flask, jsonify, request
import sys

app = Flask(__name__)

# Request counters
stats = {
    "discovery_requests": 0,
    "jwks_requests": 0,
    "token_requests": 0,
}

@app.route('/.well-known/openid-configuration')
def discovery():
    stats["discovery_requests"] += 1
    print(f"OIDC Discovery request received (count: {stats['discovery_requests']})", flush=True)
    sys.stdout.flush()
    return jsonify({
        "issuer": "http://mock-oidc-http",
        "authorization_endpoint": "http://mock-oidc-http/auth",
        "token_endpoint": "http://mock-oidc-http/token",
        "userinfo_endpoint": "http://mock-oidc-http/userinfo",
        "jwks_uri": "http://mock-oidc-http/jwks",
    })

@app.route('/jwks')
def jwks():
    stats["jwks_requests"] += 1
    print(f"JWKS request received (count: {stats['jwks_requests']})", flush=True)
    sys.stdout.flush()
    return jsonify({"keys": []})

@app.route('/token', methods=['POST'])
def token():
    stats["token_requests"] += 1
    print(f"Token request received (count: {stats['token_requests']})", flush=True)
    sys.stdout.flush()
    return jsonify({
        "access_token": "mock_access_token_12345",
        "token_type": "Bearer",
        "expires_in": 3600,
    })

@app.route('/stats')
def get_stats():
    print(f"Stats request received: {stats}", flush=True)
    sys.stdout.flush()
    return jsonify(stats)

if __name__ == '__main__':
    print("Mock OIDC server starting on port 80 with HTTP", flush=True)
    sys.stdout.flush()
    app.run(host='0.0.0.0', port=80)
PYTHON_SCRIPT
`

// VMCPServiceName returns the Kubernetes service name for a VirtualMCPServer
func VMCPServiceName(vmcpServerName string) string {
	return fmt.Sprintf("vmcp-%s", vmcpServerName)
}

// CreateMCPGroupAndWait creates an MCPGroup and waits for it to become ready.
// Returns the created MCPGroup after it reaches Ready phase.
func CreateMCPGroupAndWait(
	ctx context.Context,
	c client.Client,
	name, namespace, description string,
	timeout, pollingInterval time.Duration,
) *mcpv1alpha1.MCPGroup {
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPGroupSpec{
			Description: description,
		},
	}
	gomega.Expect(c.Create(ctx, mcpGroup)).To(gomega.Succeed())

	gomega.Eventually(func() bool {
		err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, mcpGroup)
		if err != nil {
			return false
		}
		return mcpGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
	}, timeout, pollingInterval).Should(gomega.BeTrue(), "MCPGroup should become ready")

	return mcpGroup
}

// CreateMCPServerAndWait creates an MCPServer with the specified image and waits for it to be running.
// Returns the created MCPServer after it reaches Running phase.
func CreateMCPServerAndWait(
	ctx context.Context,
	c client.Client,
	name, namespace, groupRef, image string,
	timeout, pollingInterval time.Duration,
) *mcpv1alpha1.MCPServer {
	backend := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			GroupRef:  groupRef,
			Image:     image,
			Transport: "streamable-http",
			ProxyPort: 8080,
			McpPort:   8080,
			Resources: defaultMCPServerResources(),
			Env: []mcpv1alpha1.EnvVar{
				{Name: "TRANSPORT", Value: "streamable-http"},
			},
		},
	}
	gomega.Expect(c.Create(ctx, backend)).To(gomega.Succeed())

	gomega.Eventually(func() error {
		server := &mcpv1alpha1.MCPServer{}
		err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, server)
		if err != nil {
			return fmt.Errorf("failed to get server: %w", err)
		}
		if server.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning {
			return nil
		}
		return fmt.Errorf("%s not ready yet, phase: %s", name, server.Status.Phase)
	}, timeout, pollingInterval).Should(gomega.Succeed(), fmt.Sprintf("MCPServer %s should be ready", name))

	return backend
}

// BackendConfig holds configuration for creating a backend MCPServer in tests.
type BackendConfig struct {
	Name                  string
	Namespace             string
	GroupRef              string
	Image                 string
	Transport             string // defaults to "streamable-http" if empty
	ExternalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef
	Secrets               []mcpv1alpha1.SecretRef
	Env                   []mcpv1alpha1.EnvVar // additional env vars beyond TRANSPORT
	// Resources overrides the default resource requests/limits. When nil,
	// defaultMCPServerResources() is used to ensure containers are scheduled
	// with reasonable resource guarantees and do not compete excessively.
	Resources *mcpv1alpha1.ResourceRequirements
}

// defaultMCPServerResources returns conservative resource requests/limits that
// mirror the quickstart example (vmcp_optimizer_quickstart.yaml) and are
// sufficient for functional E2E testing without starving other pods.
func defaultMCPServerResources() mcpv1alpha1.ResourceRequirements {
	return mcpv1alpha1.ResourceRequirements{
		Limits: mcpv1alpha1.ResourceList{
			CPU:    "200m",
			Memory: "256Mi",
		},
		Requests: mcpv1alpha1.ResourceList{
			CPU:    "100m",
			Memory: "128Mi",
		},
	}
}

// CreateMultipleMCPServersInParallel creates multiple MCPServers concurrently and waits for all to be running.
// This significantly reduces test setup time compared to sequential creation.
func CreateMultipleMCPServersInParallel(
	ctx context.Context,
	c client.Client,
	backends []BackendConfig,
	timeout, pollingInterval time.Duration,
) {
	// Create all backends concurrently
	for i := range backends {
		idx := i // Capture loop variable
		backendTransport := backends[idx].Transport
		if backendTransport == "" {
			backendTransport = "streamable-http"
		}

		resources := defaultMCPServerResources()
		if backends[idx].Resources != nil {
			resources = *backends[idx].Resources
		}

		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backends[idx].Name,
				Namespace: backends[idx].Namespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:              backends[idx].GroupRef,
				Image:                 backends[idx].Image,
				Transport:             backendTransport,
				ProxyPort:             8080,
				McpPort:               8080,
				ExternalAuthConfigRef: backends[idx].ExternalAuthConfigRef,
				Secrets:               backends[idx].Secrets,
				Resources:             resources,
				Env: append([]mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: backendTransport},
				}, backends[idx].Env...),
			},
		}
		gomega.Expect(c.Create(ctx, backend)).To(gomega.Succeed())
	}

	// Wait for all backends to be ready in parallel (single Eventually checking all)
	gomega.Eventually(func() error {
		for _, cfg := range backends {
			server := &mcpv1alpha1.MCPServer{}
			err := c.Get(ctx, types.NamespacedName{
				Name:      cfg.Name,
				Namespace: cfg.Namespace,
			}, server)
			if err != nil {
				return fmt.Errorf("failed to get server %s: %w", cfg.Name, err)
			}
			// Fail-fast if server enters Failed phase (e.g., bad image, crash loop)
			if server.Status.Phase == mcpv1alpha1.MCPServerPhaseFailed {
				return gomega.StopTrying(fmt.Sprintf("%s failed: %s", cfg.Name, server.Status.Message))
			}
			if server.Status.Phase != mcpv1alpha1.MCPServerPhaseRunning {
				return fmt.Errorf("%s not ready yet, phase: %s", cfg.Name, server.Status.Phase)
			}
		}
		// All backends are ready
		return nil
	}, timeout, pollingInterval).Should(gomega.Succeed(), "All MCPServers should be ready")
}

// GetVMCPNodePort waits for the VirtualMCPServer service to have a NodePort assigned
// and verifies the port is accessible.
func GetVMCPNodePort(
	ctx context.Context,
	c client.Client,
	vmcpServerName, namespace string,
	timeout, pollingInterval time.Duration,
) int32 {
	var nodePort int32
	serviceName := VMCPServiceName(vmcpServerName)

	gomega.Eventually(func() error {
		service := &corev1.Service{}
		err := c.Get(ctx, types.NamespacedName{
			Name:      serviceName,
			Namespace: namespace,
		}, service)
		if err != nil {
			return err
		}
		if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].NodePort == 0 {
			return fmt.Errorf("nodePort not assigned for vmcp service %s", serviceName)
		}
		nodePort = service.Spec.Ports[0].NodePort

		// Verify the TCP port is accessible
		if err := checkPortAccessible(nodePort, 1*time.Second); err != nil {
			return fmt.Errorf("nodePort %d assigned but not accessible: %w", nodePort, err)
		}

		// Verify the HTTP server is ready to handle requests
		if err := checkHTTPHealthReady(nodePort, 2*time.Second); err != nil {
			return fmt.Errorf("nodePort %d accessible but HTTP server not ready: %w", nodePort, err)
		}

		return nil
	}, timeout, pollingInterval).Should(gomega.Succeed(), "NodePort should be assigned and HTTP server ready")

	return nodePort
}

// checkPortAccessible verifies that the port is open and accepting TCP connections.
// This is a lightweight check that completes in milliseconds instead of the seconds
// required for a full MCP session initialization.
func checkPortAccessible(nodePort int32, timeout time.Duration) error {
	address := fmt.Sprintf("localhost:%d", nodePort)
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return fmt.Errorf("port %d not accessible: %w", nodePort, err)
	}
	// Port is accessible - close connection (ignore errors as port accessibility is confirmed)
	_ = conn.Close()
	return nil
}

// checkHTTPHealthReady verifies the HTTP server is ready by checking the /health endpoint.
// This is more reliable than just TCP check as it ensures the application is serving requests.
func checkHTTPHealthReady(nodePort int32, timeout time.Duration) error {
	httpClient := &http.Client{Timeout: timeout}
	url := fmt.Sprintf("http://localhost:%d/health", nodePort)

	resp, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("health check failed for port %d: %w", nodePort, err)
	}
	defer func() {
		// Error ignored in test cleanup
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d for port %d", resp.StatusCode, nodePort)
	}

	return nil
}

// TestToolListingAndCall is a shared helper that creates an MCP client, lists tools,
// finds a tool matching the pattern, calls it, and verifies the response.
// This eliminates the duplicate "create client → list → call" pattern found in most tests.
func TestToolListingAndCall(vmcpNodePort int32, clientName string, toolNamePattern string, testInput string) {
	ginkgo.By("Creating and initializing MCP client")
	mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, clientName, 30*time.Second)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	defer mcpClient.Close()

	ginkgo.By("Listing tools from VirtualMCPServer")
	listRequest := mcp.ListToolsRequest{}
	tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(tools.Tools).ToNot(gomega.BeEmpty())

	// Find a tool matching the pattern
	var targetToolName string
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, toolNamePattern) {
			targetToolName = tool.Name
			break
		}
	}
	gomega.Expect(targetToolName).ToNot(gomega.BeEmpty(), fmt.Sprintf("Should find a tool matching pattern: %s", toolNamePattern))

	ginkgo.By(fmt.Sprintf("Calling tool: %s", targetToolName))
	callRequest := mcp.CallToolRequest{}
	callRequest.Params.Name = targetToolName
	callRequest.Params.Arguments = map[string]any{
		"input": testInput,
	}

	result, err := mcpClient.Client.CallTool(mcpClient.Ctx, callRequest)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "Should successfully call tool")
	gomega.Expect(result).ToNot(gomega.BeNil())
	gomega.Expect(result.Content).ToNot(gomega.BeEmpty(), "Should have content in response")

	// Error ignored in test output
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "✓ Successfully called tool %s\n", targetToolName)
}

// TestToolListing is a shared helper that creates an MCP client and lists tools.
// Returns the list of tools for further assertions.
func TestToolListing(vmcpNodePort int32, clientName string) []mcp.Tool {
	ginkgo.By("Creating and initializing MCP client")
	mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, clientName, 30*time.Second)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	defer mcpClient.Close()

	ginkgo.By("Listing tools from VirtualMCPServer")
	listRequest := mcp.ListToolsRequest{}
	toolsResult, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(toolsResult.Tools).ToNot(gomega.BeEmpty())

	// Error ignored in test output
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "Listed %d tools from VirtualMCPServer\n", len(toolsResult.Tools))
	return toolsResult.Tools
}

// InstrumentedBackendScript is an instrumented backend script that tracks Bearer tokens
const InstrumentedBackendScript = `
pip install --quiet flask && python3 - <<'PYTHON_SCRIPT'
from flask import Flask, request, jsonify
import sys

app = Flask(__name__)

# Request tracking
stats = {
    "total_requests": 0,
    "bearer_token_requests": 0,
    "last_bearer_token": None,
}

@app.route('/stats')
def get_stats():
    print(f"Stats request received: {stats}", flush=True)
    sys.stdout.flush()
    return jsonify(stats)

@app.route('/<path:path>', methods=['GET', 'POST'])
def catch_all(path):
    stats["total_requests"] += 1
    print(f"=== Request {stats['total_requests']} received ===", flush=True)
    print(f"Path: {path}", flush=True)
    print("Headers:", flush=True)

    bearer_found = False
    for header, value in request.headers.items():
        print(f"  {header}: {value}", flush=True)
        if header.lower() == "authorization" and "Bearer" in value:
            bearer_found = True
            stats["bearer_token_requests"] += 1
            stats["last_bearer_token"] = value
            print(f"*** BEARER TOKEN DETECTED (count: {stats['bearer_token_requests']}): {value} ***", flush=True)

    sys.stdout.flush()
    return jsonify({"status": "ok", "path": path, "bearer_token_received": bearer_found})

if __name__ == '__main__':
    print("Instrumented backend starting on port 8080", flush=True)
    sys.stdout.flush()
    app.run(host='0.0.0.0', port=8080)
PYTHON_SCRIPT
`

// WithHttpLoggerOption returns a transport.StreamableHTTPCOption that logs to GinkgoLogr.
// This is useful for debugging HTTP requests and responses.
func WithHttpLoggerOption() transport.StreamableHTTPCOption {
	return transport.WithHTTPLogger(gingkoHttpLogger{})
}

type gingkoHttpLogger struct{}

func (gingkoHttpLogger) Infof(format string, v ...any) {
	ginkgo.GinkgoLogr.Info("INFO: "+format, v...)
}

func (gingkoHttpLogger) Errorf(format string, v ...any) {
	ginkgo.GinkgoLogr.Error(errors.New("http error"), "ERROR: "+format, v...)
}

// InitializeMCPClientWithRetries creates and initializes an MCP client with proper retry handling.
// It creates a NEW client for each retry attempt to avoid stale session state issues.
// Returns the initialized client. Caller is responsible for calling Close() on the client.
func InitializeMCPClientWithRetries(
	serverURL string,
	timeout time.Duration,
	opts ...transport.StreamableHTTPCOption,
) *mcpclient.Client {
	var mcpClient *mcpclient.Client

	gomega.Eventually(func() error {
		// Close any previous client to avoid stale session state
		if mcpClient != nil {
			_ = mcpClient.Close()
		}

		// Create fresh client for each attempt
		var err error
		mcpClient, err = mcpclient.NewStreamableHttpClient(serverURL, opts...)
		if err != nil {
			return fmt.Errorf("failed to create client: %w", err)
		}

		initCtx, initCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer initCancel()

		if err := mcpClient.Start(initCtx); err != nil {
			return fmt.Errorf("failed to start transport: %w", err)
		}

		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{
			Name:    "toolhive-e2e-test",
			Version: "1.0.0",
		}

		_, err = mcpClient.Initialize(initCtx, initRequest)
		if err != nil {
			return fmt.Errorf("failed to initialize: %w", err)
		}

		return nil
	}, timeout, 5*time.Second).Should(gomega.Succeed(), "MCP client should initialize successfully")

	return mcpClient
}

// MockHTTPServerInfo contains information about a deployed mock HTTP server
type MockHTTPServerInfo struct {
	Name      string
	Namespace string
	URL       string // In-cluster URL: http://<name>.<namespace>.svc.cluster.local
}

// CreateMockHTTPServer creates an in-cluster mock HTTP server for testing fetch tools.
// This avoids network issues with external URLs like https://example.com in CI.
func CreateMockHTTPServer(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	timeout, pollingInterval time.Duration,
) *MockHTTPServerInfo {
	configMapName := name + "-code"

	// Create ConfigMap with simple HTTP server
	httpConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"server.py": `#!/usr/bin/env python3
import http.server
import socketserver

class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-type', 'text/html')
        self.end_headers()
        self.wfile.write(b'<html><body><h1>Mock HTTP Server</h1><p>This is a test response.</p></body></html>')
        return

with socketserver.TCPServer(("", 8080), Handler) as httpd:
    print("Mock server running on port 8080")
    httpd.serve_forever()
`,
		},
	}
	gomega.Expect(c.Create(ctx, httpConfigMap)).To(gomega.Succeed())

	// Create Pod running the mock server
	mockPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app": name,
			},
		},
		Spec: corev1.PodSpec{

			// Provide a security context to avoid running as root.
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true),
				RunAsUser:    ptr.To(int64(1000)),
			},

			Containers: []corev1.Container{
				{
					Name:  "http-server",
					Image: "python:3.11-slim",
					Command: []string{
						"python3", "/app/server.py",
					},
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8080},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "server-code",
							MountPath: "/app",
						},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{
								Port: intstr.FromInt(8080),
							},
						},
						InitialDelaySeconds: 2,
						PeriodSeconds:       2,
						TimeoutSeconds:      5,
						SuccessThreshold:    1,
						FailureThreshold:    15,
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "server-code",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: configMapName,
							},
							DefaultMode: int32Ptr(0755),
						},
					},
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, mockPod)).To(gomega.Succeed())

	// Create Service
	mockService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": name,
			},
			Ports: []corev1.ServicePort{
				{
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, mockService)).To(gomega.Succeed())

	// Wait for pod to be ready
	ginkgo.By("Waiting for mock HTTP server to be ready")
	gomega.Eventually(func() bool {
		pod := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, pod); err != nil {
			return false
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(gomega.BeTrue(), "Mock HTTP server pod should be ready")

	return &MockHTTPServerInfo{
		Name:      name,
		Namespace: namespace,
		URL:       fmt.Sprintf("http://%s.%s.svc.cluster.local", name, namespace),
	}
}

// ParameterizedOIDCServerScript is a minimal Python OIDC server that issues
// RSA-signed RS256 JWTs with a caller-controlled subject.
//
// Usage: POST /token?subject=alice  → returns {"access_token": "<jwt>", ...}
// The subject defaults to "test-user" when the query parameter is omitted.
//
// The issuer is derived from the service name: the server reads the HOST
// environment variable set by the caller via the ISSUER constant below. Tests
// that deploy this script must set the correct issuer URL in the VirtualMCPServer
// InlineOIDCConfig.Issuer field.
const ParameterizedOIDCServerScript = `
import base64, json, time, http.server, socketserver
from urllib.parse import urlparse, parse_qs
from cryptography.hazmat.primitives.asymmetric import rsa, padding as asym_padding
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.backends import default_backend

private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048, backend=default_backend())
public_key = private_key.public_key()
pub_numbers = public_key.public_numbers()

def to_b64url(num):
    b = num.to_bytes((num.bit_length() + 7) // 8, byteorder="big")
    return base64.urlsafe_b64encode(b).decode().rstrip("=")

n_b64 = to_b64url(pub_numbers.n)
e_b64 = to_b64url(pub_numbers.e)
ISSUER = "http://OIDC_SERVICE_NAME.OIDC_NAMESPACE.svc.cluster.local"

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/.well-known/openid-configuration":
            self._json({"issuer": ISSUER, "authorization_endpoint": ISSUER+"/auth",
                "token_endpoint": ISSUER+"/token", "jwks_uri": ISSUER+"/jwks",
                "response_types_supported": ["code"], "subject_types_supported": ["public"],
                "id_token_signing_alg_values_supported": ["RS256"]})
        elif self.path == "/jwks":
            self._json({"keys": [{"kty": "RSA", "use": "sig", "kid": "k1", "alg": "RS256", "n": n_b64, "e": e_b64}]})
        else:
            self.send_response(404); self.end_headers()
    def do_POST(self):
        if self.path.startswith("/token"):
            params = parse_qs(urlparse(self.path).query)
            sub = params.get("subject", ["test-user"])[0]
            hdr = {"alg": "RS256", "typ": "JWT", "kid": "k1"}
            pay = {"sub": sub, "iss": ISSUER, "aud": "vmcp-audience", "exp": int(time.time())+3600, "iat": int(time.time())}
            def enc(d): return base64.urlsafe_b64encode(json.dumps(d, separators=(",",":")).encode()).decode().rstrip("=")
            h64, p64 = enc(hdr), enc(pay)
            sig = private_key.sign((h64+"."+p64).encode(), asym_padding.PKCS1v15(), hashes.SHA256())
            jwt = h64 + "." + p64 + "." + base64.urlsafe_b64encode(sig).decode().rstrip("=")
            print(f"Issued JWT for sub={sub}", flush=True)
            self._json({"access_token": jwt, "token_type": "Bearer", "expires_in": 3600})
        else:
            self.send_response(404); self.end_headers()
    def _json(self, obj):
        body = json.dumps(obj).encode()
        self.send_response(200); self.send_header("Content-Type","application/json"); self.end_headers(); self.wfile.write(body)
    def log_message(self, f, *a): pass

with socketserver.TCPServer(("", 8080), H) as s:
    print("OIDC server ready on 8080", flush=True)
    s.serve_forever()
`

// DeployParameterizedOIDCServer deploys an in-cluster mock OIDC server that
// issues RSA-signed JWTs with a caller-controlled subject claim (via
// POST /token?subject=<name>). The server is exposed via a fixed NodePort so
// the test process (running outside the cluster) can reach it.
//
// Returns the in-cluster issuer URL (http://<name>.<namespace>.svc.cluster.local)
// and a cleanup function that removes all created resources.
func DeployParameterizedOIDCServer(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	timeout, pollingInterval time.Duration,
) (issuerURL string, allocatedNodePort int32, cleanup func()) {
	configMapName := name + "-code"

	// Patch the placeholder issuer into the script so the JWT iss claim and
	// the OIDC discovery document match the in-cluster service URL.
	issuerURL = fmt.Sprintf("http://%s.%s.svc.cluster.local", name, namespace)
	script := strings.ReplaceAll(ParameterizedOIDCServerScript,
		"http://OIDC_SERVICE_NAME.OIDC_NAMESPACE.svc.cluster.local", issuerURL)

	ginkgo.By("Creating ConfigMap with parameterized OIDC server code")
	gomega.Expect(c.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: namespace},
		Data:       map[string]string{"server.py": script},
	})).To(gomega.Succeed())

	ginkgo.By("Creating parameterized OIDC server pod")
	mode := int32Ptr(0755)
	gomega.Expect(c.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:    "oidc",
				Image:   "python:3.11-slim",
				Command: []string{"sh", "-c", "pip install --no-cache-dir cryptography && python3 /app/server.py"},
				Ports:   []corev1.ContainerPort{{ContainerPort: 8080}},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/.well-known/openid-configuration",
							Port: intstr.FromInt(8080),
						},
					},
					InitialDelaySeconds: 5,
					PeriodSeconds:       2,
					FailureThreshold:    30,
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "code", MountPath: "/app"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "code",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
						DefaultMode:          mode,
					},
				},
			}},
		},
	})).To(gomega.Succeed())

	ginkgo.By("Creating parameterized OIDC server service with auto-assigned NodePort")
	oidcSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: map[string]string{"app": name},
			Ports: []corev1.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt(8080),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	gomega.Expect(c.Create(ctx, oidcSvc)).To(gomega.Succeed())

	// Read back the auto-assigned NodePort
	gomega.Expect(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, oidcSvc)).To(gomega.Succeed())
	allocatedNodePort = oidcSvc.Spec.Ports[0].NodePort
	gomega.Expect(allocatedNodePort).NotTo(gomega.BeZero(), "Kubernetes should auto-assign a NodePort")

	ginkgo.By("Waiting for parameterized OIDC server to be ready")
	gomega.Eventually(func() bool {
		pod := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, pod); err != nil {
			return false
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(gomega.BeTrue(), "parameterized OIDC server should be ready")

	cleanup = func() {
		_ = c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = c.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = c.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: namespace}})
		// Wait for the Pod and Service to be fully removed so their fixed NodePort
		// and name can be reused immediately in a subsequent test run.
		gomega.Eventually(func() bool {
			pod := &corev1.Pod{}
			svc := &corev1.Service{}
			podGone := apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, pod))
			svcGone := apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, svc))
			return podGone && svcGone
		}, timeout, pollingInterval).Should(gomega.BeTrue(), "OIDC server pod and service should be fully deleted")
	}
	return issuerURL, allocatedNodePort, cleanup
}

// CleanupMockHTTPServer removes the mock HTTP server resources
func CleanupMockHTTPServer(ctx context.Context, c client.Client, name, namespace string) {
	configMapName := name + "-code"

	_ = c.Delete(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
	_ = c.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
	_ = c.Delete(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: namespace},
	})
}

// fakeEmbeddingServerScript is a minimal Python HTTP server that mimics the
// text-embeddings-inference (TEI) API. It provides:
//   - GET /info  → {"max_client_batch_size": 32}
//   - POST /embed → 384-dim constant vectors (one per input)
//
// This is sufficient for the optimizer because FTS5 keyword search works
// without real embeddings, and the SQLite store stores the dummy embeddings
// without errors.
const fakeEmbeddingServerScript = `
python3 -c '
import json
from http.server import HTTPServer, BaseHTTPRequestHandler

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/info":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"max_client_batch_size": 32}).encode())
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        if self.path == "/embed":
            length = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(length)) if length else {}
            inputs = body.get("inputs", [])
            n = len(inputs) if isinstance(inputs, list) else 1
            embeddings = [[0.1] * 384 for _ in range(n)]
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(embeddings).encode())
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        pass  # suppress request logs

HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
'
`

// DeployFakeEmbeddingServer deploys a lightweight fake embedding server that
// mimics the TEI API. This avoids pulling the heavyweight TEI container image
// while satisfying the optimizer's embedding service requirement.
// Returns the in-cluster service URL (http://<name>.<namespace>.svc.cluster.local:8080).
func DeployFakeEmbeddingServer(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	timeout, pollingInterval time.Duration,
) string {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "fake-embedding",
							Image:   images.PythonImage,
							Command: []string{"sh", "-c"},
							Args:    []string{fakeEmbeddingServerScript},
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8080, Name: "http"},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 2,
								PeriodSeconds:       2,
								TimeoutSeconds:      5,
								SuccessThreshold:    1,
								FailureThreshold:    15,
							},
						},
					},
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, deployment)).To(gomega.Succeed())

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports: []corev1.ServicePort{
				{
					Port:       8080,
					TargetPort: intstr.FromInt(8080),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, service)).To(gomega.Succeed())

	ginkgo.By("Waiting for fake embedding server to be ready")
	gomega.Eventually(func() bool {
		dep := &appsv1.Deployment{}
		err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, dep)
		return err == nil && dep.Status.ReadyReplicas > 0
	}, timeout, pollingInterval).Should(gomega.BeTrue(), "Fake embedding server should be ready")

	return fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", name, namespace)
}

// CleanupFakeEmbeddingServer removes the fake embedding server Deployment and Service.
func CleanupFakeEmbeddingServer(ctx context.Context, c client.Client, name, namespace string) {
	_ = c.Delete(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
	_ = c.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
}

// MockOAuth2ServerScript is a minimal OAuth2 server that accepts token requests and
// tracks token request statistics. Designed to be used as the token endpoint for
// TokenExchange health-check tests.
//
// Endpoints:
//
//	POST /token  – accepts grant_type=client_credentials (Basic Auth or form body)
//	              returns {"access_token": "mock-health-check-token", ...}
//	            – accepts grant_type=urn:ietf:params:oauth:grant-type:token-exchange
//	              returns {"access_token": "mock-exchanged-token", ..., "issued_token_type": "...access_token"}
//	GET  /stats  – returns {"token_requests": N, "client_credentials_requests": N, "last_client_id": "..."}
const MockOAuth2ServerScript = `
pip install --quiet flask && python3 - <<'PYTHON_SCRIPT'
from flask import Flask, jsonify, request
import base64
import sys

app = Flask(__name__)

stats = {
    "token_requests": 0,
    "client_credentials_requests": 0,
    "last_client_id": None,
}

@app.route('/token', methods=['POST'])
def token():
    grant_type = request.form.get('grant_type', '')

    # Extract client credentials from Basic Auth header first
    client_id = None
    auth_header = request.headers.get('Authorization', '')
    if auth_header.startswith('Basic '):
        try:
            decoded = base64.b64decode(auth_header[6:]).decode('utf-8')
            parts = decoded.split(':', 1)
            if len(parts) == 2:
                client_id = parts[0]
        except Exception:
            pass

    # Fall back to form body
    if not client_id:
        client_id = request.form.get('client_id', '')

    stats['token_requests'] += 1
    stats['last_client_id'] = client_id

    print(f"Token request #{stats['token_requests']}: grant_type={grant_type} client_id={client_id}", flush=True)
    sys.stdout.flush()

    if grant_type == 'client_credentials':
        stats['client_credentials_requests'] += 1
        return jsonify({
            "access_token": "mock-health-check-token",
            "token_type": "Bearer",
            "expires_in": 3600,
        })

    if grant_type == 'urn:ietf:params:oauth:grant-type:token-exchange':
        return jsonify({
            "access_token": "mock-exchanged-token",
            "token_type": "Bearer",
            "expires_in": 3600,
            "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
        })

    return jsonify({"error": "unsupported_grant_type"}), 400

@app.route('/stats')
def get_stats():
    print(f"Stats request: {stats}", flush=True)
    sys.stdout.flush()
    return jsonify(stats)

if __name__ == '__main__':
    print("Mock OAuth2 server starting on port 8080", flush=True)
    sys.stdout.flush()
    app.run(host='0.0.0.0', port=8080)
PYTHON_SCRIPT
`

// DeployMockOAuth2Server deploys a minimal OAuth2 server in-cluster that accepts
// client_credentials and token-exchange grant requests, and exposes a /stats endpoint.
// The service is ClusterIP — use GetMockOAuth2Stats to query /stats via a curl pod
// rather than relying on NodePort reachability from the test process.
//
// Returns:
//   - inClusterTokenURL: the /token URL reachable from inside the cluster
//   - cleanup:           function that removes all created resources
func DeployMockOAuth2Server(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	timeout, pollingInterval time.Duration,
) (inClusterTokenURL string, cleanup func()) {
	inClusterTokenURL = fmt.Sprintf("http://%s.%s.svc.cluster.local:8080/token", name, namespace)

	ginkgo.By("Creating mock OAuth2 server pod: " + name)
	gomega.Expect(c.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:    "mock-oauth2",
				Image:   images.PythonImage,
				Command: []string{"sh", "-c"},
				Args:    []string{MockOAuth2ServerScript},
				Ports:   []corev1.ContainerPort{{ContainerPort: 8080, Name: "http"}},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/stats",
							Port: intstr.FromInt(8080),
						},
					},
					InitialDelaySeconds: 5,
					PeriodSeconds:       2,
					FailureThreshold:    30,
				},
			}},
		},
	})).To(gomega.Succeed())

	ginkgo.By("Creating mock OAuth2 server ClusterIP service: " + name)
	gomega.Expect(c.Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports: []corev1.ServicePort{{
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	})).To(gomega.Succeed())

	ginkgo.By("Waiting for mock OAuth2 server to be ready")
	gomega.Eventually(func() bool {
		pod := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, pod); err != nil {
			return false
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(gomega.BeTrue(), "mock OAuth2 server should be ready")

	cleanup = func() {
		_ = c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = c.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		gomega.Eventually(func() bool {
			pod := &corev1.Pod{}
			svcObj := &corev1.Service{}
			podGone := apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, pod))
			svcGone := apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, svcObj))
			return podGone && svcGone
		}, timeout, pollingInterval).Should(gomega.BeTrue(), "mock OAuth2 pod and service should be fully deleted")
	}
	return inClusterTokenURL, cleanup
}

// ---- /status and /api/backends/health HTTP helpers ----

// VMCPStatusResponse mirrors server.StatusResponse
// (pkg/vmcp/server/status.go) for test deserialization.
type VMCPStatusResponse struct {
	Backends []VMCPBackendStatus `json:"backends"`
	Healthy  bool                `json:"healthy"`
	Version  string              `json:"version"`
	GroupRef string              `json:"group_ref"`
}

// VMCPBackendStatus mirrors server.BackendStatus
// (pkg/vmcp/server/status.go) for test deserialization.
type VMCPBackendStatus struct {
	Name      string `json:"name"`
	Health    string `json:"health"` // "healthy", "degraded", "unhealthy", "unknown"
	Transport string `json:"transport"`
	AuthType  string `json:"auth_type,omitempty"`
}

// VMCPBackendsHealthResponse mirrors BackendHealthResponse
// (pkg/vmcp/server/server.go) for test deserialization.
type VMCPBackendsHealthResponse struct {
	MonitoringEnabled bool                               `json:"monitoring_enabled"`
	Backends          map[string]*VMCPBackendHealthState `json:"backends,omitempty"`
}

// VMCPBackendHealthState mirrors health.State for test deserialization.
// Field names are capitalized (no json tags on the server struct).
type VMCPBackendHealthState struct {
	Status              string `json:"Status"`
	ConsecutiveFailures int    `json:"ConsecutiveFailures"`
	LastErrorCategory   string `json:"LastErrorCategory"`
}

// getAndDecodeJSON issues a GET to url, checks for HTTP 200, and decodes the
// JSON body into a value of type T. Returns a pointer to the decoded value.
func getAndDecodeJSON[T any](url, label string) (*T, error) {
	resp, err := http.Get(url) //nolint:gosec // test helper, URL is constructed from controlled input
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", label, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", label, resp.StatusCode)
	}
	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	return &result, nil
}

// GetVMCPStatus queries the /status endpoint on the given NodePort and returns
// the parsed response.
func GetVMCPStatus(nodePort int32) (*VMCPStatusResponse, error) {
	return getAndDecodeJSON[VMCPStatusResponse](
		fmt.Sprintf("http://localhost:%d/status", nodePort), "/status")
}

// GetVMCPBackendsHealth queries the /api/backends/health endpoint on the given
// NodePort and returns the parsed response.
func GetVMCPBackendsHealth(nodePort int32) (*VMCPBackendsHealthResponse, error) {
	return getAndDecodeJSON[VMCPBackendsHealthResponse](
		fmt.Sprintf("http://localhost:%d/api/backends/health", nodePort), "/api/backends/health")
}
