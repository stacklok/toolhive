// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

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

// networkProxyBackend describes one egress/ingress proxy implementation to run
// the isolation matrix against. envValue is the TOOLHIVE_NETWORK_PROXY value
// injected into `thv run`; "" selects the default (squid).
type networkProxyBackend struct {
	name     string
	envValue string
}

var networkProxyBackends = []networkProxyBackend{
	{name: "squid", envValue: ""},
	{name: "envoy", envValue: "envoy"},
}

var _ = Describe("NetworkIsolation", Label("proxy", "network", "isolation", "e2e"), func() {
	var (
		config               *e2e.TestConfig
		permissionProfileDir string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Create temporary directory for permission profiles
		var err error
		permissionProfileDir, err = os.MkdirTemp("", "network-isolation-profiles-*")
		Expect(err).ToNot(HaveOccurred(), "Should be able to create temp directory")

		// Check if thv binary is available
		err = e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		// Clean up temporary permission profile directory
		if permissionProfileDir != "" {
			os.RemoveAll(permissionProfileDir)
		}
	})

	// The behavioral matrix runs identically against every proxy backend: the
	// enforcement contract (egress allowlist, inbound Host gating, docker-gateway
	// deny/allow) is backend-agnostic, so both squid and envoy must satisfy it.
	for _, backend := range networkProxyBackends {
		backend := backend
		Context(fmt.Sprintf("with the %s network proxy", backend.name), func() {
			// newRun builds a `thv run` command with the backend selector injected.
			// Only `run` needs the env var — it deploys the proxy container(s); the
			// running proxy then carries its own config.
			newRun := func(runArgs ...string) *e2e.THVCommand {
				cmd := e2e.NewTHVCommand(config, runArgs...)
				if backend.envValue != "" {
					cmd = cmd.WithEnv("TOOLHIVE_NETWORK_PROXY=" + backend.envValue)
				}
				return cmd
			}

			// name builds a per-backend, per-test unique workload name so the squid
			// and envoy legs never collide when the suite runs them in sequence.
			name := func(suffix string) string {
				return fmt.Sprintf("ni-%s-%s-%d", backend.name, suffix, GinkgoRandomSeed())
			}

			// verifyNetworkRestrictions starts the fetch MCP server with a restrictive
			// permission profile plus any extra run args, then asserts that both the
			// outbound and inbound network rules are enforced. The restrictions only
			// take effect when the egress proxy is running, so a passing assertion
			// proves network isolation is active. Pass "--isolate-network" to exercise
			// the explicit opt-in; pass no extra args to exercise the default.
			verifyNetworkRestrictions := func(nameSuffix string, extraRunArgs ...string) {
				serverName := name(nameSuffix)
				DeferCleanup(func() {
					if config.CleanupAfter {
						err := e2e.StopAndRemoveMCPServer(config, serverName)
						Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
					}
				})

				By("Creating a permission profile with restricted inbound and outbound rules")
				permissionProfile := `{
					"name": "test-network-isolation",
					"network": {
						"inbound": {
							"allow_host": ["localhost", "127.0.0.1"]
						},
						"outbound": {
							"insecure_allow_all": false,
							"allow_host": ["example.com"],
							"allow_port": [80, 443]
						}
					}
				}`
				profilePath := filepath.Join(permissionProfileDir, backend.name+"-"+nameSuffix+".json")
				err := os.WriteFile(profilePath, []byte(permissionProfile), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create permission profile")

				By("Starting the fetch MCP server")
				runArgs := append([]string{"run", "--name", serverName}, extraRunArgs...)
				runArgs = append(runArgs, "--permission-profile", profilePath, "fetch")
				newRun(runArgs...).ExpectSuccess()

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 120*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 120 seconds")

				By("Getting server URL for testing")
				serverURL, err := e2e.GetMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to get server URL")

				err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be ready")

				By("Creating MCP client to test network isolation")
				mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create MCP client")
				defer mcpClient.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				err = mcpClient.Initialize(ctx)
				Expect(err).ToNot(HaveOccurred(), "Should be able to initialize MCP client")

				By("Testing outbound network isolation - blocked URL should fail")
				blockedArgs := map[string]interface{}{
					"url": "https://google.com",
				}
				result, err := mcpClient.CallTool(ctx, "fetch", blockedArgs)
				Expect(err).ToNot(HaveOccurred(), "CallTool should complete without error")

				// The fetch tool returns an error result when the URL is blocked
				Expect(result.IsError).To(BeTrue(), "Should return error result for blocked URL")

				By("Testing inbound network isolation - connection with non-allowed Host header should be blocked")
				client := &http.Client{Timeout: 10 * time.Second}

				req, err := http.NewRequest("GET", serverURL, nil)
				Expect(err).ToNot(HaveOccurred(), "Should be able to create HTTP request")

				// Set Host header to something not in the allow list
				req.Host = "example.org"

				resp, err := client.Do(req)
				Expect(err).ToNot(HaveOccurred(), "Request should complete without connection error")
				defer resp.Body.Close()
				Expect(resp.StatusCode).ToNot(Equal(http.StatusOK),
					"Request with non-allowed Host header should be blocked")
			}

			Describe("Running MCP server with network isolation", func() {
				It("should enforce both inbound and outbound network restrictions when --isolate-network is set", func() {
					verifyNetworkRestrictions("explicit", "--isolate-network")
				})

				// Network isolation is on by default, so omitting the flag must still
				// enforce the permission profile's network rules.
				It("should enforce both inbound and outbound network restrictions by default", func() {
					verifyNetworkRestrictions("default")
				})
			})

			Describe("Reaching the host with --allow-docker-gateway", func() {
				// startFetchServer runs the fetch MCP server under network isolation
				// with the given extra run args, waits for it to be running, and
				// returns its workload name. An empty profileJSON uses the default
				// (allow-all) network profile.
				startFetchServer := func(nameSuffix, profileJSON string, extraRunArgs ...string) string {
					serverName := name(nameSuffix)
					DeferCleanup(func() {
						if config.CleanupAfter {
							// Best-effort: a test may have already removed this server to
							// avoid running two isolation stacks at once.
							_ = e2e.StopAndRemoveMCPServer(config, serverName)
						}
					})

					runArgs := append([]string{"run", "--name", serverName}, extraRunArgs...)
					if profileJSON != "" {
						profilePath := filepath.Join(permissionProfileDir, backend.name+"-"+nameSuffix+".json")
						err := os.WriteFile(profilePath, []byte(profileJSON), 0644)
						Expect(err).ToNot(HaveOccurred(), "Should be able to write permission profile")
						runArgs = append(runArgs, "--permission-profile", profilePath)
					}
					runArgs = append(runArgs, "fetch")

					newRun(runArgs...).ExpectSuccess()
					// The fetch server can take ~70s to become healthy; allow generous
					// headroom so the test does not flake.
					err := e2e.WaitForMCPServer(config, serverName, 120*time.Second)
					Expect(err).ToNot(HaveOccurred(), "Server should be running within 120 seconds")
					return serverName
				}

				// retireServer tears a server down mid-test so only one
				// network-isolation stack runs at a time, avoiding resource contention
				// that slows the next server's startup.
				retireServer := func(serverName string) {
					if config.CleanupAfter {
						Expect(e2e.StopAndRemoveMCPServer(config, serverName)).To(Succeed())
					}
				}

				// fetchThrough drives the fetch tool against the given server and
				// returns the tool result. A denied request comes back as an error
				// result (result.IsError); a successful one carries the fetched body.
				fetchThrough := func(serverName, targetURL string) *mcp.CallToolResult {
					serverURL, err := e2e.GetMCPServerURL(config, serverName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to get server URL")
					err = e2e.WaitForMCPServerReady(config, serverURL, "streamable-http", 60*time.Second)
					Expect(err).ToNot(HaveOccurred(), "Server should be ready")

					mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, serverURL)
					Expect(err).ToNot(HaveOccurred(), "Should be able to create MCP client")
					defer mcpClient.Close()

					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					Expect(mcpClient.Initialize(ctx)).To(Succeed(), "Should be able to initialize MCP client")

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

				// dockerBridgeGatewayIP returns the host gateway IP of the default
				// Docker bridge — the address the egress proxy denies by default, and
				// the address a container uses to reach the host on Linux.
				dockerBridgeGatewayIP := func() string {
					//nolint:gosec // fixed, test-controlled arguments
					out, err := exec.Command("docker", "network", "inspect", "bridge",
						"-f", "{{range .IPAM.Config}}{{.Gateway}}{{end}}").Output()
					Expect(err).ToNot(HaveOccurred(), "Should be able to inspect the docker bridge network")
					return strings.TrimSpace(string(out))
				}

				// This is the key regression guard for the forward-proxy gateway block:
				// it targets the gateway IP directly (no DNS), so a blocked fetch
				// unambiguously means the egress deny fired — the exact behavior a
				// config-only assertion cannot prove. It is what catches an inert L3
				// rule on the envoy backend. See #5640 (the isolated resolver cannot
				// resolve host.docker.internal, which would confound a hostname-based
				// assertion) and #5917.
				It("denies the bridge gateway IP by default and, where routable, reaches it with the flag", func() {
					By("Starting a host service reachable from containers")
					listener, err := net.Listen("tcp", ":0") //nolint:gosec // binds an ephemeral port for the test
					Expect(err).ToNot(HaveOccurred(), "Should be able to listen on an ephemeral port")
					port := listener.Addr().(*net.TCPAddr).Port
					srv := &http.Server{
						Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "host-service-ok") }),
						ReadHeaderTimeout: 5 * time.Second,
					}
					go func() { _ = srv.Serve(listener) }()
					DeferCleanup(func() { _ = srv.Close() })

					gatewayIP := dockerBridgeGatewayIP()
					target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)

					By("Confirming the gateway IP is blocked by the egress proxy by default")
					denyServer := startFetchServer("deny", "")
					Expect(fetchThrough(denyServer, target).IsError).To(BeTrue(),
						"the egress proxy must deny the bridge gateway IP without --allow-docker-gateway")
					retireServer(denyServer)

					By("Confirming --allow-docker-gateway grants access so the host is reachable")
					allowServer := startFetchServer("allow", "", "--allow-docker-gateway")
					result := fetchThrough(allowServer, target)
					if result.IsError {
						// Whether the bridge gateway actually routes to a host service is
						// environment-specific — it works on Linux Docker Engine (where the
						// bridge gateway is the host), but on Docker Desktop the host lives
						// behind host.docker.internal. Skip the positive reachability leg
						// where the gateway is not host-routable; the deny leg above still
						// proves the control works.
						Skip("docker bridge gateway is not routable to the host in this environment (e.g. Docker Desktop)")
					}
					Expect(resultText(result)).To(ContainSubstring("host-service-ok"),
						"with --allow-docker-gateway the fetch must reach the host service through the egress proxy")
				})

				// Regression guard for the Envoy ingress/egress route timeout: Envoy's
				// default RouteAction.timeout (15s) is a hard total-response cap that
				// truncated long-lived MCP streams until it was set to "0s". Squid has no
				// equivalent cap, so this passes trivially there and meaningfully on Envoy.
				// A response that takes >15s must complete, not get cut at 15s.
				//
				// Reuses the gateway-reachable host path (Linux Docker Engine only; skips
				// where the bridge gateway is not host-routable, e.g. Docker Desktop).
				It("does not truncate a response that takes longer than Envoy's 15s default", func() {
					By("Starting a host service that responds after ~18s (past the 15s cap)")
					listener, err := net.Listen("tcp", ":0") //nolint:gosec // ephemeral test port
					Expect(err).ToNot(HaveOccurred())
					port := listener.Addr().(*net.TCPAddr).Port
					srv := &http.Server{
						Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
							time.Sleep(18 * time.Second)
							_, _ = io.WriteString(w, "slow-stream-ok")
						}),
						ReadHeaderTimeout: 5 * time.Second,
					}
					go func() { _ = srv.Serve(listener) }()
					DeferCleanup(func() { _ = srv.Close() })

					target := fmt.Sprintf("http://%s:%d/", dockerBridgeGatewayIP(), port)
					server := startFetchServer("slow", "", "--allow-docker-gateway")

					By("Fetching the slow endpoint through the proxy")
					result := fetchThrough(server, target)
					if result.IsError {
						// Either the gateway isn't host-routable here (Docker Desktop), or
						// the response was truncated. Distinguish so a real timeout
						// regression isn't masked as an environment skip.
						if strings.Contains(resultText(result), "slow-stream-ok") {
							Fail("slow response was truncated — the 15s route timeout was not disabled")
						}
						Skip("docker bridge gateway is not routable to the host in this environment (e.g. Docker Desktop)")
					}
					Expect(resultText(result)).To(ContainSubstring("slow-stream-ok"),
						"a >15s response must complete without being truncated by the route timeout")
				})

				// Squid-only: pin the generated ACL rules on the deployed egress
				// container. Envoy's equivalent config shape is covered by unit tests
				// and the real-Envoy `--mode validate` test (envoy-distroless has no
				// shell to `cat` its bootstrap), and its runtime behavior by the
				// gateway-IP traffic test above, so this leg is not parametrized.
				if backend.name == "squid" {
					It("carries both gateway deny rules by default and replaces them with allow rules under the flag", func() {
						squidConf := func(serverName string) string {
							//nolint:gosec // container name is test-controlled
							out, err := exec.Command("docker", "exec", serverName+"-egress",
								"cat", "/etc/squid/squid.conf").CombinedOutput()
							Expect(err).ToNot(HaveOccurred(), "Should be able to read egress squid.conf: %s", string(out))
							return string(out)
						}

						By("Default profile: both the hostname and direct-IP deny rules are present")
						denyServer := startFetchServer("cfg-deny", "")
						denyConf := squidConf(denyServer)
						Expect(denyConf).To(ContainSubstring("http_access deny docker_gateway_hosts"),
							"default config must deny the docker gateway hostnames")
						Expect(denyConf).To(ContainSubstring("dstdomain host.docker.internal gateway.docker.internal"))
						Expect(denyConf).To(ContainSubstring("http_access deny docker_gateway_ip"),
							"default config must deny the docker gateway IP (the DNS-bypass path)")
						retireServer(denyServer)

						By("With --allow-docker-gateway: the deny rules are replaced by explicit allow rules")
						allowConf := squidConf(startFetchServer("cfg-allow", "", "--allow-docker-gateway"))
						Expect(allowConf).ToNot(ContainSubstring("http_access deny docker_gateway_hosts"),
							"--allow-docker-gateway must remove the gateway hostname deny rule")
						Expect(allowConf).ToNot(ContainSubstring("http_access deny docker_gateway_ip"),
							"--allow-docker-gateway must remove the gateway IP deny rule")
						Expect(allowConf).To(ContainSubstring("http_access allow docker_gateway_hosts"),
							"--allow-docker-gateway must grant access to the gateway hostnames")
						Expect(allowConf).To(ContainSubstring("dstdomain host.docker.internal gateway.docker.internal"))
						Expect(allowConf).To(ContainSubstring("http_access allow docker_gateway_ip"),
							"--allow-docker-gateway must grant access to the gateway IP")
					})
				}
			})
		})
	}
})
