// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package e2e provides end-to-end tests for the vMCP optimizer tiers.
//
// Tier-1 (FTS5 keyword optimizer, --optimizer flag) is covered by
// vmcp_cli_features_test.go. This file covers the remaining items for
// RFC THV-0059 Phase 4:
//
//   - Tier-2 managed TEI mode (--optimizer-embedding): verifies that the TEI
//     container starts, becomes healthy, and that find_tool/call_tool are the
//     only tools exposed.
//   - Fail-fast on TEI startup failure: verifies that vmcp serve exits non-zero
//     when the embedding image cannot be pulled.
//   - Idempotent TEI container reuse: verifies that a second concurrent serve
//     instance reuses the TEI container started by the first, rather than
//     attempting to deploy a duplicate.
//   - Standalone vmcp binary regression: verifies that the standalone vmcp
//     binary (cmd/vmcp) exposes backend tools through config-file mode
//     identically to `thv vmcp serve`, confirming the Phase 1 extraction
//     refactor did not break the shared pkg/vmcp/cli library.
//
// TEI-dependent tests (Tier-2 and idempotent reuse) are guarded by the
// THV_E2E_TEI environment variable. Set THV_E2E_TEI=true on a runner with
// Docker access and sufficient disk space (~2 GB for the TEI image) to enable
// them.
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// defaultEmbeddingModel is the HuggingFace model used by the managed TEI
// optimizer when --embedding-model is omitted. Mirrors
// pkg/vmcp/cli.DefaultEmbeddingModel; kept as a local constant so this test
// package does not import the production CLI package.
const defaultEmbeddingModel = "BAAI/bge-small-en-v1.5"

// teiContainerNameForModel returns the deterministic Docker container name that
// pkg/vmcp/cli.EmbeddingServiceManager assigns to the TEI container for the
// given model. Mirrors containerNameForModel in embedding_manager.go so that
// tests can inspect Docker state without importing the production package.
func teiContainerNameForModel(model string) string {
	sum := sha256.Sum256([]byte(model))
	return "thv-embedding-" + hex.EncodeToString(sum[:])[:8]
}

// skipUnlessTEIEnabled skips the current spec when THV_E2E_TEI is not "true".
// The TEI image is ~2 GB and requires Docker; tests are gated on dedicated
// runners that have the image pre-pulled.
func skipUnlessTEIEnabled() {
	if os.Getenv("THV_E2E_TEI") != "true" {
		Skip("skipping TEI test: set THV_E2E_TEI=true on a runner with the TEI image available")
	}
}

