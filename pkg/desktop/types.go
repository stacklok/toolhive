// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package desktop provides functionality for detecting and validating
// the ToolHive Desktop application's CLI management.
package desktop

// CurrentSchemaVersion is the expected schema version for marker files.
const CurrentSchemaVersion = 1

// CliSourceMarker represents the marker file schema written by the
// ToolHive Desktop application at ~/.toolhive/.cli-source.
// This marker indicates that the desktop app manages the CLI installation.
type CliSourceMarker struct {
	// SchemaVersion is the version of the marker file schema.
	// Must be 1 for the current implementation.
	SchemaVersion int `json:"schema_version"`

	// Source indicates who installed the CLI. Always "desktop" for
	// Desktop-managed installations.
	Source string `json:"source"`

	// InstallMethod indicates how the CLI was installed.
	// "symlink" on macOS/Linux, "copy" on Windows.
	InstallMethod string `json:"install_method"`

	// CLIVersion is the version of the CLI binary that was installed.
	CLIVersion string `json:"cli_version"`

	// SymlinkTarget is the path the symlink points to (macOS/Linux only).
	// This is the actual binary location inside the Desktop app bundle.
	SymlinkTarget string `json:"symlink_target,omitempty"`

	// CLIChecksum is the SHA256 checksum of the CLI binary (Windows only).
	// Used for validation when symlinks aren't available.
	CLIChecksum string `json:"cli_checksum,omitempty"`

	// InstalledAt is the ISO 8601 timestamp of when the CLI was installed.
	InstalledAt string `json:"installed_at"`

	// DesktopVersion is the version of the ToolHive Desktop app that
	// installed this CLI.
	DesktopVersion string `json:"desktop_version"`
}

// ValidationResult represents the result of desktop alignment validation.
type ValidationResult struct {
	// HasConflict indicates whether a conflict was detected.
	HasConflict bool

	// Message contains a user-friendly description of the conflict,
	// or empty if no conflict.
	Message string

	// DesktopCLIPath is the path to the desktop-managed CLI binary,
	// if a marker file was found.
	DesktopCLIPath string

	// CurrentCLIPath is the resolved path of the currently running CLI.
	CurrentCLIPath string
}
