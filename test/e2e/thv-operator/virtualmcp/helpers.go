// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp provides helper functions for VirtualMCP E2E tests.
package virtualmcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpclient "github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

// Shared test constants used across all e2e test files in this package.
const (
	defaultNamespace = "default"
	e2eTimeout       = 5 * time.Minute
	e2ePollInterval  = 2 * time.Second
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
	vmcpServer := &mcpv1beta1.VirtualMCPServer{}

	gomega.Eventually(func() error {
		if err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, vmcpServer); err != nil {
			return err
		}

		for _, condition := range vmcpServer.Status.Conditions {
			if condition.Type == mcpv1beta1.ConditionTypeVirtualMCPServerReady {
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
func checkPodsReady(ctx context.Context, c client.Client, namespace string, labels map[string]string) error {
	return testutil.CheckPodsReady(ctx, c, namespace, labels)
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

// getPodLogs retrieves logs from a specific pod container.
func getPodLogs(ctx context.Context, namespace, podName, containerName string, previous bool) (string, error) {
	return testutil.GetPodLogs(ctx, namespace, podName, containerName, previous)
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
func GetMCPGroupBackends(ctx context.Context, c client.Client, groupName, namespace string) ([]mcpv1beta1.MCPServer, error) {
	mcpGroup := &mcpv1beta1.MCPGroup{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      groupName,
		Namespace: namespace,
	}, mcpGroup); err != nil {
		return nil, err
	}

	// Get all MCPServers in the namespace
	mcpServerList := &mcpv1beta1.MCPServerList{}
	if err := c.List(ctx, mcpServerList,
		client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	// Filter MCPServers that reference this group
	var backends []mcpv1beta1.MCPServer
	for _, mcpServer := range mcpServerList.Items {
		if mcpServer.Spec.GroupRef.GetName() == groupName {
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
) (*mcpv1beta1.VirtualMCPServerStatus, error) {
	vmcpServer := &mcpv1beta1.VirtualMCPServer{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, vmcpServer); err != nil {
		return nil, err
	}
	return &vmcpServer.Status, nil
}

// HasCondition checks if a VirtualMCPServer has a specific condition type with expected status
func HasCondition(vmcpServer *mcpv1beta1.VirtualMCPServer, conditionType string, expectedStatus string) bool {
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
		vmcpServer := &mcpv1beta1.VirtualMCPServer{}
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
) *mcpv1beta1.MCPGroup {
	mcpGroup := &mcpv1beta1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1beta1.MCPGroupSpec{
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
		return mcpGroup.Status.Phase == mcpv1beta1.MCPGroupPhaseReady
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
) *mcpv1beta1.MCPServer {
	backend := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1beta1.MCPServerSpec{
			GroupRef:  &mcpv1beta1.MCPGroupRef{Name: groupRef},
			Image:     image,
			Transport: "streamable-http",
			ProxyPort: 8080,
			MCPPort:   8080,
			Resources: defaultMCPServerResources(),
			Env: []mcpv1beta1.EnvVar{
				{Name: "TRANSPORT", Value: "streamable-http"},
			},
		},
	}
	gomega.Expect(c.Create(ctx, backend)).To(gomega.Succeed())

	gomega.Eventually(func() error {
		server := &mcpv1beta1.MCPServer{}
		err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, server)
		if err != nil {
			return fmt.Errorf("failed to get server: %w", err)
		}
		if server.Status.Phase == mcpv1beta1.MCPServerPhaseReady {
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
	ExternalAuthConfigRef *mcpv1beta1.ExternalAuthConfigRef
	Secrets               []mcpv1beta1.SecretRef
	Env                   []mcpv1beta1.EnvVar // additional env vars beyond TRANSPORT
	// Resources overrides the default resource requests/limits. When nil,
	// defaultMCPServerResources() is used to ensure containers are scheduled
	// with reasonable resource guarantees and do not compete excessively.
	Resources *mcpv1beta1.ResourceRequirements
}

// defaultMCPServerResources returns conservative resource requests/limits that
// mirror the quickstart example (vmcp_optimizer_quickstart.yaml) and are
// sufficient for functional E2E testing without starving other pods.
func defaultMCPServerResources() mcpv1beta1.ResourceRequirements {
	return mcpv1beta1.ResourceRequirements{
		Limits: mcpv1beta1.ResourceList{
			CPU:    "200m",
			Memory: "256Mi",
		},
		Requests: mcpv1beta1.ResourceList{
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

		backend := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backends[idx].Name,
				Namespace: backends[idx].Namespace,
			},
			Spec: mcpv1beta1.MCPServerSpec{
				GroupRef:              &mcpv1beta1.MCPGroupRef{Name: backends[idx].GroupRef},
				Image:                 backends[idx].Image,
				Transport:             backendTransport,
				ProxyPort:             8080,
				MCPPort:               8080,
				ExternalAuthConfigRef: backends[idx].ExternalAuthConfigRef,
				Secrets:               backends[idx].Secrets,
				Resources:             resources,
				Env: append([]mcpv1beta1.EnvVar{
					{Name: "TRANSPORT", Value: backendTransport},
				}, backends[idx].Env...),
			},
		}
		gomega.Expect(c.Create(ctx, backend)).To(gomega.Succeed())
	}

	// Wait for all backends to be ready in parallel (single Eventually checking all)
	gomega.Eventually(func() error {
		for _, cfg := range backends {
			server := &mcpv1beta1.MCPServer{}
			err := c.Get(ctx, types.NamespacedName{
				Name:      cfg.Name,
				Namespace: cfg.Namespace,
			}, server)
			if err != nil {
				return fmt.Errorf("failed to get server %s: %w", cfg.Name, err)
			}
			// Fail-fast if server enters Failed phase (e.g., bad image, crash loop)
			if server.Status.Phase == mcpv1beta1.MCPServerPhaseFailed {
				return gomega.StopTrying(fmt.Sprintf("%s failed: %s", cfg.Name, server.Status.Message))
			}
			if server.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
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
		if err := checkHTTPHealthReady(nodePort); err != nil {
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
	_ = conn.Close()
	return nil
}

// checkHTTPHealthReady verifies the HTTP server is ready by checking the /health endpoint.
// This is more reliable than just TCP check as it ensures the application is serving requests.
func checkHTTPHealthReady(nodePort int32) error {
	httpClient := &http.Client{Timeout: 2 * time.Second}
	healthURL := fmt.Sprintf("http://localhost:%d/health", nodePort)

	resp, err := httpClient.Get(healthURL)
	if err != nil {
		return fmt.Errorf("health check failed for port %d: %w", nodePort, err)
	}
	defer func() { _ = resp.Body.Close() }()

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

// WithHttpLoggerOption returns a transport.StreamableHTTPCOption that logs to GinkgoLogr.
// This is useful for debugging HTTP requests and responses.
func WithHttpLoggerOption() transport.StreamableHTTPCOption {
	return transport.WithHTTPLogger(slog.New(logr.ToSlogHandler(ginkgo.GinkgoLogr)))
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

// DeployParameterizedOIDCServer delegates to testutil.DeployParameterizedOIDCServer.
// Kept here for backwards compatibility with existing virtualmcp tests.
func DeployParameterizedOIDCServer(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	timeout, pollingInterval time.Duration,
) (issuerURL string, allocatedNodePort int32, cleanup func()) {
	return testutil.DeployParameterizedOIDCServer(ctx, c, name, namespace, timeout, pollingInterval)
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
func getAndDecodeJSON[T any](rawURL, label string) (*T, error) {
	resp, err := http.Get(rawURL) //nolint:gosec // test helper, URL is constructed from controlled input
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

// InstrumentedMCPBackendStats holds the parsed JSON response from the instrumented MCP backend's /stats endpoint.
type InstrumentedMCPBackendStats struct {
	TotalRequests       int    `json:"total_requests"`
	BearerTokenRequests int    `json:"bearer_token_requests"`
	LastBearerToken     string `json:"last_bearer_token"`
	InitializeCalls     int    `json:"initialize_calls"`
}

// InstrumentedMCPBackendScript is a Python Flask server that implements the MCP streamable-http
// protocol and logs every inbound Authorization: Bearer token to a /stats endpoint. This backend
// is suitable for verifying that outgoing auth strategies (upstreamInject, headerInjection) inject
// tokens into backend requests, including during cross-pod session restore.
const InstrumentedMCPBackendScript = `
pip install --quiet --target=/tmp/packages flask && PYTHONPATH=/tmp/packages python3 - <<'PYTHON_SCRIPT'
from flask import Flask, request, jsonify
import json
import sys

app = Flask(__name__)

stats = {
    "total_requests": 0,
    "bearer_token_requests": 0,
    "last_bearer_token": None,
    "initialize_calls": 0,
}

@app.route('/stats')
def get_stats():
    print(f"Stats request: {stats}", flush=True)
    sys.stdout.flush()
    return jsonify(stats)

@app.route('/health')
def health():
    return jsonify({"status": "ok"})

@app.route('/mcp', methods=['GET', 'POST', 'DELETE'])
def mcp():
    stats["total_requests"] += 1

    auth = request.headers.get("Authorization", "")
    if auth.startswith("Bearer ") and len(auth) > 7:
        stats["bearer_token_requests"] += 1
        stats["last_bearer_token"] = auth[7:]
        fingerprint = __import__('hashlib').sha256(auth.encode()).hexdigest()[:16]
        print(f"*** BEARER TOKEN FINGERPRINT (count={stats['bearer_token_requests']}): {fingerprint}", flush=True)
        sys.stdout.flush()

    if request.method == "DELETE":
        return "", 204

    body = request.get_json(silent=True) or {}
    method = body.get("method", "")
    req_id = body.get("id")

    print(f"MCP request: method={method}, id={req_id}, bearer={bool(stats['last_bearer_token'])}", flush=True)
    sys.stdout.flush()

    if method == "initialize":
        stats["initialize_calls"] += 1
        return jsonify({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "protocolVersion": "2025-06-18",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "instrumented-mcp-backend", "version": "1.0.0"}
            }
        })
    elif method == "notifications/initialized":
        return "", 204
    elif method == "tools/list":
        return jsonify({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "tools": [
                    {
                        "name": "instrumented_ping",
                        "description": "Ping tool for testing token injection",
                        "inputSchema": {"type": "object", "properties": {}}
                    }
                ]
            }
        })
    elif method == "tools/call":
        return jsonify({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "content": [{"type": "text", "text": "pong"}]
            }
        })
    else:
        return jsonify({
            "jsonrpc": "2.0",
            "id": req_id,
            "error": {"code": -32601, "message": "Method not found"}
        })

if __name__ == "__main__":
    print("Instrumented MCP backend starting on port 8080", flush=True)
    sys.stdout.flush()
    app.run(host="0.0.0.0", port=8080, threaded=False)
PYTHON_SCRIPT
`

// GetInstrumentedMCPBackendStats queries the /stats endpoint of an instrumented MCP backend
// and returns the parsed statistics.
//
// Deprecated: prefer GetInstrumentedMCPBackendStatsFromURL with a pre-established
// port-forward; this variant spawns a fresh curl Pod on every call which is expensive
// inside an Eventually polling loop.
func GetInstrumentedMCPBackendStats(
	ctx context.Context, c client.Client, namespace, serviceName string,
) (*InstrumentedMCPBackendStats, error) {
	logs, err := GetServiceStats(ctx, c, namespace, serviceName, 8080)
	if err != nil {
		return nil, err
	}
	var stats InstrumentedMCPBackendStats
	if err := json.Unmarshal([]byte(logs), &stats); err != nil {
		return nil, fmt.Errorf("failed to parse instrumented backend stats JSON %q: %w", logs, err)
	}
	return &stats, nil
}

// GetInstrumentedMCPBackendStatsFromURL fetches the /stats endpoint at statsURL
// via a plain HTTP GET and returns the parsed statistics. It is suitable for use
// inside Eventually polling loops because it is a cheap single HTTP request with
// no pod lifecycle overhead.
func GetInstrumentedMCPBackendStatsFromURL(statsURL string) (*InstrumentedMCPBackendStats, error) {
	//nolint:gosec // statsURL is test-controlled (localhost port-forward)
	resp, err := http.Get(statsURL)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", statsURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: unexpected status %d", statsURL, resp.StatusCode)
	}
	var stats InstrumentedMCPBackendStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decoding stats from %s: %w", statsURL, err)
	}
	return &stats, nil
}

// dexConfig generates the Dex YAML configuration for in-cluster testing.
// The vmcpCallbackURL is the URL that Dex will redirect to after authentication
// (the embedded auth server's callback endpoint).
func dexConfig(issuerURL, vmcpCallbackURL string) string {
	return fmt.Sprintf(`issuer: %s
storage:
  type: memory
web:
  http: 0.0.0.0:5556
connectors:
  - type: mockCallback
    id: mock
    name: Mock
staticClients:
  - id: vmcp-authserver
    secret: authserver-secret
    redirectURIs:
      - %s
    name: VMCP Auth Server
`, issuerURL, vmcpCallbackURL)
}

// DexInfo holds information about a deployed Dex instance.
type DexInfo struct {
	// InClusterIssuerURL is the Dex issuer URL accessible from inside the cluster.
	InClusterIssuerURL string
	// InClusterBaseURL is the base URL for Dex from inside the cluster (for endpoint construction).
	InClusterBaseURL string
	// NodePort is the Kubernetes NodePort for accessing Dex from outside the cluster.
	NodePort int32
	// LocalURL is the Dex URL accessible from the test process (via NodePort).
	LocalURL string
}

// deployDex deploys an in-cluster Dex OIDC provider for testing.
// It uses the mockCallback connector which auto-approves all authentication requests,
// making it suitable for automated E2E tests without browser interaction.
//
// The vmcpCallbackURL must be the embedded auth server's OAuth callback URL
// (typically http://vmcp-<name>.<namespace>.svc.cluster.local:4483/oauth/callback).
// This URL is registered in Dex's static client to allow the embedded AS redirect flow.
//
// Returns a DexInfo struct and a cleanup function. The cleanup function removes all created resources.
func deployDex(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	vmcpCallbackURL string,
	timeout, pollingInterval time.Duration,
) (*DexInfo, func()) {
	inClusterBaseURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:5556", name, namespace)
	configMapName := name + "-config"

	ginkgo.By("Creating Dex ConfigMap with mockCallback connector")
	configData := dexConfig(inClusterBaseURL, vmcpCallbackURL)
	gomega.Expect(c.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: namespace},
		Data:       map[string]string{"config.yaml": configData},
	})).To(gomega.Succeed())

	labels := map[string]string{"app": name}

	ginkgo.By("Creating Dex Deployment")
	gomega.Expect(c.Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "dex",
						Image:   images.DexImage,
						Command: []string{"/usr/local/bin/dex", "serve", "/etc/dex/config.yaml"},
						Ports:   []corev1.ContainerPort{{ContainerPort: 5556, Name: "http"}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/.well-known/openid-configuration",
									Port: intstr.FromInt(5556),
								},
							},
							InitialDelaySeconds: 3,
							PeriodSeconds:       3,
							FailureThreshold:    20,
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "config",
							MountPath: "/etc/dex",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
							},
						},
					}},
				},
			},
		},
	})).To(gomega.Succeed())

	ginkgo.By("Creating Dex NodePort Service")
	dexSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Port:       5556,
				TargetPort: intstr.FromInt(5556),
				Protocol:   corev1.ProtocolTCP,
				Name:       "http",
			}},
		},
	}
	gomega.Expect(c.Create(ctx, dexSvc)).To(gomega.Succeed())

	// Wait for the NodePort to be assigned (assigned asynchronously by the endpoint controller).
	var nodePort int32
	gomega.Eventually(func() (int32, error) {
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, dexSvc); err != nil {
			return 0, err
		}
		if len(dexSvc.Spec.Ports) == 0 || dexSvc.Spec.Ports[0].NodePort == 0 {
			return 0, fmt.Errorf("NodePort not yet assigned for Dex service %s", name)
		}
		nodePort = dexSvc.Spec.Ports[0].NodePort
		return nodePort, nil
	}, timeout, pollingInterval).Should(gomega.BeNumerically(">", 0),
		"Kubernetes should auto-assign a NodePort for Dex")

	ginkgo.By("Waiting for Dex to be ready")
	gomega.Eventually(func() bool {
		dep := &appsv1.Deployment{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, dep); err != nil {
			return false
		}
		return dep.Status.ReadyReplicas > 0
	}, timeout, pollingInterval).Should(gomega.BeTrue(), "Dex should be ready")

	info := &DexInfo{
		InClusterIssuerURL: inClusterBaseURL,
		InClusterBaseURL:   inClusterBaseURL,
		NodePort:           nodePort,
		LocalURL:           fmt.Sprintf("http://localhost:%d", nodePort),
	}

	cleanup := func() {
		_ = c.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = c.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = c.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: namespace}})
	}

	return info, cleanup
}

