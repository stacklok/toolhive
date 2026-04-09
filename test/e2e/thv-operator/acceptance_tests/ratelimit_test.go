// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package acceptancetests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

var _ = Describe("MCPServer Rate Limiting", Ordered, func() {
	var (
		testNamespace   = "default"
		serverName      = "ratelimit-test"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		nodePort        int32
		httpClient      *http.Client
	)

	BeforeAll(func() {
		httpClient = &http.Client{Timeout: 10 * time.Second}

		By("Deploying Redis for session storage and rate limiting")
		DeployRedis(ctx, k8sClient, testNamespace, timeout, pollingInterval)

		By("Creating MCPServer with shared rate limit (maxTokens=3, refillPeriod=1m)")
		server := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serverName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				MCPPort:   8080,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
				SessionStorage: &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  fmt.Sprintf("redis.%s.svc.cluster.local:6379", testNamespace),
				},
				RateLimiting: &mcpv1alpha1.RateLimitConfig{
					Shared: &mcpv1alpha1.RateLimitBucket{
						MaxTokens:    3,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("Waiting for MCPServer to be running")
		testutil.WaitForMCPServerRunning(ctx, k8sClient, serverName, testNamespace, timeout, pollingInterval)

		By("Creating NodePort service for MCPServer proxy")
		testutil.CreateNodePortService(ctx, k8sClient, serverName, testNamespace)

		By("Getting NodePort")
		nodePort = testutil.GetNodePort(ctx, k8sClient, serverName+"-nodeport", testNamespace, timeout, pollingInterval)
		GinkgoWriter.Printf("MCPServer accessible at http://localhost:%d\n", nodePort)

		By("Waiting for proxy to be reachable")
		Eventually(func() error {
			resp, err := httpClient.Get(fmt.Sprintf("http://localhost:%d/health", nodePort))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("health check returned %d", resp.StatusCode)
			}
			return nil
		}, 2*time.Minute, pollingInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up NodePort service")
		_ = k8sClient.Delete(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: serverName + "-nodeport", Namespace: testNamespace},
		})

		By("Cleaning up MCPServer")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: testNamespace},
		})

		By("Cleaning up Redis")
		CleanupRedis(ctx, k8sClient, testNamespace)
	})

	It("should reject requests after shared limit exceeded (AC7)", func() {
		By("Sending 3 requests within the rate limit — all should succeed")
		for i := range 3 {
			status, body := SendToolCall(httpClient, nodePort, "echo", i+1)
			Expect(status).To(Equal(http.StatusOK),
				"request %d should succeed, got status %d: %s", i+1, status, string(body))
		}

		By("Sending a 4th request — should be rate limited with HTTP 429")
		status, body := SendToolCall(httpClient, nodePort, "echo", 4)
		Expect(status).To(Equal(http.StatusTooManyRequests),
			"4th request should be rate limited, body: %s", string(body))

		By("Verifying JSON-RPC error code -32029")
		var resp map[string]any
		Expect(json.Unmarshal(body, &resp)).To(Succeed())

		errObj, ok := resp["error"].(map[string]any)
		Expect(ok).To(BeTrue(), "response should have error object")
		Expect(errObj["code"]).To(BeEquivalentTo(-32029))
		Expect(errObj["message"]).To(Equal("Rate limit exceeded"))

		data, ok := errObj["data"].(map[string]any)
		Expect(ok).To(BeTrue(), "error should have data object")
		Expect(data["retryAfterSeconds"]).To(BeNumerically(">", 0))
	})

	It("should accept CRD with both shared and per-tool config (AC8)", func() {
		By("Creating a second MCPServer with both shared and tools config")
		server2Name := "ratelimit-both"
		server2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      server2Name,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				MCPPort:   8080,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
				SessionStorage: &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  fmt.Sprintf("redis.%s.svc.cluster.local:6379", testNamespace),
				},
				RateLimiting: &mcpv1alpha1.RateLimitConfig{
					Shared: &mcpv1alpha1.RateLimitBucket{
						MaxTokens:    100,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
					Tools: []mcpv1alpha1.ToolRateLimitConfig{
						{
							Name: "echo",
							Shared: &mcpv1alpha1.RateLimitBucket{
								MaxTokens:    10,
								RefillPeriod: metav1.Duration{Duration: time.Minute},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, server2)).To(Succeed())

		By("Waiting for MCPServer with both configs to be running")
		testutil.WaitForMCPServerRunning(ctx, k8sClient, server2Name, testNamespace, timeout, pollingInterval)

		By("Cleaning up second MCPServer")
		_ = k8sClient.Delete(ctx, server2)
	})
})

