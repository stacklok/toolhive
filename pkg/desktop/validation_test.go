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
		validateFn func(t *testing.T, marker *cliSourceMarker)
	}{
		{
			name: "valid marker file",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				marker := cliSourceMarker{
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
			validateFn: func(t *testing.T, marker *cliSourceMarker) {
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
			wantErr:    errMarkerNotFound,
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
			wantErr:    errInvalidMarker,
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
			wantErr:    errInvalidMarker,
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
			wantErr:    errInvalidMarker,
			wantMarker: false,
		},
		{
			name: "valid marker with copy method",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				marker := cliSourceMarker{
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
			validateFn: func(t *testing.T, marker *cliSourceMarker) {
				t.Helper()
				assert.Equal(t, "copy", marker.InstallMethod)
				assert.Equal(t, "abc123", marker.CLIChecksum)
			},
		},
		{
			name: "valid marker with flatpak method",
			setupFile: func(t *testing.T, dir string) string {
				t.Helper()
				marker := cliSourceMarker{
					SchemaVersion:  1,
					Source:         "desktop",
					InstallMethod:  "flatpak",
					CLIVersion:     "1.0.0",
					FlatpakTarget:  "/home/user/.local/share/flatpak/app/com.stacklok.ToolHive/x86_64/master/active/files/toolhive/resources/bin/linux-x64/thv",
					InstalledAt:    "2026-01-22T10:30:00Z",
					DesktopVersion: "2.0.0",
				}
				return writeMarkerFile(t, dir, marker)
			},
			wantErr:    nil,
			wantMarker: true,
			validateFn: func(t *testing.T, marker *cliSourceMarker) {
				t.Helper()
				assert.Equal(t, "flatpak", marker.InstallMethod)
				assert.Contains(t, marker.FlatpakTarget, "com.stacklok.ToolHive")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := tt.setupFile(t, dir)

			marker, err := readMarkerFileFromPath(path)

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
		setHomeDir(t, dir)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		assert.False(t, result.HasConflict)
	})

	t.Run("target binary does not exist - no conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Write a marker file pointing to a non-existent binary
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  "/nonexistent/path/to/thv",
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		// Point home to our temp dir
		setHomeDir(t, dir)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		assert.False(t, result.HasConflict, "should not conflict when target doesn't exist")
	})

	t.Run("current binary matches target - no conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Get the current executable path
		currentExe, err := os.Executable()
		require.NoError(t, err)

		// Resolve the current executable path
		resolvedExe, err := filepath.EvalSymlinks(currentExe)
		require.NoError(t, err)
		resolvedExe = filepath.Clean(resolvedExe)

		// Write a marker file pointing to the current executable
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  resolvedExe,
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		// Point home to our temp dir
		setHomeDir(t, dir)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		assert.False(t, result.HasConflict, "should not conflict when paths match")
	})

	t.Run("current binary differs from target - conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Create a fake target binary
		fakeBinaryPath := filepath.Join(dir, "fake-thv")
		require.NoError(t, os.WriteFile(fakeBinaryPath, []byte("fake"), 0755))

		// Write a marker file pointing to the fake binary
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  fakeBinaryPath,
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		// Point home to our temp dir
		setHomeDir(t, dir)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		assert.True(t, result.HasConflict, "should conflict when paths differ")
		assert.NotEmpty(t, result.Message)
		assert.Contains(t, result.Message, fakeBinaryPath)
	})
}

//nolint:paralleltest // These tests modify HOME env var and cannot run in parallel
func TestIsDesktopManagedCLI(t *testing.T) {
	t.Run("no marker file returns false", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()
		setHomeDir(t, dir)

		assert.False(t, IsDesktopManagedCLI())
	})

	t.Run("target binary does not exist returns false", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  "/nonexistent/path/to/thv",
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(thDir, ".cli-source"), data, 0600))

		setHomeDir(t, dir)

		assert.False(t, IsDesktopManagedCLI())
	})

	t.Run("paths match returns true", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		currentExe, err := os.Executable()
		require.NoError(t, err)
		resolvedExe, err := filepath.EvalSymlinks(currentExe)
		require.NoError(t, err)
		resolvedExe = filepath.Clean(resolvedExe)

		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  resolvedExe,
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(thDir, ".cli-source"), data, 0600))

		setHomeDir(t, dir)

		assert.True(t, IsDesktopManagedCLI())
	})

	t.Run("paths differ returns false", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		fakeBinaryPath := filepath.Join(dir, "fake-thv")
		require.NoError(t, os.WriteFile(fakeBinaryPath, []byte("fake"), 0755))

		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  fakeBinaryPath,
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(thDir, ".cli-source"), data, 0600))

		setHomeDir(t, dir)

		assert.False(t, IsDesktopManagedCLI())
	})
}

