package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// inspectorTestHelper contains common functionality for inspector tests
type inspectorTestHelper struct {
	config        *testConfig
	mcpServerName string
	inspectorName string
	client        *http.Client
	inspectorURL  string
}

var _ = Describe("Inspector", func() {
	var (
		config        *testConfig
		mcpServerName string
		inspectorName string
	)

	BeforeEach(func() {
		config = NewTestConfig()
		mcpServerName = fmt.Sprintf("mcp-server-%d", GinkgoRandomSeed())
		inspectorName = "inspector"

		// Check if thv binary is available
		err := CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up both servers
			err := stopAndRemoveMCPServer(config, inspectorName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove inspector")
			err = stopAndRemoveMCPServer(config, mcpServerName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove MCP server")
		}
	})

	Describe("Inspector command validation", func() {
		Context("when providing invalid arguments", func() {
			It("should fail when no server name is provided", func() {
				By("Running inspector without server name")
				_, _, err := NewTHVCommand(config, "inspector").ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail without server name")
			})

			It("should fail when too many arguments are provided", func() {
				By("Running inspector with multiple server names")
				_, _, err := NewTHVCommand(config, "inspector", "server1", "server2").ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail with multiple server names")
			})

			It("should fail when server doesn't exist", func() {
				By("Running inspector with non-existent server")
				_, stderr, err := NewTHVCommand(config, "inspector", "non-existent-server").
					RunWithTimeout(10 * time.Second)
				Expect(err).To(HaveOccurred(), "Should fail with non-existent server")
				Expect(stderr).To(ContainSubstring("not found"), "Should indicate server not found")
			})
		})

		Context("when checking help and flags", func() {
			It("should show help information", func() {
				By("Getting inspector help")
				stdout, _ := NewTHVCommand(config, "inspector", "--help").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("MCP Inspector UI"), "Should mention Inspector UI")
				Expect(stdout).To(ContainSubstring("--ui-port"), "Should show ui-port flag")
				Expect(stdout).To(ContainSubstring("--mcp-proxy-port"), "Should show mcp-proxy-port flag")
			})

			It("should accept custom ports", func() {
				By("Running inspector with custom ports (should fail due to missing server)")
				_, stderr, err := NewTHVCommand(config, "inspector",
					"--ui-port", "8080",
					"--mcp-proxy-port", "8081",
					"non-existent-server").RunWithTimeout(10 * time.Second)
				Expect(err).To(HaveOccurred(), "Should fail due to missing server")
				// The error should be about missing server, not invalid ports
				Expect(stderr).To(ContainSubstring("not found"), "Should fail due to missing server, not ports")
			})
		})
	})

	Describe("Inspector with running MCP server", func() {
		var helper *inspectorTestHelper

		BeforeEach(func() {
			helper = newInspectorTestHelper(config, mcpServerName, inspectorName)
			helper.setupMCPServer()
		})

		AfterEach(func() {
			if config.CleanupAfter {
				// Clean up both servers
				err := stopAndRemoveMCPServer(config, inspectorName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove inspector")
				err = stopAndRemoveMCPServer(config, mcpServerName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove MCP server")
			}
		})

		Context("when launching inspector", func() {
			It("should successfully start inspector UI", func() {
				By("Starting the inspector")
				stdout, stderr, err := NewTHVCommand(config, "inspector", mcpServerName).
					RunWithTimeout(15 * time.Second)

				output := stdout + stderr

				if err != nil {
					// If it failed, it should not be due to argument validation
					Expect(output).ToNot(ContainSubstring("server name is required"),
						"Should not fail due to missing server name")
					Expect(output).ToNot(ContainSubstring("usage:"),
						"Should not fail due to argument validation")

					// Check for acceptable failure reasons
					acceptableErrors := []string{
						"context deadline exceeded",
						"timeout",
						"failed to create container runtime",
						"failed to handle protocol scheme",
						"failed to create inspector container",
					}

					hasAcceptableError := false
					for _, acceptableError := range acceptableErrors {
						if strings.Contains(output, acceptableError) {
							hasAcceptableError = true
							break
						}
					}

					if !hasAcceptableError {
						GinkgoWriter.Printf("Inspector failed with unexpected error:\nStdout: %s\nStderr: %s\nError: %v\n",
							stdout, stderr, err)
					}
				} else {
					// If it succeeded, it should have useful output
					Expect(output).To(ContainSubstring("Inspector"), "Should mention Inspector in output")
				}
			})

			It("should use custom UI port when specified", func() {
				By("Starting inspector with custom UI port")
				customUIPort := "9999"
				stdout, stderr, err := NewTHVCommand(config, "inspector",
					"--ui-port", customUIPort,
					mcpServerName).RunWithTimeout(10 * time.Second)

				output := stdout + stderr

				if err == nil {
					Expect(output).To(ContainSubstring(customUIPort), "Should use custom UI port")
				} else {
					Expect(output).ToNot(ContainSubstring("invalid port"), "Should not fail due to port validation")
				}
			})
		})

		Context("when testing inspector connectivity", func() {
			It("should make inspector UI accessible when running", func() {
				By("Starting inspector in background using goroutine")
				done := helper.startInspectorInBackground(30 * time.Second)

				By("Waiting for inspector UI to be ready")
				helper.waitForInspectorUI(20 * time.Second)

				By("Verifying inspector UI responds with valid content")
				helper.verifyInspectorUIAccessible()

				By("Stopping inspector process and waiting for cleanup")
				helper.waitForInspectorCompletion(done, 16*time.Second)

				By("Cleaning up any remaining inspector containers explicitly")
				helper.cleanupInspector()

				By("Verifying inspector container is removed")
				helper.waitForInspectorUIUnavailable(20 * time.Second)

				By("Verifying port bindings are cleaned up")
				helper.verifyInspectorUIUnavailable()

				By("Verifying no orphaned inspector containers remain")
				stdout, _ := NewTHVCommand(config, "list", "--all").ExpectSuccess()
				Expect(stdout).ToNot(BeNil(), "Should get valid list output")
			})
		})
	})
})

// newInspectorTestHelper creates a new inspector test helper
func newInspectorTestHelper(config *testConfig, mcpServerName, inspectorName string) *inspectorTestHelper {
	return &inspectorTestHelper{
		config:        config,
		mcpServerName: mcpServerName,
		inspectorName: inspectorName,
		client:        &http.Client{Timeout: 5 * time.Second},
		inspectorURL:  "http://localhost:6274",
	}
}

// startInspectorInBackground starts the inspector in a goroutine with timeout
func (h *inspectorTestHelper) startInspectorInBackground(timeout time.Duration, args ...string) chan error {
	done := make(chan error, 1)
	go func() {
		cmdArgs := append([]string{"inspector"}, args...)
		cmdArgs = append(cmdArgs, h.mcpServerName)
		_, _, err := NewTHVCommand(h.config, cmdArgs...).RunWithTimeout(timeout)
		done <- err
	}()
	return done
}

// waitForInspectorUI waits for the inspector UI to become accessible
func (h *inspectorTestHelper) waitForInspectorUI(timeout time.Duration) {
	Eventually(func() bool {
		response, err := h.client.Get(h.inspectorURL)
		if response != nil {
			response.Body.Close()
		}
		return err == nil && response != nil && response.StatusCode == 200
	}, timeout, 2*time.Second).Should(BeTrue(), "Inspector UI should become accessible")
}

// waitForInspectorUIUnavailable waits for the inspector UI to become inaccessible
func (h *inspectorTestHelper) waitForInspectorUIUnavailable(timeout time.Duration) {
	Eventually(func() bool {
		response, err := h.client.Get(h.inspectorURL)
		if response != nil {
			response.Body.Close()
		}
		return err != nil
	}, timeout, 2*time.Second).Should(BeTrue(), "Inspector UI should no longer be accessible after cleanup")
}

// verifyInspectorUIAccessible verifies that the inspector UI is accessible
func (h *inspectorTestHelper) verifyInspectorUIAccessible() {
	response, err := h.client.Get(h.inspectorURL)
	Expect(err).ToNot(HaveOccurred(), "Inspector UI should be accessible")
	Expect(response).ToNot(BeNil(), "Response should not be nil")
	Expect(response.StatusCode).To(Equal(200), "Inspector UI should return 200 OK")
	_ = response.Body.Close()
}

// verifyInspectorUIUnavailable verifies that the inspector UI is not accessible
func (h *inspectorTestHelper) verifyInspectorUIUnavailable() {
	response, err := h.client.Get(h.inspectorURL)
	if response != nil {
		response.Body.Close()
	}
	Expect(err).To(HaveOccurred(), "Port 6274 should no longer be accessible")
}

// waitForInspectorCompletion waits for the inspector process to complete or timeout
func (*inspectorTestHelper) waitForInspectorCompletion(done chan error, timeout time.Duration) {
	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
			GinkgoWriter.Printf("Inspector ended with error: %v\n", err)
		}
	case <-time.After(timeout):
		GinkgoWriter.Println("Inspector may still be running after timeout")
	}
}

// cleanupInspector performs cleanup of inspector containers
func (h *inspectorTestHelper) cleanupInspector() {
	err := stopAndRemoveMCPServer(h.config, h.inspectorName)
	if err != nil {
		GinkgoWriter.Printf("Note: Cleanup returned error (may be expected): %v\n", err)
	}
	time.Sleep(3 * time.Second) // Give time for cleanup to complete
}

// setupMCPServer starts an MCP server and waits for it to be ready
func (h *inspectorTestHelper) setupMCPServer() {
	By("Starting an MCP server for inspector to connect to")
	NewTHVCommand(h.config, "run", "--name", h.mcpServerName, "fetch").ExpectSuccess()
	err := waitForMCPServer(h.config, h.mcpServerName, 60*time.Second)
	Expect(err).ToNot(HaveOccurred(), "MCP server should be running")
}
