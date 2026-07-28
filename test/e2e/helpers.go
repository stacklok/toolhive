// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package e2e provides end-to-end testing utilities for ToolHive.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck // Standard practice for Ginkgo
	. "github.com/onsi/gomega"    //nolint:staticcheck // Standard practice for Gomega

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
)

// GenerateUniqueServerName creates a unique server name for tests
func GenerateUniqueServerName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), GinkgoRandomSeed())
}

// TestConfig holds configuration for e2e tests
type TestConfig struct {
	THVBinary    string
	TestTimeout  time.Duration
	CleanupAfter bool
}

// NewTestConfig creates a new test configuration with defaults
func NewTestConfig() *TestConfig {
	// Look for thv binary in PATH or use a configurable path
	thvBinary := os.Getenv("THV_BINARY")
	if thvBinary == "" {
		thvBinary = "thv" // Assume it's in PATH
	}

	return &TestConfig{
		THVBinary:    thvBinary,
		TestTimeout:  10 * time.Minute,
		CleanupAfter: true,
	}
}

// THVCommand represents a ToolHive CLI command execution
type THVCommand struct {
	config *TestConfig
	args   []string
	env    []string
	dir    string
	stdin  string

	// cmd is the underlying exec.Cmd once a Run method is called.
	cmd *exec.Cmd
}

// NewTHVCommand creates a new ToolHive command
func NewTHVCommand(config *TestConfig, args ...string) *THVCommand {
	return &THVCommand{
		config: config,
		args:   args,
		env:    os.Environ(),
		dir:    "",
	}
}

// WithEnv adds environment variables to the command
func (c *THVCommand) WithEnv(env ...string) *THVCommand {
	c.env = append(c.env, env...)
	return c
}

// WithStdin sets the stdin input for the command
func (c *THVCommand) WithStdin(stdin string) *THVCommand {
	c.stdin = stdin
	return c
}

// Run executes the ToolHive command and returns stdout, stderr, and error
func (c *THVCommand) Run() (string, string, error) {
	return c.RunWithTimeout(c.config.TestTimeout)
}

// RunWithTimeout executes the ToolHive command with a specific timeout
func (c *THVCommand) RunWithTimeout(timeout time.Duration) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c.cmd = exec.CommandContext(ctx, c.config.THVBinary, c.args...) //nolint:gosec // Intentional for e2e testing
	c.cmd.Env = c.env
	if c.dir != "" {
		c.cmd.Dir = c.dir
	}
	if c.stdin != "" {
		c.cmd.Stdin = strings.NewReader(c.stdin)
	}

	var stdout, stderr strings.Builder
	c.cmd.Stdout = &stdout
	c.cmd.Stderr = &stderr

	err := c.cmd.Run()

	return stdout.String(), stderr.String(), err
}

// Interrupt interrupts the command and does NOT wait for it to exit.
func (c *THVCommand) Interrupt() error {
	return c.cmd.Process.Signal(syscall.SIGINT)
}

// ExpectSuccess runs the command and expects it to succeed
func (c *THVCommand) ExpectSuccess() (string, string) {
	stdout, stderr, err := c.Run()
	if err != nil {
		// Log the command that failed for debugging
		GinkgoWriter.Printf("Command failed: %s %v\nError: %v\nStdout: %s\nStderr: %s\n",
			c.config.THVBinary, c.args, err, stdout, stderr)
	}
	ExpectWithOffset(1, err).ToNot(HaveOccurred(),
		fmt.Sprintf("Command failed: %v\nStdout: %s\nStderr: %s", err, stdout, stderr))
	return stdout, stderr
}

// ExpectFailure runs the command and expects it to fail
func (c *THVCommand) ExpectFailure() (string, string, error) {
	stdout, stderr, err := c.Run()
	ExpectWithOffset(1, err).To(HaveOccurred(),
		fmt.Sprintf("Command should have failed but succeeded\nStdout: %s\nStderr: %s", stdout, stderr))
	return stdout, stderr, err
}

// ServerReadyTimeoutEnv overrides how long specs wait for a freshly started
// workload to reach the running state.
const ServerReadyTimeoutEnv = "TOOLHIVE_E2E_SERVER_READY_TIMEOUT"

