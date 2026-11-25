// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

		It("denies the default bridge gateway by default and reaches it with the flag", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-default-gateway-%d", GinkgoRandomSeed()))
			gatewayIP := dockerNetworkGateway("bridge")
			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)

			By("Confirming the default bridge gateway is blocked by the egress proxy")
			denyServer := startFetchServer("deny", "")
			Expect(fetchThrough(denyServer, target).IsError).To(BeTrue(),
				"the egress proxy must deny the default bridge gateway without --allow-docker-gateway")
			retireServer(denyServer)

			By("Confirming --allow-docker-gateway grants access to the same reachable target")
			allowServer := startFetchServer("allow", "", "--allow-docker-gateway")
			Expect(fetchThrough(allowServer, target).IsError).To(BeFalse(),
				"the explicit gateway opt-in must reach the host-published service")
		})

		It("blocks the gateway of every external network attached to the proxy", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-host-squid-%d", GinkgoRandomSeed()))
			server := startFetchServer("attached-gateway", "")
			gatewayIP := dockerNetworkGateway("toolhive-external")

			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)
			directOutput, directErr := directHTTPRequestFromNetwork("toolhive-external", target)
			Expect(directErr).ToNot(HaveOccurred(), "gateway target must be reachable without the proxy: %s", directOutput)
			result := fetchThrough(server, target)
			Expect(result.IsError).To(BeTrue(),
				"network isolation must block host access through every proxy network gateway")
		})

		It("blocks gateways owned by Docker bridges the proxy is not attached to", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-host-unattached-%d", GinkgoRandomSeed()))
			networkName := fmt.Sprintf("ni-unattached-%d", GinkgoRandomSeed())
			created, output := createDockerBridgeNetwork(networkName, false)
			Expect(created).To(BeTrue(), "should create an unattached bridge: %s", output)
			gatewayIP := dockerNetworkGateway(networkName)
			server := startFetchServer("unattached-gateway", "")

			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)
			directOutput, directErr := directHTTPRequestFromNetwork("toolhive-external", target)
			if directErr != nil {
				Skip("unattached bridge gateway is not reachable from the proxy network: " + directOutput)
			}
			result := fetchThrough(server, target)
			Expect(result.IsError).To(BeTrue(),
				"network isolation must block host access through reachable Docker bridge gateways")
		})

		It("blocks an attached gateway under a port-only outbound profile", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-host-port-only-%d", GinkgoRandomSeed()))
			profile := fmt.Sprintf(`{
				"name": "port-only",
				"network": {"outbound": {"insecure_allow_all": false, "allow_port": [%d]}}
			}`, port)
			server := startFetchServer("port-only", profile)
			gatewayIP := dockerNetworkGateway("toolhive-external")

			target := fmt.Sprintf("http://%s:%d/", gatewayIP, port)
			directOutput, directErr := directHTTPRequestFromNetwork("toolhive-external", target)
			Expect(directErr).ToNot(HaveOccurred(), "port-only target must be reachable without the proxy: %s", directOutput)
			result := fetchThrough(server, target)
			Expect(result.IsError).To(BeTrue(),
				"an allowed port must not grant access to a host gateway")
		})

		It("enforces gateway policy for the workload internal bridge", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-host-internal-%d", GinkgoRandomSeed()))
			denyServer := startFetchServer("internal-deny", internalGatewayDenyProfile)
			networkName, target, output, err := probeInternalGateway(denyServer, port)
			if err != nil {
				Skip(fmt.Sprintf("Docker runtime cannot route from %s to its gateway target %s: %s", networkName, target, output))
			}
			Expect(fetchThrough(denyServer, target).IsError).To(BeTrue(), "default gateway policy must block the internal gateway")
			retireServer(denyServer)

			allowServer := startFetchServer("internal-allow", internalGatewayAllowProfile, "--allow-docker-gateway")
			networkName, target, output, err = probeInternalGateway(allowServer, port)
			if err != nil {
				Skip(fmt.Sprintf("Docker runtime cannot route from %s to its gateway target %s: %s", networkName, target, output))
			}
			Expect(fetchThrough(allowServer, target).IsError).To(BeFalse(), "gateway opt-in must allow the internal gateway")
		})

		It("mitigates direct gateway access under an unrelated hostname allowlist", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-host-allowlist-%d", GinkgoRandomSeed()))
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

			Expect(fetchThrough(server, "https://example.com").IsError).To(BeFalse(),
				"the explicit hostname allowlist must preserve its permitted destination")
			result := fetchThrough(server, fmt.Sprintf("http://%s:%d/", gatewayIP, port))
			Expect(result.IsError).To(BeTrue(),
				"a bare gateway IP must not match an unrelated hostname allowlist")
		})

		It("allows each reachable startup-snapshot gateway under the explicit opt-in", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-host-opt-in-%d", GinkgoRandomSeed()))
			unattached := fmt.Sprintf("ni-opt-in-unattached-%d", GinkgoRandomSeed())
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
				Expect(fetchThrough(server, target.url).IsError).To(BeFalse(), "%s must be allowed", target.name)
			}
		})

		It("blocks a reachable IPv6 Docker bridge gateway when IPv6 is available", func() {
			port := startPublishedHTTPService(fmt.Sprintf("ni-host-ipv6-%d", GinkgoRandomSeed()))
			networkName := fmt.Sprintf("ni-ipv6-%d", GinkgoRandomSeed())
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

			Expect(fetchThrough(server, target).IsError).To(BeTrue(),
				"network isolation must block reachable IPv6 Docker bridge gateways")
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
