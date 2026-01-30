// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/desktop"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Desktop Validation", Label("core", "desktop", "e2e"), func() {
	var (
		config      *e2e.TestConfig
		tempHomeDir string
		origHome    string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		// Create a temporary home directory for testing
		tempHomeDir, err = os.MkdirTemp("", "thv-desktop-e2e-*")
		Expect(err).ToNot(HaveOccurred())

		// Save original HOME and set the temp one
		origHome = os.Getenv("HOME")
	})

	AfterEach(func() {
		// Restore original HOME
		if origHome != "" {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}

		// Clean up temp directory
		if tempHomeDir != "" {
			os.RemoveAll(tempHomeDir)
		}
	})

	Describe("CLI Desktop Alignment Validation", func() {
		Context("when no marker file exists", func() {
			It("should allow commands to run normally", func() {
				By("Setting up environment with no marker file")
				os.Setenv("HOME", tempHomeDir)

				By("Running a CLI command")
				stdout, _ := e2e.NewTHVCommand(config, "version").
					WithEnv("HOME=" + tempHomeDir).
					ExpectSuccess()

				By("Verifying the command succeeded")
				Expect(stdout).To(ContainSubstring("ToolHive"), "Version command should produce output")
			})
		})

		Context("when marker file exists but target binary does not", func() {
			It("should allow commands to run (stale marker scenario)", func() {
				By("Creating a marker file pointing to non-existent binary")
				toolhiveDir := filepath.Join(tempHomeDir, ".toolhive")
				err := os.MkdirAll(toolhiveDir, 0755)
				Expect(err).ToNot(HaveOccurred())

				marker := desktop.CliSourceMarker{
					SchemaVersion:  1,
					Source:         "desktop",
					InstallMethod:  "symlink",
					CLIVersion:     "1.0.0",
					SymlinkTarget:  "/nonexistent/path/to/thv",
					InstalledAt:    "2026-01-22T10:30:00Z",
					DesktopVersion: "2.0.0",
				}
				markerData, err := json.Marshal(marker)
				Expect(err).ToNot(HaveOccurred())

				markerPath := filepath.Join(toolhiveDir, ".cli-source")
				err = os.WriteFile(markerPath, markerData, 0600)
				Expect(err).ToNot(HaveOccurred())

				By("Running a CLI command")
				stdout, _ := e2e.NewTHVCommand(config, "version").
					WithEnv("HOME=" + tempHomeDir).
					ExpectSuccess()

				By("Verifying the command succeeded despite stale marker")
				Expect(stdout).To(ContainSubstring("ToolHive"), "Version command should produce output")
			})
		})

		Context("when marker file exists and target binary exists but differs", func() {
			It("should block the command with a conflict error", func() {
				By("Creating a fake target binary")
				toolhiveDir := filepath.Join(tempHomeDir, ".toolhive")
				err := os.MkdirAll(toolhiveDir, 0755)
				Expect(err).ToNot(HaveOccurred())

				fakeBinaryPath := filepath.Join(tempHomeDir, "fake-thv")
				err = os.WriteFile(fakeBinaryPath, []byte("fake binary content"), 0755)
				Expect(err).ToNot(HaveOccurred())

				By("Creating a marker file pointing to the fake binary")
				marker := desktop.CliSourceMarker{
					SchemaVersion:  1,
					Source:         "desktop",
					InstallMethod:  "symlink",
					CLIVersion:     "1.0.0",
					SymlinkTarget:  fakeBinaryPath,
					InstalledAt:    "2026-01-22T10:30:00Z",
					DesktopVersion: "2.0.0",
				}
				markerData, err := json.Marshal(marker)
				Expect(err).ToNot(HaveOccurred())

				markerPath := filepath.Join(toolhiveDir, ".cli-source")
				err = os.WriteFile(markerPath, markerData, 0600)
				Expect(err).ToNot(HaveOccurred())

				By("Running a CLI command")
				stdout, stderr, cmdErr := e2e.NewTHVCommand(config, "version").
					WithEnv("HOME=" + tempHomeDir).
					Run()

				By("Verifying the command was blocked due to conflict")
				Expect(cmdErr).To(HaveOccurred(), "Command should fail due to desktop conflict")
				combinedOutput := stdout + stderr
				Expect(combinedOutput).To(ContainSubstring("CLI conflict detected"),
					"Error should indicate CLI conflict")
				Expect(combinedOutput).To(ContainSubstring("ToolHive Desktop"),
					"Error should mention ToolHive Desktop")
			})
		})

		Context("when TOOLHIVE_SKIP_DESKTOP_CHECK is set", func() {
			It("should allow commands even with conflict", func() {
				By("Creating a fake target binary and marker")
				toolhiveDir := filepath.Join(tempHomeDir, ".toolhive")
				err := os.MkdirAll(toolhiveDir, 0755)
				Expect(err).ToNot(HaveOccurred())

				fakeBinaryPath := filepath.Join(tempHomeDir, "fake-thv")
				err = os.WriteFile(fakeBinaryPath, []byte("fake binary content"), 0755)
				Expect(err).ToNot(HaveOccurred())

				marker := desktop.CliSourceMarker{
					SchemaVersion:  1,
					Source:         "desktop",
					InstallMethod:  "symlink",
					CLIVersion:     "1.0.0",
					SymlinkTarget:  fakeBinaryPath,
					InstalledAt:    "2026-01-22T10:30:00Z",
					DesktopVersion: "2.0.0",
				}
				markerData, err := json.Marshal(marker)
				Expect(err).ToNot(HaveOccurred())

				markerPath := filepath.Join(toolhiveDir, ".cli-source")
				err = os.WriteFile(markerPath, markerData, 0600)
				Expect(err).ToNot(HaveOccurred())

				By("Running a CLI command with skip flag set")
				stdout, _ := e2e.NewTHVCommand(config, "version").
					WithEnv(
						"HOME="+tempHomeDir,
						"TOOLHIVE_SKIP_DESKTOP_CHECK=1",
					).
					ExpectSuccess()

				By("Verifying the command succeeded despite conflict")
				Expect(stdout).To(ContainSubstring("ToolHive"),
					"Version command should produce output when skip is set")
			})
		})

		Context("when marker file has invalid JSON", func() {
			It("should allow commands to run (treat as no marker)", func() {
				By("Creating an invalid marker file")
				toolhiveDir := filepath.Join(tempHomeDir, ".toolhive")
				err := os.MkdirAll(toolhiveDir, 0755)
				Expect(err).ToNot(HaveOccurred())

				markerPath := filepath.Join(toolhiveDir, ".cli-source")
				err = os.WriteFile(markerPath, []byte("not valid json {{{"), 0600)
				Expect(err).ToNot(HaveOccurred())

				By("Running a CLI command")
				stdout, _ := e2e.NewTHVCommand(config, "version").
					WithEnv("HOME=" + tempHomeDir).
					ExpectSuccess()

				By("Verifying the command succeeded")
				Expect(stdout).To(ContainSubstring("ToolHive"),
					"Version command should produce output with invalid marker")
			})
		})

		Context("when marker file has wrong schema version", func() {
			It("should allow commands to run (treat as invalid marker)", func() {
				By("Creating a marker file with wrong schema version")
				toolhiveDir := filepath.Join(tempHomeDir, ".toolhive")
				err := os.MkdirAll(toolhiveDir, 0755)
				Expect(err).ToNot(HaveOccurred())

				marker := map[string]interface{}{
					"schema_version":  999, // Invalid schema version
					"source":          "desktop",
					"install_method":  "symlink",
					"cli_version":     "1.0.0",
					"symlink_target":  "/some/path",
					"installed_at":    "2026-01-22T10:30:00Z",
					"desktop_version": "2.0.0",
				}
				markerData, err := json.Marshal(marker)
				Expect(err).ToNot(HaveOccurred())

				markerPath := filepath.Join(toolhiveDir, ".cli-source")
				err = os.WriteFile(markerPath, markerData, 0600)
				Expect(err).ToNot(HaveOccurred())

				By("Running a CLI command")
				stdout, _ := e2e.NewTHVCommand(config, "version").
					WithEnv("HOME=" + tempHomeDir).
					ExpectSuccess()

				By("Verifying the command succeeded")
				Expect(stdout).To(ContainSubstring("ToolHive"),
					"Version command should produce output with invalid schema version")
			})
		})
	})
})