// defaultServerReadyTimeout is the readiness budget used when
// ServerReadyTimeoutEnv is unset.
//
// Reaching `running` covers more than starting a container: the detached worker
// also polls the workload's own MCP endpoint until initialize succeeds before it
// flips the status (pkg/runner.waitForInitializeSuccess, which the product is
// willing to wait 5 minutes for). Specs that start several workloads at once
// need a budget well above container-start time, otherwise they fail while the
// workload is still legitimately starting on a loaded CI runner.
const defaultServerReadyTimeout = 2 * time.Minute

// ServerReadyTimeout returns the budget a spec should give a freshly started
// workload to reach the running state. Set ServerReadyTimeoutEnv to any Go
// duration (e.g. "3m") to raise it on a slow machine.
func ServerReadyTimeout() time.Duration {
	raw := os.Getenv(ServerReadyTimeoutEnv)
	if raw == "" {
		return defaultServerReadyTimeout
	}

	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout <= 0 {
		Fail(fmt.Sprintf("%s must be a positive Go duration (e.g. \"3m\"), got %q", ServerReadyTimeoutEnv, raw))
	}
	return timeout
}

// WaitForMCPServer waits for an MCP server to be running.
//
// The status is read from the named workload's own record in `thv list --all
// --format json`. Substring-matching the text table instead would accept "this
// name appears somewhere and some workload is running", which can return while
// the named workload is still starting. A timeout reports the last status
// observed and dumps the server state, so a CI log shows whether the workload
// was still starting, had errored, or never appeared at all.
func WaitForMCPServer(config *TestConfig, serverName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastObserved string
	for {
		workload, err := findWorkload(config, serverName)
		switch {
		case err != nil:
			// `thv list` can fail transiently while other workloads are
			// starting, so keep polling and surface the failure only if the
			// timeout expires.
			lastObserved = fmt.Sprintf("thv list failed: %v", err)
		case workload == nil:
			lastObserved = "not listed"
		case workload.Status == rt.WorkloadStatusRunning:
			return nil
		default:
			lastObserved = fmt.Sprintf("status %q", workload.Status)
			if workload.StatusContext != "" {
				lastObserved += fmt.Sprintf(" (%s)", workload.StatusContext)
			}
		}

		select {
		case <-ctx.Done():
			DebugServerState(config, serverName)
			return fmt.Errorf("timeout waiting for MCP server %s to be running after %v; last observed: %s",
				serverName, timeout, lastObserved)
		case <-ticker.C:
		}
	}
}

// ExpectMCPServersRunning waits for each named workload to reach the running
// state within ServerReadyTimeout, and fails naming the workload that did not.
//
// All workloads are polled concurrently so that the total wait is bounded by
// the slowest workload rather than the sum of all waits.
func ExpectMCPServersRunning(config *TestConfig, serverNames ...string) {
	errs := make([]error, len(serverNames))
	var wg sync.WaitGroup
	for i, name := range serverNames {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			errs[i] = WaitForMCPServer(config, name, ServerReadyTimeout())
		}(i, name)
	}
	wg.Wait()
	for i, serverName := range serverNames {
		ExpectWithOffset(1, errs[i]).ToNot(HaveOccurred(),
			"workload %s (of %v) should reach the running state", serverName, serverNames)
	}
}

// IsServerRunning checks if an MCP server is running
func IsServerRunning(config *TestConfig, serverName string) bool {
	workload, err := findWorkload(config, serverName)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "should be able to list workloads")
	return workload != nil && workload.Status == rt.WorkloadStatusRunning
}

// listTimeout bounds a single `thv list` call. It is well above how long a list
// takes even on a loaded runner, and well below any readiness budget: callers
// poll, so a list that overruns is retried rather than fatal. Run() would apply
// TestConfig.TestTimeout instead, letting one hung list outlast the wait it is
// being polled for.
const listTimeout = 30 * time.Second

