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

var _ = Describe("NetworkIsolation", Label("network", "isolation", "e2e"), func() {
	var (
		config               *e2e.TestConfig
		serverName           string
		permissionProfileDir string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = fmt.Sprintf("network-isolation-test-%d", GinkgoRandomSeed())

		// Create temporary directory for permission profiles
		var err error
		permissionProfileDir, err = os.MkdirTemp("", "network-isolation-profiles-*")
		Expect(err).ToNot(HaveOccurred(), "Should be able to create temp directory")

		// Check if thv binary is available
		err = e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up the server if it exists
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}

		// Clean up temporary permission profile directory
		if permissionProfileDir != "" {
			os.RemoveAll(permissionProfileDir)
		}
	})

	Describe("Running MCP server with network isolation", func() {
		It("should enforce both inbound and outbound network restrictions", func() {
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
			profilePath := filepath.Join(permissionProfileDir, "network-isolation.json")
			err := os.WriteFile(profilePath, []byte(permissionProfile), 0644)
			Expect(err).ToNot(HaveOccurred(), "Should be able to create permission profile")

			By("Starting the fetch MCP server with network isolation")
			stdout, stderr := e2e.NewTHVCommand(config, "run",
				"--name", serverName,
				"--isolate-network",
				"--permission-profile", profilePath,
				"fetch").ExpectSuccess()

			Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

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
		})
	})
})
