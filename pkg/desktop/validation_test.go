// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadMarkerFileFromPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupFile  func(t *testing.T, dir string) string
		wantErr    error
		wantMarker bool
		validateFn func(t *testing.T, marker *CliSourceMarker)
	}{
		{
			name: "valid marker file",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				marker := CliSourceMarker{
					SchemaVersion:  1,
					Source:         "desktop",
					InstallMethod:  "symlink",
					CLIVersion:     "1.0.0",
					SymlinkTarget:  "/path/to/binary",
					InstalledAt:    "2026-01-22T10:30:00Z",
					DesktopVersion: "2.0.0",
				}
				return writeMarkerFile(t, dir, marker)
			},
			wantErr:    nil,
			wantMarker: true,
			validateFn: func(t *testing.T, marker *CliSourceMarker) {
				t.Helper()
				assert.Equal(t, 1, marker.SchemaVersion)
				assert.Equal(t, "desktop", marker.Source)
				assert.Equal(t, "symlink", marker.InstallMethod)
				assert.Equal(t, "1.0.0", marker.CLIVersion)
				assert.Equal(t, "/path/to/binary", marker.SymlinkTarget)
			},
		},
		{
			name: "file not found",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				return filepath.Join(dir, "nonexistent")
			},
			wantErr:    ErrMarkerNotFound,
			wantMarker: false,
		},
		{
			name: "invalid JSON",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "invalid.json")
				require.NoError(t, os.WriteFile(path, []byte("not valid json"), 0600))
				return path
			},
			wantErr:    ErrInvalidMarker,
			wantMarker: false,
		},
		{
			name: "wrong schema version",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				marker := map[string]any{
					"schema_version":  99,
					"source":          "desktop",
					"install_method":  "symlink",
					"cli_version":     "1.0.0",
					"installed_at":    "2026-01-22T10:30:00Z",
					"desktop_version": "2.0.0",
				}
				return writeMarkerFileRaw(t, dir, marker)
			},
			wantErr:    ErrInvalidMarker,
			wantMarker: false,
		},
		{
			name: "wrong source value",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				marker := map[string]any{
					"schema_version":  1,
					"source":          "manual",
					"install_method":  "symlink",
					"cli_version":     "1.0.0",
					"installed_at":    "2026-01-22T10:30:00Z",
					"desktop_version": "2.0.0",
				}
				return writeMarkerFileRaw(t, dir, marker)
			},
			wantErr:    ErrInvalidMarker,
			wantMarker: false,
		},
		{
			name: "valid marker with copy method",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				marker := CliSourceMarker{
					SchemaVersion:  1,
					Source:         "desktop",
					InstallMethod:  "copy",
					CLIVersion:     "1.0.0",
					CLIChecksum:    "abc123",
					InstalledAt:    "2026-01-22T10:30:00Z",
					DesktopVersion: "2.0.0",
				}
				return writeMarkerFile(t, dir, marker)
			},
			wantErr:    nil,
			wantMarker: true,
			validateFn: func(t *testing.T, marker *CliSourceMarker) {
				t.Helper()
				assert.Equal(t, "copy", marker.InstallMethod)
				assert.Equal(t, "abc123", marker.CLIChecksum)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := tt.setupFile(t, dir)

			marker, err := ReadMarkerFileFromPath(path)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, marker)
			} else {
				require.NoError(t, err)
				require.NotNil(t, marker)
				if tt.validateFn != nil {
					tt.validateFn(t, marker)
				}
			}
		})
	}
}

//nolint:paralleltest // These tests modify HOME env var and cannot run in parallel
func TestCheckDesktopAlignment(t *testing.T) {
	// Save and restore original ReadMarkerFile behavior
	// We test with actual files instead of mocking

	t.Run("no marker file - no conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		// Use a temporary directory that doesn't have a marker file
		dir := t.TempDir()

		// Point the home directory to our temp dir
		originalHome := os.Getenv("HOME")
		t.Cleanup(func() {
			os.Setenv("HOME", originalHome)
		})
		os.Setenv("HOME", dir)

		result, err := CheckDesktopAlignment()
		require.NoError(t, err)
		assert.False(t, result.HasConflict)
	})

	t.Run("target binary does not exist - no conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		toolhiveDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(toolhiveDir, 0755))

		// Write a marker file pointing to a non-existent binary
		marker := CliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  "/nonexistent/path/to/thv",
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(toolhiveDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		// Point home to our temp dir
		originalHome := os.Getenv("HOME")
		t.Cleanup(func() {
			os.Setenv("HOME", originalHome)
		})
		os.Setenv("HOME", dir)

		result, err := CheckDesktopAlignment()
		require.NoError(t, err)
		assert.False(t, result.HasConflict, "should not conflict when target doesn't exist")
	})

	t.Run("current binary matches target - no conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		toolhiveDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(toolhiveDir, 0755))

		// Get the current executable path
		currentExe, err := os.Executable()
		require.NoError(t, err)

		// Resolve the current executable path
		resolvedExe, err := filepath.EvalSymlinks(currentExe)
		require.NoError(t, err)
		resolvedExe = filepath.Clean(resolvedExe)

		// Write a marker file pointing to the current executable
		marker := CliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  resolvedExe,
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(toolhiveDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		// Point home to our temp dir
		originalHome := os.Getenv("HOME")
		t.Cleanup(func() {
			os.Setenv("HOME", originalHome)
		})
		os.Setenv("HOME", dir)

		result, err := CheckDesktopAlignment()
		require.NoError(t, err)
		assert.False(t, result.HasConflict, "should not conflict when paths match")
	})

	t.Run("current binary differs from target - conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		toolhiveDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(toolhiveDir, 0755))

		// Create a fake target binary
		fakeBinaryPath := filepath.Join(dir, "fake-thv")
		require.NoError(t, os.WriteFile(fakeBinaryPath, []byte("fake"), 0755))

		// Write a marker file pointing to the fake binary
		marker := CliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  fakeBinaryPath,
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(toolhiveDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		// Point home to our temp dir
		originalHome := os.Getenv("HOME")
		t.Cleanup(func() {
			os.Setenv("HOME", originalHome)
		})
		os.Setenv("HOME", dir)

		result, err := CheckDesktopAlignment()
		require.NoError(t, err)
		assert.True(t, result.HasConflict, "should conflict when paths differ")
		assert.NotEmpty(t, result.Message)
		assert.Contains(t, result.Message, fakeBinaryPath)
	})
}

