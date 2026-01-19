package e2e_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Status Command", Label("core", "status", "e2e"), func() {
	var (
		config     *e2e.TestConfig
		serverName string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = fmt.Sprintf("status-test-%d", GinkgoRandomSeed())

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up the server if it exists
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Describe("Getting status of MCP servers", func() {
		Context("when getting status of a running server", func() {
			It("should display detailed status information in text format", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Getting the status of the server")
				stdout, _ = e2e.NewTHVCommand(config, "status", serverName).ExpectSuccess()

				By("Verifying the status output contains expected fields")
				Expect(stdout).To(ContainSubstring("Name:"), "Output should contain Name field")
				Expect(stdout).To(ContainSubstring(serverName), "Output should contain server name")
				Expect(stdout).To(ContainSubstring("Status:"), "Output should contain Status field")
				Expect(stdout).To(ContainSubstring("running"), "Output should show running status")
				Expect(stdout).To(ContainSubstring("URL:"), "Output should contain URL field")
				Expect(stdout).To(ContainSubstring("Port:"), "Output should contain Port field")
				Expect(stdout).To(ContainSubstring("Transport:"), "Output should contain Transport field")
				Expect(stdout).To(ContainSubstring("Created:"), "Output should contain Created field")
			})

			It("should display status in JSON format", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Getting the status in JSON format")
				stdout, _ = e2e.NewTHVCommand(config, "status", "--format", "json", serverName).ExpectSuccess()

				By("Verifying the JSON output is valid and contains expected fields")
				var workload core.Workload
				err = json.Unmarshal([]byte(stdout), &workload)
				Expect(err).ToNot(HaveOccurred(), "Output should be valid JSON")
				Expect(workload.Name).To(Equal(serverName), "JSON should contain correct server name")
				Expect(string(workload.Status)).To(Equal("running"), "JSON should show running status")
				Expect(workload.URL).ToNot(BeEmpty(), "JSON should contain URL")
				Expect(workload.Port).To(BeNumerically(">", 0), "JSON should contain valid port")
			})
		})

		Context("when getting status of a non-existent server", func() {
			It("should return an error", func() {
				By("Getting status of a server that doesn't exist")
				stdout, stderr, err := e2e.NewTHVCommand(config, "status", "non-existent-server-12345").Run()

				Expect(err).To(HaveOccurred(), "Command should fail for non-existent server")
				Expect(stdout+stderr).To(ContainSubstring("not found"),
					"Error message should indicate server was not found")
			})
		})

		Context("when getting status of a stopped server", func() {
			It("should display stopped status", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Stopping the server")
				stdout, _ = e2e.NewTHVCommand(config, "stop", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring("stop"), "Output should mention stop operation")

				By("Waiting for the server to be stopped")
				Eventually(func() bool {
					stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
					lines := strings.Split(stdout, "\n")
					for _, line := range lines {
						if strings.Contains(line, serverName) {
							return strings.Contains(line, "stopped")
						}
					}
					return false
				}, 10*time.Second, 1*time.Second).Should(BeTrue(), "Server should be stopped")

				By("Getting the status of the stopped server")
				stdout, _ = e2e.NewTHVCommand(config, "status", serverName).ExpectSuccess()

				By("Verifying the status shows stopped")
				Expect(stdout).To(ContainSubstring("Name:"), "Output should contain Name field")
				Expect(stdout).To(ContainSubstring(serverName), "Output should contain server name")
				Expect(stdout).To(ContainSubstring("Status:"), "Output should contain Status field")
				Expect(stdout).To(ContainSubstring("stopped"), "Output should show stopped status")
			})
		})
	})
})
