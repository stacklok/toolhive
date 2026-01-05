package docker

import (
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stretchr/testify/assert"
)

func TestConvertRelativePathToAbsolute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		sourcePath     string
		expectedResult bool
		shouldMatch    bool // true if result should match input (absolute), false if should be different (relative)
	}{
		// WSL paths - should be detected as absolute and returned as-is
		{
			name:           "WSL C drive path",
			sourcePath:     "/mnt/c/Users/samu/Documents/toolhive",
			expectedResult: true,
			shouldMatch:    true,
		},
		{
			name:           "WSL D drive path",
			sourcePath:     "/mnt/d/projects/myproject",
			expectedResult: true,
			shouldMatch:    true,
		},
		{
			name:           "WSL minimal path",
			sourcePath:     "/mnt/c/",
			expectedResult: true,
			shouldMatch:    true,
		},
		// Traditional Unix absolute paths - should be detected as absolute
		{
			name:           "Unix home path",
			sourcePath:     "/home/user/data",
			expectedResult: true,
			shouldMatch:    true,
		},
		{
			name:           "Unix root path",
			sourcePath:     "/etc/config",
			expectedResult: true,
			shouldMatch:    true,
		},
		// Relative paths - should be converted to absolute
		{
			name:           "Relative path with dot",
			sourcePath:     "./relative/path",
			expectedResult: true,
			shouldMatch:    false, // Will be joined with CWD and cleaned
		},
		{
			name:           "Relative path without dot",
			sourcePath:     "relative/path",
			expectedResult: true,
			shouldMatch:    false, // Will be joined with CWD
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mountDecl := permissions.MountDeclaration(tt.sourcePath)

			resultPath, success := convertRelativePathToAbsolute(tt.sourcePath, mountDecl)

			// Check if function succeeded
			assert.Equal(t, tt.expectedResult, success, "convertRelativePathToAbsolute success")

			if tt.shouldMatch {
				// For absolute paths, result should match input
				assert.Equal(t, tt.sourcePath, resultPath, "absolute path should be returned as-is")
			} else {
				// For relative paths, result should be different (joined with CWD)
				assert.NotEqual(t, tt.sourcePath, resultPath, "relative path should be converted")
				// Result should contain the cleaned version of the path
				cleanedSource := strings.TrimPrefix(tt.sourcePath, "./")
				assert.Contains(t, resultPath, cleanedSource, "result should contain cleaned version of original path")
			}

			// Result should never be empty for successful conversions
			if success {
				assert.NotEmpty(t, resultPath, "successful conversion should not return empty path")
			}
		})
	}
}

func TestConvertRelativePathToAbsolute_WSLSpecific(t *testing.T) {
	t.Parallel()

	// Specific test for WSL paths that were problematic
	wslPaths := []string{
		"/mnt/c/Users/samu/Documents/toolhive",
		"/mnt/c/Users/user/Projects/myapp",
		"/mnt/d/workspace/project",
		"/mnt/c/Program Files/Docker",
		"/mnt/c/", // Edge case
	}

	for _, path := range wslPaths {
		t.Run("WSL_path_"+path, func(t *testing.T) {
			t.Parallel()
			mountDecl := permissions.MountDeclaration(path)

			resultPath, success := convertRelativePathToAbsolute(path, mountDecl)

			// WSL paths should be treated as absolute
			assert.True(t, success, "WSL path should be successfully processed")
			assert.Equal(t, path, resultPath, "WSL path should be returned unchanged")

			// Verify path was not modified (indicating it was treated as absolute)
			assert.NotContains(t, resultPath, "/Users/samu/Projects/toolhive", "WSL path should not be joined with current directory")
		})
	}
}