func TestValidateDesktopAlignment(t *testing.T) {
	t.Run("skip validation when env var is set", func(t *testing.T) {
		// Set the skip env var
		t.Setenv(EnvSkipDesktopCheck, "1")

		// Even with a conflicting setup, validation should be skipped
		err := ValidateDesktopAlignment()
		assert.NoError(t, err)
	})

	t.Run("skip validation when env var is true", func(t *testing.T) {
		t.Setenv(EnvSkipDesktopCheck, "true")

		err := ValidateDesktopAlignment()
		assert.NoError(t, err)
	})

	t.Run("skip validation when env var is TRUE", func(t *testing.T) {
		t.Setenv(EnvSkipDesktopCheck, "TRUE")

		err := ValidateDesktopAlignment()
		assert.NoError(t, err)
	})

	t.Run("does not skip when env var is false", func(t *testing.T) {
		t.Setenv(EnvSkipDesktopCheck, "false")

		// With no marker file, should succeed
		dir := t.TempDir()
		originalHome := os.Getenv("HOME")
		t.Cleanup(func() {
			os.Setenv("HOME", originalHome)
		})
		os.Setenv("HOME", dir)

		err := ValidateDesktopAlignment()
		assert.NoError(t, err)
	})
}

func TestPathsEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path1  string
		path2  string
		expect bool
	}{
		{
			name:   "identical paths",
			path1:  "/usr/local/bin/thv",
			path2:  "/usr/local/bin/thv",
			expect: true,
		},
		{
			name:   "different paths",
			path1:  "/usr/local/bin/thv",
			path2:  "/opt/homebrew/bin/thv",
			expect: false,
		},
	}

	// Add platform-specific tests
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" { //nolint:goconst // platform checks are clearer inline
		tests = append(tests, struct {
			name   string
			path1  string
			path2  string
			expect bool
		}{
			name:   "case insensitive match",
			path1:  "/Users/Test/bin/thv",
			path2:  "/users/test/bin/thv",
			expect: true,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := pathsEqual(tt.path1, tt.path2)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestBuildConflictMessage(t *testing.T) {
	t.Parallel()

	marker := &CliSourceMarker{
		SchemaVersion:  1,
		Source:         "desktop",
		InstallMethod:  "symlink",
		CLIVersion:     "1.0.0",
		SymlinkTarget:  "/Applications/ToolHive.app/Contents/Resources/bin/thv",
		InstalledAt:    "2026-01-22T10:30:00Z",
		DesktopVersion: "2.0.0",
	}

	msg := buildConflictMessage(
		"/Applications/ToolHive.app/Contents/Resources/bin/thv",
		"/usr/local/bin/thv",
		marker,
	)

	assert.Contains(t, msg, "/Applications/ToolHive.app/Contents/Resources/bin/thv")
	assert.Contains(t, msg, "/usr/local/bin/thv")
	assert.Contains(t, msg, ".toolhive/bin")
	assert.Contains(t, msg, "Desktop version: 2.0.0")
}

func TestGetTargetPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		marker *CliSourceMarker
		expect string
	}{
		{
			name: "symlink method with target",
			marker: &CliSourceMarker{
				InstallMethod: "symlink",
				SymlinkTarget: "/path/to/binary",
			},
			expect: "/path/to/binary",
		},
		{
			name: "symlink method without target",
			marker: &CliSourceMarker{
				InstallMethod: "symlink",
				SymlinkTarget: "",
			},
			expect: "",
		},
		{
			name: "copy method",
			marker: &CliSourceMarker{
				InstallMethod: "copy",
				CLIChecksum:   "abc123",
			},
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := getTargetPath(tt.marker)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestResolvePath(t *testing.T) {
	t.Parallel()

	t.Run("resolves regular file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		filePath := filepath.Join(dir, "testfile")
		require.NoError(t, os.WriteFile(filePath, []byte("test"), 0644))

		resolved, err := resolvePath(filePath)
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(resolved))
	})

	t.Run("resolves symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks may require special permissions on Windows")
		}

		t.Parallel()
		dir := t.TempDir()
		realPath := filepath.Join(dir, "realfile")
		require.NoError(t, os.WriteFile(realPath, []byte("test"), 0644))

		linkPath := filepath.Join(dir, "symlink")
		require.NoError(t, os.Symlink(realPath, linkPath))

		resolved, err := resolvePath(linkPath)
		require.NoError(t, err)

		// Should resolve to the real file
		expectedResolved, _ := filepath.EvalSymlinks(realPath)
		assert.Equal(t, expectedResolved, resolved)
	})

	t.Run("fails for non-existent file", func(t *testing.T) {
		t.Parallel()
		_, err := resolvePath("/nonexistent/path/to/file")
		assert.Error(t, err)
	})
}

// Helper functions for tests

func writeMarkerFile(t *testing.T, dir string, marker CliSourceMarker) string {
	t.Helper()
	path := filepath.Join(dir, "marker.json")
	data, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
	return path
}

func writeMarkerFileRaw(t *testing.T, dir string, marker map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, "marker.json")
	data, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
	return path
}
