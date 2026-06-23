// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
})
