// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adrg/xdg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Unhealthy Workload Detection", Label("stability", "unhealthy", "e2e"), func() {
	var (
		config     *e2e.TestConfig
		serverName string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = generateUnhealthyTestServerName("unhealthy-test")

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

	Describe("Detecting unhealthy workloads", func() {
		Context("when the proxy process is killed", func() {
			It("should mark the workload as unhealthy", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying the server is healthy initially")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should be listed")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")

				By("Finding and killing the proxy process")
				proxyPID, err := findProxyProcess(serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to find proxy process")
				Expect(proxyPID).ToNot(BeZero(), "Proxy PID should not be zero")

				// Kill the proxy process
				err = killProcess(proxyPID)
				Expect(err).ToNot(HaveOccurred(), "Should be able to kill proxy process")

				By("Waiting for the workload to be detected as unhealthy")
				err = e2e.WaitForWorkloadUnhealthy(config, serverName, 10*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be marked as unhealthy within 10 seconds")

				By("Verifying the workload shows unhealthy status with context")
				stdout, _ = e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should be listed")
				Expect(stdout).To(ContainSubstring("unhealthy"), "Server should be marked as unhealthy")
			})
		})

		Context("when the docker container is killed", func() {
			It("should mark the workload as unhealthy", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying the server is healthy initially")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should be listed")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")

				By("Finding and killing the docker container")
				containerName, err := findDockerContainer(serverName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to find docker container")
				Expect(containerName).ToNot(BeEmpty(), "Container name should not be empty")

				// Kill the docker container
				err = killDockerContainer(containerName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to kill docker container")

				By("Waiting for the workload to be detected as unhealthy")
				err = e2e.WaitForWorkloadUnhealthy(config, serverName, 10*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be marked as unhealthy within 10 seconds")

				By("Verifying the workload shows unhealthy status with context")
				stdout, _ = e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should be listed")
				Expect(stdout).To(ContainSubstring("unhealthy"), "Server should be marked as unhealthy")
			})
		})
	})
})

// Helper functions for process and container management

// workloadStatusFile represents the JSON structure stored on disk
type workloadStatusFile struct {
	Status        string    `json:"status"`
	StatusContext string    `json:"status_context,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ProcessID     int       `json:"process_id"`
}

// findProxyProcess finds the PID of the proxy process for a given server name
// by reading it from the workload status file
func findProxyProcess(serverName string) (int, error) {
	// The proxy process PID is stored in the status file
	statusFilePath, err := xdg.DataFile(filepath.Join("toolhive", "statuses", serverName+".json"))
	if err != nil {
		return 0, fmt.Errorf("failed to get status file path: %w", err)
	}

	// Read the status file
	statusBytes, err := os.ReadFile(statusFilePath)
	if err != nil {
		return 0, fmt.Errorf("failed to read status file %s: %w", statusFilePath, err)
	}

	// Parse the JSON
	var statusFile workloadStatusFile
	if err := json.Unmarshal(statusBytes, &statusFile); err != nil {
		return 0, fmt.Errorf("failed to parse status file %s: %w", statusFilePath, err)
	}

	pid := statusFile.ProcessID
	if pid == 0 {
		return 0, fmt.Errorf("process ID is 0 in status file")
	}

	// Verify the process is actually running
	if !isProcessRunning(pid) {
		return 0, fmt.Errorf("process with PID %d is not running", pid)
	}

	return pid, nil
}

// isProcessRunning checks if a process with the given PID is running
func isProcessRunning(pid int) bool {
	// Try to find the process
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Send signal 0 to check if the process exists
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// killProcess kills a process by its PID
func killProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process with PID %d: %w", pid, err)
	}

	// Send SIGTERM first for graceful shutdown
	err = proc.Signal(syscall.SIGTERM)
	if err != nil {
		return fmt.Errorf("failed to send SIGTERM to process %d: %w", pid, err)
	}

	// Give it a moment to terminate gracefully
	time.Sleep(1 * time.Second)

	// Check if it's still running, if so use SIGKILL
	if isProcessRunning(pid) {
		err = proc.Signal(syscall.SIGKILL)
		if err != nil {
			return fmt.Errorf("failed to send SIGKILL to process %d: %w", pid, err)
		}
	}

	return nil
}

// findDockerContainer finds the docker container name for a given server name
func findDockerContainer(serverName string) (string, error) {
	// Use docker ps to find the container
	cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", serverName), "--format", "{{.Names}}")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to list docker containers: %w", err)
	}

	containerName := strings.TrimSpace(string(output))
	if containerName == "" {
		return "", fmt.Errorf("no container found with name pattern %s", serverName)
	}

	// If multiple containers are returned, take the first one
	lines := strings.Split(containerName, "\n")
	if len(lines) > 1 {
		containerName = lines[0]
	}

	return containerName, nil
}

// killDockerContainer kills a docker container by name
func killDockerContainer(containerName string) error {
	// First try docker kill (SIGKILL)
	cmd := exec.Command("docker", "kill", containerName)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to kill docker container %s: %w", containerName, err)
	}

	return nil
}

// generateUnhealthyTestServerName creates a unique server name for unhealthy workload tests
func generateUnhealthyTestServerName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, GinkgoRandomSeed())
}