// FindWorkload returns the named workload's record as reported by `thv list`, or
// nil if it is not listed. newCmd builds each list invocation, so a spec that
// runs thv under an isolated config/home/data env passes its own builder and
// observes the workloads created in that state rather than the real config.
//
// --all is required so that workloads which have not reached running yet
// (starting, error) are visible rather than filtered out.
func FindWorkload(newCmd func(args ...string) *THVCommand, serverName string) (*core.Workload, error) {
	stdout, stderr, err := newCmd("list", "--all", "--format", "json").RunWithTimeout(listTimeout)
	if err != nil {
		return nil, fmt.Errorf("thv list: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	var workloads []core.Workload
	if err := json.Unmarshal([]byte(stdout), &workloads); err != nil {
		return nil, fmt.Errorf("failed to parse thv list output %q: %w", stdout, err)
	}

	for i := range workloads {
		if workloads[i].Name == serverName {
			return &workloads[i], nil
		}
	}
	return nil, nil
}

// StopAndRemoveMCPServer stops and removes an MCP server
// This function is designed for cleanup and tolerates servers that don't exist
func StopAndRemoveMCPServer(config *TestConfig, serverName string) error {
	// Try to stop the server first (ignore errors as server might not exist)
	_, _, _ = NewTHVCommand(config, "stop", serverName).Run()

	// Then remove it
	_, stderr, err := NewTHVCommand(config, "rm", serverName).Run()
	if err != nil {
		// In cleanup scenarios, it's okay if the container doesn't exist
		if strings.Contains(stderr, "not found") {
			return nil
		}
		return err
	}

	return nil
}

// GetMCPServerURL gets the URL for an MCP server
func GetMCPServerURL(config *TestConfig, serverName string) (string, error) {
	stdout, stderr, err := NewTHVCommand(config, "list").Run()
	if err != nil {
		GinkgoWriter.Printf("Failed to list servers: %v\nStdout: %s\nStderr: %s\n", err, stdout, stderr)
		return "", fmt.Errorf("failed to list servers: %w", err)
	}

	GinkgoWriter.Printf("thv list output:\n%s\n", stdout)

	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		if strings.Contains(line, serverName) {
			GinkgoWriter.Printf("Found server line: %s\n", line)
			// Parse the URL from the list output
			// This is a simplified parser - you might need to adjust based on actual output format
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://") {
					GinkgoWriter.Printf("Found URL: %s\n", part)
					return part, nil
				}
			}
		}
	}

	return "", fmt.Errorf("could not find URL for server %s in output: %s", serverName, stdout)
}

// GetServerLogs gets the logs for a server to help with debugging
func GetServerLogs(config *TestConfig, serverName string) (string, error) {
	stdout, stderr, err := NewTHVCommand(config, "logs", serverName).Run()
	if err != nil {
		return "", fmt.Errorf("failed to get logs for %s: %w (stderr: %s)", serverName, err, stderr)
	}
	return stdout, nil
}

// DebugServerState prints debugging information about a server
func DebugServerState(config *TestConfig, serverName string) {
	GinkgoWriter.Printf("=== Debugging server state for %s ===\n", serverName)

	// Get list output
	stdout, stderr, err := NewTHVCommand(config, "list").Run()
	GinkgoWriter.Printf("thv list output:\nStdout: %s\nStderr: %s\nError: %v\n", stdout, stderr, err)

	// Get logs
	logs, err := GetServerLogs(config, serverName)
	if err != nil {
		GinkgoWriter.Printf("Failed to get logs: %v\n", err)
	} else {
		GinkgoWriter.Printf("Server logs:\n%s\n", logs)
	}

	// The container log alone has repeatedly been useless for readiness
	// timeouts: a workload stuck in `starting` or flipped to `error` fails
	// inside the detached supervisor (transport start, readiness probe,
	// restart loop), whose output goes to the proxy log file — not to the
	// container. Dump it too so a CI failure names the actual blocker.
	proxyLogs, stderr, err := NewTHVCommand(config, "logs", serverName, "--proxy").Run()
	if err != nil {
		GinkgoWriter.Printf("Failed to get proxy logs: %v\nStderr: %s\n", err, stderr)
	} else {
		GinkgoWriter.Printf("Proxy logs:\n%s\n", proxyLogs)
	}

	GinkgoWriter.Printf("=== End debugging for %s ===\n", serverName)
}