func TestValidateDesktopAlignment(t *testing.T) {
	t.Run("skip validation when env var is set", func(t *testing.T) {
		// Set the skip env var
		t.Setenv(envSkipDesktopCheck, "1")

		// Even with a conflicting setup, validation should be skipped
		err := ValidateDesktopAlignment()
		assert.NoError(t, err)
	})

	t.Run("skip validation when env var is true", func(t *testing.T) {
		t.Setenv(envSkipDesktopCheck, "true")

		err := ValidateDesktopAlignment()
		assert.NoError(t, err)
	})

	t.Run("skip validation when env var is TRUE", func(t *testing.T) {
		t.Setenv(envSkipDesktopCheck, "TRUE")

		err := ValidateDesktopAlignment()
		assert.NoError(t, err)
	})

	t.Run("does not skip when env var is false", func(t *testing.T) {
		t.Setenv(envSkipDesktopCheck, "false")

		// With no marker file, should succeed
		dir := t.TempDir()
		setHomeDir(t, dir)

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

	// Add platform-specific tests for case sensitivity
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" { //nolint:goconst // platform checks are clearer inline
		// Case-insensitive filesystems (macOS, Windows)
		tests = append(tests, struct {
			name   string
			path1  string
			path2  string
			expect bool
		}{
			name:   "case insensitive match on darwin/windows",
			path1:  "/Users/Test/bin/thv",
			path2:  "/users/test/bin/thv",
			expect: true,
		})
	} else {
		// Case-sensitive filesystems (Linux)
		tests = append(tests, struct {
			name   string
			path1  string
			path2  string
			expect bool
		}{
			name:   "case sensitive mismatch on linux",
			path1:  "/home/Test/bin/thv",
			path2:  "/home/test/bin/thv",
			expect: false,
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

	marker := &cliSourceMarker{
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
	// Platform-specific path check
	if runtime.GOOS == "windows" {
		assert.Contains(t, msg, "ToolHive")
		assert.Contains(t, msg, "bin")
	} else {
		assert.Contains(t, msg, ".toolhive/bin")
	}
	assert.Contains(t, msg, "Desktop version: 2.0.0")
}

func TestGetTargetPath(t *testing.T) {
	t.Parallel()

	t.Run("symlink method with target", func(t *testing.T) {
		t.Parallel()
		marker := &cliSourceMarker{
			InstallMethod: "symlink",
			SymlinkTarget: "/path/to/binary",
		}
		result := getTargetPath(marker)
		assert.Equal(t, "/path/to/binary", result)
	})

	t.Run("symlink method without target", func(t *testing.T) {
		t.Parallel()
		marker := &cliSourceMarker{
			InstallMethod: "symlink",
			SymlinkTarget: "",
		}
		result := getTargetPath(marker)
		assert.Equal(t, "", result)
	})

	t.Run("flatpak method with target", func(t *testing.T) {
		t.Parallel()
		marker := &cliSourceMarker{
			InstallMethod: "flatpak",
			FlatpakTarget: "/home/user/.local/share/flatpak/app/com.stacklok.ToolHive/x86_64/master/active/files/toolhive/resources/bin/linux-x64/thv",
		}
		result := getTargetPath(marker)
		assert.Equal(t, marker.FlatpakTarget, result)
	})

	t.Run("flatpak method without target", func(t *testing.T) {
		t.Parallel()
		marker := &cliSourceMarker{
			InstallMethod: "flatpak",
			FlatpakTarget: "",
		}
		result := getTargetPath(marker)
		assert.Equal(t, "", result)
	})

	t.Run("copy method", func(t *testing.T) {
		t.Parallel()
		marker := &cliSourceMarker{
			InstallMethod: "copy",
			CLIChecksum:   "abc123",
		}
		result := getTargetPath(marker)
		// On Windows, copy method returns the expected CLI path
		// On other platforms, it returns empty (copy method is Windows-only)
		if runtime.GOOS == "windows" {
			assert.Contains(t, result, "ToolHive")
			assert.Contains(t, result, "bin")
			assert.Contains(t, result, "thv.exe")
		} else {
			assert.Equal(t, "", result)
		}
	})
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

	t.Run("handles relative path input", func(t *testing.T) {
		t.Parallel()
		// Create a temp file in current directory context
		dir := t.TempDir()
		filePath := filepath.Join(dir, "testfile")
		require.NoError(t, os.WriteFile(filePath, []byte("test"), 0644))

		// resolvePath should still return absolute path
		resolved, err := resolvePath(filePath)
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(resolved))
	})
}

func TestReadMarkerFileFromPathWithReadError(t *testing.T) {
	t.Parallel()

	// Create a directory instead of a file - reading it will fail with a different error
	dir := t.TempDir()
	path := filepath.Join(dir, "marker-dir")
	require.NoError(t, os.MkdirAll(path, 0755))

	marker, err := readMarkerFileFromPath(path)
	// Should return an error that is NOT errMarkerNotFound (it's a read error)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, errMarkerNotFound)
	assert.Nil(t, marker)
}

//nolint:paralleltest // subtests modify HOME env var
func TestMarkerFileExists(t *testing.T) {
	t.Run("returns true when marker exists", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory and marker file
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))
		markerPath := filepath.Join(thDir, ".cli-source")
		require.NoError(t, os.WriteFile(markerPath, []byte("{}"), 0600))

		setHomeDir(t, dir)

		exists, err := markerFileExists()
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("returns false when marker does not exist", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		setHomeDir(t, dir)

		exists, err := markerFileExists()
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

//nolint:paralleltest // subtests modify HOME env var
func TestReadMarkerFile(t *testing.T) {
	t.Run("reads marker from home directory", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory and marker file
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  "/path/to/binary",
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		setHomeDir(t, dir)

		result, err := readMarkerFile()
		require.NoError(t, err)
		assert.Equal(t, "1.0.0", result.CLIVersion)
	})

	t.Run("returns error when marker not found", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		setHomeDir(t, dir)

		_, err := readMarkerFile()
		assert.ErrorIs(t, err, errMarkerNotFound)
	})
}

func TestGetMarkerFilePath(t *testing.T) {
	t.Parallel()

	t.Run("returns path in home directory", func(t *testing.T) {
		t.Parallel()
		path, err := getMarkerFilePath()
		require.NoError(t, err)
		assert.Contains(t, path, ".toolhive")
		assert.Contains(t, path, ".cli-source")
	})
}

//nolint:paralleltest // subtests modify HOME env var
func TestValidateDesktopAlignmentWithConflict(t *testing.T) {
	t.Run("returns error on conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Create a fake target binary
		fakeBinaryPath := filepath.Join(dir, "fake-thv")
		require.NoError(t, os.WriteFile(fakeBinaryPath, []byte("fake"), 0755))

		// Write a marker file pointing to the fake binary
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "symlink",
			CLIVersion:     "1.0.0",
			SymlinkTarget:  fakeBinaryPath,
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		setHomeDir(t, dir)

		err = ValidateDesktopAlignment()
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrDesktopConflict)
	})
}

//nolint:paralleltest // subtests modify HOME env var
func TestCheckDesktopAlignmentCopyMethod(t *testing.T) {
	t.Run("copy method on non-Windows returns no conflict", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		if runtime.GOOS == "windows" {
			t.Skip("this test is for non-Windows platforms")
		}

		dir := t.TempDir()

		// Create the .toolhive directory
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Write a marker file with copy method (no symlink target)
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "copy",
			CLIVersion:     "1.0.0",
			CLIChecksum:    "abc123",
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		setHomeDir(t, dir)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		assert.False(t, result.HasConflict, "copy method on non-Windows should not cause conflict (validation skipped)")
	})

	t.Run("copy method on Windows validates against LOCALAPPDATA path", func(t *testing.T) { //nolint:paralleltest // modifies env vars
		if runtime.GOOS != "windows" {
			t.Skip("this test is for Windows only")
		}

		dir := t.TempDir()

		// Create the .toolhive directory for the marker file
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Create the LOCALAPPDATA directory structure and fake binary
		localAppData := filepath.Join(dir, "AppData", "Local")
		toolhiveBinDir := filepath.Join(localAppData, "ToolHive", "bin")
		require.NoError(t, os.MkdirAll(toolhiveBinDir, 0755))

		// Create a fake CLI binary in the expected location
		fakeCLIPath := filepath.Join(toolhiveBinDir, "thv.exe")
		require.NoError(t, os.WriteFile(fakeCLIPath, []byte("fake cli"), 0755))

		// Write a marker file with copy method
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "copy",
			CLIVersion:     "1.0.0",
			CLIChecksum:    "abc123",
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		setHomeDir(t, dir)
		t.Setenv("LOCALAPPDATA", localAppData)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		// Should detect a conflict because current exe is not the fake CLI
		assert.True(t, result.HasConflict, "copy method on Windows should detect conflict when running different CLI")
	})

	t.Run("copy method on Windows no conflict when target does not exist", func(t *testing.T) { //nolint:paralleltest // modifies env vars
		if runtime.GOOS != "windows" {
			t.Skip("this test is for Windows only")
		}

		dir := t.TempDir()

		// Create the .toolhive directory for the marker file
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Set LOCALAPPDATA to a directory that does NOT have thv.exe
		localAppData := filepath.Join(dir, "AppData", "Local")
		require.NoError(t, os.MkdirAll(localAppData, 0755))

		// Write a marker file with copy method
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "copy",
			CLIVersion:     "1.0.0",
			CLIChecksum:    "abc123",
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		setHomeDir(t, dir)
		t.Setenv("LOCALAPPDATA", localAppData)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		// Should not conflict because the target binary doesn't exist
		assert.False(t, result.HasConflict, "copy method on Windows should not conflict when target doesn't exist")
	})
}

