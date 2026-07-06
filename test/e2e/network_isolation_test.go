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

	// verifyNetworkRestrictions starts the fetch MCP server with a restrictive
	// permission profile plus any extra run args, then asserts that both the
	// outbound and inbound network rules are enforced. The restrictions only take
	// effect when the egress proxy is running, so a passing assertion proves
	// network isolation is active. Pass "--isolate-network" to exercise the
	// explicit opt-in; pass no extra args to exercise the default (network
	// isolation is on by default).
	verifyNetworkRestrictions := func(nameSuffix string, extraRunArgs ...string) {
		serverName := fmt.Sprintf("network-isolation-%s-%d", nameSuffix, GinkgoRandomSeed())
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
		profilePath := filepath.Join(permissionProfileDir, nameSuffix+".json")
		err := os.WriteFile(profilePath, []byte(permissionProfile), 0644)
		Expect(err).ToNot(HaveOccurred(), "Should be able to create permission profile")

		By("Starting the fetch MCP server")
		runArgs := append([]string{"run", "--name", serverName}, extraRunArgs...)
		runArgs = append(runArgs, "--permission-profile", profilePath, "fetch")
		e2e.NewTHVCommand(config, runArgs...).ExpectSuccess()

		By("Waiting for the server to be running")
		err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
		Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

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
		// startFetchServer runs the fetch MCP server under network isolation with
		// the given extra run args (e.g. --allow-docker-gateway), waits for it to
		// be running, and returns its workload name. An empty profileJSON uses the
		// default (allow-all) network profile.
		startFetchServer := func(nameSuffix, profileJSON string, extraRunArgs ...string) string {
			serverName := fmt.Sprintf("ni-gw-%s-%d", nameSuffix, GinkgoRandomSeed())
			DeferCleanup(func() {
				if config.CleanupAfter {
					// Best-effort: a test may have already removed this server to
					// avoid running two isolation stacks at once.
					_ = e2e.StopAndRemoveMCPServer(config, serverName)
				}
			})

			runArgs := append([]string{"run", "--name", serverName}, extraRunArgs...)
			if profileJSON != "" {
				profilePath := filepath.Join(permissionProfileDir, nameSuffix+".json")
				err := os.WriteFile(profilePath, []byte(profileJSON), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should be able to write permission profile")
				runArgs = append(runArgs, "--permission-profile", profilePath)
			}
			runArgs = append(runArgs, "fetch")

			e2e.NewTHVCommand(config, runArgs...).ExpectSuccess()
			// The fetch server can take ~70s to become healthy; allow generous
			// headroom so the test does not flake.
			err := e2e.WaitForMCPServer(config, serverName, 120*time.Second)
			Expect(err).ToNot(HaveOccurred(), "Server should be running within 120 seconds")
			return serverName
		}

		// retireServer tears a server down mid-test so only one network-isolation
		// stack (MCP + dns + egress + ingress containers) runs at a time, avoiding
		// resource contention that slows the next server's startup.
		retireServer := func(serverName string) {
			if config.CleanupAfter {
				Expect(e2e.StopAndRemoveMCPServer(config, serverName)).To(Succeed())
			}
		}

		// fetchThrough drives the fetch tool against the given server and returns
		// the tool result. A denied request comes back as an error result
		// (result.IsError); a successful one carries the fetched body.
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

		// dockerBridgeGatewayIP returns the host gateway IP of the default Docker
		// bridge — the same address the egress proxy denies by default, and the
		// address a container uses to reach the host on Linux.
		dockerBridgeGatewayIP := func() string {
			//nolint:gosec // fixed, test-controlled arguments
			out, err := exec.Command("docker", "network", "inspect", "bridge",
				"-f", "{{range .IPAM.Config}}{{.Gateway}}{{end}}").Output()
			Expect(err).ToNot(HaveOccurred(), "Should be able to inspect the docker bridge network")
			return strings.TrimSpace(string(out))
		}

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

			// Target the bridge gateway IP, not host.docker.internal: an IP needs no
			// DNS resolution, so a blocked fetch unambiguously means the egress
			// `docker_gateway_ip` deny fired (see #5640 — the isolated resolver
			// cannot resolve host.docker.internal, which would confound a
			// hostname-based assertion). The hostname deny rule is pinned by the
			// config test below instead.
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
				// The deny→allow swap is pinned deterministically by the config test
				// below; whether the bridge gateway actually routes to a host
				// service is environment-specific — it works on Linux Docker Engine
				// (where the bridge gateway is the host), but on Docker Desktop the
				// host lives behind host.docker.internal, not the bridge gateway. Skip
				// the positive reachability leg where the gateway is not host-routable.
				Skip("docker bridge gateway is not routable to the host in this environment (e.g. Docker Desktop)")
			}
			Expect(resultText(result)).To(ContainSubstring("host-service-ok"),
				"with --allow-docker-gateway the fetch must reach the host service through the egress proxy")
		})

		// This test pins the egress ACL rules deterministically on a real, deployed
		// container — the default hostname and direct-IP deny rules (which the
		// traffic test above cannot exercise without the host.docker.internal DNS
		// confounder of #5640), and the allow rules the flag emits in their place.
		// It complements, rather than duplicates, the unit test for
		// createTempEgressSquidConf: it proves thv actually generates and mounts the
		// config into the running egress proxy, alongside the real-traffic assertion above.
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
	})
})
