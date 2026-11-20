// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewProcessor(t *testing.T) {
	t.Parallel()
	processor := NewProcessor(&Config{
		LoadGlobal:    true,
		PrintOverlays: false,
	})
	if processor == nil {
		t.Error("NewProcessor should return a non-nil processor")
		return
	}
	if len(processor.GlobalPatterns) != 0 {
		t.Error("GlobalPatterns should be empty initially")
	}
	if len(processor.LocalPatterns) != 0 {
		t.Error("LocalPatterns should be empty initially")
	}
}

func TestLoadIgnoreFile(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name          string
		fileContent   string
		expectedCount int
		expectError   bool
	}{
		{
			name: "valid ignore file",
			fileContent: `# This is a comment
.ssh/
*.bak
.env

# Another comment
node_modules/`,
			expectedCount: 4,
			expectError:   false,
		},
		{
			name:          "empty file",
			fileContent:   "",
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "only comments and empty lines",
			fileContent: `# Comment 1

# Comment 2

`,
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "mixed content",
			fileContent: `.git/
# Ignore logs
*.log

temp/
# End`,
			expectedCount: 3,
			expectError:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create temporary file
			tmpDir := t.TempDir()
			ignoreFile := filepath.Join(tmpDir, ".thvignore")

			err := os.WriteFile(ignoreFile, []byte(tc.fileContent), 0644)
			if err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			processor := NewProcessor(&Config{
				LoadGlobal:    true,
				PrintOverlays: false,
			})
			patterns, err := processor.loadIgnoreFile(ignoreFile)

			if tc.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
				if len(patterns) != tc.expectedCount {
					t.Errorf("Expected %d patterns, but got %d", tc.expectedCount, len(patterns))
				}
			}
		})
	}
}

func TestLoadLocal(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name          string
		createFile    bool
		fileContent   string
		expectedCount int
		expectError   bool
	}{
		{
			name:          "file exists",
			createFile:    true,
			fileContent:   ".ssh/\n*.env\nnode_modules/",
			expectedCount: 3,
			expectError:   false,
		},
		{
			name:          "file does not exist",
			createFile:    false,
			expectedCount: 0,
			expectError:   false, // Should not error, just no patterns
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()

			if tc.createFile {
				ignoreFile := filepath.Join(tmpDir, ".thvignore")
				err := os.WriteFile(ignoreFile, []byte(tc.fileContent), 0644)
				if err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
			}

			processor := NewProcessor(&Config{
				LoadGlobal:    true,
				PrintOverlays: false,
			})
			err := processor.LoadLocal(tmpDir)

			if tc.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
				if len(processor.LocalPatterns) != tc.expectedCount {
					t.Errorf("Expected %d patterns, but got %d", tc.expectedCount, len(processor.LocalPatterns))
				}
			}
		})
	}
}

func TestPatternMatchesInDirectory(t *testing.T) {
	t.Parallel()
	// Create test directory structure
	tmpDir := t.TempDir()

	// Create test files and directories
	sshDir := filepath.Join(tmpDir, ".ssh")
	err := os.Mkdir(sshDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create .ssh directory: %v", err)
	}

	envFile := filepath.Join(tmpDir, ".env")
	err = os.WriteFile(envFile, []byte("TEST=value"), 0644)
	if err != nil {
		t.Fatalf("Failed to create .env file: %v", err)
	}

	backupFile := filepath.Join(tmpDir, "data.bak")
	err = os.WriteFile(backupFile, []byte("backup"), 0644)
	if err != nil {
		t.Fatalf("Failed to create backup file: %v", err)
	}

	processor := NewProcessor(&Config{
		LoadGlobal:    true,
		PrintOverlays: false,
	})

	testCases := []struct {
		name     string
		pattern  string
		expected bool
	}{
		{
			name:     "directory pattern matches",
			pattern:  ".ssh/",
			expected: true,
		},
		{
			name:     "file pattern matches",
			pattern:  ".env",
			expected: true,
		},
		{
			name:     "glob pattern matches",
			pattern:  "*.bak",
			expected: true,
		},
		{
			name:     "pattern does not match",
			pattern:  "nonexistent",
			expected: false,
		},
		{
			name:     "directory without slash",
			pattern:  ".ssh",
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := processor.patternMatchesInDirectory(tmpDir, tc.pattern)
			if result != tc.expected {
				t.Errorf("Expected pattern '%s' to match: %v, but got: %v", tc.pattern, tc.expected, result)
			}
		})
	}
}

