// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// launchYardstickLegacyOnPort starts a non-stateless (Legacy-only) yardstick
// backend on the given port in the given group. Deliberately structured as a
// near-duplicate of launchYardstickModernOnPort below rather than reusing the
// shared launchYardstickOnPort (vmcp_cli_helpers_test.go), so that everything
// differing between the two backends this suite stands up is visible side by
// side. Both explicitly set BACKEND_MODE=echo rather than one relying on
// yardstick-server's default, leaving STATELESS as the only difference.
func launchYardstickLegacyOnPort(config *e2e.TestConfig, groupName, backendName string, port int) {
	portStr := strconv.Itoa(port)
	e2e.NewTHVCommand(config,
		"run", images.YardstickServerImage,
		"--name", backendName,
		"--group", groupName,
		"--transport", "streamable-http",
		"--isolate-network=false",
		"--target-port", portStr,
		"--env", "TRANSPORT=streamable-http",
		"--env", "BACKEND_MODE=echo",
		"--", "-port", portStr, "-transport", "streamable-http",
	).ExpectSuccess()
}

// startEraBackendOnPort launches a backend via launch and waits for it to
// become ready, retrying once with a freshly allocated port if the container
// lost a race binding its host port. allocateVMCPPort only probes that a port
// is free at call time and closes its own listener immediately -- the actual
// bind happens later, asynchronously, inside the container Docker starts for
// the detached `thv run` process, so another workload's container can grab
// the same ephemeral port in between (observed as "address already in use").
func startEraBackendOnPort(config *e2e.TestConfig, backendName string, port int, launch func(port int)) {
	launch(port)
	err := e2e.WaitForMCPServer(config, backendName, 120*time.Second)
	if err != nil && strings.Contains(err.Error(), "address already in use") {
		GinkgoWriter.Printf("%s lost a port-bind race on %d, retrying with a fresh port: %v\n", backendName, port, err)
		e2e.NewTHVCommand(config, "rm", backendName).ExpectSuccess()
		port = allocateVMCPPort()
		launch(port)
		err = e2e.WaitForMCPServer(config, backendName, 120*time.Second)
	}
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("yardstick backend %q should become running", backendName))
}

// startYardstickLegacyOnPort runs launchYardstickLegacyOnPort and waits for
// the backend to become ready.
func startYardstickLegacyOnPort(config *e2e.TestConfig, groupName, backendName string, port int) {
	startEraBackendOnPort(config, backendName, port, func(p int) {
		launchYardstickLegacyOnPort(config, groupName, backendName, p)
	})
}

// launchYardstickModernOnPort starts a stateless (Modern-capable) yardstick
// backend on the given port in the given group. It differs from
// launchYardstickLegacyOnPort above only in STATELESS=true, which is what makes
// yardstick's own discover response advertise 2026-07-28 support (confirmed at
// test/e2e/thv-operator/acceptance_tests/dual_era_k8s_test.go:151-155).
//
// Both backends run the same image, which since #6004 is a single go-sdk v1.7
// build serving both eras: in session mode it negotiates down and vMCP
// classifies it Legacy from server/discover's supportedVersions. That is why
// the suite's BeforeEach asserts each backend's negotiated revision via
// /status rather than trusting the env. One forgotten STATELESS would
// otherwise leave both backends Legacy, and every cell here would still pass
// while testing the Legacy backend edge twice.
func launchYardstickModernOnPort(config *e2e.TestConfig, groupName, backendName string, port int) {
	portStr := strconv.Itoa(port)
	e2e.NewTHVCommand(config,
		"run", images.YardstickServerImage,
		"--name", backendName,
		"--group", groupName,
		"--transport", "streamable-http",
		"--isolate-network=false",
		"--target-port", portStr,
		"--env", "TRANSPORT=streamable-http",
		"--env", "STATELESS=true",
		"--env", "BACKEND_MODE=echo",
		"--", "-port", portStr, "-transport", "streamable-http",
	).ExpectSuccess()
}

// startYardstickModernOnPort runs launchYardstickModernOnPort and waits for
// the backend to become ready.
func startYardstickModernOnPort(config *e2e.TestConfig, groupName, backendName string, port int) {
	startEraBackendOnPort(config, backendName, port, func(p int) {
		launchYardstickModernOnPort(config, groupName, backendName, p)
	})
}