// embeddedASTokenResult holds the result of an embedded AS token request.
type embeddedASTokenResult struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// getEmbeddedASToken performs the OAuth2 PKCE authorization code flow against the embedded
// auth server (running inside a vMCP pod) using Dex as the upstream OIDC provider.
//
// It works from outside the cluster by:
//  1. Calling the embedded AS /oauth/authorize via port-forward
//  2. Rewriting the Dex redirect URL from in-cluster to the Dex NodePort
//  3. Following Dex's mockCallback auto-approval redirect
//  4. Rewriting the callback URL from in-cluster to the port-forward address
//  5. Exchanging the resulting auth code for an access token
//
// Parameters:
//   - vmcpLocalURL: base URL for the embedded AS via port-forward (e.g., "http://localhost:9090")
//   - dexLocalURL: Dex base URL via NodePort (e.g., "http://localhost:32000")
//   - dexInClusterHost: Dex host as seen from inside the cluster (e.g., "dex.default.svc.cluster.local:5556")
//   - vmcpInClusterHost: vMCP host as seen from inside the cluster (e.g., "vmcp-foo.default.svc.cluster.local:4483")
//   - audience: the resource/audience to include in the OAuth2 request (RFC 8707)
//
// Returns the embedded AS access token (a JWT with a "tsid" claim).
//
//nolint:gocyclo // The complexity arises from sequential error handling in the 9-step OAuth2 PKCE flow, not branching logic.
func getEmbeddedASToken(vmcpLocalURL, dexLocalURL, dexInClusterHost, vmcpInClusterHost, audience string) (string, error) {
	noRedirectClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Step 1: Register an OAuth2 client via DCR
	clientID, clientSecret, err := registerOAuthClient(noRedirectClient, vmcpLocalURL)
	if err != nil {
		return "", fmt.Errorf("DCR failed: %w", err)
	}

	// Step 2: Generate PKCE verifier + challenge
	verifier, challenge := generatePKCEPair()
	clientState := "e2e-test-state"
	clientRedirectURI := "http://localhost:19999/callback"

	// Step 3: Start authorization at embedded AS
	authParams := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {clientRedirectURI},
		"state":                 {clientState},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {"openid offline_access"},
		"resource":              {audience},
	}
	authURL := vmcpLocalURL + "/oauth/authorize?" + authParams.Encode()

	resp, err := noRedirectClient.Get(authURL)
	if err != nil {
		return "", fmt.Errorf("authorize request failed: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return "", fmt.Errorf("expected 302 from /oauth/authorize, got %d", resp.StatusCode)
	}
	dexRedirectURL := resp.Header.Get("Location")
	if dexRedirectURL == "" {
		return "", fmt.Errorf("no Location header from /oauth/authorize")
	}

	// Step 4: Rewrite the Dex in-cluster URL to the local NodePort URL
	localDexURL, err := rewriteURLBase(dexRedirectURL, dexInClusterHost, dexLocalURL)
	if err != nil {
		return "", fmt.Errorf("rewriting Dex URL: %w", err)
	}

	// Step 5: Call Dex auth endpoint. Dex's mockCallback connector may issue
	// one or more intermediate relative redirects (e.g. /auth/mock) before
	// finally redirecting to the VMCP callback URL. Follow any relative
	// redirects on the Dex server until we land on an absolute URL that
	// matches the VMCP in-cluster host.
	dexBaseURL, err := url.Parse(localDexURL)
	if err != nil {
		return "", fmt.Errorf("parsing local Dex URL: %w", err)
	}
	currentURL := localDexURL
	var vmcpCallbackURL string
	for range 10 {
		resp, err = noRedirectClient.Get(currentURL)
		if err != nil {
			return "", fmt.Errorf("calling Dex endpoint %s: %w", currentURL, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		location := resp.Header.Get("Location")

		// Dex v2.42+ shows a consent/approval page (HTTP 200) even for the
		// mockCallback connector. Detect it by path and auto-POST to approve.
		// req and hmac are already in currentURL query params (set by Dex's
		// /callback handler); no form-body inclusion is needed.
		if resp.StatusCode == http.StatusOK {
			parsedCurrent, _ := url.Parse(currentURL)
			if strings.HasSuffix(parsedCurrent.Path, "/approval") {
				formData := url.Values{"approval": {"approve"}}
				postResp, postErr := noRedirectClient.PostForm(currentURL, formData)
				if postErr != nil {
					return "", fmt.Errorf("posting approval form: %w", postErr)
				}
				_, _ = io.Copy(io.Discard, postResp.Body)
				_ = postResp.Body.Close()
				location = postResp.Header.Get("Location")
				resp = postResp
			}
		}

		if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
			return "", fmt.Errorf("expected 302/303 from Dex at %s, got %d", currentURL, resp.StatusCode)
		}
		if location == "" {
			return "", fmt.Errorf("no Location header from Dex at %s", currentURL)
		}
		// Resolve relative redirects against the current Dex base URL.
		resolved, err := dexBaseURL.Parse(location)
		if err != nil {
			return "", fmt.Errorf("resolving redirect URL %q: %w", location, err)
		}
		if resolved.Host == dexBaseURL.Host {
			// Relative redirect still on the NodePort host — follow.
			currentURL = resolved.String()
			continue
		}
		// Dex may redirect from its /auth/mock handler to its OWN in-cluster
		// callback URL (e.g. http://e2e-dex-<ts>.svc.cluster.local:5556/callback).
		// The test process cannot reach that in-cluster URL directly — rewrite
		// it to the NodePort URL and continue following.
		if resolved.Host == dexInClusterHost {
			resolved.Scheme = dexBaseURL.Scheme
			resolved.Host = dexBaseURL.Host
			currentURL = resolved.String()
			continue
		}
		// Absolute URL on a different host — this is the VMCP callback.
		vmcpCallbackURL = resolved.String()
		break
	}
	if vmcpCallbackURL == "" {
		return "", fmt.Errorf("dex did not redirect to the VMCP callback after 10 hops")
	}

	// Step 6: Rewrite the embedded AS callback in-cluster URL to local URL
	localCallbackURL, err := rewriteURLBase(vmcpCallbackURL, vmcpInClusterHost, vmcpLocalURL)
	if err != nil {
		return "", fmt.Errorf("rewriting callback URL: %w", err)
	}

	// Step 7: Call the embedded AS callback to complete the Dex code exchange
	resp, err = noRedirectClient.Get(localCallbackURL)
	if err != nil {
		return "", fmt.Errorf("AS callback request failed: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
		return "", fmt.Errorf("expected 302/303 from AS callback, got %d", resp.StatusCode)
	}
	clientCallbackURL := resp.Header.Get("Location")
	if clientCallbackURL == "" {
		return "", fmt.Errorf("no Location header from AS callback")
	}

	// Step 8: Parse the AS auth code from the redirect to client callback URL
	parsedClientURL, err := url.Parse(clientCallbackURL)
	if err != nil {
		return "", fmt.Errorf("parsing client callback URL: %w", err)
	}
	asCode := parsedClientURL.Query().Get("code")
	if asCode == "" {
		return "", fmt.Errorf("no code in client callback URL: %s", clientCallbackURL)
	}

	// Step 9: Exchange the AS auth code for an access token
	tokenParams := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {asCode},
		"redirect_uri":  {clientRedirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	if clientSecret != "" {
		tokenParams.Set("client_secret", clientSecret)
	}
	tokenURL := vmcpLocalURL + "/oauth/token"
	tokenResp, err := noRedirectClient.PostForm(tokenURL, tokenParams)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = tokenResp.Body.Close() }()

	if tokenResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request returned %d", tokenResp.StatusCode)
	}
	var result embeddedASTokenResult
	if err := json.NewDecoder(tokenResp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("empty access_token in token response")
	}
	return result.AccessToken, nil
}

// registerOAuthClient performs Dynamic Client Registration against the embedded AS.
// Returns the client_id and client_secret (empty string for public clients).
func registerOAuthClient(httpClient *http.Client, vmcpBaseURL string) (clientID, clientSecret string, err error) {
	body, err := json.Marshal(map[string]interface{}{
		"client_name":   "e2e-upstreamInject-test",
		"redirect_uris": []string{"http://localhost:19999/callback"},
		"grant_types":   []string{"authorization_code"},
	})
	if err != nil {
		return "", "", fmt.Errorf("marshaling DCR request body: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, vmcpBaseURL+"/oauth/register", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("building DCR HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("DCR returned status %d", resp.StatusCode)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decoding DCR response: %w", err)
	}
	id, _ := result["client_id"].(string)
	if id == "" {
		return "", "", fmt.Errorf("no client_id in DCR response")
	}
	secret, _ := result["client_secret"].(string)
	return id, secret, nil
}

// generatePKCEPair returns a PKCE (code_verifier, code_challenge) pair using S256.
func generatePKCEPair() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error since Go 1.20 (panics on OS failure instead)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge
}

// rewriteURLBase replaces the scheme+host of urlStr's with the base URL of newBase,
// keeping the original path and query. Returns an error if the original host doesn't
// match expectedHost.
func rewriteURLBase(urlStr, expectedHost, newBase string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", fmt.Errorf("parsing URL %q: %w", urlStr, err)
	}
	if u.Host != expectedHost {
		// Strip ports, compare hostname and port independently so a same-hostname
		// different-port URL does not bypass the guard.
		actualHost, actualPort, splitErrA := net.SplitHostPort(u.Host)
		expectedHostName, expectedPort, splitErrE := net.SplitHostPort(expectedHost)
		if splitErrA != nil {
			actualHost = u.Host
		}
		if splitErrE != nil {
			expectedHostName = expectedHost
		}
		if actualHost != expectedHostName || (expectedPort != "" && actualPort != expectedPort) {
			return "", fmt.Errorf("URL host %q does not match expected host %q (URL: %s)", u.Host, expectedHost, urlStr)
		}
	}
	base, err := url.Parse(newBase)
	if err != nil {
		return "", fmt.Errorf("parsing new base %q: %w", newBase, err)
	}
	u.Scheme = base.Scheme
	u.Host = base.Host
	return u.String(), nil
}