// CheckTHVBinaryAvailable checks if the thv binary is available
func CheckTHVBinaryAvailable(config *TestConfig) error {
	_, _, err := NewTHVCommand(config, "--help").Run()
	if err != nil {
		return fmt.Errorf("thv binary not available at %s: %w", config.THVBinary, err)
	}
	return nil
}

// StartLongRunningTHVCommand starts a long-running ToolHive command and returns the process
func StartLongRunningTHVCommand(config *TestConfig, args ...string) *exec.Cmd {
	cmd := exec.Command(config.THVBinary, args...) //nolint:gosec // Intentional for e2e testing
	cmd.Env = os.Environ()

	// Capture stdout and stderr for debugging
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter

	err := cmd.Start()
	ExpectWithOffset(1, err).ToNot(HaveOccurred(),
		fmt.Sprintf("Failed to start long-running command: %s %v", config.THVBinary, args))

	return cmd
}

// StartDockerCommand starts a docker command with proper environment setup and returns the command
func StartDockerCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", args...) //nolint:gosec // Intentional for e2e testing
	cmd.Env = os.Environ()
	return cmd
}

// WaitForWorkloadUnhealthy waits for a workload to be marked as unhealthy
func WaitForWorkloadUnhealthy(config *TestConfig, serverName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for workload %s to be marked as unhealthy", serverName)
		case <-ticker.C:
			stdout, _, err := NewTHVCommand(config, "list", "--all").Run()
			if err != nil {
				continue
			}

			// Check if the server is listed and marked as unhealthy
			lines := strings.Split(stdout, "\n")
			for _, line := range lines {
				if strings.Contains(line, serverName) && strings.Contains(line, "unhealthy") {
					return nil
				}
			}
		}
	}
}

// RemoveGroup removes a group by name
func RemoveGroup(config *TestConfig, groupName string) error {
	stdout, stderr, err := NewTHVCommand(config, "group", "rm", groupName).
		WithStdin("y\n").
		Run()

	if err != nil {
		// In cleanup scenarios, it's okay if the group doesn't exist
		combinedOutput := stdout + stderr
		if strings.Contains(combinedOutput, "does not exist") {
			return nil
		}
		return err
	}
	return nil
}

// CreateAndTrackGroup creates a group and tracks it for cleanup
func CreateAndTrackGroup(config *TestConfig, groupName string, createdGroups *[]string) {
	NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
	*createdGroups = append(*createdGroups, groupName)
}

// CreateFakeBrowserDir writes stub open/xdg-open scripts into a "fakebin"
// subdirectory of tempDir. The stubs GET the auth URL without following the
// redirect, so the OIDC mock server receives the request and populates
// authRequestChan while CompleteAuthRequest drives the callback.
// Returns the directory so callers can prepend it to PATH.
func CreateFakeBrowserDir(tempDir string) (string, error) {
	dir := filepath.Join(tempDir, "fakebin")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	// curl: -sf = silent + fail-on-HTTP-error; no -L so 302 is not followed.
	// wget: --max-redirect=0 prevents following the 302 to the callback URL,
	//       which would race with CompleteAuthRequest and make the test flaky.
	// If neither tool is available the script exits 1 with a clear message so
	// the test fails fast instead of hanging until WaitForAuthRequest times out.
	script := []byte("#!/bin/sh\n" +
		"if command -v curl >/dev/null 2>&1; then\n" +
		"  curl -sf \"$1\" >/dev/null 2>&1\n" +
		"elif command -v wget >/dev/null 2>&1; then\n" +
		"  wget -q --max-redirect=0 \"$1\" -O /dev/null 2>&1\n" +
		"else\n" +
		"  echo 'fake-browser: neither curl nor wget found' >&2; exit 1\n" +
		"fi\n")
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(dir, name), script, 0750); err != nil { //nolint:gosec // shell scripts must be executable
			return "", err
		}
	}
	return dir, nil
}

// findWorkload returns the named workload's record as reported by `thv list`,
// or nil if it is not listed.
func findWorkload(config *TestConfig, serverName string) (*core.Workload, error) {
	return FindWorkload(func(args ...string) *THVCommand {
		return NewTHVCommand(config, args...)
	}, serverName)
}
