// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/workloads/upgrade"
	"github.com/stacklok/toolhive/test/e2e"
)

const (
	// rawOSVImage is the concrete image the bundled "osv" registry entry resolves
	// to. Running it by its raw reference (rather than by a registry name)
	// produces a working MCP server whose RunConfig has no RegistryServerName,
	// which is exactly the input the "not-registry-sourced" path needs.
	rawOSVImage = "ghcr.io/stackloklabs/osv-mcp/server:0.1.3"

	// osvImageLow and osvImageHigh are two real, pullable osv-mcp releases. The
	// upgrade-flow fixtures advertise these tags so the checker reports an
	// available upgrade from low to high. Both tags run identically (only a minor
	// version bump), and osv declares no required env vars or secrets, so
	// `upgrade apply --yes` runs non-interactively without prompting.
	osvImageLow  = "ghcr.io/stackloklabs/osv-mcp/server:0.1.1"
	osvImageHigh = "ghcr.io/stackloklabs/osv-mcp/server:0.1.3"

	// upgradeRegistryServerName is the server name the fixtures expose (a bare
	// "name": "osv-upgrade-test", so the recorded RegistryServerName matches it
	// exactly). `thv run` is invoked with this name so RegistryServerName is set.
	upgradeRegistryServerName = "osv-upgrade-test"
)

// upgradeCheckResults mirrors the JSON emitted by `thv upgrade check --format
// json`, which is an array of upgrade.CheckResult. We decode into the real type
// so the test breaks if the wire shape changes.
type upgradeCheckResults []upgrade.CheckResult

