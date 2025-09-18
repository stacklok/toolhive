package server

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
)

func TestParseRunServerArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		request  mcp.CallToolRequest
		expected *runServerArgs
		wantErr  bool
	}{
		{
			name: "valid args with all fields",
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]interface{}{
						"server": "test-server",
						"name":   "custom-name",
						"host":   "192.168.1.1",
						"env": map[string]interface{}{
							"KEY1": "value1",
							"KEY2": "value2",
						},
					},
				},
			},
			expected: &runServerArgs{
				Server: "test-server",
				Name:   "custom-name",
				Host:   "192.168.1.1",
				Env: map[string]string{
					"KEY1": "value1",
					"KEY2": "value2",
				},
			},
			wantErr: false,
		},
		{
			name: "minimal args - server only",
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]interface{}{
						"server": "test-server",
					},
				},
			},
			expected: &runServerArgs{
				Server: "test-server",
				Name:   "test-server", // Should default to server name
				Host:   "127.0.0.1",   // Should default to 127.0.0.1
				Env:    nil,
			},
			wantErr: false,
		},
		{
			name: "empty name defaults to server name",
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]interface{}{
						"server": "my-server",
						"name":   "",
					},
				},
			},
			expected: &runServerArgs{
				Server: "my-server",
				Name:   "my-server",
				Host:   "127.0.0.1",
				Env:    nil,
			},
			wantErr: false,
		},
		{
			name: "empty host defaults to 127.0.0.1",
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]interface{}{
						"server": "test-server",
						"host":   "",
					},
				},
			},
			expected: &runServerArgs{
				Server: "test-server",
				Name:   "test-server",
				Host:   "127.0.0.1",
				Env:    nil,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := parseRunServerArgs(tt.request)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestConfigureTransport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		imageMetadata     *registry.ImageMetadata
		expectedTransport string
	}{
		{
			name:              "nil metadata returns SSE",
			imageMetadata:     nil,
			expectedTransport: "sse",
		},
		{
			name: "metadata with empty transport returns SSE",
			imageMetadata: &registry.ImageMetadata{
				BaseServerMetadata: registry.BaseServerMetadata{
					Transport: "",
				},
			},
			expectedTransport: "sse",
		},
		{
			name: "metadata with stdio transport",
			imageMetadata: &registry.ImageMetadata{
				BaseServerMetadata: registry.BaseServerMetadata{
					Transport: "stdio",
				},
			},
			expectedTransport: "stdio",
		},
		{
			name: "metadata with streamable-http transport",
			imageMetadata: &registry.ImageMetadata{
				BaseServerMetadata: registry.BaseServerMetadata{
					Transport: "streamable-http",
				},
			},
			expectedTransport: "streamable-http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := []runner.RunConfigBuilderOption{}
			transport := configureTransport(&opts, tt.imageMetadata)

			assert.Equal(t, tt.expectedTransport, transport)
		})
	}
}

func TestPrepareEnvironmentVariables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		imageMetadata *registry.ImageMetadata
		userEnv       map[string]string
		expected      map[string]string
	}{
		{
			name:          "nil metadata and nil user env",
			imageMetadata: nil,
			userEnv:       nil,
			expected:      map[string]string{},
		},
		{
			name: "metadata with defaults, no user env",
			imageMetadata: &registry.ImageMetadata{
				EnvVars: []*registry.EnvVar{
					{Name: "VAR1", Default: "default1"},
					{Name: "VAR2", Default: "default2"},
				},
			},
			userEnv: nil,
			expected: map[string]string{
				"VAR1": "default1",
				"VAR2": "default2",
			},
		},
		{
			name: "metadata with defaults, user overrides",
			imageMetadata: &registry.ImageMetadata{
				EnvVars: []*registry.EnvVar{
					{Name: "VAR1", Default: "default1"},
					{Name: "VAR2", Default: "default2"},
				},
			},
			userEnv: map[string]string{
				"VAR1": "user1",
				"VAR3": "user3",
			},
			expected: map[string]string{
				"VAR1": "user1",
				"VAR2": "default2",
				"VAR3": "user3",
			},
		},
		{
			name:          "no metadata, only user env",
			imageMetadata: nil,
			userEnv: map[string]string{
				"USER_VAR": "user_value",
			},
			expected: map[string]string{
				"USER_VAR": "user_value",
			},
		},
		{
			name: "metadata with empty defaults ignored",
			imageMetadata: &registry.ImageMetadata{
				EnvVars: []*registry.EnvVar{
					{Name: "VAR1", Default: ""},
					{Name: "VAR2", Default: "value2"},
				},
			},
			userEnv: nil,
			expected: map[string]string{
				"VAR2": "value2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := prepareEnvironmentVariables(tt.imageMetadata, tt.userEnv)

			// Compare maps directly
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildServerConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	args := &runServerArgs{
		Server: "test-server",
		Name:   "test-name",
		Host:   "127.0.0.1",
		Env:    map[string]string{"TEST_VAR": "test_value"},
	}

	tests := []struct {
		name          string
		imageURL      string
		imageMetadata *registry.ImageMetadata
		expectError   bool
	}{
		{
			name:          "valid config with nil metadata",
			imageURL:      "test/image:latest",
			imageMetadata: nil,
			expectError:   false, // Actually succeeds because container runtime creation works
		},
		{
			name:     "valid config with metadata",
			imageURL: "test/image:latest",
			imageMetadata: &registry.ImageMetadata{
				BaseServerMetadata: registry.BaseServerMetadata{
					Transport: "stdio",
				},
				Image: "test/image:latest",
				Args:  []string{"--test"},
				EnvVars: []*registry.EnvVar{
					{Name: "DEFAULT_VAR", Default: "default_value"},
				},
			},
			expectError: false, // Actually succeeds and tests the type assertion line
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runConfig, err := buildServerConfig(ctx, args, tt.imageURL, tt.imageMetadata)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, runConfig)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, runConfig)
			}
		})
	}
}
