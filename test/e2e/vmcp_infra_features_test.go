// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package e2e_test contains infrastructure-heavy vMCP CLI e2e tests that require
// external services (OIDC server, Redis) as test fixtures.
// These complement the basic feature tests in vmcp_cli_features_test.go.
// Tracked by: https://github.com/stacklok/toolhive/issues/4944
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// fetchClientCredentialsToken obtains an access token from the mock OIDC server
// using the client_credentials grant. The token is suitable for use as a Bearer
// token in Authorization headers when the vMCP server has OIDC incoming auth
// configured with the same issuer.
func fetchClientCredentialsToken(oidcPort int, clientID, clientSecret, audience string) string {
	tokenURL := fmt.Sprintf("http://localhost:%d/token", oidcPort)
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {"openid"},
		"audience":      {audience},
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.PostForm(tokenURL, form) //nolint:gosec // URL is test-controlled
	Expect(err).ToNot(HaveOccurred(), "should POST to OIDC token endpoint")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK),
		"token endpoint should return 200; body: %s", body)
	var result map[string]any
	Expect(json.Unmarshal(body, &result)).To(Succeed())
	token, ok := result["access_token"].(string)
	Expect(ok).To(BeTrue(), "token response should contain access_token; body: %s", body)
	return token
}

// startRedisContainer starts a Redis container on the given host port with the
// given container name. The container is started detached and removed on stop.
func startRedisContainer(containerName string, hostPort int) {
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"--name", containerName,
		"-p", fmt.Sprintf("127.0.0.1:%d:6379", hostPort),
		images.RedisImage,
	).CombinedOutput()
	Expect(err).ToNot(HaveOccurred(), "should start Redis container: %s", out)
}

// stopRedisContainer stops a running Redis container.
func stopRedisContainer(containerName string) {
	_ = exec.Command("docker", "stop", containerName).Run()
}

// waitForRedisReady polls the Redis container until it responds to PING.
func waitForRedisReady(containerName string, timeout time.Duration) {
	GinkgoWriter.Printf("waiting for Redis container %q to respond to PING\n", containerName)
	Eventually(func() error {
		out, err := exec.Command("docker", "exec", containerName, "redis-cli", "ping").CombinedOutput()
		if err != nil {
			return fmt.Errorf("redis-cli ping: %w; output: %s", err, out)
		}
		if !strings.Contains(string(out), "PONG") {
			return fmt.Errorf("unexpected ping response: %q", string(out))
		}
		return nil
	}, timeout, 2*time.Second).Should(Succeed(), "Redis should respond to PING")
}

