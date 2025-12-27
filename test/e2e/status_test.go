package e2e

import (
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck // Standard practice for Ginkgo
	. "github.com/onsi/gomega"    //nolint:staticcheck // Standard practice for Gomega
)

var _ = Describe("Status Command", Label("status"), func() {
	var (
		config     *TestConfig
		serverName string
	)

	BeforeEach(func() {
		config = NewTestConfig()
		serverName = "status-test-server"

		// Clean up any existing server with this name
		_ = StopAndRemoveMCPServer(config, serverName)
	})

	AfterEach(func() {
		// Clean up the server
		_ = StopAndRemoveMCPServer(config, serverName)
	})

	Context("when a server is running", func() {
		BeforeEach(func() {
			// Start a test MCP server using OSV scanner (a commonly available image)
			stdout, stderr := NewTHVCommand(config,
				"run", "ghcr.io/stacklok/mcp-server-osv:latest",
				"--name", serverName,
				"--transport", "sse",
				"--permission", "none",
			).ExpectSuccess()

			GinkgoWriter.Printf("Server start stdout: %s\nServer start stderr: %s\n", stdout, stderr)

			// Wait for the server to be running
			err := WaitForMCPServer(config, serverName, 2*time.Minute)
			Expect(err).ToNot(HaveOccurred(), "Server should be running")
		})

		It("should show status in text format", func() {
			stdout, stderr := NewTHVCommand(config, "status", serverName).ExpectSuccess()

			GinkgoWriter.Printf("Status stdout: %s\nStatus stderr: %s\n", stdout, stderr)

			// Verify key fields are present in the output
			Expect(stdout).To(ContainSubstring("Name:"))
			Expect(stdout).To(ContainSubstring(serverName))
			Expect(stdout).To(ContainSubstring("Status:"))
			Expect(stdout).To(ContainSubstring("running"))
			Expect(stdout).To(ContainSubstring("Health:"))
			Expect(stdout).To(ContainSubstring("healthy"))
			Expect(stdout).To(ContainSubstring("Uptime:"))
			Expect(stdout).To(ContainSubstring("Group:"))
			Expect(stdout).To(ContainSubstring("Transport:"))
			Expect(stdout).To(ContainSubstring("URL:"))
			Expect(stdout).To(ContainSubstring("Port:"))
		})

		It("should show status in JSON format", func() {
			stdout, stderr := NewTHVCommand(config, "status", serverName, "--format", "json").ExpectSuccess()

			GinkgoWriter.Printf("Status JSON stdout: %s\nStatus stderr: %s\n", stdout, stderr)

			// Verify the output is valid JSON
			var statusOutput map[string]interface{}
			err := json.Unmarshal([]byte(stdout), &statusOutput)
			Expect(err).ToNot(HaveOccurred(), "Output should be valid JSON")

			// Verify key fields are present
			Expect(statusOutput).To(HaveKey("name"))
			Expect(statusOutput["name"]).To(Equal(serverName))
			Expect(statusOutput).To(HaveKey("status"))
			Expect(statusOutput["status"]).To(Equal("running"))
			Expect(statusOutput).To(HaveKey("health"))
			Expect(statusOutput["health"]).To(Equal("healthy"))
			Expect(statusOutput).To(HaveKey("uptime"))
			Expect(statusOutput).To(HaveKey("group"))
			Expect(statusOutput).To(HaveKey("transport"))
			Expect(statusOutput).To(HaveKey("url"))
			Expect(statusOutput).To(HaveKey("port"))
		})

		It("should show uptime for a running server", func() {
			// Wait a few seconds for measurable uptime
			time.Sleep(3 * time.Second)

			stdout, _ := NewTHVCommand(config, "status", serverName, "--format", "json").ExpectSuccess()

			var statusOutput map[string]interface{}
			err := json.Unmarshal([]byte(stdout), &statusOutput)
			Expect(err).ToNot(HaveOccurred())

			// Verify uptime is not empty or zero
			uptime, ok := statusOutput["uptime"].(string)
			Expect(ok).To(BeTrue())
			Expect(uptime).ToNot(Equal("-"))
			Expect(uptime).ToNot(BeEmpty())

			// Verify uptime_seconds is greater than 0
			uptimeSeconds, ok := statusOutput["uptime_seconds"].(float64)
			Expect(ok).To(BeTrue())
			Expect(uptimeSeconds).To(BeNumerically(">", 0))
		})
	})

	Context("when a server is stopped", func() {
		BeforeEach(func() {
			// Start a test MCP server
			NewTHVCommand(config,
				"run", "ghcr.io/stacklok/mcp-server-osv:latest",
				"--name", serverName,
				"--transport", "sse",
				"--permission", "none",
			).ExpectSuccess()

			// Wait for the server to be running
			err := WaitForMCPServer(config, serverName, 2*time.Minute)
			Expect(err).ToNot(HaveOccurred(), "Server should be running")

			// Stop the server
			NewTHVCommand(config, "stop", serverName).ExpectSuccess()

			// Wait a moment for the server to stop
			time.Sleep(2 * time.Second)
		})

		It("should show stopped status", func() {
			stdout, stderr := NewTHVCommand(config, "status", serverName).ExpectSuccess()

			GinkgoWriter.Printf("Stopped status stdout: %s\nStderr: %s\n", stdout, stderr)

			Expect(stdout).To(ContainSubstring("Status:"))
			Expect(stdout).To(ContainSubstring("stopped"))
			Expect(stdout).To(ContainSubstring("Health:"))
			Expect(stdout).To(ContainSubstring("stopped"))
			// Uptime should be dash for stopped servers
			Expect(stdout).To(ContainSubstring("Uptime:"))
		})

		It("should show stopped status in JSON format", func() {
			stdout, _ := NewTHVCommand(config, "status", serverName, "--format", "json").ExpectSuccess()

			var statusOutput map[string]interface{}
			err := json.Unmarshal([]byte(stdout), &statusOutput)
			Expect(err).ToNot(HaveOccurred())

			Expect(statusOutput["status"]).To(Equal("stopped"))
			Expect(statusOutput["health"]).To(Equal("stopped"))
			Expect(statusOutput["uptime"]).To(Equal("-"))
		})
	})

	Context("when a server does not exist", func() {
		It("should return an error", func() {
			_, stderr, err := NewTHVCommand(config, "status", "non-existent-server").ExpectFailure()

			// Should contain an error message about not finding the workload
			combinedOutput := strings.ToLower(stderr)
			Expect(combinedOutput).To(SatisfyAny(
				ContainSubstring("not found"),
				ContainSubstring("failed"),
				ContainSubstring("error"),
			))
			Expect(err).To(HaveOccurred())
		})
	})

	Context("when no arguments are provided", func() {
		It("should return an error", func() {
			_, stderr, err := NewTHVCommand(config, "status").ExpectFailure()

			// Should complain about missing argument
			Expect(stderr).To(ContainSubstring("accepts 1 arg(s)"))
			Expect(err).To(HaveOccurred())
		})
	})
})