//nolint:paralleltest // subtests modify HOME env var
func TestCheckDesktopAlignmentFlatpakMethod(t *testing.T) {
	t.Run("flatpak method detects conflict when target exists and paths differ", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Create a fake binary simulating the host-visible Flatpak binary
		fakeFlatpakBinary := filepath.Join(dir, "flatpak-app", "thv")
		require.NoError(t, os.MkdirAll(filepath.Dir(fakeFlatpakBinary), 0755))
		require.NoError(t, os.WriteFile(fakeFlatpakBinary, []byte("fake"), 0755))

		// Write a marker file with flatpak method
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "flatpak",
			CLIVersion:     "1.0.0",
			FlatpakTarget:  fakeFlatpakBinary,
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		setHomeDir(t, dir)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		assert.True(t, result.HasConflict, "flatpak method should detect conflict when running a different CLI")
		assert.NotEmpty(t, result.Message)
	})

	t.Run("flatpak method no conflict when target does not exist", func(t *testing.T) { //nolint:paralleltest // modifies HOME
		dir := t.TempDir()

		// Create the .toolhive directory
		thDir := filepath.Join(dir, ".toolhive")
		require.NoError(t, os.MkdirAll(thDir, 0755))

		// Write a marker file pointing to a non-existent Flatpak binary
		// (simulates Flatpak being uninstalled)
		marker := cliSourceMarker{
			SchemaVersion:  1,
			Source:         "desktop",
			InstallMethod:  "flatpak",
			CLIVersion:     "1.0.0",
			FlatpakTarget:  "/nonexistent/flatpak/app/thv",
			InstalledAt:    "2026-01-22T10:30:00Z",
			DesktopVersion: "2.0.0",
		}
		markerPath := filepath.Join(thDir, ".cli-source")
		data, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, data, 0600))

		setHomeDir(t, dir)

		result, err := checkDesktopAlignment()
		require.NoError(t, err)
		assert.False(t, result.HasConflict, "flatpak method should not conflict when target doesn't exist (Flatpak uninstalled)")
	})

}