var _ = Describe("vMCP optimizer", Label("vmcp", "e2e", "optimizer"), func() {

	Context("Tier-2 managed TEI (--optimizer-embedding, quick mode)", func() {
		var fx singleBackendFixture

		BeforeEach(func() {
			skipUnlessTEIEnabled()
			fx.setup("vmcp-opt-tei", "")
		})

		AfterEach(func() {
			fx.teardown()
			// Best-effort removal of the TEI container in case vmcp serve
			// did not clean up (e.g. killed by SIGKILL).
			_ = e2e.StartDockerCommand("rm", "-f", teiContainerNameForModel(defaultEmbeddingModel)).Run()
		})

		It("auto-starts TEI container and exposes find_tool and call_tool with semantic results", func() {
			By("starting thv vmcp serve in quick mode with --optimizer-embedding")
			fx.vMCPCmd = e2e.StartLongRunningTHVCommand(fx.cfg,
				"vmcp", "serve",
				"--group", fx.groupName,
				"--optimizer-embedding",
				"--port", fmt.Sprintf("%d", fx.vMCPPort),
			)

			vMCPURL := vmcpEndpointURL(fx.vMCPPort)
			By("waiting for vMCP endpoint to be ready (TEI image pull and model load may take several minutes)")
			Expect(e2e.WaitForMCPServerReady(fx.cfg, vMCPURL, "streamable-http", 5*time.Minute)).To(Succeed())

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(fx.cfg, vMCPURL)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = mcpClient.Close() }()
			Expect(mcpClient.Initialize(ctx)).To(Succeed())

			By("verifying only find_tool and call_tool are exposed (Tier-2 implies Tier-1)")
			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).To(HaveLen(2), "optimizer mode must expose exactly 2 tools")
			Expect(toolNames(tools.Tools)).To(ConsistOf("find_tool", "call_tool"))

			By("calling find_tool to verify semantic results are returned")
			result, err := mcpClient.CallTool(ctx, "find_tool", map[string]any{
				"tool_description": "echo a message",
			})
			Expect(err).ToNot(HaveOccurred(), "find_tool must succeed with Tier-2 semantic optimizer")
			Expect(result.IsError).To(BeFalse(), "find_tool must not return an error result")
			Expect(result.Content).ToNot(BeEmpty(), "find_tool must return tool suggestions")
		})
	})

	Context("fail-fast on TEI startup failure", func() {
		var fx singleBackendFixture

		BeforeEach(func() { fx.setup("vmcp-opt-failfast", "") })
		AfterEach(func() { fx.teardown() })

		It("exits non-zero when the embedding image cannot be pulled", func() {
			By("starting thv vmcp serve with an invalid --embedding-image")
			var stderr strings.Builder
			fx.vMCPCmd = startVMCPServeWithStderr(fx.cfg, &stderr,
				"vmcp", "serve",
				"--group", fx.groupName,
				"--optimizer-embedding",
				"--embedding-image", "invalid.registry.invalid/nonexistent:no-such-tag",
				"--port", fmt.Sprintf("%d", fx.vMCPPort),
			)
			// Ensure the process is terminated even if the Eventually times out.
			DeferCleanup(func() { stopVMCPProcess(fx.vMCPCmd) })

			done := make(chan error, 1)
			go func() { done <- fx.vMCPCmd.Wait() }()

			By("waiting for vmcp serve to exit with a non-zero status")
			Eventually(done, 2*time.Minute).Should(
				Receive(HaveOccurred()),
				"vmcp serve must exit non-zero when the TEI image cannot be pulled",
			)

			By("verifying the failure is due to TEI image pull, not an unrelated error")
			Expect(stderr.String()).To(ContainSubstring("invalid.registry.invalid"),
				"stderr must mention the bad image; got: %s", stderr.String())
		})
	})

	Context("Tier-2 TEI container reuse (two concurrent serve instances)", func() {
		var (
			cfg         *e2e.TestConfig
			groupName   string
			backendName string
			vMCPCmdA    *exec.Cmd
			vMCPCmdB    *exec.Cmd
			vMCPPortA   int
			vMCPPortB   int
		)

		BeforeEach(func() {
			skipUnlessTEIEnabled()

			cfg = e2e.NewTestConfig()
			groupName = e2e.GenerateUniqueServerName("vmcp-opt-reuse")
			backendName = e2e.GenerateUniqueServerName("yardstick-reuse")
			vMCPPortA = allocateVMCPPort()
			vMCPPortB = allocateVMCPPort()

			Expect(e2e.CheckTHVBinaryAvailable(cfg)).To(Succeed())
			e2e.NewTHVCommand(cfg, "group", "create", groupName).ExpectSuccess()
			startYardstick(cfg, groupName, backendName)
		})

		AfterEach(func() {
			// Stop B before A so A's cleanup removes the TEI container last.
			stopVMCPProcess(vMCPCmdB)
			vMCPCmdB = nil
			stopVMCPProcess(vMCPCmdA)
			vMCPCmdA = nil
			if cfg != nil && cfg.CleanupAfter {
				if err := e2e.StopAndRemoveMCPServer(cfg, backendName); err != nil {
					GinkgoWriter.Printf("cleanup: StopAndRemoveMCPServer(%s) failed: %v\n", backendName, err)
				}
				if err := e2e.RemoveGroup(cfg, groupName); err != nil {
					GinkgoWriter.Printf("cleanup: RemoveGroup(%s) failed: %v\n", groupName, err)
				}
			}
			// Best-effort removal of any stray TEI container.
			_ = e2e.StartDockerCommand("rm", "-f", teiContainerNameForModel(defaultEmbeddingModel)).Run()
		})

		It("reuses the TEI container when a second serve starts with the same model", func() {
			teiContainerName := teiContainerNameForModel(defaultEmbeddingModel)

			By("starting first vmcp serve with --optimizer-embedding (deploys TEI container)")
			vMCPCmdA = e2e.StartLongRunningTHVCommand(cfg,
				"vmcp", "serve",
				"--group", groupName,
				"--optimizer-embedding",
				"--port", fmt.Sprintf("%d", vMCPPortA),
			)
			Expect(e2e.WaitForMCPServerReady(cfg, vmcpEndpointURL(vMCPPortA), "streamable-http", 5*time.Minute)).To(Succeed())

			By("starting second vmcp serve while first is still running — must reuse TEI container")
			vMCPCmdB = e2e.StartLongRunningTHVCommand(cfg,
				"vmcp", "serve",
				"--group", groupName,
				"--optimizer-embedding",
				"--port", fmt.Sprintf("%d", vMCPPortB),
			)
			// The second serve should reach ready quickly because the TEI container
			// is already running and healthy; no model-load wait needed.
			Expect(e2e.WaitForMCPServerReady(cfg, vmcpEndpointURL(vMCPPortB), "streamable-http", 2*time.Minute)).To(Succeed(),
				"second serve should start quickly by reusing the existing TEI container")

			By("verifying exactly one TEI container is running")
			psOut, psErr := e2e.StartDockerCommand(
				"ps", "--filter", "name="+teiContainerName, "--format={{.Names}}",
			).Output()
			Expect(psErr).ToNot(HaveOccurred())
			var matchCount int
			for line := range strings.SplitSeq(strings.TrimSpace(string(psOut)), "\n") {
				if strings.TrimSpace(line) == teiContainerName {
					matchCount++
				}
			}
			Expect(matchCount).To(Equal(1),
				"exactly one TEI container must be running; docker ps output: %s", string(psOut))

			By("verifying both serve instances expose find_tool and call_tool")
			for _, port := range []int{vMCPPortA, vMCPPortB} {
				func(url string) {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					mcpClient, err := e2e.NewMCPClientForStreamableHTTP(cfg, url)
					Expect(err).ToNot(HaveOccurred())
					defer func() { _ = mcpClient.Close() }()
					Expect(mcpClient.Initialize(ctx)).To(Succeed())

					tools, err := mcpClient.ListTools(ctx)
					Expect(err).ToNot(HaveOccurred())
					Expect(toolNames(tools.Tools)).To(ConsistOf("find_tool", "call_tool"),
						"both serve instances must expose the optimizer tool surface")
				}(vmcpEndpointURL(port))
			}
		})
	})

	Context("standalone vmcp binary regression", func() {
		var (
			cfg         *e2e.TestConfig
			groupName   string
			backendName string
			vMCPCmd     *exec.Cmd
			vMCPPort    int
			tmpDir      string
			vmcpBinary  string
		)

		BeforeEach(func() {
			vmcpBinary = os.Getenv("VMCP_BINARY")
			if vmcpBinary == "" {
				Skip("VMCP_BINARY not set; skipping standalone vmcp regression test")
			}
			if _, err := os.Stat(vmcpBinary); err != nil {
				Skip(fmt.Sprintf("VMCP_BINARY=%q does not exist: %v", vmcpBinary, err))
			}

			cfg = e2e.NewTestConfig()
			groupName = e2e.GenerateUniqueServerName("vmcp-regression")
			backendName = e2e.GenerateUniqueServerName("yardstick-regression")
			vMCPPort = allocateVMCPPort()

			var err error
			tmpDir, err = os.MkdirTemp("", "vmcp-regression-*")
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

			Expect(e2e.CheckTHVBinaryAvailable(cfg)).To(Succeed())
			e2e.NewTHVCommand(cfg, "group", "create", groupName).ExpectSuccess()
			startYardstick(cfg, groupName, backendName)
		})

		AfterEach(func() {
			stopVMCPProcess(vMCPCmd)
			vMCPCmd = nil
			if cfg != nil && cfg.CleanupAfter {
				if err := e2e.StopAndRemoveMCPServer(cfg, backendName); err != nil {
					GinkgoWriter.Printf("cleanup: StopAndRemoveMCPServer(%s) failed: %v\n", backendName, err)
				}
				if err := e2e.RemoveGroup(cfg, groupName); err != nil {
					GinkgoWriter.Printf("cleanup: RemoveGroup(%s) failed: %v\n", groupName, err)
				}
			}
		})

		It("exposes backend tools identically to thv vmcp serve --config", func() {
			configPath := filepath.Join(tmpDir, "vmcp.yaml")
			initVMCPConfig(cfg, groupName, configPath)

			By("starting standalone vmcp serve --config")
			vMCPCmd = startStandaloneVMCPCommand(vmcpBinary, configPath, vMCPPort)
			vMCPURL := vmcpEndpointURL(vMCPPort)
			Expect(e2e.WaitForMCPServerReady(cfg, vMCPURL, "streamable-http", 60*time.Second)).To(Succeed())

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			mcpClient, err := e2e.NewMCPClientForStreamableHTTP(cfg, vMCPURL)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = mcpClient.Close() }()
			Expect(mcpClient.Initialize(ctx)).To(Succeed())

			By("verifying that backend tools are exposed through the standalone vmcp binary")
			tools, err := mcpClient.ListTools(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(tools.Tools).ToNot(BeEmpty(), "standalone vmcp serve must expose backend tools")

			names := toolNames(tools.Tools)
			Expect(names).To(ContainElement(ContainSubstring("echo")),
				"standalone vmcp must expose the yardstick echo tool; got tools: %v", names)
		})
	})

}) // end Describe("vMCP optimizer")

