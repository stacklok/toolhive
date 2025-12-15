package e2e_test

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Restart Zombie Process Prevention", Label("core", "restart", "e2e"), func() {
	var (
		config     *e2e.TestConfig
		serverName string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = generateTestServerName("restart-zombie-test")

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

	Describe("Preventing zombie supervisor processes", func() {
		Context("when restarting a running server multiple times", func() {
			It("should not accumulate supervisor processes", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				// Count supervisor processes before restart
				countBefore := countSupervisorProcesses(serverName)
				GinkgoWriter.Printf("Supervisor processes before restart: %d\n", countBefore)
				Expect(countBefore).To(Equal(1), "Should have exactly 1 supervisor process before restart")

				By("Starting the server")
				stdout, stderr = e2e.NewTHVCommand(config, "start", serverName).ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("start"), "Output should mention start operation")

				By("Waiting for the server to be running again")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running again within 60 seconds")

				// Wait a moment for any lingering processes to stabilize
				time.Sleep(2 * time.Second)

				// Count supervisor processes after restart
				countAfter := countSupervisorProcesses(serverName)
				GinkgoWriter.Printf("Supervisor processes after restart: %d\n", countAfter)
				Expect(countAfter).To(Equal(1), "Should still have exactly 1 supervisor process after restart")

				By("Starting the server a second time")
				stdout, stderr = e2e.NewTHVCommand(config, "start", serverName).ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("start"), "Output should mention start operation")

				By("Waiting for the server to be running again")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running again within 60 seconds")

				// Wait a moment for any lingering processes to stabilize
				time.Sleep(2 * time.Second)

				// Count supervisor processes after second restart
				countAfterSecond := countSupervisorProcesses(serverName)
				GinkgoWriter.Printf("Supervisor processes after second restart: %d\n", countAfterSecond)
				Expect(countAfterSecond).To(Equal(1), "Should still have exactly 1 supervisor process after second restart")

				By("Verifying the server is functional after multiple restarts")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should be listed")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
			})
		})
	})
})

// countSupervisorProcesses counts the number of supervisor processes for a given workload
// by looking for "thv start <workloadName> --foreground" processes
func countSupervisorProcesses(workloadName string) int {
	// Use ps to find processes matching "thv start <workloadName> --foreground"
	cmd := exec.Command("ps", "aux")
	output, err := cmd.Output()
	if err != nil {
		GinkgoWriter.Printf("Error running ps command: %v\n", err)
		return 0
	}

	lines := strings.Split(string(output), "\n")
	count := 0
	searchPattern := fmt.Sprintf("thv start %s --foreground", workloadName)

	for _, line := range lines {
		if strings.Contains(line, searchPattern) && !strings.Contains(line, "grep") {
			count++
			GinkgoWriter.Printf("Found supervisor process: %s\n", line)
		}
	}

	return count
}
