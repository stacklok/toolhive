// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Rate Limiting", Ordered, func() {
	const (
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
	)

	var (
		httpClient   *http.Client
		mcpGroupName string
		backendName  string
		vmcpName     string
		oidcName     string
		oidcConfig   string
		redisName    string
		oidcNodePort int32
		vmcpNodePort int32
		cleanupOIDC  func()
	)

	BeforeAll(func() {
		httpClient = &http.Client{Timeout: 30 * time.Second}

		timestamp := time.Now().UnixNano()
		mcpGroupName = fmt.Sprintf("e2e-vmcp-rl-group-%d", timestamp)
		backendName = fmt.Sprintf("e2e-vmcp-rl-backend-%d", timestamp)
		vmcpName = fmt.Sprintf("e2e-vmcp-rl-%d", timestamp)
		oidcName = fmt.Sprintf("e2e-vmcp-rl-oidc-%d", timestamp)
		oidcConfig = fmt.Sprintf("e2e-vmcp-rl-oidc-config-%d", timestamp)
		redisName = fmt.Sprintf("e2e-vmcp-rl-redis-%d", timestamp)

		By("Deploying Redis for vMCP session storage and rate limiting")
		deployRedis(redisName)

		By("Deploying parameterized OIDC server for per-user identity")
		var issuerURL string
		issuerURL, oidcNodePort, cleanupOIDC = DeployParameterizedOIDCServer(
			ctx, k8sClient, oidcName, defaultNamespace, timeout, pollingInterval)

		By("Creating MCPOIDCConfig for vMCP incoming auth")
		Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfig, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{
					Issuer:                          issuerURL,
					InsecureAllowHTTP:               true,
					JWKSAllowPrivateIP:              true,
					ProtectedResourceAllowPrivateIP: true,
				},
			},
		})).To(Succeed())

		By("Creating MCPGroup and yardstick backend")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, defaultNamespace,
			"VirtualMCPServer rate limit e2e group", timeout, pollingInterval)
		CreateMCPServerAndWait(ctx, k8sClient, backendName, defaultNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer with per-user rate limit")
		redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)
		Expect(k8sClient.Create(ctx, &mcpv1beta1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.VirtualMCPServerSpec{
				GroupRef: &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
				IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
					Type: "oidc",
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
						Name:     oidcConfig,
						Audience: "vmcp-audience",
					},
				},
				ServiceType: "NodePort",
				SessionStorage: &mcpv1beta1.SessionStorageConfig{
					Provider: mcpv1beta1.SessionStorageProviderRedis,
					Address:  redisAddr,
				},
				RateLimiting: &mcpv1beta1.RateLimitConfig{
					PerUser: &mcpv1beta1.RateLimitBucket{
						MaxTokens:    2,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
		})).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollingInterval)
		WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollingInterval)
	})

	AfterAll(func() {
		_ = k8sClient.Delete(ctx, &mcpv1beta1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfig, Namespace: defaultNamespace},
		})
		cleanupRedis(redisName)
		if cleanupOIDC != nil {
			cleanupOIDC()
		}
	})

	It("rejects tools/call after the per-user limit is exceeded", func() {
		token := getVMCPRateLimitOIDCToken(ctx, httpClient, oidcNodePort, "vmcp-rate-limit-user")
		sessionID := sendVMCPRateLimitInitialize(ctx, httpClient, vmcpNodePort, token)

		By("Sending requests within the per-user rate limit")
		for i := range 2 {
			status, body, _ := sendVMCPRateLimitToolCall(ctx, httpClient, vmcpNodePort, "echo", i+1, token, sessionID)
			Expect(status).To(Equal(http.StatusOK),
				"request %d should succeed, got status %d: %s", i+1, status, string(body))
		}

		By("Sending request that exceeds the per-user rate limit")
		status, body, retryAfter := sendVMCPRateLimitToolCall(ctx, httpClient, vmcpNodePort, "echo", 3, token, sessionID)
		Expect(status).To(Equal(http.StatusTooManyRequests),
			"third request should be rate limited, body: %s", string(body))
		Expect(retryAfter).ToNot(BeEmpty(), "Retry-After header should be set")

		var rpcResp map[string]any
		Expect(json.Unmarshal(body, &rpcResp)).To(Succeed())
		errObj, ok := rpcResp["error"].(map[string]any)
		Expect(ok).To(BeTrue(), "response should include JSON-RPC error")
		Expect(errObj["code"]).To(BeEquivalentTo(-32029))
		Expect(errObj["message"]).To(Equal("Rate limit exceeded"))
	})
})

func getVMCPRateLimitOIDCToken(ctx context.Context, httpClient *http.Client, oidcNodePort int32, subject string) string {
	url := fmt.Sprintf("http://localhost:%d/token?subject=%s", oidcNodePort, subject)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	Expect(err).ToNot(HaveOccurred())

	resp, err := httpClient.Do(req)
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	Expect(json.NewDecoder(resp.Body).Decode(&tokenResp)).To(Succeed())
	Expect(tokenResp.AccessToken).ToNot(BeEmpty(), "OIDC server should return a token")
	return tokenResp.AccessToken
}

func sendVMCPRateLimitInitialize(
	ctx context.Context, httpClient *http.Client, port int32, bearerToken string,
) string {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "vmcp-rate-limit-e2e",
				"version": "1.0.0",
			},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	Expect(err).ToNot(HaveOccurred())

	url := fmt.Sprintf("http://localhost:%d/mcp", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	Expect(err).ToNot(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := httpClient.Do(req)
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "initialize should succeed")

	sessionID := resp.Header.Get("Mcp-Session-Id")
	Expect(sessionID).ToNot(BeEmpty(), "initialize response should include Mcp-Session-Id header")
	return sessionID
}

func sendVMCPRateLimitToolCall(
	ctx context.Context,
	httpClient *http.Client,
	port int32,
	toolName string,
	requestID int,
	bearerToken string,
	sessionID string,
) (int, []byte, string) {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": map[string]any{"input": "test"},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	Expect(err).ToNot(HaveOccurred())

	url := fmt.Sprintf("http://localhost:%d/mcp", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	Expect(err).ToNot(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := httpClient.Do(req)
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	Expect(err).ToNot(HaveOccurred())
	return resp.StatusCode, respBody, resp.Header.Get("Retry-After")
}
