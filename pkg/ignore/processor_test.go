package ignore

import (
	"os"
	"path/filepath"
	"testing"

	log "github.com/stacklok/toolhive/pkg/logger"
)

func TestNewProcessor(t *testing.T) {
	t.Parallel()

	logger := log.NewLogger()

	processor := NewProcessor(&Config{
		LoadGlobal:    true,
		PrintOverlays: false,
	}, logger)
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

	logger := log.NewLogger()

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
			}, logger)
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

	logger := log.NewLogger()

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
			}, logger)
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

	// Setup logger
	logger := log.NewLogger()

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
	}, logger)

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

	logger := log.NewLogger()

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
	}, logger)
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
}

func TestShouldIgnore(t *testing.T) {
	t.Parallel()

	logger := log.NewLogger()

	processor := NewProcessor(&Config{
		LoadGlobal:    true,
		PrintOverlays: false,
	}, logger)
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
