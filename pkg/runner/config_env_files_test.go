package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestRunConfig_WithEnvFile(t *testing.T) {
	t.Parallel()

	// Initialize logger to prevent nil pointer dereference
	logger.Initialize()

	tests := []struct {
		name            string
		initialEnvVars  map[string]string
		fileContent     string
		expectedEnvVars map[string]string
		wantErr         bool
	}{
		{
			name:           "basic env file",
			initialEnvVars: nil,
			fileContent:    "API_KEY=test123\nDB_URL=postgres://localhost:5432/db",
			expectedEnvVars: map[string]string{
				"API_KEY": "test123",
				"DB_URL":  "postgres://localhost:5432/db",
			},
		},
		{
			name:            "env file with existing env vars",
			initialEnvVars:  map[string]string{"EXISTING": "value"},
			fileContent:     "API_KEY=test123",
			expectedEnvVars: map[string]string{"EXISTING": "value", "API_KEY": "test123"},
		},
		{
			name:            "env file overrides existing",
			initialEnvVars:  map[string]string{"API_KEY": "old_value"},
			fileContent:     "API_KEY=new_value",
			expectedEnvVars: map[string]string{"API_KEY": "new_value"},
		},
		{
			name:            "empty env file",
			initialEnvVars:  map[string]string{"EXISTING": "value"},
			fileContent:     "# Just comments\n",
			expectedEnvVars: map[string]string{"EXISTING": "value"},
		},
		{
			name:           "env file with export statements",
			initialEnvVars: nil,
			fileContent:    "export API_KEY=test123\nexport DB_URL=postgres://localhost:5432/db\nNORMAL=value",
			expectedEnvVars: map[string]string{
				"API_KEY": "test123",
				"DB_URL":  "postgres://localhost:5432/db",
				"NORMAL":  "value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create temporary env file
			tmpDir := t.TempDir()
			envFile := filepath.Join(tmpDir, "test.env")
			err := os.WriteFile(envFile, []byte(tt.fileContent), 0644)
			require.NoError(t, err)

			// Create RunConfig
			config := &RunConfig{
				EnvVars: tt.initialEnvVars,
			}

			// Call WithEnvFile
			result, err := config.WithEnvFile(envFile)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, config, result) // Should return same instance
			assert.Equal(t, tt.expectedEnvVars, config.EnvVars)
		})
	}
}

func TestRunConfig_WithEnvFilesFromDirectory(t *testing.T) {
	t.Parallel()

	// Initialize logger to prevent nil pointer dereference
	logger.Initialize()

	tests := []struct {
		name            string
		initialEnvVars  map[string]string
		files           map[string]string // filename -> content
		expectedEnvVars map[string]string
		wantErr         bool
	}{
		{
			name:           "single env file",
			initialEnvVars: nil,
			files: map[string]string{
				"app.env": "API_KEY=test123",
			},
			expectedEnvVars: map[string]string{"API_KEY": "test123"},
		},
		{
			name:           "multiple env files",
			initialEnvVars: nil,
			files: map[string]string{
				"app.env": "API_KEY=test123",
				"db.env":  "DB_URL=postgres://localhost:5432/db",
			},
			expectedEnvVars: map[string]string{
				"API_KEY": "test123",
				"DB_URL":  "postgres://localhost:5432/db",
			},
		},
		{
			name:           "files override each other",
			initialEnvVars: nil,
			files: map[string]string{
				"01-base.env":     "API_KEY=original\nDB_HOST=localhost",
				"02-override.env": "API_KEY=overridden",
			},
			expectedEnvVars: map[string]string{
				"API_KEY": "overridden",
				"DB_HOST": "localhost",
			},
		},
		{
			name:            "merge with existing env vars",
			initialEnvVars:  map[string]string{"EXISTING": "value"},
			files:           map[string]string{"app.env": "API_KEY=test123"},
			expectedEnvVars: map[string]string{"EXISTING": "value", "API_KEY": "test123"},
		},
		{
			name:            "nonexistent directory",
			initialEnvVars:  map[string]string{"EXISTING": "value"},
			files:           nil, // Will use nonexistent directory path
			expectedEnvVars: map[string]string{"EXISTING": "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var dirPath string
			if tt.files != nil {
				// Create temporary directory with files
				tmpDir := t.TempDir()
				dirPath = tmpDir
				for filename, content := range tt.files {
					err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
					require.NoError(t, err)
				}
			} else {
				// Use nonexistent directory
				dirPath = "/path/that/does/not/exist"
			}

			// Create RunConfig
			config := &RunConfig{
				EnvVars: tt.initialEnvVars,
			}

			// Call WithEnvFilesFromDirectory
			result, err := config.WithEnvFilesFromDirectory(dirPath)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, config, result) // Should return same instance
			assert.Equal(t, tt.expectedEnvVars, config.EnvVars)
		})
	}
}

func TestRunConfig_WithEnvFile_ErrorHandling(t *testing.T) {
	t.Parallel()

	// Initialize logger to prevent nil pointer dereference
	logger.Initialize()

	config := &RunConfig{}

	// Test with nonexistent file
	_, err := config.WithEnvFile("/path/that/does/not/exist.env")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to process env file")
}