func TestGetOverlayPaths(t *testing.T) {
	t.Parallel()
	// Create test directory structure
	tmpDir := t.TempDir()

	// Create test files and directories
	sshDir := filepath.Join(tmpDir, ".ssh")
	err := os.Mkdir(sshDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create .ssh directory: %v", err)
	}

	envFile := filepath.Join(tmpDir, ".env")
	err = os.WriteFile(envFile, []byte("TEST=value"), 0644)
	if err != nil {
		t.Fatalf("Failed to create .env file: %v", err)
	}

	processor := NewProcessor(&Config{
		LoadGlobal:    true,
		PrintOverlays: false,
	})
	processor.GlobalPatterns = []string{"node_modules/", ".DS_Store"}
	processor.LocalPatterns = []string{".ssh/", ".env"}

	overlayPaths := processor.GetOverlayPaths(tmpDir, "/app")

	// Should get overlays for patterns that match existing files/dirs
	expectedPaths := []string{
		"/app/.ssh", // matches .ssh/ directory
		"/app/.env", // matches .env file
	}

	if len(overlayPaths) != len(expectedPaths) {
		t.Errorf("Expected %d overlay paths, but got %d", len(expectedPaths), len(overlayPaths))
	}

	for _, expectedPath := range expectedPaths {
		found := false
		for _, actualPath := range overlayPaths {
			if actualPath == expectedPath {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected overlay path '%s' not found in results", expectedPath)
		}
	}

	// Clean up processor artifacts
	if err := processor.Cleanup(); err != nil {
		t.Errorf("Failed to cleanup processor: %v", err)
	}
}

func TestGetOverlayMounts(t *testing.T) {
	t.Parallel()
	// Create test directory structure
	tmpDir := t.TempDir()

	// Create test files and directories
	sshDir := filepath.Join(tmpDir, ".ssh")
	err := os.Mkdir(sshDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create .ssh directory: %v", err)
	}

	envFile := filepath.Join(tmpDir, ".env")
	err = os.WriteFile(envFile, []byte("TEST=value"), 0644)
	if err != nil {
		t.Fatalf("Failed to create .env file: %v", err)
	}

	processor := NewProcessor(&Config{
		LoadGlobal:    true,
		PrintOverlays: false,
	})
	processor.GlobalPatterns = []string{"node_modules/", ".DS_Store"}
	processor.LocalPatterns = []string{".ssh/", ".env"}

	overlayMounts := processor.GetOverlayMounts(tmpDir, "/app")

	// Should get overlays for patterns that match existing files/dirs
	if len(overlayMounts) != 2 {
		t.Errorf("Expected 2 overlay mounts, but got %d", len(overlayMounts))
	}

	// Check that all mounts are bind mounts (no more tmpfs)
	for _, mount := range overlayMounts {
		if mount.Type != "bind" {
			t.Errorf("Expected all mounts to be 'bind' type, but got '%s' for %s", mount.Type, mount.ContainerPath)
		}
		if mount.HostPath == "" {
			t.Errorf("Expected non-empty HostPath for bind mount at %s", mount.ContainerPath)
		}

		// Verify that host paths exist
		if _, err := os.Stat(mount.HostPath); err != nil {
			t.Errorf("Host path %s does not exist: %v", mount.HostPath, err)
		}
	}

	// Check specific mounts
	foundSSH := false
	foundEnv := false
	for _, mount := range overlayMounts {
		if mount.ContainerPath == "/app/.ssh" {
			foundSSH = true
			// Verify it's an empty directory
			info, err := os.Stat(mount.HostPath)
			if err != nil {
				t.Errorf("Failed to stat SSH overlay directory: %v", err)
			} else if !info.IsDir() {
				t.Errorf("Expected SSH overlay to be a directory")
			}
		}
		if mount.ContainerPath == "/app/.env" {
			foundEnv = true
			// Verify it's an empty file
			info, err := os.Stat(mount.HostPath)
			if err != nil {
				t.Errorf("Failed to stat env overlay file: %v", err)
			} else if info.IsDir() {
				t.Errorf("Expected env overlay to be a file, not a directory")
			} else if info.Size() != 0 {
				t.Errorf("Expected env overlay file to be empty, but size is %d", info.Size())
			}
		}
	}

	if !foundSSH {
		t.Error("Expected to find overlay mount for /app/.ssh")
	}
	if !foundEnv {
		t.Error("Expected to find overlay mount for /app/.env")
	}

	// Clean up processor artifacts
	if err := processor.Cleanup(); err != nil {
		t.Errorf("Failed to cleanup processor: %v", err)
	}
}

func TestGetOverlayMountsConfinedToMount(t *testing.T) {
	t.Parallel()

	// Layout:
	//   root/ws/sub/secret        inside the mount
	//   root/ws/alias -> sub      symlink that stays inside the mount
	//   root/ws/link  -> outside  symlink that leaves the mount
	//   root/outside/secret       outside the mount
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "sub"), 0o750))
	require.NoError(t, os.MkdirAll(outside, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "sub", "secret"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), nil, 0o600))
	require.NoError(t, os.Symlink("sub", filepath.Join(ws, "alias")))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "link")))
	require.NoError(t, os.Symlink("missing", filepath.Join(ws, "dangling")))

	testCases := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{name: "path inside the mount", pattern: "sub/secret", expected: []string{"/app/sub/secret"}},
		{name: "symlink inside the mount maps to its target", pattern: "alias/secret", expected: []string{"/app/sub/secret"}},
		{name: "parent directory", pattern: "..", expected: nil},
		{name: "mount root", pattern: ".", expected: nil},
		{name: "mount root through a subdirectory", pattern: "sub/..", expected: nil},
		{name: "dangling symlink inside the mount", pattern: "dangling", expected: nil},
		{name: "sibling directory", pattern: "../outside", expected: nil},
		{name: "file beyond the mount", pattern: "../outside/secret", expected: nil},
		{name: "directory pattern beyond the mount", pattern: "../outside/", expected: nil},
		{name: "glob beyond the mount", pattern: "../out*", expected: nil},
		{name: "deep traversal clamped at root", pattern: "../../../../../../../../../../../../" + outside, expected: nil},
		{name: "symlink leaving the mount", pattern: "link", expected: nil},
		{name: "path through a symlink leaving the mount", pattern: "link/secret", expected: nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			processor := NewProcessor(&Config{})
			t.Cleanup(func() { _ = processor.Cleanup() })
			processor.LocalPatterns = []string{tc.pattern}

			var got []string
			for _, m := range processor.GetOverlayMounts(ws, "/app") {
				got = append(got, m.ContainerPath)
			}
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestResolveWithin(t *testing.T) {
	t.Parallel()

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	inside := filepath.Join(root, "ws")
	require.NoError(t, os.Mkdir(inside, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(inside, "file"), nil, 0o600))
	require.NoError(t, os.Symlink("missing", filepath.Join(inside, "dangling")))
	require.NoError(t, os.Symlink(root, filepath.Join(inside, "up")))

	testCases := []struct {
		name      string
		matchPath string
		wantReal  string
		wantRel   string
		wantErr   error
	}{
		{name: "match inside the mount", matchPath: filepath.Join(inside, "file"),
			wantReal: filepath.Join(inside, "file"), wantRel: "file"},
		{name: "mount root itself", matchPath: inside, wantReal: inside, wantRel: "."},
		{name: "symlink out of the mount", matchPath: filepath.Join(inside, "up"), wantErr: errOutsideMount},
		{name: "dangling symlink", matchPath: filepath.Join(inside, "dangling"), wantErr: os.ErrNotExist},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			realMatch, rel, err := resolveWithin(inside, tc.matchPath)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantReal, realMatch)
			require.Equal(t, tc.wantRel, rel)
		})
	}
}

