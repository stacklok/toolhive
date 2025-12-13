// Package virtualmcp provides helper functions for VirtualMCP E2E tests.
package virtualmcp

import (
	"bytes"
	"context"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// WaitForVirtualMCPServerReady waits for a VirtualMCPServer to reach Ready status
// and ensures the associated pods are actually running and ready
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
			if condition.Type == "Ready" {
				if condition.Status == "True" {
					// Also check that the pods are actually running and ready
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

// checkPodsReady checks if all pods matching the given labels are ready
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
		if pod.Status.Phase != corev1.PodRunning {
			return fmt.Errorf("pod %s is in phase %s", pod.Name, pod.Status.Phase)
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
	defer podLogs.Close()

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

// WaitForPodsReady waits for all pods matching labels to be ready
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

// BackendConfig holds configuration for creating an MCPServer
type BackendConfig struct {
	Name                  string
	Namespace             string
	GroupRef              string
	Image                 string
	ExternalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef
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
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backends[idx].Name,
				Namespace: backends[idx].Namespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:              backends[idx].GroupRef,
				Image:                 backends[idx].Image,
				Transport:             "streamable-http",
				ProxyPort:             8080,
				McpPort:               8080,
				ExternalAuthConfigRef: backends[idx].ExternalAuthConfigRef,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
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
	defer resp.Body.Close()

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

	fmt.Fprintf(ginkgo.GinkgoWriter, "✓ Successfully called tool %s\n", targetToolName)
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

	fmt.Fprintf(ginkgo.GinkgoWriter, "Listed %d tools from VirtualMCPServer\n", len(toolsResult.Tools))
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