var _ = Describe("MCPServer Per-User Rate Limiting", Ordered, func() {
	var (
		testNamespace   = "default"
		serverName      = "peruser-rl-test"
		oidcServerName  = "oidc-peruser-rl"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second
		nodePort        int32
		oidcNodePort    int32
		httpClient      *http.Client
		oidcCleanup     func()
	)

	BeforeAll(func() {
		httpClient = &http.Client{Timeout: 10 * time.Second}

		By("Ensuring Redis is available for session storage and rate limiting")
		EnsureRedis(ctx, k8sClient, testNamespace, timeout, pollingInterval)

		By("Deploying mock OIDC server for per-user identity")
		var issuerURL string
		issuerURL, oidcNodePort, oidcCleanup = testutil.DeployParameterizedOIDCServer(
			ctx, k8sClient, oidcServerName, testNamespace, timeout, pollingInterval)
		GinkgoWriter.Printf("Mock OIDC server: issuer=%s nodePort=%d\n", issuerURL, oidcNodePort)

		By("Creating MCPServer with per-user rate limit and inline OIDC auth")
		server := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serverName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				McpPort:   8080,
				Env: []mcpv1alpha1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
				SessionStorage: &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  fmt.Sprintf("redis.%s.svc.cluster.local:6379", testNamespace),
				},
				OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: "inline",
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:             issuerURL,
						Audience:           "vmcp-audience",
						JWKSAllowPrivateIP: true,
						InsecureAllowHTTP:  true,
					},
				},
				RateLimiting: &mcpv1alpha1.RateLimitConfig{
					PerUser: &mcpv1alpha1.RateLimitBucket{
						MaxTokens:    2,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("Waiting for MCPServer to be running")
		testutil.WaitForMCPServerRunning(ctx, k8sClient, serverName, testNamespace, timeout, pollingInterval)

		By("Creating NodePort service for MCPServer proxy")
		testutil.CreateNodePortService(ctx, k8sClient, serverName, testNamespace)

		By("Getting NodePort")
		nodePort = testutil.GetNodePort(ctx, k8sClient, serverName+"-nodeport", testNamespace, timeout, pollingInterval)
		GinkgoWriter.Printf("MCPServer accessible at http://localhost:%d\n", nodePort)

		By("Waiting for proxy to be reachable")
		Eventually(func() error {
			resp, err := httpClient.Get(fmt.Sprintf("http://localhost:%d/health", nodePort))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("health check returned %d", resp.StatusCode)
			}
			return nil
		}, 2*time.Minute, pollingInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up NodePort service")
		_ = k8sClient.Delete(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: serverName + "-nodeport", Namespace: testNamespace},
		})

		By("Cleaning up MCPServer")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: testNamespace},
		})

		By("Cleaning up OIDC server")
		if oidcCleanup != nil {
			oidcCleanup()
		}
	})

	It("should reject user after per-user limit exceeded and allow independent user (AC11, AC12)", func() {
		By("Getting JWT for user-a")
		tokenA := GetOIDCToken(httpClient, oidcNodePort, "user-a")

		By("Sending 2 requests as user-a — all should succeed")
		for i := range 2 {
			status, body, _ := SendAuthenticatedToolCall(httpClient, nodePort, "echo", i+1, tokenA)
			Expect(status).To(Equal(http.StatusOK),
				"user-a request %d should succeed, got status %d: %s", i+1, status, string(body))
		}

		By("Sending a 3rd request as user-a — should be rate limited with HTTP 429")
		status, body, retryAfter := SendAuthenticatedToolCall(httpClient, nodePort, "echo", 3, tokenA)
		Expect(status).To(Equal(http.StatusTooManyRequests),
			"user-a 3rd request should be rate limited, body: %s", string(body))

		By("Verifying Retry-After header is present (AC12)")
		Expect(retryAfter).ToNot(BeEmpty(), "Retry-After header should be set on 429 response")

		By("Verifying JSON-RPC error code -32029 with retryAfterSeconds")
		var resp map[string]any
		Expect(json.Unmarshal(body, &resp)).To(Succeed())

		errObj, ok := resp["error"].(map[string]any)
		Expect(ok).To(BeTrue(), "response should have error object")
		Expect(errObj["code"]).To(BeEquivalentTo(-32029))

		data, ok := errObj["data"].(map[string]any)
		Expect(ok).To(BeTrue(), "error should have data object")
		Expect(data["retryAfterSeconds"]).To(BeNumerically(">", 0))

		By("Getting JWT for user-b")
		tokenB := GetOIDCToken(httpClient, oidcNodePort, "user-b")

		By("Sending request as user-b — should succeed (independent bucket)")
		status, body, _ = SendAuthenticatedToolCall(httpClient, nodePort, "echo", 4, tokenB)
		Expect(status).To(Equal(http.StatusOK),
			"user-b should not be blocked by user-a's limit, got status %d: %s", status, string(body))
	})
})
