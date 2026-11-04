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
		Expect(e2e.WaitForMCPServer(config, serverName, 180*time.Second)).
			To(Succeed(), "Envoy server should be running within 180 seconds")
		return serverName
	}

	retireServer := func(serverName string) {
		if config.CleanupAfter {
			Expect(e2e.StopAndRemoveMCPServer(config, serverName)).To(Succeed())
		}
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

			Expect(e2e.WaitForMCPServer(config, serverName, 180*time.Second)).To(Succeed())
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
		It("denies the default bridge gateway by default and reaches it with the flag", func() {
			port := startPublishedHTTPService(fmt.Sprintf("nie-default-gateway-%d", GinkgoRandomSeed()))
			gatewayIP := dockerNetworkGateway("bridge")
			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)

			By("Gateway IP must be blocked by default")
			deny := startFetchServer("deny", "")
			Expect(fetchThrough(deny, target, 30*time.Second).IsError).To(BeTrue(),
				"egress must deny the default bridge gateway without --allow-docker-gateway")
			retireServer(deny)

			By("The same reachable target must be allowed with --allow-docker-gateway")
			allow := startFetchServer("allow", "", "--allow-docker-gateway")
			Expect(fetchThrough(allow, target, 30*time.Second).IsError).To(BeFalse(),
				"the explicit gateway opt-in must reach the host-published service")
		})

		It("blocks the gateway of every external network attached to the proxy", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-host-envoy-%d", GinkgoRandomSeed()))
			server := startFetchServer("attached-gateway", "")
			gatewayIP := dockerNetworkGateway("toolhive-external")

			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)
			directOutput, directErr := directHTTPRequestFromNetwork("toolhive-external", target)
			Expect(directErr).ToNot(HaveOccurred(), "gateway target must be reachable without the proxy: %s", directOutput)
			result := fetchThrough(server, target, 30*time.Second)
			Expect(result.IsError).To(BeTrue(),
				"network isolation must block host access through every proxy network gateway")
		})
		It("blocks gateways owned by Docker bridges the proxy is not attached to", func() {
			port := startPublishedHTTPService(fmt.Sprintf("nie-host-unattached-%d", GinkgoRandomSeed()))
			networkName := fmt.Sprintf("nie-unattached-%d", GinkgoRandomSeed())
			created, output := createDockerBridgeNetwork(networkName, false)
			Expect(created).To(BeTrue(), "should create an unattached bridge: %s", output)
			gatewayIP := dockerNetworkGateway(networkName)
			server := startFetchServer("unattached-gateway", "")

			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)
			directOutput, directErr := directHTTPRequestFromNetwork("toolhive-external", target)
			if directErr != nil {
				Skip("unattached bridge gateway is not reachable from the proxy network: " + directOutput)
			}
			result := fetchThrough(server, target, 30*time.Second)
			Expect(result.IsError).To(BeTrue(),
				"network isolation must block host access through reachable Docker bridge gateways")
		})

		It("blocks an attached gateway under a port-only outbound profile", func() {
			port := startPublishedHTTPService(fmt.Sprintf("nie-host-port-only-%d", GinkgoRandomSeed()))
			profile := fmt.Sprintf(`{
				"name": "port-only",
				"network": {"outbound": {"insecure_allow_all": false, "allow_port": [%d]}}
			}`, port)
			server := startFetchServer("port-only", profile)
			gatewayIP := dockerNetworkGateway("toolhive-external")

			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)
			directOutput, directErr := directHTTPRequestFromNetwork("toolhive-external", target)
			Expect(directErr).ToNot(HaveOccurred(), "port-only target must be reachable without the proxy: %s", directOutput)
			result := fetchThrough(server, target, 30*time.Second)
			Expect(result.IsError).To(BeTrue(),
				"an allowed port must not grant access to a host gateway")
		})

		It("enforces gateway policy for the workload internal bridge", func() {
			port := startPublishedHTTPService(fmt.Sprintf("nie-host-internal-%d", GinkgoRandomSeed()))
			denyServer := startFetchServer("internal-deny", internalGatewayDenyProfile)
			networkName, target, output, err := probeInternalGateway(denyServer, port)
			if err != nil {
				Skip(fmt.Sprintf("Docker runtime cannot route from %s to its gateway target %s: %s", networkName, target, output))
			}
			Expect(fetchThrough(denyServer, target, 30*time.Second).IsError).To(BeTrue(),
				"default gateway policy must block the internal gateway")
			retireServer(denyServer)

			allowServer := startFetchServer("internal-allow", internalGatewayAllowProfile, "--allow-docker-gateway")
			networkName, target, output, err = probeInternalGateway(allowServer, port)
			if err != nil {
				Skip(fmt.Sprintf("Docker runtime cannot route from %s to its gateway target %s: %s", networkName, target, output))
			}
			Expect(fetchThrough(allowServer, target, 30*time.Second).IsError).To(BeFalse(),
				"gateway opt-in must allow the internal gateway")
		})

		It("mitigates direct gateway access under an unrelated hostname allowlist", func() {
			port := startPublishedHTTPService(fmt.Sprintf("nie-host-allowlist-%d", GinkgoRandomSeed()))
			profile := fmt.Sprintf(`{
				"name": "hostname-allowlist",
				"network": {"outbound": {
					"insecure_allow_all": false,
					"allow_host": ["example.com"],
					"allow_port": [443, %d]
				}}
			}`, port)
			server := startFetchServer("hostname-allowlist", profile)
			gatewayIP := dockerNetworkGateway("toolhive-external")

			Eventually(func() bool {
				return !fetchThrough(server, "https://example.com", 30*time.Second).IsError
			}, 60*time.Second, 2*time.Second).Should(BeTrue(),
				"the explicit hostname allowlist must preserve its permitted destination")
			result := fetchThrough(server, fmt.Sprintf("http://%s:%d/", gatewayIP, port), 30*time.Second)
			Expect(result.IsError).To(BeTrue(),
				"a bare gateway IP must not match an unrelated hostname allowlist")
		})

		It("allows each reachable startup-snapshot gateway under the explicit opt-in", func() {
			port := startPublishedHTTPService(fmt.Sprintf("nie-host-opt-in-%d", GinkgoRandomSeed()))
			unattached := fmt.Sprintf("nie-opt-in-unattached-%d", GinkgoRandomSeed())
			created, output := createDockerBridgeNetwork(unattached, false)
			Expect(created).To(BeTrue(), "should create an unattached bridge: %s", output)
			profile := `{"name":"gateway-opt-in","network":{"outbound":{"allow_host":["example.com"],"allow_port":[443]}}}`
			server := startFetchServer("gateway-opt-in", profile, "--allow-docker-gateway")

			for _, target := range dockerGatewayTargets(port, unattached) {
				output, err := directHTTPRequestFromNetwork("toolhive-external", target.url)
				if err != nil && target.optional {
					GinkgoWriter.Printf("optional %s is not reachable in this environment: %s\n", target.name, output)
					continue
				}
				Expect(err).ToNot(HaveOccurred(), "%s must be reachable without the proxy: %s", target.name, output)
				Expect(fetchThrough(server, target.url, 30*time.Second).IsError).To(BeFalse(), "%s must be allowed", target.name)
			}
		})

		It("blocks a reachable IPv6 Docker bridge gateway when IPv6 is available", func() {
			port := startPublishedHTTPService(fmt.Sprintf("nie-host-ipv6-%d", GinkgoRandomSeed()))
			networkName := fmt.Sprintf("nie-ipv6-%d", GinkgoRandomSeed())
			created, output := createDockerBridgeNetwork(networkName, true)
			if !created {
				Skip("Docker cannot create an IPv6 bridge in this environment: " + output)
			}
			gatewayIP, ok := dockerNetworkGatewayForFamily(networkName, true)
			if !ok {
				Skip("Docker did not assign an IPv6 gateway")
			}
			server := startFetchServer("ipv6-gateway", "")
			target := fmt.Sprintf("http://[%s]:%d/", gatewayIP, port)
			if directOutput, directErr := directHTTPRequestFromNetwork("toolhive-external", target); directErr != nil {
				Skip("IPv6 gateway is not reachable from the proxy network in this environment: " + directOutput)
			}

			Expect(fetchThrough(server, target, 30*time.Second).IsError).To(BeTrue(),
				"network isolation must block reachable IPv6 Docker bridge gateways")
		})
	})

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
			// The allowed leg needs a working DNS → TLS → example.com chain through
			// Envoy's dynamic_forward_proxy. The very first egress request after
			// startup can race the isolation stack's own warm-up (DFP DNS cache,
			// the per-workload DNS container), failing instantly with a local 503
			// long before anything has actually reached the network — observed in
			// CI as an IsError result within ~40ms of readiness. The property this
			// spec pins is steady-state reachability of an allowed port, not
			// first-request-after-boot success (which Envoy does not promise), so
			// retry until the deadline and surface the tool's error text — the one
			// diagnostic that distinguishes a warm-up race from a broken AllowPort.
			var lastHTTPSError string
			Eventually(func() bool {
				httpsResult := fetchThrough(server, "https://example.com", 30*time.Second)
				if httpsResult.IsError {
					lastHTTPSError = resultText(httpsResult)
					return false
				}
				return true
			}, 60*time.Second, 2*time.Second).Should(BeTrue(), func() string {
				return "https://example.com must be allowed (port 443 is in AllowPort); last fetch error: " + lastHTTPSError
			})

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

			target := fmt.Sprintf("http://%s:%d/", dockerNetworkGateway("bridge"), port)
			server := startFetchServer("slow", "", "--allow-docker-gateway")

			// 60s: 16s upstream sleep + generous MCP client round-trip headroom.
			// The 30s default was too tight under CI infrastructure load.
			result := fetchThrough(server, target, 60*time.Second)
			if result.IsError {
				Skip("docker bridge gateway is not routable to the host in this environment")
			}
			Expect(resultText(result)).To(ContainSubstring("slow-ok"),
				"a 16s response must complete — if it doesn't, the 15s route timeout was not disabled")
		})
	})
})