func TestGetOverlayMountsMissingMount(t *testing.T) {
	t.Parallel()
	processor := NewProcessor(&Config{})
	processor.LocalPatterns = []string{"file"}
	require.Empty(t, processor.GetOverlayMounts(filepath.Join(t.TempDir(), "missing"), "/app"))
}

func TestRelWithin(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		dir     string
		target  string
		wantRel string
		wantOK  bool
	}{
		{name: "child", dir: "/app", target: "/app/a/b", wantRel: "a/b", wantOK: true},
		{name: "same directory", dir: "/app", target: "/app", wantRel: ".", wantOK: true},
		{name: "name starting with dots", dir: "/app", target: "/app/..hidden", wantRel: "..hidden", wantOK: true},
		{name: "parent", dir: "/app", target: "/"},
		{name: "sibling", dir: "/app", target: "/apple"},
		{name: "relative target against absolute dir", dir: "/app", target: "app/x"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rel, ok := relWithin(tc.dir, tc.target)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantRel, rel)
		})
	}
}

func TestProcessorCleanup(t *testing.T) {
	t.Parallel()
	// Create test directory structure
	tmpDir := t.TempDir()

	// Create test files and directories
	sshDir := filepath.Join(tmpDir, ".ssh")
	err := os.Mkdir(sshDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create .ssh directory: %v", err)
	}

	envFile := filepath.Join(tmpDir, ".env")
	err = os.WriteFile(envFile, []byte("TEST=value"), 0644)
	if err != nil {
		t.Fatalf("Failed to create .env file: %v", err)
	}

	processor := NewProcessor(&Config{
		LoadGlobal:    true,
		PrintOverlays: false,
	})
	processor.LocalPatterns = []string{".ssh/", ".env"}

	// Generate overlay mounts (which creates artifacts)
	overlayMounts := processor.GetOverlayMounts(tmpDir, "/app")

	// Collect artifact paths for verification
	var artifactPaths []string
	for _, mount := range overlayMounts {
		if mount.HostPath != "" {
			artifactPaths = append(artifactPaths, mount.HostPath)
		}
	}

	// Verify artifacts exist before cleanup
	for _, path := range artifactPaths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Expected artifact %s to exist before cleanup: %v", path, err)
		}
	}

	// Perform cleanup
	if err := processor.Cleanup(); err != nil {
		t.Errorf("Cleanup failed: %v", err)
	}

	// Verify artifacts are removed after cleanup
	for _, path := range artifactPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("Expected artifact %s to be removed after cleanup", path)
		}
	}
}