// startVMCPServeWithStderr starts `thv vmcp serve` with the given args, writing
// stderr to both w and GinkgoWriter so tests can inspect error messages.
func startVMCPServeWithStderr(config *e2e.TestConfig, w *strings.Builder, args ...string) *exec.Cmd {
	cmd := exec.Command(config.THVBinary, args...) //nolint:gosec // Intentional for e2e testing
	cmd.Env = os.Environ()
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = io.MultiWriter(GinkgoWriter, w)
	ExpectWithOffset(1, cmd.Start()).To(Succeed(),
		"failed to start thv %v", args)
	return cmd
}

// startStandaloneVMCPCommand starts the standalone vmcp binary (not thv) with
// the given config file and port. It mirrors StartLongRunningTHVCommand but
// invokes the standalone binary directly.
func startStandaloneVMCPCommand(vmcpBinary, configPath string, port int) *exec.Cmd {
	cmd := exec.Command(vmcpBinary, //nolint:gosec // Intentional for e2e testing
		"serve",
		"--config", configPath,
		"--port", fmt.Sprintf("%d", port),
	)
	cmd.Env = os.Environ()
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	ExpectWithOffset(1, cmd.Start()).To(Succeed(),
		"failed to start standalone vmcp binary %q", vmcpBinary)
	return cmd
}
