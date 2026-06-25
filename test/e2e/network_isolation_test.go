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
		const gatewayHost = "host.docker.internal"

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

		// fetchBlocked drives the fetch tool against the given server and reports
		// whether the request was blocked (the fetch tool returns an error result
		// when the egress proxy denies the request).
		fetchBlocked := func(serverName, targetURL string) bool {
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
			return result.IsError
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

		It("blocks the docker gateway by default and reaches it with the flag", func() {
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

			By("Confirming the gateway is blocked by default, even under allow-all")
			denyServer := startFetchServer("deny", "")
			Expect(fetchBlocked(denyServer, target)).To(BeTrue(),
				"gateway IP must be blocked without --allow-docker-gateway")
			Expect(fetchBlocked(denyServer, fmt.Sprintf("http://%s:80/", gatewayHost))).To(BeTrue(),
				"host.docker.internal must be blocked without --allow-docker-gateway")
			retireServer(denyServer)

			By("Confirming the flag removes the deny so the host becomes reachable")
			allowServer := startFetchServer("allow", "", "--allow-docker-gateway")
			if fetchBlocked(allowServer, target) {
				// The deny removal is proven by the negative assertions above and
				// the config test; whether the bridge gateway actually routes to a
				// host service is environment-specific (works on Linux Docker
				// Engine; on Docker Desktop the host lives behind host.docker.internal
				// instead). Skip the positive reachability leg where it does not apply.
				Skip("docker bridge gateway is not routable to the host in this environment")
			}
		})

		It("removes the egress proxy gateway deny rules only when the flag is set", func() {
			squidConf := func(serverName string) string {
				//nolint:gosec // container name is test-controlled
				out, err := exec.Command("docker", "exec", serverName+"-egress",
					"cat", "/etc/squid/squid.conf").CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Should be able to read egress squid.conf: %s", string(out))
				return string(out)
			}

			By("Default profile: gateway deny rules are present")
			denyServer := startFetchServer("cfg-deny", "")
			denyConf := squidConf(denyServer)
			Expect(denyConf).To(ContainSubstring("http_access deny docker_gateway_hosts"),
				"default config must deny the docker gateway hostnames")
			Expect(denyConf).To(ContainSubstring("dstdomain host.docker.internal"))
			retireServer(denyServer)

			By("With --allow-docker-gateway: gateway deny rules are absent")
			allowServer := startFetchServer("cfg-allow", "", "--allow-docker-gateway")
			Expect(squidConf(allowServer)).ToNot(ContainSubstring("docker_gateway_hosts"),
				"--allow-docker-gateway must remove the gateway deny rules")
		})
	})
})