func TestShouldIgnore(t *testing.T) {
	t.Parallel()
	processor := NewProcessor(&Config{
		LoadGlobal:    true,
		PrintOverlays: false,
	})
	processor.GlobalPatterns = []string{"node_modules", "*.log"}
	processor.LocalPatterns = []string{".ssh", ".env"}

	testCases := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "matches global pattern",
			path:     "/some/path/node_modules",
			expected: true,
		},
		{
			name:     "matches local pattern",
			path:     "/home/user/.ssh",
			expected: true,
		},
		{
			name:     "matches glob pattern",
			path:     "/var/log/app.log",
			expected: true,
		},
		{
			name:     "does not match any pattern",
			path:     "/home/user/document.txt",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := processor.ShouldIgnore(tc.path)
			if result != tc.expected {
				t.Errorf("Expected ShouldIgnore('%s') to return %v, but got %v", tc.path, tc.expected, result)
			}
		})
	}
}

// TestGetOverlayMountsConcurrentCleanup guards against one processor's Cleanup
// removing artifact storage that another live processor is about to use.
func TestGetOverlayMountsConcurrentCleanup(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, ".ssh"), 0755); err != nil {
		t.Fatalf("Failed to create .ssh directory: %v", err)
	}

	const workers = 16
	const iterations = 50

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				processor := NewProcessor(&Config{LoadGlobal: false})
				processor.LocalPatterns = []string{".ssh/"}

				mounts := processor.GetOverlayMounts(tmpDir, "/app")
				if len(mounts) != 1 {
					t.Errorf("Expected 1 overlay mount, but got %d", len(mounts))
				}
				if err := processor.Cleanup(); err != nil {
					t.Errorf("Failed to cleanup processor: %v", err)
				}
			}
		}()
	}
	wg.Wait()
}