func TestBuildConflictMessageWithoutDesktopVersion(t *testing.T) {
	t.Parallel()

	marker := &cliSourceMarker{
		SchemaVersion:  1,
		Source:         "desktop",
		InstallMethod:  "symlink",
		CLIVersion:     "1.0.0",
		SymlinkTarget:  "/path/to/thv",
		InstalledAt:    "2026-01-22T10:30:00Z",
		DesktopVersion: "", // Empty desktop version
	}

	msg := buildConflictMessage(
		"/path/to/thv",
		"/usr/local/bin/thv",
		marker,
	)

	assert.Contains(t, msg, "/path/to/thv")
	assert.Contains(t, msg, "/usr/local/bin/thv")
	assert.NotContains(t, msg, "Desktop version:")
}

// Helper functions for tests

func writeMarkerFile(t *testing.T, dir string, marker cliSourceMarker) string {
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

// setHomeDir sets the home directory environment variables for testing.
// On Windows, it sets USERPROFILE; on Unix, it sets HOME.
// It also cleans up after the test.
func setHomeDir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		originalUserProfile := os.Getenv("USERPROFILE")
		t.Cleanup(func() {
			os.Setenv("USERPROFILE", originalUserProfile)
		})
		os.Setenv("USERPROFILE", dir)
	} else {
		originalHome := os.Getenv("HOME")
		t.Cleanup(func() {
			os.Setenv("HOME", originalHome)
		})
		os.Setenv("HOME", dir)
	}
}
