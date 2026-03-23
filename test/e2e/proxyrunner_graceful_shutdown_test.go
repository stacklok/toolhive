// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("ProxyRunner Graceful Shutdown", Label("proxyrunner", "graceful-shutdown", "e2e"), Serial, func() {
	var (
		config         *e2e.TestConfig
		serverName     string
		tempDir        string
		proxyRunnerCmd *exec.Cmd
		exportedConfig *runner.RunConfig
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available for testing")

		_, err = exec.LookPath(proxyRunnerBinaryPath())
		Expect(err).ToNot(HaveOccurred(),
			"thv-proxyrunner binary not found; set THV_PROXYRUNNER_BINARY or add it to PATH")

		serverName = fmt.Sprintf("proxyrunner-shutdown-%d", GinkgoRandomSeed())
		tempDir = GinkgoT().TempDir()

		By("Starting an OSV MCP server via thv to obtain a valid runconfig.json")
		e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()

		err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
		Expect(err).ToNot(HaveOccurred(), "OSV server should start within 60s")

		By("Exporting the run configuration to tempDir/runconfig.json")
		configPath := filepath.Join(tempDir, "runconfig.json")
		e2e.NewTHVCommand(config, "export", serverName, configPath).ExpectSuccess()

		configData, err := os.ReadFile(configPath)
		Expect(err).ToNot(HaveOccurred(), "exported runconfig.json should be readable")

		exportedConfig = &runner.RunConfig{}
		Expect(json.Unmarshal(configData, exportedConfig)).To(Succeed(), "exported config should be valid JSON")
		Expect(exportedConfig.Image).ToNot(BeEmpty(), "exported config should have a non-empty image")
		Expect(exportedConfig.Port).To(BeNumerically(">", 0), "exported config should have a valid port")

		By("Stopping the thv-managed server to free the container name and port")
		Expect(e2e.StopAndRemoveMCPServer(config, serverName)).To(Succeed())

		By("Waiting for the port to be released")
		Eventually(func() bool {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", exportedConfig.Port), 1*time.Second)
			if err != nil {
				return true // port is free
			}
			conn.Close()
			return false
		}, 15*time.Second, 500*time.Millisecond).Should(BeTrue(), "port should be released after server removal")
	})

	AfterEach(func() {
		if proxyRunnerCmd != nil && proxyRunnerCmd.Process != nil {
			_ = proxyRunnerCmd.Process.Kill()
			_, _ = proxyRunnerCmd.Process.Wait()
			proxyRunnerCmd = nil
		}
		// Best-effort: remove any container left behind by the proxyrunner
		_ = e2e.StopAndRemoveMCPServer(config, serverName)
	})

	It("exits cleanly when SIGTERM is received after the proxy is ready", func() {
		By("Starting thv-proxyrunner with the exported runconfig.json")
		proxyRunnerCmd = exec.Command( //nolint:gosec // Intentional for e2e testing
			proxyRunnerBinaryPath(), "run", exportedConfig.Image,
		)
		proxyRunnerCmd.Dir = tempDir // runconfig.json is picked up from the working directory
		proxyRunnerCmd.Stdout = GinkgoWriter
		proxyRunnerCmd.Stderr = GinkgoWriter
		Expect(proxyRunnerCmd.Start()).To(Succeed(), "thv-proxyrunner should start")

		proxyAddr := fmt.Sprintf("localhost:%d", exportedConfig.Port)

		By(fmt.Sprintf("Waiting for the proxy to accept connections on %s", proxyAddr))
		Eventually(func() error {
			conn, err := net.DialTimeout("tcp", proxyAddr, 1*time.Second)
			if err != nil {
				return err
			}
			conn.Close()
			return nil
		}, 90*time.Second, 2*time.Second).Should(Succeed(), "proxy port should become reachable within 90s")

		By("Sending SIGTERM to thv-proxyrunner (simulating Kubernetes pod termination)")
		Expect(proxyRunnerCmd.Process.Signal(syscall.SIGTERM)).To(Succeed())

		By("Asserting thv-proxyrunner exits within 30 seconds of SIGTERM")
		done := make(chan error, 1)
		go func() { done <- proxyRunnerCmd.Wait() }()

		var waitErr error
		select {
		case waitErr = <-done:
		case <-time.After(30 * time.Second):
			Fail("thv-proxyrunner did not exit after SIGTERM within 30s")
		}
		proxyRunnerCmd = nil

		Expect(waitErr).ToNot(HaveOccurred(),
			"thv-proxyrunner should exit with code 0 on graceful shutdown, not be killed by signal")

		By("Asserting the proxy port is no longer listening after shutdown")
		Eventually(func() bool {
			conn, err := net.DialTimeout("tcp", proxyAddr, 1*time.Second)
			if err != nil {
				return true // port is closed
			}
			conn.Close()
			return false
		}, 10*time.Second, 500*time.Millisecond).Should(BeTrue(), "proxy port should stop listening after graceful shutdown")
	})
})

// proxyRunnerBinaryPath returns the path to the thv-proxyrunner binary.
// It checks THV_PROXYRUNNER_BINARY first, then falls back to "thv-proxyrunner" in PATH.
func proxyRunnerBinaryPath() string {
	if b := os.Getenv("THV_PROXYRUNNER_BINARY"); b != "" {
		return b
	}
	return "thv-proxyrunner"
}
