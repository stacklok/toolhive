// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// inspectorAutoCleanupTestHelper contains functionality for testing inspector auto-cleanup
type inspectorAutoCleanupTestHelper struct {
	config        *e2e.TestConfig
	mcpServerName string
	inspectorName string // Always "inspector"
	inspectorCmd  *exec.Cmd
}

// newInspectorAutoCleanupTestHelper creates a new test helper for auto-cleanup testing
func newInspectorAutoCleanupTestHelper(config *e2e.TestConfig, mcpServerName string) *inspectorAutoCleanupTestHelper {
	return &inspectorAutoCleanupTestHelper{
		config:        config,
		mcpServerName: mcpServerName,
		inspectorName: "inspector",
	}
}

// setupMCPServer starts an MCP server for the inspector to connect to
func (h *inspectorAutoCleanupTestHelper) setupMCPServer() {
	By("Starting an MCP server for inspector to connect to")
	e2e.NewTHVCommand(h.config, "run", "--name", h.mcpServerName, "fetch").ExpectSuccess()
	err := e2e.WaitForMCPServer(h.config, h.mcpServerName, 60*time.Second)
	Expect(err).ToNot(HaveOccurred(), "MCP server should be running")
}

// startInspector starts the inspector command and returns the process
func (h *inspectorAutoCleanupTestHelper) startInspector() {
	args := []string{"inspector", h.mcpServerName}
	GinkgoWriter.Printf("Starting inspector with args: %v\n", args)

	cmd := e2e.StartLongRunningTHVCommand(h.config, args...)
	h.inspectorCmd = cmd
}

// interruptInspector sends SIGINT to the inspector process
func (h *inspectorAutoCleanupTestHelper) interruptInspector() error {
	if h.inspectorCmd == nil {
		return fmt.Errorf("inspector command not started")
	}

	GinkgoWriter.Printf("Sending SIGINT to inspector process (PID: %d)\n", h.inspectorCmd.Process.Pid)
	return h.inspectorCmd.Process.Signal(syscall.SIGINT)
}

// waitForInspectorExit waits for the inspector process to exit
func (h *inspectorAutoCleanupTestHelper) waitForInspectorExit(timeout time.Duration) error {
	if h.inspectorCmd == nil {
		return fmt.Errorf("inspector command not started")
	}

	GinkgoWriter.Printf("Waiting for inspector process to exit (timeout: %v)\n", timeout)

	done := make(chan error, 1)
	go func() {
		done <- h.inspectorCmd.Wait()
	}()

	select {
	case err := <-done:
		GinkgoWriter.Printf("Inspector process exited with error: %v\n", err)
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for inspector process to exit")
	}
}

// verifyInspectorContainerExists checks if the inspector container exists
func (h *inspectorAutoCleanupTestHelper) verifyInspectorContainerExists() bool {
	stdout, _ := e2e.NewTHVCommand(h.config, "list", "--all").ExpectSuccess()
	return strings.Contains(stdout, h.inspectorName)
}

// verifyInspectorContainerGone checks if the inspector container is removed
func (h *inspectorAutoCleanupTestHelper) verifyInspectorContainerGone() bool {
	stdout, _ := e2e.NewTHVCommand(h.config, "list", "--all").ExpectSuccess()
	return !strings.Contains(stdout, h.inspectorName)
}

// cleanup performs final cleanup of any remaining containers
func (h *inspectorAutoCleanupTestHelper) cleanup() {
	// Clean up MCP server
	err := e2e.StopAndRemoveMCPServer(h.config, h.mcpServerName)
	if err != nil {
		GinkgoWriter.Printf("Warning: Failed to cleanup MCP server: %v\n", err)
	}

	// Clean up inspector container if it still exists
	if h.verifyInspectorContainerExists() {
		err = e2e.StopAndRemoveMCPServer(h.config, h.inspectorName)
		if err != nil {
			GinkgoWriter.Printf("Warning: Failed to cleanup inspector container: %v\n", err)
		}
	}
}

var _ = Describe("Inspector Auto-Cleanup", Label("mcp", "e2e", "inspector", "cleanup"), func() {
	var config *e2e.TestConfig

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Context("Startup interruption scenarios", func() {
		It("should auto-cleanup container when interrupted during startup", func() {
			mcpServerName := fmt.Sprintf("mcp-earlyint-%d", GinkgoRandomSeed())
			helper := newInspectorAutoCleanupTestHelper(config, mcpServerName)

			defer helper.cleanup()

			By("Starting an MCP server for inspector to connect to")
			helper.setupMCPServer()

			By("Starting inspector command")
			helper.startInspector()

			By("Immediately sending interrupt signal (before ready)")
			// Give it a moment to start but interrupt before it's ready
			time.Sleep(2 * time.Second)
			err := helper.interruptInspector()
			Expect(err).ToNot(HaveOccurred(), "Should be able to interrupt inspector")

			By("Waiting for inspector process to exit")
			err = helper.waitForInspectorExit(15 * time.Second)
			Expect(err).ToNot(HaveOccurred(), "Inspector should exit after interrupt")

			By("Verifying inspector container is cleaned up")
			Expect(helper.verifyInspectorContainerGone()).To(BeTrue(), "Container should be cleaned up")

			By("Verifying no orphaned containers remain")
			stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
			Expect(stdout).ToNot(ContainSubstring("inspector"), "No inspector should remain")
		})

		It("should auto-cleanup container when interrupted immediately after start", func() {
			mcpServerName := fmt.Sprintf("mcp-immediateint-%d", GinkgoRandomSeed())
			helper := newInspectorAutoCleanupTestHelper(config, mcpServerName)

			defer helper.cleanup()

			By("Starting an MCP server for inspector to connect to")
			helper.setupMCPServer()

			By("Starting inspector command")
			helper.startInspector()

			By("Immediately sending interrupt signal (minimal delay)")
			// Interrupt almost immediately
			time.Sleep(500 * time.Millisecond)
			err := helper.interruptInspector()
			Expect(err).ToNot(HaveOccurred(), "Should be able to interrupt inspector")

			By("Waiting for inspector process to exit")
			err = helper.waitForInspectorExit(15 * time.Second)
			Expect(err).ToNot(HaveOccurred(), "Inspector should exit after interrupt")

			By("Verifying inspector container is cleaned up")
			Expect(helper.verifyInspectorContainerGone()).To(BeTrue(), "Container should be cleaned up")
		})
	})
})
