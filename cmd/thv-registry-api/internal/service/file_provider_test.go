package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/registry"
)

func TestFileRegistryDataProvider_GetRegistryData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name        string
		fileContent string
		wantErr     bool
		errContains string
		validate    func(*testing.T, *registry.Registry)
	}{
		{
			name: "valid registry file",
			fileContent: `{
				"version": "1.0",
				"last_updated": "2024-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"name": "test-server",
						"description": "A test server",
						"image": "test:latest"
					}
				},
				"remote_servers": {}
			}`,
			wantErr: false,
			validate: func(t *testing.T, reg *registry.Registry) {
				t.Helper()
				assert.Equal(t, "1.0", reg.Version)
				assert.Equal(t, "2024-01-01T00:00:00Z", reg.LastUpdated)
				assert.Len(t, reg.Servers, 1)
				assert.Contains(t, reg.Servers, "test-server")
				assert.Equal(t, "test-server", reg.Servers["test-server"].Name)
				assert.Equal(t, "A test server", reg.Servers["test-server"].Description)
				assert.Equal(t, "test:latest", reg.Servers["test-server"].Image)
			},
		},
		{
			name: "empty registry file",
			fileContent: `{
				"version": "1.0",
				"last_updated": "",
				"servers": {},
				"remote_servers": {}
			}`,
			wantErr: false,
			validate: func(t *testing.T, reg *registry.Registry) {
				t.Helper()
				assert.Equal(t, "1.0", reg.Version)
				assert.Len(t, reg.Servers, 0)
				assert.Len(t, reg.RemoteServers, 0)
			},
		},
		{
			name:        "invalid JSON",
			fileContent: `{invalid json}`,
			wantErr:     true,
			errContains: "failed to parse registry data",
		},
		{
			name:        "empty file",
			fileContent: "",
			wantErr:     true,
			errContains: "failed to parse registry data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create temporary file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "registry.json")

			err := os.WriteFile(tmpFile, []byte(tt.fileContent), 0644)
			require.NoError(t, err)

			// Create provider
			provider := NewFileRegistryDataProvider(tmpFile)

			// Test GetRegistryData
			result, err := provider.GetRegistryData(ctx)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				if tt.validate != nil {
					tt.validate(t, result)
				}
			}
		})
	}
}

func TestFileRegistryDataProvider_GetRegistryData_FileErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name        string
		filePath    string
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty file path",
			filePath:    "",
			wantErr:     true,
			errContains: "file path not configured",
		},
		{
			name:        "non-existent file",
			filePath:    "/non/existent/file.json",
			wantErr:     true,
			errContains: "registry file not found",
		},
		{
			name:        "directory instead of file",
			filePath:    t.TempDir(),
			wantErr:     true,
			errContains: "failed to read registry file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := NewFileRegistryDataProvider(tt.filePath)
			result, err := provider.GetRegistryData(ctx)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestFileRegistryDataProvider_GetSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filePath string
		expected string
	}{
		{
			name:     "absolute path",
			filePath: "/data/registry/registry.json",
			expected: "file:/data/registry/registry.json",
		},
		{
			name:     "relative path",
			filePath: "./registry.json",
			expected: "file:registry.json", // filepath.Clean removes ./
		},
		{
			name:     "empty path",
			filePath: "",
			expected: "file:<not-configured>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := NewFileRegistryDataProvider(tt.filePath)
			result := provider.GetSource()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNewFileRegistryDataProvider(t *testing.T) {
	t.Parallel()
	filePath := "/test/path/registry.json"
	provider := NewFileRegistryDataProvider(filePath)

	assert.NotNil(t, provider)
	assert.Equal(t, "file:/test/path/registry.json", provider.GetSource())
}

func TestFileRegistryDataProvider_GetRegistryName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filePath string
		expected string
	}{
		{
			name:     "file with extension",
			filePath: "/data/registry/my-registry.json",
			expected: "my-registry",
		},
		{
			name:     "file without extension",
			filePath: "/data/registry/registry",
			expected: "registry",
		},
		{
			name:     "nested path with extension",
			filePath: "/very/deep/path/test-registry.json",
			expected: "test-registry",
		},
		{
			name:     "relative path",
			filePath: "./registry.json",
			expected: "registry",
		},
		{
			name:     "empty path",
			filePath: "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := NewFileRegistryDataProvider(tt.filePath)
			result := provider.GetRegistryName()
			assert.Equal(t, tt.expected, result)
		})
	}
}
