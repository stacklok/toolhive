// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("vMCP CLI", Label("vmcp", "e2e"), func() {
	var (
		config      *e2e.TestConfig
		groupName   string
		backendName string
		vMCPCmd     *exec.Cmd
		vMCPPort    int
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		groupName = e2e.GenerateUniqueServerName("vmcp-e2e-group")
		backendName = e2e.GenerateUniqueServerName("vmcp-e2e-backend")
		vMCPCmd = nil
		vMCPPort = allocateVMCPPort()

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		stopVMCPProcess(vMCPCmd)
		vMCPCmd = nil

		if config.CleanupAfter {
			if err := e2e.StopAndRemoveMCPServer(config, backendName); err != nil {
				GinkgoWriter.Printf("cleanup: StopAndRemoveMCPServer(%s) failed: %v\n", backendName, err)
			}
			if err := e2e.RemoveGroup(config, groupName); err != nil {
				GinkgoWriter.Printf("cleanup: RemoveGroup(%s) failed: %v\n", groupName, err)
			}
		}
	})

	// -------------------------------------------------------------------------
	// Quick mode: thv vmcp serve --group <name>
	// -------------------------------------------------------------------------
	Context("quick mode (--group, no --config)", func() {
		BeforeEach(func() {
			By("creating group and starting backend workload")
			e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
			startYardstick(config, groupName, backendName)
		})

		It("starts vMCP and exposes backend tools via MCP", func() {
			By("starting thv vmcp serve in quick mode")
			vMCPCmd = e2e.StartLongRunningTHVCommand(config,
				"vmcp", "serve",
				"--group", groupName,
				"--port", fmt.Sprintf("%d", vMCPPort),
			)
			vMCPURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", vMCPPort)
			By("waiting for vMCP endpoint to be ready")
			err := e2e.WaitForMCPServerReady(config, vMCPURL, "streamable-http", 60*time.Second)
			Expect(err).ToNot(HaveOccurred(), "vMCP server should become ready")

			By("connecting MCP client and listing tools")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, vMCPURL)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = mcpClient.Close() }()

			Expect(mcpClient.Initialize(ctx)).To(Succeed())
			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "vMCP should expose at least one tool from the backend")
		})

		It("binds only to 127.0.0.1", func() {
			By("starting thv vmcp serve in quick mode")
			vMCPCmd = e2e.StartLongRunningTHVCommand(config,
				"vmcp", "serve",
				"--group", groupName,
				"--port", fmt.Sprintf("%d", vMCPPort),
			)
			vMCPURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", vMCPPort)
			By("waiting for vMCP endpoint to be ready on loopback")
			err := e2e.WaitForMCPServerReady(config, vMCPURL, "streamable-http", 60*time.Second)
			Expect(err).ToNot(HaveOccurred(), "vMCP server should be reachable on 127.0.0.1")

			By("verifying the listener is bound only to 127.0.0.1")
			// Use OS-level socket inspection to confirm the bind address rather than
			// dialing, which is ambiguous (0.0.0.0 dials succeed on Linux loopback).
			portStr := fmt.Sprintf("%d", vMCPPort)
			switch runtime.GOOS {
			case "darwin":
				// lsof -nP -i TCP:<port> -sTCP:LISTEN prints the listen socket.
				// A 127.0.0.1 listener shows "127.0.0.1:<port>"; a wildcard shows "*:<port>".
				if _, lookErr := exec.LookPath("lsof"); lookErr != nil {
					Skip("lsof not available; skipping bind-address verification")
				}
				out, err := exec.Command("lsof", "-nP",
					fmt.Sprintf("-iTCP:%s", portStr), "-sTCP:LISTEN").Output()
				Expect(err).ToNot(HaveOccurred(), "lsof must succeed")
				Expect(string(out)).To(ContainSubstring("127.0.0.1:"+portStr),
					"listener must be bound to 127.0.0.1")
				Expect(string(out)).ToNot(ContainSubstring("*:"+portStr),
					"listener must not be bound to all interfaces")
			case "linux":
				// ss -tlnH 'sport = :<port>' prints one row per listen socket.
				// Local address column is "127.0.0.1:<port>" for loopback or "0.0.0.0:<port>" for wildcard.
				if _, lookErr := exec.LookPath("ss"); lookErr != nil {
					Skip("ss not available; skipping bind-address verification")
				}
				out, err := exec.Command("ss", "-tlnH",
					fmt.Sprintf("sport = :%s", portStr)).Output()
				Expect(err).ToNot(HaveOccurred(), "ss must succeed")
				Expect(string(out)).To(ContainSubstring("127.0.0.1:"+portStr),
					"listener must be bound to 127.0.0.1")
				Expect(string(out)).ToNot(ContainSubstring("0.0.0.0:"+portStr),
					"listener must not be bound to all interfaces")
			default:
				Skip(fmt.Sprintf("bind-address verification not supported on %s", runtime.GOOS))
			}
		})
	})

	// -------------------------------------------------------------------------
	// Config-file mode: thv vmcp init → validate → serve --config
	// -------------------------------------------------------------------------
	Context("config-file mode (init → validate → serve --config)", func() {
		var configFilePath string

		BeforeEach(func() {
			By("creating group and starting backend workload")
			e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
			startYardstick(config, groupName, backendName)

			tmpDir, err := os.MkdirTemp("", "vmcp-e2e-config-*")
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
			configFilePath = filepath.Join(tmpDir, "vmcp.yaml")
		})

		It("init generates a non-empty valid config file", func() {
			By("running thv vmcp init")
			e2e.NewTHVCommand(config,
				"vmcp", "init",
				"--group", groupName,
				"--config", configFilePath,
			).ExpectSuccess()

			By("checking the generated file is non-empty")
			info, err := os.Stat(configFilePath)
			Expect(err).ToNot(HaveOccurred(), "config file should exist")
			Expect(info.Size()).To(BeNumerically(">", 0), "config file should be non-empty")
		})

		It("validate accepts the config generated by init", func() {
			By("running thv vmcp init")
			e2e.NewTHVCommand(config,
				"vmcp", "init",
				"--group", groupName,
				"--config", configFilePath,
			).ExpectSuccess()

			By("running thv vmcp validate")
			e2e.NewTHVCommand(config,
				"vmcp", "validate",
				"--config", configFilePath,
			).ExpectSuccess()
		})

		It("serve --config starts vMCP and exposes backend tools", func() {
			By("generating config with thv vmcp init")
			e2e.NewTHVCommand(config,
				"vmcp", "init",
				"--group", groupName,
				"--config", configFilePath,
			).ExpectSuccess()

			By("validating the generated config")
			e2e.NewTHVCommand(config,
				"vmcp", "validate",
				"--config", configFilePath,
			).ExpectSuccess()

			By("starting thv vmcp serve --config")
			vMCPCmd = e2e.StartLongRunningTHVCommand(config,
				"vmcp", "serve",
				"--config", configFilePath,
				"--port", fmt.Sprintf("%d", vMCPPort),
			)
			vMCPURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", vMCPPort)
			By("waiting for vMCP endpoint to be ready")
			err := e2e.WaitForMCPServerReady(config, vMCPURL, "streamable-http", 60*time.Second)
			Expect(err).ToNot(HaveOccurred(), "vMCP server should become ready")

			By("connecting MCP client and listing tools")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(config, vMCPURL)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = mcpClient.Close() }()

			Expect(mcpClient.Initialize(ctx)).To(Succeed())
			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "vMCP should expose at least one tool from the backend")
		})
	})

	// -------------------------------------------------------------------------
	// Error cases
	// -------------------------------------------------------------------------
	Context("error cases", func() {
		It("exits non-zero when neither --config nor --group is provided", func() {
			By("running thv vmcp serve with no flags")
			stdout, stderr, err := e2e.NewTHVCommand(config, "vmcp", "serve").
				RunWithTimeout(10 * time.Second)
			Expect(err).To(HaveOccurred(), "serve should fail without --config or --group")
			combined := stdout + stderr
			Expect(combined).To(ContainSubstring("either --config or --group must be specified"),
				"error message should guide the user toward --config or --group")
		})

		It("validate exits non-zero for a non-existent config file", func() {
			By("running thv vmcp validate with a non-existent path")
			_, _, err := e2e.NewTHVCommand(config,
				"vmcp", "validate",
				"--config", "/nonexistent/path/vmcp.yaml",
			).RunWithTimeout(10 * time.Second)
			Expect(err).To(HaveOccurred(), "validate should fail for a non-existent config file")
		})
	})
})
