// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = ginkgo.Describe("VirtualMCPServer Session Management", func() {
	const (
		timeout           = time.Minute * 2
		pollInterval      = time.Second * 2
		defaultNamespace  = "default"
		vmcpContainerName = "vmcp"
	)

	// ---------------------------------------------------------------------------
	// Context 1: HMAC secret auto-management and functional session tests
	// ---------------------------------------------------------------------------

	ginkgo.Context("When session management is enabled", ginkgo.Ordered, func() {
		var (
			mcpGroupName       string
			virtualMCPName     string
			backendName        string
			expectedSecretName string
			vmcpNodePort       int32
		)

		ginkgo.BeforeAll(func() {
			timestamp := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-sm-%d", timestamp)
			virtualMCPName = fmt.Sprintf("e2e-vmcp-sm-%d", timestamp)
			backendName = fmt.Sprintf("e2e-yardstick-sm-%d", timestamp)
			expectedSecretName = virtualMCPName + "-hmac-secret"

			ginkgo.By("Creating MCPGroup")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
				Spec:       mcpv1alpha1.MCPGroupSpec{Description: "Session management e2e group"},
			})).To(gomega.Succeed())

			ginkgo.By("Creating yardstick backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			ginkgo.By("Creating VirtualMCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: virtualMCPName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					ServiceType:  "NodePort",
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for VirtualMCPServer to be ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, virtualMCPName, defaultNamespace, timeout, pollInterval)

			ginkgo.By("Getting NodePort")
			vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, virtualMCPName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: virtualMCPName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: virtualMCPName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying HMAC secret is garbage-collected via owner reference")
			gomega.Eventually(func() bool {
				secret := &corev1.Secret{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: expectedSecretName, Namespace: defaultNamespace}, secret)
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue(), "HMAC secret should be garbage-collected when VirtualMCPServer is deleted")
		})

		ginkgo.It("Should automatically create HMAC secret", func() {
			ginkgo.By("Waiting for HMAC secret to be created by operator")
			secret := &corev1.Secret{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedSecretName,
					Namespace: defaultNamespace,
				}, secret)
			}, timeout, pollInterval).Should(gomega.Succeed())

			gomega.Expect(secret.Name).To(gomega.Equal(expectedSecretName))
		})

		ginkgo.It("Should have correct secret structure and metadata", func() {
			secret := &corev1.Secret{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedSecretName,
					Namespace: defaultNamespace,
				}, secret)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Verifying secret type")
			gomega.Expect(secret.Type).To(gomega.Equal(corev1.SecretTypeOpaque))

			ginkgo.By("Verifying labels")
			gomega.Expect(secret.Labels).To(gomega.HaveKeyWithValue("app.kubernetes.io/name", "virtualmcpserver"))
			gomega.Expect(secret.Labels).To(gomega.HaveKeyWithValue("app.kubernetes.io/instance", virtualMCPName))
			gomega.Expect(secret.Labels).To(gomega.HaveKeyWithValue("app.kubernetes.io/component", "session-security"))
			gomega.Expect(secret.Labels).To(gomega.HaveKeyWithValue("app.kubernetes.io/managed-by", "toolhive-operator"))

			ginkgo.By("Verifying annotations")
			gomega.Expect(secret.Annotations).To(gomega.HaveKeyWithValue("toolhive.stacklok.dev/purpose", "hmac-secret-for-session-token-binding"))

			ginkgo.By("Verifying owner reference for cascade deletion")
			gomega.Expect(secret.OwnerReferences).To(gomega.HaveLen(1))
			gomega.Expect(secret.OwnerReferences[0].Name).To(gomega.Equal(virtualMCPName))
			gomega.Expect(secret.OwnerReferences[0].Kind).To(gomega.Equal("VirtualMCPServer"))
		})

		ginkgo.It("Should contain a valid 32-byte base64-encoded HMAC secret", func() {
			secret := &corev1.Secret{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedSecretName,
					Namespace: defaultNamespace,
				}, secret)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Verifying secret has hmac-secret key")
			gomega.Expect(secret.Data).To(gomega.HaveKey("hmac-secret"))

			hmacSecretBase64 := string(secret.Data["hmac-secret"])
			gomega.Expect(hmacSecretBase64).NotTo(gomega.BeEmpty())

			ginkgo.By("Verifying secret is valid base64")
			decoded, err := base64.StdEncoding.DecodeString(hmacSecretBase64)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.By("Verifying decoded secret is exactly 32 bytes")
			gomega.Expect(decoded).To(gomega.HaveLen(32))

			ginkgo.By("Verifying secret is not all zeros")
			gomega.Expect(decoded).NotTo(gomega.Equal(make([]byte, 32)))
		})

		ginkgo.It("Should inject HMAC secret into deployment as environment variable", func() {
			deployment := &appsv1.Deployment{}

			ginkgo.By("Waiting for deployment to be created")
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				}, deployment)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Finding vmcp container in deployment")
			gomega.Expect(deployment.Spec.Template.Spec.Containers).NotTo(gomega.BeEmpty())

			var vmcpContainer *corev1.Container
			for i, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == vmcpContainerName {
					vmcpContainer = &deployment.Spec.Template.Spec.Containers[i]
					break
				}
			}
			gomega.Expect(vmcpContainer).NotTo(gomega.BeNil())

			ginkgo.By("Verifying VMCP_SESSION_HMAC_SECRET environment variable exists")
			var hmacSecretEnvVar *corev1.EnvVar
			for i, env := range vmcpContainer.Env {
				if env.Name == "VMCP_SESSION_HMAC_SECRET" {
					hmacSecretEnvVar = &vmcpContainer.Env[i]
					break
				}
			}
			gomega.Expect(hmacSecretEnvVar).NotTo(gomega.BeNil())

			ginkgo.By("Verifying env var is sourced from the secret")
			gomega.Expect(hmacSecretEnvVar.ValueFrom).NotTo(gomega.BeNil())
			gomega.Expect(hmacSecretEnvVar.ValueFrom.SecretKeyRef).NotTo(gomega.BeNil())
			gomega.Expect(hmacSecretEnvVar.ValueFrom.SecretKeyRef.Name).To(gomega.Equal(expectedSecretName))
			gomega.Expect(hmacSecretEnvVar.ValueFrom.SecretKeyRef.Key).To(gomega.Equal("hmac-secret"))
		})

		ginkgo.It("Should allow multiple clients to connect with independent sessions", func() {
			ginkgo.By("Creating first client")
			firstClient, err := CreateInitializedMCPClient(vmcpNodePort, "client-first", 30*time.Second)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			defer firstClient.Close()

			sessionIDFirst := firstClient.Client.GetSessionId()
			gomega.Expect(sessionIDFirst).NotTo(gomega.BeEmpty(), "first client should have a session ID")

			ginkgo.By("Creating second client")
			secondClient, err := CreateInitializedMCPClient(vmcpNodePort, "client-second", 30*time.Second)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			defer secondClient.Close()

			sessionIDSecond := secondClient.Client.GetSessionId()
			gomega.Expect(sessionIDSecond).NotTo(gomega.BeEmpty(), "second client should have a session ID")

			ginkgo.By("Verifying sessions are independent (different IDs)")
			gomega.Expect(sessionIDFirst).NotTo(gomega.Equal(sessionIDSecond))

			ginkgo.By("Both clients can list tools from the backend")
			toolsFirst, err := firstClient.Client.ListTools(firstClient.Ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(toolsFirst.Tools).NotTo(gomega.BeEmpty())

			toolsSecond, err := secondClient.Client.ListTools(secondClient.Ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(toolsSecond.Tools).NotTo(gomega.BeEmpty())

			ginkgo.By("Both clients see the same tool catalog")
			gomega.Expect(toolsFirst.Tools).To(gomega.HaveLen(len(toolsSecond.Tools)))
		})

		ginkgo.It("Should allow a client to make multiple calls on the same session", func() {
			client, err := CreateInitializedMCPClient(vmcpNodePort, "multi-call-client", 30*time.Second)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			defer client.Close()

			sessionID := client.Client.GetSessionId()
			gomega.Expect(sessionID).NotTo(gomega.BeEmpty())

			ginkgo.By("Listing tools multiple times on the same session")
			for i := range 3 {
				tools, err := client.Client.ListTools(client.Ctx, mcp.ListToolsRequest{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "call %d should succeed", i+1)
				gomega.Expect(tools.Tools).NotTo(gomega.BeEmpty())
				// Session ID must remain stable across calls
				gomega.Expect(client.Client.GetSessionId()).To(gomega.Equal(sessionID))
			}
		})

		ginkgo.It("Should route tool calls through the session to the backend", func() {
			// TestToolListingAndCall discovers the actual (possibly-prefixed) tool name via
			// ListTools and calls it with alphanumeric-only input (yardstick requirement).
			TestToolListingAndCall(vmcpNodePort, "tool-call-client", "echo", "sessiontest")
		})
	})

	// ---------------------------------------------------------------------------
	// Context 2: HMAC secret created by default
	// ---------------------------------------------------------------------------

	ginkgo.Context("When creating VirtualMCPServer without explicit session management flag", ginkgo.Ordered, func() {
		var (
			mcpGroupName       string
			virtualMCPName     string
			expectedSecretName string
		)

		ginkgo.BeforeAll(func() {
			timestamp := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-default-sm-%d", timestamp)
			virtualMCPName = fmt.Sprintf("e2e-vmcp-default-sm-%d", timestamp)
			expectedSecretName = virtualMCPName + "-hmac-secret"

			ginkgo.By("Creating MCPGroup")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
				Spec:       mcpv1alpha1.MCPGroupSpec{Description: "Default session management group"},
			})).To(gomega.Succeed())

			ginkgo.By("Creating VirtualMCPServer with default configuration")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: virtualMCPName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config:       vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
				},
			})).To(gomega.Succeed())
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: virtualMCPName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
		})

		ginkgo.It("Should create HMAC secret by default when no flag is set", func() {
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedSecretName,
					Namespace: defaultNamespace,
				}, &corev1.Secret{})
			}, timeout, pollInterval).Should(gomega.Succeed(), "HMAC secret should be created when no session management flag is set (default is true)")
		})

		ginkgo.It("Should inject HMAC secret env var by default when no flag is set", func() {
			deployment := &appsv1.Deployment{}

			ginkgo.By("Waiting for deployment to be created")
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: defaultNamespace,
				}, deployment)
			}, timeout, pollInterval).Should(gomega.Succeed())

			ginkgo.By("Finding vmcp container")
			var vmcpContainer *corev1.Container
			for i, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == vmcpContainerName {
					vmcpContainer = &deployment.Spec.Template.Spec.Containers[i]
					break
				}
			}
			gomega.Expect(vmcpContainer).NotTo(gomega.BeNil())

			ginkgo.By("Verifying VMCP_SESSION_HMAC_SECRET env var exists")
			found := false
			for _, env := range vmcpContainer.Env {
				if env.Name == "VMCP_SESSION_HMAC_SECRET" {
					found = true
					break
				}
			}
			gomega.Expect(found).To(gomega.BeTrue(), "VMCP_SESSION_HMAC_SECRET env var should be present by default")
		})
	})

	// ---------------------------------------------------------------------------
	// Context 3: HMAC token binding prevents session hijacking with JWT auth
	// ---------------------------------------------------------------------------

	ginkgo.Context("Session token binding prevents session hijacking", ginkgo.Ordered, func() {
		const (
			oidcServiceName = "mock-oidc-session-test"
		)

		var (
			mcpGroupName string
			vmcpName     string
			backendName  string
			vmcpNodePort int32
			oidcNodePort int32
			oidcIssuer   string
			oidcCleanup  func()
		)

		// getJWTForSubject fetches a signed JWT from the in-cluster OIDC server
		// for the given subject via the test-accessible NodePort.
		getJWTForSubject := func(subject string) string {
			url := fmt.Sprintf("http://localhost:%d/token?subject=%s", oidcNodePort, subject)
			resp, err := http.Post(url, "application/x-www-form-urlencoded", nil) //nolint:noctx
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			defer resp.Body.Close() // safe: only registered after the nil-safe error check above
			gomega.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK))

			var tokenResp struct {
				AccessToken string `json:"access_token"`
			}
			gomega.Expect(json.NewDecoder(resp.Body).Decode(&tokenResp)).To(gomega.Succeed())
			gomega.Expect(tokenResp.AccessToken).NotTo(gomega.BeEmpty())
			return tokenResp.AccessToken
		}

		// newAuthHTTPClient wraps an HTTP client that adds Bearer token to every request.
		newAuthHTTPClient := func(token string) *http.Client {
			return &http.Client{
				Transport: &authRoundTripper{token: token, transport: http.DefaultTransport},
				Timeout:   30 * time.Second,
			}
		}

		// connectWithToken initialises an MCP client authenticated with the given JWT.
		connectWithToken := func(serverURL, token string) *mcpclient.Client {
			httpClient := newAuthHTTPClient(token)
			mc := InitializeMCPClientWithRetries(serverURL, 2*time.Minute,
				transport.WithHTTPBasicClient(httpClient),
			)
			return mc
		}

		ginkgo.BeforeAll(func() {
			timestamp := time.Now().UnixNano()
			mcpGroupName = fmt.Sprintf("e2e-hijack-%d", timestamp)
			vmcpName = fmt.Sprintf("e2e-vmcp-hijack-%d", timestamp)
			backendName = fmt.Sprintf("e2e-yardstick-hijack-%d", timestamp)

			// ---- Deploy parameterized mock OIDC server ----
			oidcIssuer, oidcNodePort, oidcCleanup = DeployParameterizedOIDCServer(
				ctx, k8sClient, oidcServiceName, defaultNamespace, 3*time.Minute, pollInterval,
			)

			// ---- Deploy yardstick backend ----

			ginkgo.By("Creating MCPGroup")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
				Spec:       mcpv1alpha1.MCPGroupSpec{Description: "Session hijacking test group"},
			})).To(gomega.Succeed())

			ginkgo.By("Creating yardstick backend MCPServer")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     images.YardstickServerImage,
					Transport: "streamable-http",
					ProxyPort: 8080,
					McpPort:   8080,
				},
			})).To(gomega.Succeed())

			// ---- Deploy VirtualMCPServer with OIDC incoming auth ----

			ginkgo.By("Creating VirtualMCPServer with OIDC incoming auth")
			gomega.Expect(k8sClient.Create(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
							Type: "inline",
							Inline: &mcpv1alpha1.InlineOIDCConfig{
								Issuer:                          oidcIssuer,
								Audience:                        "vmcp-audience",
								InsecureAllowHTTP:               true,
								JWKSAllowPrivateIP:              true,
								ProtectedResourceAllowPrivateIP: true,
							},
						},
					},
					ServiceType: "NodePort",
				},
			})).To(gomega.Succeed())

			ginkgo.By("Waiting for VirtualMCPServer to be ready")
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)

			ginkgo.By("Getting NodePort for VirtualMCPServer")
			vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		})

		ginkgo.AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			})
			oidcCleanup()

			// Wait for the vMCP to be fully gone before the next test context starts.
			gomega.Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1alpha1.VirtualMCPServer{})
				return apierrors.IsNotFound(err)
			}, timeout, pollInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("Client using another client's session ID with a different token is rejected", func() {
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

			ginkgo.By("Alice establishes a session")
			aliceToken := getJWTForSubject("alice")
			aliceClient := connectWithToken(serverURL, aliceToken)
			defer aliceClient.Close()

			aliceSessionID := aliceClient.GetSessionId()
			gomega.Expect(aliceSessionID).NotTo(gomega.BeEmpty())

			ginkgo.By("Bob gets a different JWT")
			bobToken := getJWTForSubject("bob")
			gomega.Expect(bobToken).NotTo(gomega.Equal(aliceToken), "alice and bob must have different tokens")

			ginkgo.By("Bob tries to call a tool using Alice's session ID")
			// Bob sends a raw JSON-RPC request with Alice's session ID but his own Authorization header.
			// The server must reject this because the token hash stored in Alice's session does not
			// match Bob's token hash.
			// Use "echo" — the tool exposed by the yardstick backend — with a valid
			// argument so that rejection is unambiguously from token-binding, not from
			// a missing tool or argument validation error.
			reqBody := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"input":"hijack-test"}},"id":1}`
			httpReq, err := http.NewRequest(http.MethodPost, serverURL, strings.NewReader(reqBody))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			httpReq.Header.Set("Authorization", "Bearer "+bobToken)
			httpReq.Header.Set("Mcp-Session-Id", aliceSessionID)
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Accept", "application/json, text/event-stream")

			resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			defer resp.Body.Close()

			ginkgo.By("Verifying the hijacking attempt is rejected")
			body, readErr := io.ReadAll(resp.Body)
			gomega.Expect(readErr).ToNot(gomega.HaveOccurred())

			// The session manager handles ErrUnauthorizedCaller by terminating the
			// session and returning mcp.NewToolResultError — always HTTP 200 with
			// result.isError=true. A non-200 response here (e.g. 401 from the auth
			// middleware) would mean Bob's JWT was unexpectedly rejected before the
			// token-binding check could run, which is a different failure and should
			// not silently pass as "hijacking was blocked".
			gomega.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK),
				"expected HTTP 200 with MCP-level isError rejection, got body: %s", string(body))

			var rpcResp struct {
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
				Result *struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			gomega.Expect(json.Unmarshal(body, &rpcResp)).To(gomega.Succeed())

			rejected := (rpcResp.Error != nil) || (rpcResp.Result != nil && rpcResp.Result.IsError)
			gomega.Expect(rejected).To(gomega.BeTrue(),
				"expected session hijacking to be rejected via MCP isError, got: %s", string(body))
		})

		ginkgo.It("Each client gets their own independent session", func() {
			serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

			ginkgo.By("Alice and Bob each connect with their own token")
			aliceToken := getJWTForSubject("alice")
			bobToken := getJWTForSubject("bob")

			aliceClient := connectWithToken(serverURL, aliceToken)
			defer aliceClient.Close()

			bobClient := connectWithToken(serverURL, bobToken)
			defer bobClient.Close()

			ginkgo.By("Verifying they have distinct session IDs")
			aliceSessionID := aliceClient.GetSessionId()
			bobSessionID := bobClient.GetSessionId()
			gomega.Expect(aliceSessionID).NotTo(gomega.BeEmpty())
			gomega.Expect(bobSessionID).NotTo(gomega.BeEmpty())
			gomega.Expect(aliceSessionID).NotTo(gomega.Equal(bobSessionID))

			ginkgo.By("Both clients can independently list tools")
			toolsA, err := aliceClient.ListTools(ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(toolsA.Tools).NotTo(gomega.BeEmpty())

			toolsB, err := bobClient.ListTools(ctx, mcp.ListToolsRequest{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(toolsB.Tools).NotTo(gomega.BeEmpty())

			ginkgo.By("Both clients can independently call tools on their own sessions")
			// Discover the real tool name (may be prefixed as backendName_echo).
			// Use alphanumeric-only input — yardstick rejects values with hyphens.
			var echoToolName string
			for _, tool := range toolsA.Tools {
				if strings.Contains(tool.Name, "echo") {
					echoToolName = tool.Name
					break
				}
			}
			gomega.Expect(echoToolName).NotTo(gomega.BeEmpty(), "should find an echo tool in the tool list")

			callReq := mcp.CallToolRequest{}
			callReq.Params.Name = echoToolName
			callReq.Params.Arguments = map[string]any{"input": "aliceindependentcall"}
			aliceResult, err := aliceClient.CallTool(ctx, callReq)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(aliceResult.IsError).To(gomega.BeFalse())

			callReq.Params.Arguments = map[string]any{"input": "bobindependentcall"}
			bobResult, err := bobClient.CallTool(ctx, callReq)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(bobResult.IsError).To(gomega.BeFalse())
		})
	})

})