// healthCheckConfigYAML is appended to a `thv vmcp init`-generated config file
// to turn on health monitoring. This is required for /status to ever report
// mcp_revision: pkg/vmcp/server/status.go's BackendStatus.MCPRevision doc
// notes it "is only observable through the live health state", and
// pkg/vmcp/cli/serve.go only builds a health.MonitorConfig when
// Operational.FailureHandling.HealthCheckInterval > 0. Quick mode (--group)
// generates a config with no Operational section at all and has no CLI flag
// to add one (see generateQuickModeConfig), so this suite uses config-file
// mode instead, purely to make mcp_revision observable.
const healthCheckConfigYAML = `
operational:
  failureHandling:
    healthCheckInterval: 1s
    unhealthyThreshold: 1
`

// appendHealthCheckConfig appends healthCheckConfigYAML to the config file at
// path (a fresh top-level YAML key; thv vmcp init never emits an
// "operational:" section, so there is nothing to overwrite).
func appendHealthCheckConfig(path string) {
	GinkgoHelper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	Expect(err).ToNot(HaveOccurred())
	defer f.Close()
	_, err = f.WriteString(healthCheckConfigYAML)
	Expect(err).ToNot(HaveOccurred())
}

// startDualEraVMCP starts `thv vmcp serve`, using --config configPath when
// configPath is non-empty, else quick mode (--group groupName). vMCP serves both
// MCP revisions whenever the capability gate is open (modern_gate.go), and these
// specs configure no blockers, so this only assembles the arguments —
// it no longer overrides the inherited environment, and so delegates the process
// start to e2e.StartLongRunningTHVCommand.
func startDualEraVMCP(config *e2e.TestConfig, groupName, configPath string, port int) *exec.Cmd {
	GinkgoHelper()
	args := []string{"vmcp", "serve", "--port", strconv.Itoa(port)}
	if configPath != "" {
		args = append(args, "--config", configPath)
	} else {
		args = append(args, "--group", groupName)
	}
	return e2e.StartLongRunningTHVCommand(config, args...)
}

// vmcpStatusResponse decodes the subset of pkg/vmcp/server.StatusResponse
// this suite needs. Decoded independently rather than by importing
// pkg/vmcp/server: that package is a server implementation detail a
// black-box e2e client should not depend on.
type vmcpStatusResponse struct {
	Backends []struct {
		Name        string `json:"name"`
		MCPRevision string `json:"mcp_revision"`
	} `json:"backends"`
}

// vmcpStatusURL returns the /status endpoint URL for a vMCP serve process
// listening on the given port.
func vmcpStatusURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/status", port)
}

// fetchVMCPStatus GETs statusURL and decodes it as a vmcpStatusResponse.
func fetchVMCPStatus(statusURL string) (vmcpStatusResponse, error) {
	httpClient := http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Get(statusURL) //nolint:gosec // test-controlled URL
	if err != nil {
		return vmcpStatusResponse{}, err
	}
	defer resp.Body.Close()

	var out vmcpStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return vmcpStatusResponse{}, err
	}
	return out, nil
}

// waitForBackendRevision polls statusURL until it reports backendName's
// mcp_revision as wantRevision, or fails the spec after timeout.
//
// This is the only positive proof in this suite that vMCP classified a
// backend's MCP edge correctly. Every other assertion here (tool call
// succeeds, echoes the right value) is satisfied identically whether vMCP
// classified the "Modern" backend as Modern or silently fell back to Legacy
// -- both backends run the same yardstick echo tool, so a misclassification
// would not change any response body. Without this check the 2x2 matrix this
// suite claims to cover could silently collapse to 1x2.
func waitForBackendRevision(statusURL, backendName, wantRevision string) {
	GinkgoHelper()
	Eventually(func() (string, error) {
		status, err := fetchVMCPStatus(statusURL)
		if err != nil {
			return "", err
		}
		for _, b := range status.Backends {
			if b.Name == backendName {
				return b.MCPRevision, nil
			}
		}
		return "", nil
	}, 30*time.Second, 500*time.Millisecond).Should(Equal(wantRevision),
		"vMCP never classified backend %q as %q via /status", backendName, wantRevision)
}
