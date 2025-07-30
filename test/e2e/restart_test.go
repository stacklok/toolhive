package e2e

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Server Restart", func() {
	var (
		config     *testConfig
		serverName string
	)

	BeforeEach(func() {
		config = NewTestConfig()
		serverName = generateTestServerName("restart-test")

		// Check if thv binary is available
		err := CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up the server if it exists
			err := stopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Describe("Restarting MCP servers", func() {
		Context("when restarting a running server", func() {
			It("should successfully restart and remain accessible", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := waitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				// Get the server URL before restart
				originalURL, err := getMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to get server URL")

				By("Restarting the server")
				stdout, stderr = NewTHVCommand(config, "restart", serverName).ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("restart"), "Output should mention restart operation")

				By("Waiting for the server to be running again")
				err = waitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running again within 60 seconds")

				// Get the server URL after restart
				newURL, err := getMCPServerURL(config, serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to get server URL after restart")

				// The URLs should be the same after restart
				Expect(newURL).To(Equal(originalURL), "Server URL should remain the same after restart")

				By("Verifying the server is functional after restart")
				// List server to verify it's operational
				stdout, _ = NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should be listed")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
			})
		})

		Context("when restarting a stopped server", func() {
			It("should start the server if it was stopped", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := waitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Stopping the server")
				stdout, _ = NewTHVCommand(config, "stop", serverName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring("stop"), "Output should mention stop operation")

				By("Verifying the server is stopped")
				Eventually(func() bool {
					stdout, _ := NewTHVCommand(config, "list", "--all").ExpectSuccess()
					lines := strings.Split(stdout, "\n")
					for _, line := range lines {
						if strings.Contains(line, serverName) {
							// Check if this specific server line contains "running"
							return !strings.Contains(line, "running")
						}
					}
					return false // Server not found in list
				}, 10*time.Second, 1*time.Second).Should(BeTrue(), "Server should be stopped")

				By("Restarting the stopped server")
				stdout, stderr = NewTHVCommand(config, "restart", serverName).ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("restart"), "Output should mention restart operation")

				By("Waiting for the server to be running again")
				err = waitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running again within 60 seconds")

				By("Verifying the server is functional after restart")
				stdout, _ = NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should be listed")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
			})
		})

		Context("when restarting servers with --groups flag", func() {
			It("should restart servers belonging to the specified group", func() {
				// Define group name
				groupName := fmt.Sprintf("restart-group-%d", GinkgoRandomSeed())

				// Create two servers
				serverName1 := generateTestServerName("restart-group-test1")
				serverName2 := generateTestServerName("restart-group-test2")

				By("Creating a group first")
				stdout, stderr := NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("group"), "Output should mention group creation")

				By("Starting the first server")
				stdout, stderr = NewTHVCommand(config, "run", "--name", serverName1, "--group", groupName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Starting the second server")
				stdout, stderr = NewTHVCommand(config, "run", "--name", serverName2, "--group", groupName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for both servers to be running")
				err := waitForMCPServer(config, serverName1, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "First server should be running within 60 seconds")

				err = waitForMCPServer(config, serverName2, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Second server should be running within 60 seconds")

				By("Stopping both servers")
				stdout, _ = NewTHVCommand(config, "stop", serverName1).ExpectSuccess()
				Expect(stdout).To(ContainSubstring("stop"), "Output should mention stop operation for first server")

				stdout, _ = NewTHVCommand(config, "stop", serverName2).ExpectSuccess()
				Expect(stdout).To(ContainSubstring("stop"), "Output should mention stop operation for second server")

				By("Verifying the servers are stopped")
				Eventually(func() bool {
					stdout, _ := NewTHVCommand(config, "list", "--all").ExpectSuccess()
					lines := strings.Split(stdout, "\n")
					server1Found := false
					server2Found := false
					server1Running := false
					server2Running := false

					for _, line := range lines {
						if strings.Contains(line, serverName1) {
							server1Found = true
							server1Running = strings.Contains(line, "running")
						}
						if strings.Contains(line, serverName2) {
							server2Found = true
							server2Running = strings.Contains(line, "running")
						}
					}

					// Both servers should be found and neither should be running
					return server1Found && server2Found && !server1Running && !server2Running
				}, 10*time.Second, 1*time.Second).Should(BeTrue(), "Both servers should be stopped")

				By("Restarting all servers in the group")
				stdout, stderr = NewTHVCommand(config, "restart", "--group", groupName).ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("restart"), "Output should mention restart operation")

				By("Waiting for both servers to be running again")
				err = waitForMCPServer(config, serverName1, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "First server should be running again within 60 seconds")

				err = waitForMCPServer(config, serverName2, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Second server should be running again within 60 seconds")

				By("Verifying both servers are functional after restart")
				stdout, _ = NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName1), "First server should be listed")
				Expect(stdout).To(ContainSubstring(serverName2), "Second server should be listed")
				Expect(stdout).To(ContainSubstring("running"), "Servers should be in running state")

				// Clean up these specific servers at the end of the test
				defer func() {
					if config.CleanupAfter {
						_ = stopAndRemoveMCPServer(config, serverName1)
						_ = stopAndRemoveMCPServer(config, serverName2)
					}
				}()
			})
		})
	})
})

// generateTestServerName creates a unique server name for restart tests
func generateTestServerName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, GinkgoRandomSeed())
}