var _ = Describe("vMCP infra features", Label("vmcp", "e2e", "infra"), func() {

	// -------------------------------------------------------------------------
	// JWT/OIDC incoming auth
	// Verifies that vMCP enforces OIDC token validation on incoming connections:
	//   - Unauthenticated MCP clients are rejected.
	//   - A client presenting a valid Bearer JWT can connect and list tools.
	//
	// Uses the OIDCMockServer from test/e2e/oidc_mock.go (Ory Fosite-backed)
	// and obtains a token via the client_credentials grant.
	// -------------------------------------------------------------------------
	Context("JWT/OIDC incoming auth (config-file mode)", func() {
		var fx singleBackendFixture
		var oidcServer *e2e.OIDCMockServer
		var oidcPort int

		BeforeEach(func() {
			fx.setup("vmcp-auth-oidc", "vmcp-auth-oidc-*")

			oidcPort = allocateVMCPPort()
			var err error
			oidcServer, err = e2e.NewOIDCMockServerWithClientOptions(oidcPort, "test-client", "test-secret",
				e2e.WithClientAudience("vmcp-e2e-test"),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(oidcServer.Start()).To(Succeed())
			discoveryURL := fmt.Sprintf("http://localhost:%d/.well-known/openid-configuration", oidcPort)
			Eventually(func() error {
				resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(discoveryURL) //nolint:gosec // URL is test-controlled
				if err != nil {
					return err
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return fmt.Errorf("OIDC discovery returned %d", resp.StatusCode)
				}
				return nil
			}, 10*time.Second, 500*time.Millisecond).Should(Succeed(),
				"mock OIDC server should be reachable before proceeding")
			DeferCleanup(func() {
				if oidcServer != nil {
					_ = oidcServer.Stop()
				}
			})
		})

		AfterEach(func() { fx.teardown() })

		It("rejects unauthenticated clients and accepts clients with a valid JWT", func() {
			issuer := fmt.Sprintf("http://localhost:%d", oidcPort)
			configPath := filepath.Join(fx.tmpDir, "vmcp.yaml")
			initVMCPConfig(fx.cfg, fx.groupName, configPath)

			Expect(modifyVMCPConfig(configPath, func(c *vmcpconfig.Config) {
				c.IncomingAuth = &vmcpconfig.IncomingAuthConfig{
					Type: "oidc",
					OIDC: &vmcpconfig.OIDCConfig{
						Issuer:             issuer,
						ClientID:           "test-client",
						Audience:           "vmcp-e2e-test",
						InsecureAllowHTTP:  true,
						JwksAllowPrivateIP: true,
					},
				}
			})).To(Succeed())

			By("starting vMCP serve with OIDC incoming auth")
			fx.vMCPCmd = e2e.StartLongRunningTHVCommand(fx.cfg,
				"vmcp", "serve",
				"--config", configPath,
				"--port", fmt.Sprintf("%d", fx.vMCPPort),
			)
			healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", fx.vMCPPort)
			Expect(e2e.WaitForVMCPHealthReady(healthURL, 60*time.Second)).To(Succeed())

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			vMCPURL := vmcpEndpointURL(fx.vMCPPort)

			By("verifying unauthenticated request receives 401")
			unauthResp, err := (&http.Client{Timeout: 10 * time.Second}).Post( //nolint:gosec // URL is test-controlled
				vMCPURL, "application/json", strings.NewReader(`{}`))
			Expect(err).ToNot(HaveOccurred(), "POST to MCP endpoint should not fail at transport level")
			_, _ = io.Copy(io.Discard, unauthResp.Body)
			_ = unauthResp.Body.Close()
			Expect(unauthResp.StatusCode).To(Equal(http.StatusUnauthorized),
				"unauthenticated request must return 401")

			By("fetching a valid JWT from the mock OIDC server via client_credentials")
			token := fetchClientCredentialsToken(oidcPort, "test-client", "test-secret", "vmcp-e2e-test")
			Expect(token).ToNot(BeEmpty())

			By("verifying authenticated MCP client can connect and list tools")
			authClient, err := e2e.NewMCPClientForStreamableHTTPWithToken(fx.cfg, vMCPURL, token)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = authClient.Close() }()
			Expect(authClient.Initialize(ctx)).To(Succeed(),
				"Initialize with a valid Bearer token must succeed")

			tools, err := authClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(),
				"authenticated client should see backend tools")
		})
	})

	// -------------------------------------------------------------------------
	// Redis-backed session storage
	// Verifies that vMCP starts and operates correctly when Redis is configured
	// as the session storage backend via vmcpconfig.SessionStorageConfig.
	// The test starts a Redis container as a fixture, wires its address into the
	// vMCP config, and confirms that MCP connectivity and tool listing work.
	// -------------------------------------------------------------------------
	Context("Redis-backed session storage (config-file mode)", func() {
		var fx singleBackendFixture
		var redisName string
		var redisPort int

		BeforeEach(func() {
			fx.setup("vmcp-redis-sessions", "vmcp-redis-*")

			redisPort = allocateVMCPPort()
			redisName = e2e.GenerateUniqueServerName("e2e-redis")
			startRedisContainer(redisName, redisPort)
			DeferCleanup(func() { stopRedisContainer(redisName) })
			waitForRedisReady(redisName, 30*time.Second)
		})

		AfterEach(func() { fx.teardown() })

		It("starts and serves tools correctly when Redis session storage is configured", func() {
			configPath := filepath.Join(fx.tmpDir, "vmcp.yaml")
			initVMCPConfig(fx.cfg, fx.groupName, configPath)

			Expect(modifyVMCPConfig(configPath, func(c *vmcpconfig.Config) {
				c.SessionStorage = &vmcpconfig.SessionStorageConfig{
					Provider:  "redis",
					Address:   fmt.Sprintf("127.0.0.1:%d", redisPort),
					KeyPrefix: "e2e-test:",
				}
			})).To(Succeed())

			By("starting vMCP serve with Redis session storage")
			fx.vMCPCmd = e2e.StartLongRunningTHVCommand(fx.cfg,
				"vmcp", "serve",
				"--config", configPath,
				"--port", fmt.Sprintf("%d", fx.vMCPPort),
			)
			vMCPURL := vmcpEndpointURL(fx.vMCPPort)
			Expect(e2e.WaitForMCPServerReady(fx.cfg, vMCPURL, "streamable-http", 60*time.Second)).To(Succeed())

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			By("connecting an MCP client and listing tools")
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(fx.cfg, vMCPURL)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = mcpClient.Close() }()
			Expect(mcpClient.Initialize(ctx)).To(Succeed())

			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(),
				"backend tools should be visible with Redis session storage")
		})
	})

}) // end Describe("vMCP infra features")
