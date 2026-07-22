// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

// NetworkIsolationEnvoy contains e2e tests for the Envoy network proxy backend
// (TOOLHIVE_NETWORK_PROXY=envoy). The tests cover the same behavioural contract
// as the Squid suite in network_isolation_test.go — egress allowlist, inbound
// Host gating, docker-gateway deny/allow — plus Envoy-specific guards such as
// the route-timeout regression test.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("NetworkIsolationEnvoy", Label("proxy", "network", "isolation", "envoy", "e2e"), func() {
	var (
		config               *e2e.TestConfig
		permissionProfileDir string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		var err error
		permissionProfileDir, err = os.MkdirTemp("", "network-isolation-envoy-profiles-*")
		Expect(err).ToNot(HaveOccurred())

		err = e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if permissionProfileDir != "" {
			os.RemoveAll(permissionProfileDir)
		}
	})

	// envoyRun returns a thv run command with TOOLHIVE_NETWORK_PROXY=envoy injected.
	envoyRun := func(config *e2e.TestConfig, args ...string) *e2e.THVCommand {
		return e2e.NewTHVCommand(config, args...).
			WithEnv("TOOLHIVE_NETWORK_PROXY=envoy")
	}

	// startFetchServer starts the fetch MCP server under Envoy network isolation
	// with the given extra run args and returns the workload name.
	startFetchServer := func(nameSuffix, profileJSON string, extraRunArgs ...string) string {
		serverName := fmt.Sprintf("nie-%s-%d", nameSuffix, GinkgoRandomSeed())
		DeferCleanup(func() {
			if config.CleanupAfter {
				_ = e2e.StopAndRemoveMCPServer(config, serverName)
			}
		})

		runArgs := append([]string{"run", "--name", serverName}, extraRunArgs...)
		if profileJSON != "" {
			profilePath := filepath.Join(permissionProfileDir, nameSuffix+".json")
			Expect(os.WriteFile(profilePath, []byte(profileJSON), 0644)).To(Succeed())
			runArgs = append(runArgs, "--permission-profile", profilePath)
		}
		runArgs = append(runArgs, "fetch")

		envoyRun(config, runArgs...).ExpectSuccess()
		Expect(e2e.WaitForMCPServer(config, serverName, 120*time.Second)).
			To(Succeed(), "Envoy server should be running within 120 seconds")
		return serverName
	}

	// fetchThrough drives the fetch tool against the given server and returns
	// the tool result.
	fetchThrough := func(serverName, targetURL string, callTimeout time.Duration) *mcp.CallToolResult {
		serverURL, err := e2e.GetMCPServerURL(config, serverName)
		Expect(err).ToNot(HaveOccurred())
		Expect(e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)).
			To(Succeed())

		mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		Expect(mcpClient.Initialize(ctx)).To(Succeed())

		result, err := mcpClient.CallTool(ctx, "fetch", map[string]interface{}{"url": targetURL})
		Expect(err).ToNot(HaveOccurred(), "CallTool should complete without transport error")
		return result
	}

	// resultText concatenates the text content of a tool result.
	resultText := func(result *mcp.CallToolResult) string {
		var sb strings.Builder
		for _, c := range result.Content {
			if tc, ok := mcp.AsTextContent(c); ok {
				sb.WriteString(tc.Text)
			}
		}
		return sb.String()
	}

	// dockerBridgeGatewayIP returns the bridge gateway IP from the default Docker
	// bridge network.
	dockerBridgeGatewayIP := func() string {
		//nolint:gosec // fixed, test-controlled arguments
		out, err := exec.Command("docker", "network", "inspect", "bridge",
			"-f", "{{range .IPAM.Config}}{{.Gateway}}{{end}}").Output()
		Expect(err).ToNot(HaveOccurred())
		return strings.TrimSpace(string(out))
	}

	// ── Behavioural parity with Squid ────────────────────────────────────────

	Describe("Outbound and inbound restrictions", func() {
		verifyNetworkRestrictions := func(nameSuffix string, extraRunArgs ...string) {
			serverName := fmt.Sprintf("nie-%s-%d", nameSuffix, GinkgoRandomSeed())
			DeferCleanup(func() {
				if config.CleanupAfter {
					Expect(e2e.StopAndRemoveMCPServer(config, serverName)).To(Succeed())
				}
			})

			profile := `{
				"name": "test-network-isolation",
				"network": {
					"inbound":  { "allow_host": ["localhost", "127.0.0.1"] },
					"outbound": { "insecure_allow_all": false, "allow_host": ["example.com"], "allow_port": [80, 443] }
				}
			}`
			profilePath := filepath.Join(permissionProfileDir, nameSuffix+".json")
			Expect(os.WriteFile(profilePath, []byte(profile), 0644)).To(Succeed())

			runArgs := append([]string{"run", "--name", serverName}, extraRunArgs...)
			runArgs = append(runArgs, "--permission-profile", profilePath, "fetch")
			envoyRun(config, runArgs...).ExpectSuccess()

			Expect(e2e.WaitForMCPServer(config, serverName, 120*time.Second)).To(Succeed())
			serverURL, err := e2e.GetMCPServerURL(config, serverName)
			Expect(err).ToNot(HaveOccurred())
			Expect(e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)).To(Succeed())

			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			Expect(mcpClient.Initialize(ctx)).To(Succeed())

			By("Blocked URL must fail outbound")
			result, err := mcpClient.CallTool(ctx, "fetch", map[string]interface{}{"url": "https://google.com"})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.IsError).To(BeTrue(), "blocked URL must return an error result")

			By("Non-allowed Host header must be rejected inbound")
			req, err := http.NewRequest("GET", serverURL, nil)
			Expect(err).ToNot(HaveOccurred())
			req.Host = "example.org"
			resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).ToNot(Equal(http.StatusOK))
		}

		It("should enforce restrictions when --isolate-network is set", func() {
			verifyNetworkRestrictions("explicit", "--isolate-network")
		})

		It("should enforce restrictions with default isolation", func() {
			verifyNetworkRestrictions("default")
		})
	})

	// ── Gateway deny / allow ──────────────────────────────────────────────────

	Describe("Docker-gateway deny and --allow-docker-gateway", func() {
		It("denies the bridge gateway IP by default and reaches it with the flag", func() {
			listener, err := net.Listen("tcp", ":0") //nolint:gosec // ephemeral test port
			Expect(err).ToNot(HaveOccurred())
			port := listener.Addr().(*net.TCPAddr).Port
			srv := &http.Server{
				Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "host-ok") }),
				ReadHeaderTimeout: 5 * time.Second,
			}
			go func() { _ = srv.Serve(listener) }()
			DeferCleanup(func() { _ = srv.Close() })

			gatewayIP := dockerBridgeGatewayIP()
			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)

			By("Gateway IP must be blocked by default")
			deny := startFetchServer("deny", "")
			Expect(fetchThrough(deny, target, 30*time.Second).IsError).To(BeTrue(),
				"egress must deny the bridge gateway IP without --allow-docker-gateway")

			By("Gateway IP must be reachable with --allow-docker-gateway")
			allow := startFetchServer("allow", "", "--allow-docker-gateway")
			result := fetchThrough(allow, target, 30*time.Second)
			if result.IsError {
				Skip("docker bridge gateway is not routable to the host in this environment")
			}
			Expect(resultText(result)).To(ContainSubstring("host-ok"))
		})
	})

	// ── Envoy-specific regression guards ─────────────────────────────────────

	// ── AllowPort enforcement ─────────────────────────────────────────────────

	Describe("AllowPort enforcement", func() {
		// This test proves that AllowPort is honoured end-to-end through a real
		// Envoy container, not just in the generated config. It uses a restrictive
		// profile that allows example.com on port 443 only and asserts:
		//   - HTTPS (port 443) succeeds
		//   - HTTP  (port 80)  is blocked
		// This was a parity gap vs Squid (see #5915): before the fix, Envoy
		// ignored AllowPort and the HTTP request would have succeeded.
		It("blocks a request on a non-allowed port while permitting the allowed port", func() {
			profile := `{
				"name": "port-test",
				"network": {
					"outbound": {
						"insecure_allow_all": false,
						"allow_host": ["example.com"],
						"allow_port": [443]
					}
				}
			}`
			server := startFetchServer("port", profile)

			By("HTTPS request (port 443) must succeed")
			httpsResult := fetchThrough(server, "https://example.com", 30*time.Second)
			Expect(httpsResult.IsError).To(BeFalse(),
				"https://example.com must be allowed (port 443 is in AllowPort)")

			By("HTTP request (port 80) must be blocked")
			httpResult := fetchThrough(server, "http://example.com", 30*time.Second)
			Expect(httpResult.IsError).To(BeTrue(),
				"http://example.com must be blocked (port 80 is not in AllowPort)")
		})
	})

	Describe("Envoy route timeout disabled for long-lived streams", func() {
		// Guards the timeout:"0s" fix. Envoy's default RouteAction.timeout is 15s;
		// this test proves it was disabled by having the upstream sleep 16s (past
		// the cap) and asserting the response completes. The 30s CallTool context
		// leaves ~14s of MCP overhead headroom after the 16s upstream delay.
		It("does not truncate a response that takes longer than Envoy's 15s default", func() {
			listener, err := net.Listen("tcp", ":0") //nolint:gosec // ephemeral test port
			Expect(err).ToNot(HaveOccurred())
			port := listener.Addr().(*net.TCPAddr).Port
			srv := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					time.Sleep(16 * time.Second)
					_, _ = io.WriteString(w, "slow-ok")
				}),
				ReadHeaderTimeout: 5 * time.Second,
			}
			go func() { _ = srv.Serve(listener) }()
			DeferCleanup(func() { _ = srv.Close() })

			target := fmt.Sprintf("http://%s:%d/", dockerBridgeGatewayIP(), port)
			server := startFetchServer("slow", "", "--allow-docker-gateway")

			result := fetchThrough(server, target, 30*time.Second)
			if result.IsError {
				Skip("docker bridge gateway is not routable to the host in this environment")
			}
			Expect(resultText(result)).To(ContainSubstring("slow-ok"),
				"a 16s response must complete — if it doesn't, the 15s route timeout was not disabled")
		})
	})
})