var _ = Describe("Upgrade Command", Label("core", "upgrade", "e2e"), func() {
	var (
		config     *e2e.TestConfig
		serverName string

		// tempHome / tempData isolate this spec's ToolHive config and workload
		// state from the developer's / CI runner's real ToolHive directories.
		// `thv config set-registry` writes to $XDG_CONFIG_HOME/toolhive, and
		// workload state lives under $XDG_DATA_HOME/toolhive; routing every thv
		// invocation (run, check, apply, export, set/unset-registry, and cleanup)
		// through thvCmd keeps both off the real config and consistent across the
		// run -> check -> apply -> export -> cleanup sequence so they all see the
		// same workload. The container runtime (Docker/Podman socket) is not
		// HOME-dependent, so an isolated HOME does not affect it; PATH and the rest
		// of the environment are inherited unchanged.
		tempHome string
		tempData string

		// thvCmd builds a THVCommand bound to the isolated config/home/data dirs.
		thvCmd func(args ...string) *e2e.THVCommand
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("upgrade-test")
		tempHome = GinkgoT().TempDir()
		tempData = GinkgoT().TempDir()

		thvCmd = func(args ...string) *e2e.THVCommand {
			return e2e.NewTHVCommand(config, args...).
				WithEnv(
					"XDG_CONFIG_HOME="+tempHome,
					"HOME="+tempHome,
					"XDG_DATA_HOME="+tempData,
				)
		}

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Stop and remove the workload using the SAME isolated env so cleanup
			// targets the workload created by this spec. Tolerate a missing
			// workload (a spec may have failed before creating it).
			_, _, _ = thvCmd("stop", serverName).Run()
			_, _, _ = thvCmd("rm", serverName).Run()
		}
		// The registry config lives under the discarded temp dirs, so there is
		// nothing to restore; the real ToolHive config is never touched.
	})

	// Negative / cheap path: a workload created from a raw image reference is not
	// registry-sourced, so no upgrade can ever be determined for it. This needs
	// no registry fixture and is the safety-net coverage.
	Describe("Checking a workload that is not registry-sourced", func() {
		Context("when the workload was run from a raw image reference", func() {
			It("should report not-registry-sourced in text output", func() {
				By("Running an MCP server from a raw image reference")
				thvCmd("run", "--name", serverName, rawOSVImage).ExpectSuccess()

				By("Waiting for the server to be running")
				waitForIsolatedMCPServer(thvCmd, serverName, 60*time.Second)

				By("Checking the workload for an available upgrade")
				stdout, _ := thvCmd("upgrade", "check", serverName).ExpectSuccess()

				By("Verifying the report says the workload is not registry-sourced")
				Expect(stdout).To(ContainSubstring(serverName), "Report should name the workload")
				Expect(stdout).To(ContainSubstring(string(upgrade.StatusNotRegistrySourced)),
					"Report should show the not-registry-sourced status")
			})

			It("should emit parseable JSON with the expected fields", func() {
				By("Running an MCP server from a raw image reference")
				thvCmd("run", "--name", serverName, rawOSVImage).ExpectSuccess()

				By("Waiting for the server to be running")
				waitForIsolatedMCPServer(thvCmd, serverName, 60*time.Second)

				By("Checking the workload for an available upgrade in JSON format")
				stdout, _ := thvCmd("upgrade", "check", serverName, "--format", "json").ExpectSuccess()

				By("Verifying the JSON output is valid and contains the expected fields")
				var results upgradeCheckResults
				err := json.Unmarshal([]byte(stdout), &results)
				Expect(err).ToNot(HaveOccurred(), "Output should be valid JSON")
				Expect(results).To(HaveLen(1), "JSON should contain exactly one result")

				result := results[0]
				Expect(result.WorkloadName).To(Equal(serverName), "JSON should name the workload")
				Expect(result.Status).To(Equal(upgrade.StatusNotRegistrySourced),
					"JSON should show the not-registry-sourced status")
				Expect(result.CurrentImage).To(Equal(rawOSVImage),
					"JSON should record the raw image the workload is running")
				Expect(result.RegistryServer).To(BeEmpty(),
					"A raw-image workload should not have a registry server name")
				Expect(result.CandidateImage).To(BeEmpty(),
					"There should be no candidate image when no upgrade can be determined")
			})
		})
	})

	// Full upgrade flow: a registry-sourced workload whose registry advertises a
	// newer tag is upgraded in place, and we verify it runs on the new image with
	// its prior configuration preserved.
	//
	// Determinism: the two fixtures (testdata/upgrade/registry-{low,high}.json)
	// advertise tags 0.1.1 and 0.1.3 of ghcr.io/stackloklabs/osv-mcp/server, both
	// real, pullable releases that run identically. The fixtures are in the
	// upstream MCP-registry format that `thv config set-registry` requires for a
	// local file (the local provider rejects the legacy ToolHive-native format),
	// and they mirror the bundled osv entry's transport (streamable-http) and
	// internal target port (8080) exactly so the workload comes up the same way
	// `thv run osv` would. osv declares no required env vars, so `upgrade apply
	// --yes` never prompts. This runs by default in CI.
	Describe("Applying an available upgrade to a registry-sourced workload", func() {
		var (
			tempDir     string
			fixtureLow  string
			fixtureHigh string
		)

		BeforeEach(func() {
			tempDir = GinkgoT().TempDir()

			// set-registry persists the path and resolves it later, so it must be
			// absolute regardless of the working directory at resolution time.
			var err error
			fixtureLow, err = filepath.Abs(filepath.Join("testdata", "upgrade", "registry-low.json"))
			Expect(err).ToNot(HaveOccurred())
			fixtureHigh, err = filepath.Abs(filepath.Join("testdata", "upgrade", "registry-high.json"))
			Expect(err).ToNot(HaveOccurred())
			Expect(fixtureLow).To(BeAnExistingFile(), "low-tag registry fixture should exist")
			Expect(fixtureHigh).To(BeAnExistingFile(), "high-tag registry fixture should exist")
		})

		It("should pull the candidate, recreate the workload, and preserve config", func() {
			By("Pointing thv at the older-tag registry fixture")
			thvCmd("config", "set-registry", fixtureLow).ExpectSuccess()

			By("Running the server by its registry name with an environment variable")
			thvCmd("run", "--name", serverName, "--env", "FOO=bar", upgradeRegistryServerName).ExpectSuccess()

			By("Waiting for the server to be running")
			waitForIsolatedMCPServer(thvCmd, serverName, 120*time.Second)

			By("Confirming the workload is registry-sourced and up to date against the older fixture")
			stdout, _ := thvCmd("upgrade", "check", serverName, "--format", "json").ExpectSuccess()
			var before upgradeCheckResults
			Expect(json.Unmarshal([]byte(stdout), &before)).To(Succeed(), "Output should be valid JSON")
			Expect(before).To(HaveLen(1))
			Expect(before[0].RegistryServer).To(Equal(upgradeRegistryServerName),
				"Running by registry name should record the registry server name")
			Expect(before[0].CurrentImage).To(Equal(osvImageLow),
				"The workload should be running the lower-tag image")
			Expect(before[0].Status).To(Equal(upgrade.StatusUpToDate),
				"No upgrade should be available against the older-tag fixture")

			By("Repointing thv at the registry fixture advertising the newer tag")
			thvCmd("config", "set-registry", fixtureHigh).ExpectSuccess()

			By("Checking that an upgrade is now available")
			stdout, _ = thvCmd("upgrade", "check", serverName, "--format", "json").ExpectSuccess()
			var avail upgradeCheckResults
			Expect(json.Unmarshal([]byte(stdout), &avail)).To(Succeed(), "Output should be valid JSON")
			Expect(avail).To(HaveLen(1))
			Expect(avail[0].Status).To(Equal(upgrade.StatusUpgradeAvailable),
				"An upgrade should be available after repointing to the newer tag")
			Expect(avail[0].CandidateImage).To(Equal(osvImageHigh),
				"The candidate image should carry the newer tag")

			By("Applying the upgrade non-interactively")
			thvCmd("upgrade", "apply", serverName, "--yes").ExpectSuccess()

			By("Waiting for the upgraded workload to be running again")
			waitForIsolatedMCPServer(thvCmd, serverName, 120*time.Second)

			By("Verifying the recorded image carries the newer tag and config is preserved")
			exportPath := filepath.Join(tempDir, "upgraded-export.json")
			thvCmd("export", serverName, exportPath).ExpectSuccess()

			fileContent, err := os.ReadFile(exportPath)
			Expect(err).ToNot(HaveOccurred())

			var runConfig runner.RunConfig
			Expect(json.Unmarshal(fileContent, &runConfig)).To(Succeed(), "Export should be valid JSON")
			Expect(runConfig.Image).To(Equal(osvImageHigh),
				"The upgraded workload should record the newer image tag")
			Expect(runConfig.EnvVars).To(HaveKeyWithValue("FOO", "bar"),
				"The upgrade should preserve the workload's environment variables")
		})
	})
})

// waitForIsolatedMCPServer polls `thv list` (through the supplied isolated-env
// command builder) until the named workload reports running, or fails the spec
// on timeout. It mirrors e2e.WaitForMCPServer but runs every poll under the same
// isolated config/home/data env as the rest of the spec, so it observes the
// workload created in that isolated state rather than the real ToolHive config.
func waitForIsolatedMCPServer(thvCmd func(args ...string) *e2e.THVCommand, serverName string, timeout time.Duration) {
	GinkgoHelper()
	Eventually(func() bool {
		stdout, _, err := thvCmd("list").Run()
		if err != nil {
			return false
		}
		return strings.Contains(stdout, serverName) && strings.Contains(stdout, "running")
	}, timeout, 1*time.Second).Should(BeTrue(),
		"workload %q should be running within %s", serverName, timeout)
}
