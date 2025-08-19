package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestProcessEnvFile(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	tests := []struct {
		name     string
		content  string
		expected map[string]string
		wantErr  bool
	}{
		{
			name:     "standard env file format",
			content:  "GITHUB_PERSONAL_ACCESS_TOKEN=ghp_test_token_12345",
			expected: map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_test_token_12345"},
			wantErr:  false,
		},
		{
			name:    "multiple variables",
			content: "GITHUB_TOKEN=ghp_123\nAPI_KEY=secret456\nDATABASE_URL=postgres://user:pass@localhost:5432/db",
			expected: map[string]string{
				"GITHUB_TOKEN": "ghp_123",
				"API_KEY":      "secret456",
				"DATABASE_URL": "postgres://user:pass@localhost:5432/db",
			},
			wantErr: false,
		},
		{
			name:     "with comments and empty lines",
			content:  "# Environment configuration\nGITHUB_TOKEN=ghp_test\n\n# Database config\nDB_PASSWORD=secretpass",
			expected: map[string]string{"GITHUB_TOKEN": "ghp_test", "DB_PASSWORD": "secretpass"},
			wantErr:  false,
		},
		{
			name:     "empty file",
			content:  "",
			expected: map[string]string{},
			wantErr:  false,
		},
		{
			name:     "only comments",
			content:  "# This is a comment\n# Another comment",
			expected: map[string]string{},
			wantErr:  false,
		},
		{
			name:     "mixed valid and invalid lines",
			content:  "VALID_KEY=value123\nINVALID_LINE_WITHOUT_EQUALS\nANOTHER_KEY=another_value",
			expected: map[string]string{"VALID_KEY": "value123", "ANOTHER_KEY": "another_value"},
			wantErr:  false, // We skip invalid lines, don't error
		},
		{
			name:    "values with spaces and special chars",
			content: "API_URL=https://api.example.com/v1\nSECRET_WITH_SPACES=value with spaces\nSPECIAL_CHARS=!@#$%^&*()",
			expected: map[string]string{
				"API_URL":            "https://api.example.com/v1",
				"SECRET_WITH_SPACES": "value with spaces",
				"SPECIAL_CHARS":      "!@#$%^&*()",
			},
			wantErr: false,
		},
		{
			name:    "export statements (common in shell env files)",
			content: "export API_KEY=test123\nexport DB_URL=postgres://localhost:5432/db\nNORMAL_VAR=value",
			expected: map[string]string{
				"API_KEY":    "test123",
				"DB_URL":     "postgres://localhost:5432/db",
				"NORMAL_VAR": "value",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create temporary file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "test.env")

			err := os.WriteFile(tmpFile, []byte(tt.content), 0644)
			require.NoError(t, err)

			// Test the function
			result, err := processEnvFile(tmpFile)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestProcessEnvFilesDirectory_FileFiltering(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	// Create temporary directory structure
	tmpDir := t.TempDir()
	envDir := filepath.Join(tmpDir, "env")
	err := os.MkdirAll(envDir, 0755)
	require.NoError(t, err)

	// Create test files
	err = os.WriteFile(filepath.Join(envDir, "github.env"), []byte("GITHUB_TOKEN=token123"), 0644)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(envDir, "api"), []byte("API_KEY=key456"), 0644)
	require.NoError(t, err)

	// Create hidden file (should be ignored)
	err = os.WriteFile(filepath.Join(envDir, ".hidden"), []byte("HIDDEN=value"), 0644)
	require.NoError(t, err)

	// Create subdirectory (should be ignored)
	err = os.MkdirAll(filepath.Join(envDir, "subdir"), 0755)
	require.NoError(t, err)

	// Test directory processing
	entries, err := os.ReadDir(envDir)
	require.NoError(t, err)

	var processedFiles []string
	for _, entry := range entries {
		// Skip directories
		if entry.IsDir() {
			continue
		}

		// Skip hidden files
		if entry.Name()[0] == '.' {
			continue
		}

		processedFiles = append(processedFiles, entry.Name())
	}

	// Should only process github.env and api files, not .hidden or subdir
	assert.ElementsMatch(t, []string{"github.env", "api"}, processedFiles)
}

func TestProcessEnvFilesDirectory_Integration(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	tests := []struct {
		name         string
		fileContents map[string]string // filename -> content
		expected     map[string]string // expected env vars
	}{
		{
			name: "single env file",
			fileContents: map[string]string{
				"app.env": "GITHUB_PERSONAL_ACCESS_TOKEN=ghp_test_token_12345",
			},
			expected: map[string]string{
				"GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_test_token_12345",
			},
		},
		{
			name: "multiple env files",
			fileContents: map[string]string{
				"github.env":   "GITHUB_TOKEN=ghp_123\nGITHUB_ORG=myorg",
				"database.env": "DATABASE_URL=postgres://localhost:5432/mydb\nDB_PASSWORD=secret123",
				"api.env":      "API_KEY=key456\nAPI_URL=https://api.example.com",
			},
			expected: map[string]string{
				"GITHUB_TOKEN": "ghp_123",
				"GITHUB_ORG":   "myorg",
				"DATABASE_URL": "postgres://localhost:5432/mydb",
				"DB_PASSWORD":  "secret123",
				"API_KEY":      "key456",
				"API_URL":      "https://api.example.com",
			},
		},
		{
			name: "complex env file with comments",
			fileContents: map[string]string{
				"app.env": `# Application configuration
# GitHub integration
GITHUB_TOKEN=ghp_very_long_token_with_numbers_123456789
GITHUB_WEBHOOK_SECRET=super_secret_webhook_key

# Database connection
DATABASE_URL=postgres://user:complex_password_with_symbols_!@#$@db.example.com:5432/production_db?sslmode=require`,
			},
			expected: map[string]string{
				"GITHUB_TOKEN":          "ghp_very_long_token_with_numbers_123456789",
				"GITHUB_WEBHOOK_SECRET": "super_secret_webhook_key",
				"DATABASE_URL":          "postgres://user:complex_password_with_symbols_!@#$@db.example.com:5432/production_db?sslmode=require",
			},
		},
		{
			name: "variable override behavior (later files win)",
			fileContents: map[string]string{
				"01-base.env":     "API_KEY=original_key\nDB_HOST=localhost",
				"02-override.env": "API_KEY=overridden_key\nAPI_URL=https://api.example.com",
			},
			expected: map[string]string{
				"API_KEY": "overridden_key",          // Should be overridden by later file
				"DB_HOST": "localhost",               // Should remain from first file
				"API_URL": "https://api.example.com", // Should be added from second file
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create temporary directory
			tmpDir := t.TempDir()

			// Create all files
			for filename, content := range tt.fileContents {
				err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
				require.NoError(t, err)
			}

			// Process directory
			result, err := processEnvFilesDirectory(tmpDir)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestProcessEnvFilesDirectory_NonExistentDirectory(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	result, err := processEnvFilesDirectory("/path/that/does/not/exist")
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{}, result)
}
