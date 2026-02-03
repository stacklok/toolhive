// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
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
	httpClient    *http.Client
	inspectorURL  string
}

// newInspectorAutoCleanupTestHelper creates a new test helper for auto-cleanup testing
func newInspectorAutoCleanupTestHelper(config *e2e.TestConfig, mcpServerName string) *inspectorAutoCleanupTestHelper {
	return &inspectorAutoCleanupTestHelper{
		config:        config,
		mcpServerName: mcpServerName,
		inspectorName: "inspector",
		httpClient:    &http.Client{Timeout: 5 * time.Second},
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

	// Build the expected inspector URL
	h.inspectorURL = "http://localhost:6274"
}

// interruptInspector sends SIGINT to the inspector process
func (h *inspectorAutoCleanupTestHelper) interruptInspector() error {
	if h.inspectorCmd == nil {
		return fmt.Errorf("inspector command not started")
	}

	GinkgoWriter.Printf("Sending SIGINT to inspector process (PID: %d)\n", h.inspectorCmd.Process.Pid)
	return h.inspectorCmd.Process.Signal(syscall.SIGINT)
}

// waitForInspectorReady waits for the inspector UI to become accessible
func (h *inspectorAutoCleanupTestHelper) waitForInspectorReady(timeout time.Duration) error {
	GinkgoWriter.Printf("Waiting for inspector UI to be ready at %s\n", h.inspectorURL)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for inspector UI to be ready")
		case <-ticker.C:
			resp, err := h.httpClient.Get(h.inspectorURL)
			if err == nil && resp.StatusCode == 200 {
				_ = resp.Body.Close()
				GinkgoWriter.Println("Inspector UI is ready")
				return nil
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
			GinkgoWriter.Println("Inspector UI not ready yet, waiting...")
		}
	}
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

// verifyUIAccessible checks if the inspector UI is accessible
func (h *inspectorAutoCleanupTestHelper) verifyUIAccessible() bool {
	if h.inspectorURL == "" {
		return false
	}

	resp, err := h.httpClient.Get(h.inspectorURL)
	if err == nil {
		_ = resp.Body.Close()
		return resp.StatusCode == 200
	}
	return false
}

// verifyUIInaccessible checks if the inspector UI is not accessible
func (h *inspectorAutoCleanupTestHelper) verifyUIInaccessible() bool {
	if h.inspectorURL == "" {
		return true // No URL means it's inaccessible
	}

	resp, err := h.httpClient.Get(h.inspectorURL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err != nil
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

	Context("Normal operation scenarios", func() {
		It("should auto-cleanup container when interrupted after ready", func() {
			mcpServerName := fmt.Sprintf("mcp-autoclean-%d", GinkgoRandomSeed())
			helper := newInspectorAutoCleanupTestHelper(config, mcpServerName)

			defer helper.cleanup()

			By("Starting an MCP server for inspector to connect to")
			helper.setupMCPServer()

			By("Starting inspector command")
			helper.startInspector()

			By("Waiting for inspector UI to be ready")
			err := helper.waitForInspectorReady(45 * time.Second)
			Expect(err).ToNot(HaveOccurred(), "Inspector should become ready")

			By("Verifying inspector UI is accessible")
			Expect(helper.verifyUIAccessible()).To(BeTrue(), "UI should be accessible")

			By("Verifying inspector container exists")
			Expect(helper.verifyInspectorContainerExists()).To(BeTrue(), "Container should exist")

			By("Waiting a moment before sending interrupt to ensure inspector is fully ready")
			time.Sleep(5 * time.Second)

			By("Sending interrupt signal to inspector")
			err = helper.interruptInspector()
			Expect(err).ToNot(HaveOccurred(), "Should be able to interrupt inspector")

			By("Waiting for inspector process to exit")
			err = helper.waitForInspectorExit(10 * time.Second)
			Expect(err).ToNot(HaveOccurred(), "Inspector should exit after interrupt")

			By("Verifying inspector container is cleaned up")
			Expect(helper.verifyInspectorContainerGone()).To(BeTrue(), "Container should be cleaned up")

			By("Verifying UI is no longer accessible")
			Expect(helper.verifyUIInaccessible()).To(BeTrue(), "UI should be inaccessible")

			By("Verifying no orphaned containers remain")
			stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
			Expect(stdout).ToNot(ContainSubstring("inspector"), "No inspector should remain")
		})

		It("should handle multiple start-stop cycles without conflicts", func() {
			mcpServerName := fmt.Sprintf("mcp-multicycles-%d", GinkgoRandomSeed())
			helper := newInspectorAutoCleanupTestHelper(config, mcpServerName)

			defer helper.cleanup()

			By("Starting an MCP server for inspector to connect to")
			helper.setupMCPServer()

			// Test multiple cycles
			for i := range 3 {
				By(fmt.Sprintf("Starting inspector cycle %d", i+1))

				By("Starting inspector command")
				helper.startInspector()

				By("Waiting for inspector UI to be ready")
				err := helper.waitForInspectorReady(30 * time.Second)
				Expect(err).ToNot(HaveOccurred(), "Inspector should become ready")

				By("Verifying inspector container exists")
				Expect(helper.verifyInspectorContainerExists()).To(BeTrue(), "Container should exist")

				By("Sending interrupt signal to inspector")
				err = helper.interruptInspector()
				Expect(err).ToNot(HaveOccurred(), "Should be able to interrupt inspector")

				By("Waiting for inspector process to exit")
				err = helper.waitForInspectorExit(10 * time.Second)
				Expect(err).ToNot(HaveOccurred(), "Inspector should exit after interrupt")

				By("Verifying inspector container is cleaned up")
				Expect(helper.verifyInspectorContainerGone()).To(BeTrue(), "Container should be cleaned up")

				By("Verifying UI is no longer accessible")
				Expect(helper.verifyUIInaccessible()).To(BeTrue(), "UI should be inaccessible")

				// Small delay between cycles
				time.Sleep(2 * time.Second)
			}

			By("Verifying no orphaned containers remain after all cycles")
			stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
			Expect(stdout).ToNot(ContainSubstring("inspector"), "No inspector should remain")
		})
	})

	Context("Interruption scenarios", func() {
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

	Context("Container lifecycle verification", func() {
		It("should properly transition container states during lifecycle", func() {
			mcpServerName := fmt.Sprintf("mcp-lifecycle-%d", GinkgoRandomSeed())
			helper := newInspectorAutoCleanupTestHelper(config, mcpServerName)

			defer helper.cleanup()

			By("Verifying no inspector container exists initially")
			Expect(helper.verifyInspectorContainerGone()).To(BeTrue(), "No inspector container should exist initially")

			By("Starting an MCP server for inspector to connect to")
			helper.setupMCPServer()

			By("Starting inspector command")
			helper.startInspector()

			By("Waiting for inspector container to be created and running")
			// Give container time to be created
			time.Sleep(5 * time.Second)
			Expect(helper.verifyInspectorContainerExists()).To(BeTrue(), "Container should exist after start")

			By("Waiting for inspector UI to be ready")
			err := helper.waitForInspectorReady(45 * time.Second)
			Expect(err).ToNot(HaveOccurred(), "Inspector should become ready")

			By("Verifying inspector UI is accessible")
			Expect(helper.verifyUIAccessible()).To(BeTrue(), "UI should be accessible")

			By("Sending interrupt signal to inspector")
			err = helper.interruptInspector()
			Expect(err).ToNot(HaveOccurred(), "Should be able to interrupt inspector")

			By("Waiting for inspector process to exit")
			err = helper.waitForInspectorExit(10 * time.Second)
			Expect(err).ToNot(HaveOccurred(), "Inspector should exit after interrupt")

			By("Verifying inspector container is completely removed (not just stopped)")
			Expect(helper.verifyInspectorContainerGone()).To(BeTrue(), "Container should be completely removed")

			By("Verifying UI is no longer accessible")
			Expect(helper.verifyUIInaccessible()).To(BeTrue(), "UI should be inaccessible")
		})
	})

	Context("Error handling and edge cases", func() {
		It("should handle cleanup when container creation fails", func() {
			// Test scenario where container creation might fail
			// This is more complex to simulate, so we'll test the graceful handling
			mcpServerName := fmt.Sprintf("mcp-errorcase-%d", GinkgoRandomSeed())
			helper := newInspectorAutoCleanupTestHelper(config, mcpServerName)

			defer helper.cleanup()

			By("Starting an MCP server for inspector to connect to")
			helper.setupMCPServer()

			By("Starting inspector command and immediately interrupting")
			helper.startInspector()

			// Interrupt very quickly to potentially catch container during creation
			time.Sleep(100 * time.Millisecond)
			err := helper.interruptInspector()
			Expect(err).ToNot(HaveOccurred(), "Should be able to interrupt inspector")

			By("Waiting for inspector process to exit")
			err = helper.waitForInspectorExit(15 * time.Second)
			// We expect this to succeed even if container creation was in progress
			Expect(err).ToNot(HaveOccurred(), "Inspector should handle interrupt gracefully")

			By("Verifying no orphaned containers remain")
			Expect(helper.verifyInspectorContainerGone()).To(BeTrue(), "No inspector containers should remain")
		})
	})
})
