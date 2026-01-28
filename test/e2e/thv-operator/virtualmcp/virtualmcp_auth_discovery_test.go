// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// authRoundTripper is an HTTP RoundTripper that adds Bearer token authentication
type authRoundTripper struct {
	token     string
	transport http.RoundTripper
}

func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	clonedReq := req.Clone(req.Context())
	clonedReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.token))
	return a.transport.RoundTrip(clonedReq)
}

var _ = Describe("VirtualMCPServer Auth Discovery", Ordered, func() {
	const (
		mockAuthServerName = "mock-auth-server"
	)

	var (
		testNamespace        = "default"
		mcpGroupName         = "test-auth-discovery-group"
		vmcpServerName       = "test-vmcp-auth-discovery"
		backend1Name         = "backend-with-token-exchange"
		backend2Name         = "backend-with-header-injection"
		backend3Name         = "backend-no-auth"
		authConfig1Name      = "test-token-exchange-auth"
		authConfig2Name      = "test-header-injection-auth"
		authSecret1Name      = "test-token-exchange-secret"
		authSecret2Name      = "test-header-injection-secret"
		oidcClientSecretName = "test-oidc-client-secret"
		timeout              = 3 * time.Minute
		pollingInterval      = 1 * time.Second
		mockServer           *httptest.Server
	)

	BeforeAll(func() {
		By("Setting up mock HTTP server for fetch tool testing")
		// Deploy as Kubernetes service instead of local httptest server
		// so it's accessible from inside the cluster
		mockHTTPServerName := "mock-http-server"
		mockHTTPServiceName := "mock-http-server"

		// Create ConfigMap with simple HTTP server
		httpConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-http-server-code",
				Namespace: testNamespace,
			},
			Data: map[string]string{
				"server.py": `#!/usr/bin/env python3
import http.server
import socketserver
from datetime import datetime

class SimpleHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        print(f"[{datetime.now()}] GET request to {self.path}", flush=True)
        self.send_response(200)
        self.send_header('Content-Type', 'text/plain')
        self.end_headers()
        self.wfile.write(b"Mock response for auth discovery test")

    def log_message(self, format, *args):
        print(f"[{datetime.now()}] HTTP: {format % args}", flush=True)

PORT = 8080
with socketserver.TCPServer(("", PORT), SimpleHandler) as httpd:
    print(f"Mock HTTP server listening on port {PORT}", flush=True)
    httpd.serve_forever()
`,
			},
		}
		Expect(k8sClient.Create(ctx, httpConfigMap)).To(Succeed())

		// Create the HTTP server pod
		httpServerPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mockHTTPServerName,
				Namespace: testNamespace,
				Labels: map[string]string{
					"app": "mock-http-server",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "http-server",
						Image: "python:3.11-slim",
						Command: []string{
							"python3",
							"/app/server.py",
						},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 8080,
								Protocol:      corev1.ProtocolTCP,
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
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "server-code",
								MountPath: "/app",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "server-code",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "mock-http-server-code",
								},
								DefaultMode: int32Ptr(0755),
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, httpServerPod)).To(Succeed())

		// Create service for HTTP server
		httpServerService := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mockHTTPServiceName,
				Namespace: testNamespace,
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app": "mock-http-server",
				},
				Ports: []corev1.ServicePort{
					{
						Port:       80,
						TargetPort: intstr.FromInt(8080),
						Protocol:   corev1.ProtocolTCP,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, httpServerService)).To(Succeed())

		// Wait for pod to be ready
		Eventually(func() bool {
			pod := &corev1.Pod{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mockHTTPServerName,
				Namespace: testNamespace,
			}, pod)
			if err != nil {
				return false
			}
			return pod.Status.Phase == corev1.PodRunning
		}, 2*time.Minute, 2*time.Second).Should(BeTrue(), "Mock HTTP server pod should be running")

		// Set the mockServer URL to the Kubernetes service
		mockServer = &httptest.Server{}
		mockServer.URL = fmt.Sprintf("http://%s.%s.svc.cluster.local", mockHTTPServiceName, testNamespace)

		By("Setting up mock OAuth token exchange server as a Kubernetes pod")
		// Create a simple HTTP server pod in Kubernetes that will capture token exchange requests
		authServerPodName := mockAuthServerName
		authServerServiceName := mockAuthServerName

		// Create ConfigMap with the server code
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-auth-server-code",
				Namespace: testNamespace,
			},
			Data: map[string]string{
				"server.py": `#!/usr/bin/env python3
import http.server
import socketserver
import json
import urllib.parse
from datetime import datetime

class AuthHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        print(f"[{datetime.now()}] POST request to {self.path}", flush=True)
        print(f"  Headers: {dict(self.headers)}", flush=True)

        if self.path == '/token':
            content_length = int(self.headers['Content-Length'])
            post_data = self.rfile.read(content_length)
            params = urllib.parse.parse_qs(post_data.decode('utf-8'))

            # NOTE: Logging sensitive information (client_secret) is intentional for debugging E2E test failures.
            # This is test-only code and should NEVER be done in production environments.
            print(f"[{datetime.now()}] Token exchange request received:", flush=True)
            print(f"  client_id: {params.get('client_id', [''])[0]}", flush=True)
            print(f"  client_secret: {params.get('client_secret', [''])[0]}", flush=True)
            print(f"  audience: {params.get('audience', [''])[0]}", flush=True)
            print(f"  grant_type: {params.get('grant_type', [''])[0]}", flush=True)

            # Return mock token response (RFC 8693 compliant)
            response = {
                "access_token": "mock_access_token_from_k8s_server",
                "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
                "token_type": "Bearer",
                "expires_in": 3600
            }
            print(f"[{datetime.now()}] Returning token exchange response:", flush=True)
            print(f"  access_token: {response['access_token']}", flush=True)
            print(f"  token_type: {response['token_type']}", flush=True)
            print(f"  expires_in: {response['expires_in']}", flush=True)
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())
        else:
            print(f"[{datetime.now()}] 404 - Path not found: {self.path}", flush=True)
            self.send_response(404)
            self.end_headers()

    def do_GET(self):
        print(f"[{datetime.now()}] GET request to {self.path}", flush=True)
        self.send_response(404)
        self.end_headers()

    def log_message(self, format, *args):
        print(f"[{datetime.now()}] HTTP: {format % args}", flush=True)

PORT = 8080
with socketserver.TCPServer(("", PORT), AuthHandler) as httpd:
    print(f"Mock auth server listening on port {PORT}", flush=True)
    httpd.serve_forever()
`,
			},
		}
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())

		// Create the pod
		authServerPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authServerPodName,
				Namespace: testNamespace,
				Labels: map[string]string{
					"app": "mock-auth-server",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "auth-server",
						Image: "python:3.11-slim",
						Command: []string{
							"python3",
							"/app/server.py",
						},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 8080,
								Protocol:      corev1.ProtocolTCP,
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
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "server-code",
								MountPath: "/app",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "server-code",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "mock-auth-server-code",
								},
								DefaultMode: int32Ptr(0755),
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, authServerPod)).To(Succeed())

		// Create a service for the auth server
		authServerService := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authServerServiceName,
				Namespace: testNamespace,
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app": "mock-auth-server",
				},
				Ports: []corev1.ServicePort{
					{
						Port:       80,
						TargetPort: intstr.FromInt(8080),
						Protocol:   corev1.ProtocolTCP,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, authServerService)).To(Succeed())

		// Wait for the pod to be ready
		Eventually(func() bool {
			pod := &corev1.Pod{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      authServerPodName,
				Namespace: testNamespace,
			}, pod)
			if err != nil {
				return false
			}

			// Check pod is running
			if pod.Status.Phase != corev1.PodRunning {
				return false
			}

			// Check containers are ready
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.ContainersReady && condition.Status == corev1.ConditionTrue {
					return true
				}
			}
			return false
		}, 2*time.Minute, 2*time.Second).Should(BeTrue(), "Mock auth server pod should be running and ready")

		GinkgoWriter.Printf("Mock auth server deployed in Kubernetes at: http://%s.%s.svc.cluster.local/token\n",
			authServerServiceName, testNamespace)

		By("Setting up mock OIDC server as a Kubernetes pod")
		// Deploy a simple OIDC server that issues test tokens
		oidcServerPodName := "mock-oidc-server"
		oidcServerServiceName := "mock-oidc-server"

		// Create ConfigMap with the OIDC server code
		oidcConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-oidc-server-code",
				Namespace: testNamespace,
			},
			Data: map[string]string{
				"server.py": `#!/usr/bin/env python3
import http.server
import socketserver
import json
import base64
import time
from datetime import datetime
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.primitives import serialization, hashes
from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives.asymmetric import padding as asym_padding
import hashlib
import hmac

# Generate a 2048-bit RSA key pair at startup
print("Generating 2048-bit RSA key pair...", flush=True)
private_key = rsa.generate_private_key(
    public_exponent=65537,
    key_size=2048,
    backend=default_backend()
)
public_key = private_key.public_key()

# Extract public key components for JWKS
public_numbers = public_key.public_numbers()
n = public_numbers.n
e = public_numbers.e

# Convert to base64url format for JWKS
def int_to_base64url(num):
    num_bytes = num.to_bytes((num.bit_length() + 7) // 8, byteorder='big')
    return base64.urlsafe_b64encode(num_bytes).decode('utf-8').rstrip('=')

n_b64 = int_to_base64url(n)
e_b64 = int_to_base64url(e)

print(f"RSA key pair generated. Modulus size: {n.bit_length()} bits", flush=True)

class OIDCHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        print(f"[{datetime.now()}] GET request to {self.path}", flush=True)

        if self.path == '/.well-known/openid-configuration':
            # OIDC discovery endpoint
            discovery = {
                "issuer": "http://mock-oidc-server.default.svc.cluster.local",
                "authorization_endpoint": "http://mock-oidc-server.default.svc.cluster.local/auth",
                "token_endpoint": "http://mock-oidc-server.default.svc.cluster.local/token",
                "jwks_uri": "http://mock-oidc-server.default.svc.cluster.local/jwks",
                "response_types_supported": ["code"],
                "subject_types_supported": ["public"],
                "id_token_signing_alg_values_supported": ["RS256"]
            }
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(discovery).encode())
        elif self.path == '/jwks':
            # JWKS endpoint - return the real public key
            jwks = {
                "keys": [{
                    "kty": "RSA",
                    "use": "sig",
                    "kid": "test-key-1",
                    "alg": "RS256",
                    "n": n_b64,
                    "e": e_b64
                }]
            }
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(jwks).encode())
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        print(f"[{datetime.now()}] POST request to {self.path}", flush=True)

        if self.path == '/token':
            # Token endpoint - return a properly signed JWT
            header = {"alg": "RS256", "typ": "JWT", "kid": "test-key-1"}
            payload = {
                "sub": "test-user",
                "iss": "http://mock-oidc-server.default.svc.cluster.local",
                "aud": "vmcp-audience",
                "exp": int(time.time()) + 3600,
                "iat": int(time.time())
            }

            # Create base64url encoded header and payload
            header_b64 = base64.urlsafe_b64encode(json.dumps(header, separators=(',', ':')).encode()).decode().rstrip('=')
            payload_b64 = base64.urlsafe_b64encode(json.dumps(payload, separators=(',', ':')).encode()).decode().rstrip('=')

            # Sign with RSA private key
            message = f"{header_b64}.{payload_b64}".encode()
            signature = private_key.sign(
                message,
                asym_padding.PKCS1v15(),
                hashes.SHA256()
            )
            signature_b64 = base64.urlsafe_b64encode(signature).decode().rstrip('=')

            jwt_token = f"{header_b64}.{payload_b64}.{signature_b64}"

            response = {
                "access_token": jwt_token,
                "token_type": "Bearer",
                "expires_in": 3600
            }

            print(f"[{datetime.now()}] Issuing signed access token with kid=test-key-1", flush=True)
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        print(f"[{datetime.now()}] HTTP: {format % args}", flush=True)

PORT = 8080
with socketserver.TCPServer(("", PORT), OIDCHandler) as httpd:
    print(f"Mock OIDC server listening on port {PORT}", flush=True)
    httpd.serve_forever()
`,
			},
		}
		Expect(k8sClient.Create(ctx, oidcConfigMap)).To(Succeed())

		// Create the OIDC server pod
		oidcServerPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcServerPodName,
				Namespace: testNamespace,
				Labels: map[string]string{
					"app": "mock-oidc-server",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "oidc-server",
						Image: "python:3.11-slim",
						Command: []string{
							"sh",
							"-c",
							"pip install --no-cache-dir cryptography && python3 /app/server.py",
						},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 8080,
								Protocol:      corev1.ProtocolTCP,
							},
						},
						// Readiness probe ensures the HTTP server is actually listening
						// before the pod is considered ready. This is important because
						// pip install runs first and takes time.
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/.well-known/openid-configuration",
									Port: intstr.FromInt(8080),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       2,
							TimeoutSeconds:      5,
							SuccessThreshold:    1,
							FailureThreshold:    30, // Allow up to 60s for pip install
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "server-code",
								MountPath: "/app",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "server-code",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "mock-oidc-server-code",
								},
								DefaultMode: int32Ptr(0755),
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, oidcServerPod)).To(Succeed())

		// Create a service for the OIDC server with NodePort for test client access
		oidcServerService := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcServerServiceName,
				Namespace: testNamespace,
			},
			Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
				Selector: map[string]string{
					"app": "mock-oidc-server",
				},
				Ports: []corev1.ServicePort{
					{
						Port:       80,
						TargetPort: intstr.FromInt(8080),
						Protocol:   corev1.ProtocolTCP,
						NodePort:   30010, // Fixed NodePort for test client access
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, oidcServerService)).To(Succeed())

		// Wait for the OIDC server pod to be ready (both Running and ContainersReady)
		Eventually(func() bool {
			pod := &corev1.Pod{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcServerPodName,
				Namespace: testNamespace,
			}, pod)
			if err != nil {
				return false
			}

			// Check pod is running
			if pod.Status.Phase != corev1.PodRunning {
				return false
			}

			// Check containers are ready
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.ContainersReady && condition.Status == corev1.ConditionTrue {
					return true
				}
			}
			return false
		}, 2*time.Minute, 2*time.Second).Should(BeTrue(), "Mock OIDC server pod should be running and ready")

		GinkgoWriter.Printf("Mock OIDC server deployed in Kubernetes at: http://%s.%s.svc.cluster.local\n",
			oidcServerServiceName, testNamespace)

		By("Creating secrets for authentication")
		// Secret for token exchange
		secret1 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecret1Name,
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"client-secret": []byte("test-client-secret-value"),
			},
		}
		Expect(k8sClient.Create(ctx, secret1)).To(Succeed())

		// Secret for header injection
		secret2 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authSecret2Name,
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"api-key": []byte("test-api-key-value"),
			},
		}
		Expect(k8sClient.Create(ctx, secret2)).To(Succeed())

		// Secret for OIDC client secret
		oidcClientSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcClientSecretName,
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"client-secret": []byte("vmcp-secret"),
			},
		}
		Expect(k8sClient.Create(ctx, oidcClientSecret)).To(Succeed())

		By("Creating MCPExternalAuthConfig for token exchange")
		// Use the Kubernetes service URL for our mock auth server
		tokenURL := fmt.Sprintf("http://mock-auth-server.%s.svc.cluster.local/token", testNamespace)
		GinkgoWriter.Printf("Configuring token exchange to use mock server: %s\n", tokenURL)

		authConfig1 := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfig1Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
					TokenURL: tokenURL,
					ClientID: "test-client-id",
					ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: authSecret1Name,
						Key:  "client-secret",
					},
					Audience:         "https://api.example.com",
					Scopes:           []string{"read", "write"},
					SubjectTokenType: "access_token",
				},
			},
		}
		Expect(k8sClient.Create(ctx, authConfig1)).To(Succeed())

		By("Creating MCPExternalAuthConfig for header injection")
		authConfig2 := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      authConfig2Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
					ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: authSecret2Name,
						Key:  "api-key",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, authConfig2)).To(Succeed())

		By("Creating MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPGroupSpec{
				Description: "Test MCP Group for auth discovery E2E tests",
			},
		}
		Expect(k8sClient.Create(ctx, mcpGroup)).To(Succeed())

		By("Waiting for MCPGroup to be ready")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			}, mcpGroup)
			if err != nil {
				return false
			}
			return mcpGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
		}, timeout, pollingInterval).Should(BeTrue())

		By("Creating backend MCPServers in parallel with different auth configurations")
		CreateMultipleMCPServersInParallel(ctx, k8sClient, []BackendConfig{
			{
				Name:      backend1Name,
				Namespace: testNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.GofetchServerImage,
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: authConfig1Name,
				},
			},
			{
				Name:      backend2Name,
				Namespace: testNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.OSVMCPServerImage,
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: authConfig2Name,
				},
			},
			{
				Name:      backend3Name,
				Namespace: testNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.GofetchServerImage,
				// No ExternalAuthConfigRef - this backend has no auth
			},
		}, timeout, pollingInterval)

		// Wait for backend pods to be ready
		for _, backendName := range []string{backend1Name, backend2Name, backend3Name} {
			backendLabels := map[string]string{
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": backendName,
			}
			WaitForPodsReady(ctx, k8sClient, testNamespace, backendLabels, timeout, pollingInterval)
		}

		By("Creating VirtualMCPServer with discovered auth mode and short token cache TTL")
		// Create PodTemplateSpec with debug environment variables
		podTemplateSpec := corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "vmcp",
						Env: []corev1.EnvVar{
							{
								Name:  "TOOLHIVE_DEBUG",
								Value: "true",
							},
							{
								Name:  "LOG_LEVEL",
								Value: "debug",
							},
						},
					},
				},
			},
		}

		podTemplateRaw, err := json.Marshal(podTemplateSpec)
		Expect(err).ToNot(HaveOccurred())

		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
					// No TokenCache configured - tokens should be fetched on each request
					Aggregation: &vmcpconfig.AggregationConfig{
						ConflictResolution: "prefix",
					},
				},
				// OIDC incoming auth - clients must present valid OIDC tokens
				// vMCP will validate tokens and then exchange them for backend-specific tokens
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "oidc",
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: "inline",
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   fmt.Sprintf("http://mock-oidc-server.%s.svc.cluster.local", testNamespace),
							ClientID: "vmcp-client",
							Audience: "vmcp-audience",
							ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
								Name: oidcClientSecretName,
								Key:  "client-secret",
							},
							InsecureAllowHTTP:               true,
							JWKSAllowPrivateIP:              true,
							ProtectedResourceAllowPrivateIP: true,
						},
					},
				},
				// DISCOVERED MODE: vMCP will discover outgoing auth from backend MCPServers
				// Backend has token exchange configured, vMCP will discover and use it
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
				},
				ServiceType: "NodePort",
				// Enable debug logging via PodTemplateSpec
				PodTemplateSpec: &runtime.RawExtension{
					Raw: podTemplateRaw,
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		// Wait for vMCP pods to be fully running and ready
		By("Waiting for vMCP pods to be running and ready")
		vmcpLabels := map[string]string{
			"app.kubernetes.io/name":     "virtualmcpserver",
			"app.kubernetes.io/instance": vmcpServerName,
		}
		WaitForPodsReady(ctx, k8sClient, testNamespace, vmcpLabels, timeout, pollingInterval)
	})

	AfterAll(func() {
		// Use a shorter timeout for cleanup - pods should delete quickly
		cleanupTimeout := 60 * time.Second

		By("Cleaning up mock HTTP server")
		_ = k8sClient.Delete(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-http-server",
				Namespace: testNamespace,
			},
		})
		_ = k8sClient.Delete(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-http-server",
				Namespace: testNamespace,
			},
		})
		_ = k8sClient.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-http-server-code",
				Namespace: testNamespace,
			},
		})

		By("Cleaning up mock auth server")
		_ = k8sClient.Delete(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-auth-server",
				Namespace: testNamespace,
			},
		})
		_ = k8sClient.Delete(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-auth-server",
				Namespace: testNamespace,
			},
		})
		_ = k8sClient.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-auth-server-code",
				Namespace: testNamespace,
			},
		})

		By("Cleaning up mock OIDC server")
		_ = k8sClient.Delete(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-oidc-server",
				Namespace: testNamespace,
			},
		})
		_ = k8sClient.Delete(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-oidc-server",
				Namespace: testNamespace,
			},
		})
		_ = k8sClient.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mock-oidc-server-code",
				Namespace: testNamespace,
			},
		})

		By("Waiting for mock server pods to be fully deleted")
		WaitForPodDeletion(ctx, k8sClient, "mock-http-server", testNamespace, cleanupTimeout, pollingInterval)
		WaitForPodDeletion(ctx, k8sClient, "mock-auth-server", testNamespace, cleanupTimeout, pollingInterval)
		WaitForPodDeletion(ctx, k8sClient, "mock-oidc-server", testNamespace, cleanupTimeout, pollingInterval)

		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, vmcpServer)

		By("Cleaning up backend MCPServers")
		for _, backendName := range []string{backend1Name, backend2Name, backend3Name} {
			backend := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      backendName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, backend)
		}

		By("Cleaning up MCPExternalAuthConfigs")
		for _, authConfigName := range []string{authConfig1Name, authConfig2Name} {
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      authConfigName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, authConfig)
		}

		By("Cleaning up secrets")
		for _, secretName := range []string{authSecret1Name, authSecret2Name, oidcClientSecretName} {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: testNamespace,
				},
			}
			_ = k8sClient.Delete(ctx, secret)
		}

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpGroup)
	})

	Context("when verifying discovered auth configuration", func() {
		It("should have correct discovered auth configuration with all backends and auth configs", func() {
			By("Verifying VirtualMCPServer has discovered auth mode")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer)
			Expect(err).ToNot(HaveOccurred())
			Expect(vmcpServer.Spec.OutgoingAuth).ToNot(BeNil())
			Expect(vmcpServer.Spec.OutgoingAuth.Source).To(Equal("discovered"))

			By("Verifying all three backends are discovered in the group")
			backends, err := GetMCPGroupBackends(ctx, k8sClient, mcpGroupName, testNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(backends).To(HaveLen(3))

			backendNames := make([]string, len(backends))
			for i, backend := range backends {
				backendNames[i] = backend.Name
			}
			Expect(backendNames).To(ContainElements(backend1Name, backend2Name, backend3Name))

			By("Verifying ExternalAuthConfigRef on backends with auth")
			backend1 := &mcpv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backend1Name, Namespace: testNamespace}, backend1)).To(Succeed())
			Expect(backend1.Spec.ExternalAuthConfigRef).ToNot(BeNil())
			Expect(backend1.Spec.ExternalAuthConfigRef.Name).To(Equal(authConfig1Name))

			backend2 := &mcpv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backend2Name, Namespace: testNamespace}, backend2)).To(Succeed())
			Expect(backend2.Spec.ExternalAuthConfigRef).ToNot(BeNil())
			Expect(backend2.Spec.ExternalAuthConfigRef.Name).To(Equal(authConfig2Name))

			backend3 := &mcpv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backend3Name, Namespace: testNamespace}, backend3)).To(Succeed())
			Expect(backend3.Spec.ExternalAuthConfigRef).To(BeNil())

			By("Verifying token exchange MCPExternalAuthConfig")
			authConfig1 := &mcpv1alpha1.MCPExternalAuthConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: authConfig1Name, Namespace: testNamespace}, authConfig1)).To(Succeed())
			expectedTokenURL := fmt.Sprintf("http://mock-auth-server.%s.svc.cluster.local/token", testNamespace)
			Expect(authConfig1.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeTokenExchange))
			Expect(authConfig1.Spec.TokenExchange).ToNot(BeNil())
			Expect(authConfig1.Spec.TokenExchange.TokenURL).To(Equal(expectedTokenURL))
			Expect(authConfig1.Spec.TokenExchange.ClientID).To(Equal("test-client-id"))
			Expect(authConfig1.Spec.TokenExchange.Audience).To(Equal("https://api.example.com"))
			Expect(authConfig1.Spec.TokenExchange.Scopes).To(Equal([]string{"read", "write"}))
			Expect(authConfig1.Spec.TokenExchange.ClientSecretRef.Name).To(Equal(authSecret1Name))
			Expect(authConfig1.Spec.TokenExchange.ClientSecretRef.Key).To(Equal("client-secret"))

			By("Verifying header injection MCPExternalAuthConfig")
			authConfig2 := &mcpv1alpha1.MCPExternalAuthConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: authConfig2Name, Namespace: testNamespace}, authConfig2)).To(Succeed())
			Expect(authConfig2.Spec.Type).To(Equal(mcpv1alpha1.ExternalAuthTypeHeaderInjection))
			Expect(authConfig2.Spec.HeaderInjection).ToNot(BeNil())
			Expect(authConfig2.Spec.HeaderInjection.HeaderName).To(Equal("X-API-Key"))
			Expect(authConfig2.Spec.HeaderInjection.ValueSecretRef.Name).To(Equal(authSecret2Name))
			Expect(authConfig2.Spec.HeaderInjection.ValueSecretRef.Key).To(Equal("api-key"))

			By("Verifying secrets have correct values")
			secret1 := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: authSecret1Name, Namespace: testNamespace}, secret1)).To(Succeed())
			Expect(secret1.Data).To(HaveKey("client-secret"))
			Expect(string(secret1.Data["client-secret"])).To(Equal("test-client-secret-value"))

			secret2 := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: authSecret2Name, Namespace: testNamespace}, secret2)).To(Succeed())
			Expect(secret2.Data).To(HaveKey("api-key"))
			Expect(string(secret2.Data["api-key"])).To(Equal("test-api-key-value"))
		})
	})

	Context("when verifying VirtualMCPServer state", func() {
		It("should have VirtualMCPServer and all backends in ready state", func() {
			By("Verifying VirtualMCPServer is Ready")
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpServerName, Namespace: testNamespace}, vmcpServer)).To(Succeed())
			Expect(vmcpServer.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady))

			By("Verifying all backends are Running")
			for _, backendName := range []string{backend1Name, backend2Name, backend3Name} {
				backend := &mcpv1alpha1.MCPServer{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendName, Namespace: testNamespace}, backend)).To(Succeed())
				Expect(backend.Status.Phase).To(Equal(mcpv1alpha1.MCPServerPhaseRunning))
			}

			GinkgoWriter.Println("Discovered auth mode successfully aggregates backends with:")
			GinkgoWriter.Println("  - Token exchange authentication (OAuth 2.0)")
			GinkgoWriter.Println("  - Header injection authentication (API Key)")
			GinkgoWriter.Println("  - No authentication")
		})
	})

	Context("when testing discovered auth behavior with real MCP requests", func() {
		var vmcpNodePort int32

		BeforeAll(func() {
			By("Verifying VirtualMCPServer is still ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

			By("Verifying vMCP pods are still running and ready")
			vmcpLabels := map[string]string{
				"app.kubernetes.io/name":     "virtualmcpserver",
				"app.kubernetes.io/instance": vmcpServerName,
			}
			WaitForPodsReady(ctx, k8sClient, testNamespace, vmcpLabels, timeout, pollingInterval)

			By("Getting NodePort for VirtualMCPServer")
			Eventually(func() error {
				service := &corev1.Service{}
				serviceName := fmt.Sprintf("vmcp-%s", vmcpServerName)
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      serviceName,
					Namespace: testNamespace,
				}, service)
				if err != nil {
					return err
				}

				// Wait for NodePort to be assigned by Kubernetes
				if len(service.Spec.Ports) == 0 || service.Spec.Ports[0].NodePort == 0 {
					return fmt.Errorf("nodePort not assigned yet")
				}
				vmcpNodePort = service.Spec.Ports[0].NodePort
				return nil
			}, timeout, pollingInterval).Should(Succeed())

			By(fmt.Sprintf("VirtualMCPServer accessible at http://localhost:%d", vmcpNodePort))
		})

		// Helper function to get OIDC token from mock server via client credentials flow
		getOIDCToken := func() string {
			tokenURL := "http://localhost:30010/token"
			resp, err := http.PostForm(tokenURL, nil)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var tokenResp struct {
				AccessToken string `json:"access_token"`
				TokenType   string `json:"token_type"`
				ExpiresIn   int    `json:"expires_in"`
			}
			Expect(json.NewDecoder(resp.Body).Decode(&tokenResp)).To(Succeed())
			Expect(tokenResp.AccessToken).ToNot(BeEmpty())
			return tokenResp.AccessToken
		}

		// Helper function to create authenticated HTTP client
		createAuthenticatedHTTPClient := func(token string) *http.Client {
			return &http.Client{
				Transport: &authRoundTripper{
					token:     token,
					transport: http.DefaultTransport,
				},
				Timeout: 30 * time.Second,
			}
		}

		It("should list and call tools from all backends with discovered auth", func() {
			By("Verifying vMCP pods are still running and ready before health check")
			vmcpLabels := map[string]string{
				"app.kubernetes.io/name":     "virtualmcpserver",
				"app.kubernetes.io/instance": vmcpServerName,
			}
			WaitForPodsReady(ctx, k8sClient, testNamespace, vmcpLabels, 30*time.Second, 2*time.Second)

			// Create HTTP client with timeout for health checks
			healthCheckClient := &http.Client{
				Timeout: 10 * time.Second,
			}

			By("Verifying HTTP connectivity to VirtualMCPServer health endpoint")
			Eventually(func() error {
				// Re-check pod readiness before each health check attempt
				if err := checkPodsReady(ctx, k8sClient, testNamespace, vmcpLabels); err != nil {
					return fmt.Errorf("pods not ready: %w", err)
				}
				url := fmt.Sprintf("http://localhost:%d/health", vmcpNodePort)
				resp, err := healthCheckClient.Get(url)
				if err != nil {
					return fmt.Errorf("health check failed: %w", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
				}
				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("Getting OIDC token from mock OIDC server")
			oidcToken := getOIDCToken()

			By("Starting transport and initializing connection with retries")
			// Retry MCP initialization to handle timing issues where the VirtualMCPServer's
			// auth middleware (OIDC validation and auth discovery) may not be fully ready
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			authenticatedHTTPClient := createAuthenticatedHTTPClient(oidcToken)

			testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			mcpClient := InitializeMCPClientWithRetries(serverURL, 2*time.Minute, WithHttpLoggerOption(), transport.WithHTTPBasicClient(authenticatedHTTPClient))
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(testCtx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())
			Expect(len(tools.Tools)).To(BeNumerically(">=", 2), "Should aggregate tools from multiple backends")

			GinkgoWriter.Printf("VirtualMCPServer aggregates %d tools with discovered auth\n", len(tools.Tools))

			By("Calling fetch tools from backends with different auth configurations")
			toolsToTest := []string{"backend-with-token-exchange_fetch", "backend-no-auth_fetch"}

			for _, targetToolName := range toolsToTest {
				var toolFound bool
				for _, tool := range tools.Tools {
					if tool.Name == targetToolName {
						toolFound = true
						break
					}
				}
				Expect(toolFound).To(BeTrue(), "Expected tool %s should be available", targetToolName)

				toolCallCtx, toolCallCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer toolCallCancel()

				callRequest := mcp.CallToolRequest{}
				callRequest.Params.Name = targetToolName
				callRequest.Params.Arguments = map[string]any{"url": mockServer.URL}

				result, err := mcpClient.CallTool(toolCallCtx, callRequest)
				Expect(err).ToNot(HaveOccurred(), "Tool call should succeed: %s", targetToolName)
				Expect(result).ToNot(BeNil())
				GinkgoWriter.Printf("✓ Successfully called tool: %s\n", targetToolName)
			}
		})

		It("should send auth tokens to configured auth servers", func() {
			By("Calling tools to trigger token exchange")

			By("Getting OIDC token for test client authentication")
			token := getOIDCToken()

			// Create authenticated MCP client and call tools to generate traffic
			By("Creating authenticated MCP client for VirtualMCPServer")
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
			httpClient := createAuthenticatedHTTPClient(token)
			mcpClient, err := mcpclient.NewStreamableHttpClient(
				serverURL,
				transport.WithHTTPBasicClient(httpClient),
			)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Starting transport and initializing connection with retries")
			// Retry MCP initialization to handle timing issues where the VirtualMCPServer's
			// auth middleware (OIDC validation and auth discovery) may not be fully ready
			testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			Eventually(func() error {
				initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer initCancel()

				err := mcpClient.Start(initCtx)
				if err != nil {
					return fmt.Errorf("failed to start transport: %w", err)
				}

				initRequest := mcp.InitializeRequest{}
				initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				initRequest.Params.ClientInfo = mcp.Implementation{
					Name:    "toolhive-auth-test",
					Version: "1.0.0",
				}

				_, err = mcpClient.Initialize(initCtx, initRequest)
				if err != nil {
					return fmt.Errorf("failed to initialize: %w", err)
				}

				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "MCP client should initialize successfully")

			By("Listing and calling tools from backend with token exchange")
			listRequest := mcp.ListToolsRequest{}
			tools, err := mcpClient.ListTools(testCtx, listRequest)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty())

			// Debug: Print all tools
			GinkgoWriter.Printf("\n=== All tools returned by vMCP ===\n")
			for _, tool := range tools.Tools {
				GinkgoWriter.Printf("  - %s\n", tool.Name)
			}
			GinkgoWriter.Printf("Looking for tools containing '%s' and 'fetch'\n", backend1Name)

			// Find and call a tool from the backend with token exchange auth
			var calledTokenExchangeTool bool
			for _, tool := range tools.Tools {
				if strings.Contains(tool.Name, backend1Name) && strings.Contains(tool.Name, "fetch") {
					GinkgoWriter.Printf("Calling tool with token exchange auth: %s\n", tool.Name)
					toolCallCtx, toolCallCancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer toolCallCancel()

					callRequest := mcp.CallToolRequest{}
					callRequest.Params.Name = tool.Name
					callRequest.Params.Arguments = map[string]any{
						"url": mockServer.URL,
					}

					_, err := mcpClient.CallTool(toolCallCtx, callRequest)
					if err == nil {
						GinkgoWriter.Printf("✓ Successfully called tool: %s\n", tool.Name)
						calledTokenExchangeTool = true
					}
					break
				}
			}

			Expect(calledTokenExchangeTool).To(BeTrue(), "Should have called at least one tool from token exchange backend")

			By("Checking mock auth server logs for token exchange requests")
			authServerPodName := "mock-auth-server"

			// Wait for auth server to receive and log token exchange requests
			// Token exchange may happen during vMCP startup or initialization, not necessarily during tool calls
			var logs string
			Eventually(func() bool {
				var err error
				logs, err = GetPodLogs(ctx, authServerPodName, testNamespace, "auth-server")
				if err != nil {
					GinkgoWriter.Printf("Unable to get auth server logs: %v\n", err)
					return false
				}
				// Check if logs contain evidence of token exchange
				return strings.Contains(logs, "Token exchange") || strings.Contains(logs, "token") || len(logs) > 100
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "Auth server should have received requests")

			Expect(logs).ToNot(BeEmpty(), "Should have logs from mock auth server")

			// Also check vMCP logs to see if it's attempting token exchange
			By("Checking vMCP logs for token exchange activity")
			// Dynamically discover vMCP pod name (Deployment uses random suffix, not StatefulSet-style -0)
			vmcpPodList := &corev1.PodList{}
			err = k8sClient.List(ctx, vmcpPodList,
				client.InNamespace(testNamespace),
				client.MatchingLabels{
					"app.kubernetes.io/name":     "virtualmcpserver",
					"app.kubernetes.io/instance": vmcpServerName,
				})
			Expect(err).ToNot(HaveOccurred(), "Should be able to list vMCP pods")
			Expect(vmcpPodList.Items).ToNot(BeEmpty(), "Should have at least one vMCP pod")
			vmcpPodName := vmcpPodList.Items[0].Name
			vmcpLogs, vmcpErr := GetPodLogs(ctx, vmcpPodName, testNamespace, "vmcp")
			if vmcpErr == nil {
				GinkgoWriter.Printf("\n=== vMCP Logs (full) ===\n")
				GinkgoWriter.Printf("%s\n", vmcpLogs)
			} else {
				GinkgoWriter.Printf("Warning: Could not get vMCP logs: %v\n", vmcpErr)
			}

			// Check if the logs contain token exchange requests - THIS MUST HAPPEN
			hasTokenExchange := strings.Contains(logs, "Token exchange request received")
			Expect(hasTokenExchange).To(BeTrue(),
				"Mock auth server should have received token exchange requests from vMCP.\n"+
					"This indicates that vMCP is properly propagating identity context to authentication strategies.\n"+
					"Auth server logs:\n%s", logs)

			// Verify the auth parameters are in the logs
			// Note: client_id and client_secret are sent in Authorization header (Basic auth),
			// so we check for the header presence instead of POST body params
			Expect(logs).To(ContainSubstring("'Authorization': 'Basic"),
				"Token request should include Basic auth header with client credentials")

			Expect(logs).To(ContainSubstring("audience: https://api.example.com"),
				"Token request should include audience")

			Expect(logs).To(ContainSubstring("grant_type: urn:ietf:params:oauth:grant-type:token-exchange"),
				"Token request should use token exchange grant type")

			// Verify token exchange succeeded (returned an access_token)
			Expect(logs).To(ContainSubstring("access_token"),
				"Token exchange response should include access_token")

			GinkgoWriter.Printf("✓ Found Authorization header with client credentials in token request\n")
			GinkgoWriter.Printf("✓ Found audience in token request\n")
			GinkgoWriter.Printf("✓ Found token exchange grant type in token request\n")
			GinkgoWriter.Printf("✓ Token exchange succeeded (access_token returned)\n")

			GinkgoWriter.Printf("\n✓ Token exchange verification complete:\n")
			GinkgoWriter.Printf("  - vMCP discovered token exchange auth from backend ExternalAuthConfigRef\n")
			GinkgoWriter.Printf("  - vMCP successfully propagated identity context to auth strategies\n")
			GinkgoWriter.Printf("  - vMCP exchanged client's OIDC token for backend-specific token\n")
			GinkgoWriter.Printf("  - Auth server received client credentials (Basic auth), audience, and grant type\n")
			GinkgoWriter.Printf("  - Tool calls succeeded proving end-to-end auth flow works\n")
		})

	})
})
