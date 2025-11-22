package virtualmcp

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// WaitForVirtualMCPServerReady waits for a VirtualMCPServer to reach Ready status
func WaitForVirtualMCPServerReady(ctx context.Context, c client.Client, name, namespace string, timeout time.Duration) {
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
					return nil
				}
				return fmt.Errorf("ready condition is %s: %s", condition.Status, condition.Message)
			}
		}
		return fmt.Errorf("ready condition not found")
	}, timeout, 5*time.Second).Should(gomega.Succeed())
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
func WaitForPodsReady(ctx context.Context, c client.Client, namespace string, labels map[string]string, timeout time.Duration) {
	gomega.Eventually(func() error {
		podList := &corev1.PodList{}
		if err := c.List(ctx, podList,
			client.InNamespace(namespace),
			client.MatchingLabels(labels)); err != nil {
			return err
		}

		if len(podList.Items) == 0 {
			return fmt.Errorf("no pods found with labels %v", labels)
		}

		for _, pod := range podList.Items {
			if pod.Status.Phase != corev1.PodRunning {
				return fmt.Errorf("pod %s is in phase %s", pod.Name, pod.Status.Phase)
			}

			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.ContainersReady && condition.Status != corev1.ConditionTrue {
					return fmt.Errorf("pod %s containers not ready", pod.Name)
				}
			}
		}
		return nil
	}, timeout, 5*time.Second).Should(gomega.Succeed())
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
	}, timeout, 5*time.Second).Should(gomega.Succeed())
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
							Image:   "python:3.9-slim",
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
	}, 3*time.Minute, 5*time.Second).Should(gomega.BeTrue(), "Mock OIDC server should be ready")
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
							Image:   "python:3.9-slim",
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
	}, 3*time.Minute, 5*time.Second).Should(gomega.BeTrue(), "Instrumented backend should be ready")
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

// GetPodLogsForDeployment returns information about pods for a deployment (for debugging)
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

	podName := pods.Items[0].Name
	return fmt.Sprintf("Pod: %s (run: kubectl logs %s -n %s)", podName, podName, namespace)
}

func int32Ptr(i int32) *int32 {
	return &i
}

// MockOIDCServerHTTPScript is a mock OIDC server script with HTTP (for testing with private IPs)
const MockOIDCServerHTTPScript = `
pip install --quiet flask && python3 - <<'PYTHON_SCRIPT'
from flask import Flask, jsonify, request
import sys

app = Flask(__name__)

@app.route('/.well-known/openid-configuration')
def discovery():
    print("OIDC Discovery request received", flush=True)
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
    print("JWKS request received", flush=True)
    sys.stdout.flush()
    return jsonify({"keys": []})

@app.route('/token', methods=['POST'])
def token():
    print("Token request received", flush=True)
    sys.stdout.flush()
    return jsonify({
        "access_token": "mock_access_token_12345",
        "token_type": "Bearer",
        "expires_in": 3600,
    })

if __name__ == '__main__':
    print("Mock OIDC server starting on port 80 with HTTP", flush=True)
    sys.stdout.flush()
    app.run(host='0.0.0.0', port=80)
PYTHON_SCRIPT
`

// InstrumentedBackendScript is an instrumented backend script that logs all headers including Bearer tokens
const InstrumentedBackendScript = `
pip install --quiet flask && python3 - <<'PYTHON_SCRIPT'
from flask import Flask, request, jsonify
import sys

app = Flask(__name__)

@app.route('/<path:path>', methods=['GET', 'POST'])
def catch_all(path):
    print("=== Request received ===", flush=True)
    print(f"Path: {path}", flush=True)
    print("Headers:", flush=True)
    for header, value in request.headers.items():
        print(f"  {header}: {value}", flush=True)
        if header.lower() == "authorization" and "Bearer" in value:
            print(f"*** BEARER TOKEN DETECTED: {value} ***", flush=True)
    sys.stdout.flush()
    return jsonify({"status": "ok", "path": path})

if __name__ == '__main__':
    print("Instrumented backend starting on port 8080", flush=True)
    sys.stdout.flush()
    app.run(host='0.0.0.0', port=8080)
PYTHON_SCRIPT
`
