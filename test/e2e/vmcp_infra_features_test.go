// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package e2e_test contains infrastructure-heavy vMCP CLI e2e tests that require
// external services (OIDC server, Redis) as test fixtures.
// These complement the basic feature tests in vmcp_cli_features_test.go.
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/redisconfig"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/testkit/redistls"
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

// startTLSRedisContainer starts a Redis container that only accepts TLS
// connections (the plaintext port is disabled) and requires password, using
// the server certificate in certs. certs' directory is mounted read-only.
func startTLSRedisContainer(containerName string, hostPort int, certs *redistls.Certificates, password string) {
	certDir := filepath.Dir(certs.CACertFile)
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"--name", containerName,
		"-p", fmt.Sprintf("127.0.0.1:%d:6379", hostPort),
		"-v", certDir+":/certs:ro",
		images.RedisImage,
		"redis-server",
		"--port", "0",
		"--tls-port", "6379",
		"--tls-cert-file", "/certs/"+filepath.Base(certs.ServerCertFile),
		"--tls-key-file", "/certs/"+filepath.Base(certs.ServerKeyFile),
		"--tls-ca-cert-file", "/certs/"+filepath.Base(certs.CACertFile),
		"--tls-auth-clients", "no",
		"--requirepass", password,
	).CombinedOutput()
	Expect(err).ToNot(HaveOccurred(), "should start TLS Redis container: %s", out)
}

// redisCLI runs redis-cli inside the container with the given arguments
// (connection flags such as TLS options, followed by the command).
func redisCLI(containerName string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"exec", containerName, "redis-cli"}, args...)
	return exec.Command("docker", cmdArgs...).CombinedOutput()
}

// waitForRedisReady polls the Redis container until it responds to PING.
// connArgs are extra redis-cli connection arguments (e.g. TLS flags).
func waitForRedisReady(containerName string, timeout time.Duration, connArgs ...string) {
	GinkgoWriter.Printf("waiting for Redis container %q to respond to PING\n", containerName)
	Eventually(func() error {
		out, err := redisCLI(containerName, append(connArgs, "ping")...)
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

			var err error
			oidcServer, err = e2e.NewOIDCMockServer(0, "test-client", "test-secret",
				e2e.WithClientAudience("vmcp-e2e-test"),
			)
			Expect(err).ToNot(HaveOccurred())
			oidcPort = oidcServer.Port()
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

	// -------------------------------------------------------------------------
	// TLS-only Redis session storage
	// Verifies that sessionStorage.tls makes vMCP talk TLS to a Redis that has
	// no plaintext listener and requires a password, so the password and the
	// session records only ever cross the network encrypted.
	// -------------------------------------------------------------------------
	Context("TLS-only Redis session storage (config-file mode)", func() {
		const redisPassword = "e2e-redis-tls-secret"
		var fx singleBackendFixture
		var redisName string
		var redisPort int
		var certs *redistls.Certificates
		var redisCLITLSArgs []string

		BeforeEach(func() {
			fx.setup("vmcp-redis-tls", "vmcp-redis-tls-*")

			certDir := filepath.Join(fx.tmpDir, "redis-tls")
			Expect(os.Mkdir(certDir, 0o750)).To(Succeed())
			var err error
			certs, err = redistls.NewCertificates(certDir)
			Expect(err).ToNot(HaveOccurred())
			// The redis user inside the container must be able to read the
			// bind-mounted key; the directory is private to this test run.
			Expect(os.Chmod(certs.ServerKeyFile, 0o644)).To(Succeed())
			Expect(os.Chmod(certDir, 0o755)).To(Succeed())
			redisCLITLSArgs = []string{
				"--tls", "--cacert", "/certs/" + filepath.Base(certs.CACertFile),
				"-a", redisPassword, "--no-auth-warning",
			}

			redisPort = allocateVMCPPort()
			redisName = e2e.GenerateUniqueServerName("e2e-redis-tls")
			startTLSRedisContainer(redisName, redisPort, certs, redisPassword)
			DeferCleanup(func() { stopRedisContainer(redisName) })
			waitForRedisReady(redisName, 30*time.Second, redisCLITLSArgs...)
		})

		AfterEach(func() { fx.teardown() })

		It("stores sessions over verified TLS with password authentication", func() {
			configPath := filepath.Join(fx.tmpDir, "vmcp.yaml")
			initVMCPConfig(fx.cfg, fx.groupName, configPath)

			Expect(modifyVMCPConfig(configPath, func(c *vmcpconfig.Config) {
				c.SessionStorage = &vmcpconfig.SessionStorageConfig{
					Provider:  "redis",
					Address:   fmt.Sprintf("127.0.0.1:%d", redisPort),
					KeyPrefix: "e2e-tls:",
					TLS:       &redisconfig.TLSConfig{CACertFile: certs.CACertFile},
				}
			})).To(Succeed())

			By("starting vMCP serve with TLS Redis session storage and a password")
			// Pass the password only to this child process: os.Setenv would leak
			// it into concurrently running specs.
			fx.vMCPCmd = exec.Command(fx.cfg.THVBinary,
				"vmcp", "serve",
				"--config", configPath,
				"--port", fmt.Sprintf("%d", fx.vMCPPort),
			)
			fx.vMCPCmd.Env = append(os.Environ(), vmcpconfig.RedisPasswordEnvVar+"="+redisPassword)
			fx.vMCPCmd.Stdout = GinkgoWriter
			fx.vMCPCmd.Stderr = GinkgoWriter
			Expect(fx.vMCPCmd.Start()).To(Succeed())

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
				"backend tools should be visible with TLS Redis session storage")

			By("opening a Legacy (session-bearing) MCP session")
			// Modern clients are sessionless and store nothing; a Legacy
			// initialize is what persists session data to Redis.
			rawClient, err := e2e.NewRawMCPClient(30 * time.Second)
			Expect(err).ToNot(HaveOccurred())
			initResp, err := rawClient.Send(ctx, vMCPURL, e2e.NewLegacyInitializeRequest("redis-tls-e2e", "1.0"))
			Expect(err).ToNot(HaveOccurred())
			Expect(initResp.StatusCode).To(Equal(http.StatusOK), "initialize body: %s", initResp.Body)
			sessionID := initResp.Headers.Get(e2e.HeaderMCPSessionID)
			Expect(sessionID).ToNot(BeEmpty(), "Legacy initialize should assign a session id")

			By("verifying the session was written to the TLS-only Redis")
			Eventually(func() (string, error) {
				out, err := redisCLI(redisName, append(redisCLITLSArgs, "exists", "e2e-tls:"+sessionID)...)
				return strings.TrimSpace(string(out)), err
			}, 10*time.Second, time.Second).Should(Equal("1"),
				"vMCP should have stored the session in Redis over TLS")
		})
	})

}) // end Describe("vMCP infra features")
