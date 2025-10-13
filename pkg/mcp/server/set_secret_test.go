package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	configmocks "github.com/stacklok/toolhive/pkg/config/mocks"
	registrymocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func TestHandler_SetSecret(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	// Create a temporary directory for test files
	tempDir := t.TempDir()

	// Create test files
	validFile := filepath.Join(tempDir, "valid_secret.txt")
	err := os.WriteFile(validFile, []byte("my-secret-value\n"), 0600)
	require.NoError(t, err)

	emptyFile := filepath.Join(tempDir, "empty_secret.txt")
	err = os.WriteFile(emptyFile, []byte("   \n  \n"), 0600)
	require.NoError(t, err)

	largeFile := filepath.Join(tempDir, "large_secret.txt")
	largeContent := make([]byte, 2*1024*1024) // 2MB
	for i := range largeContent {
		largeContent[i] = 'a'
	}
	err = os.WriteFile(largeFile, largeContent, 0600)
	require.NoError(t, err)

	nonExistentFile := filepath.Join(tempDir, "nonexistent.txt")

	tests := []struct {
		name        string
		args        map[string]interface{}
		setupMocks  func(*configmocks.MockProvider)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "missing secret name",
			args: map[string]interface{}{
				"file_path": validFile,
			},
			setupMocks: func(_ *configmocks.MockProvider) {},
			wantErr:    false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
		{
			name: "missing file path",
			args: map[string]interface{}{
				"name": "test-secret",
			},
			setupMocks: func(_ *configmocks.MockProvider) {},
			wantErr:    false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
		{
			name: "file does not exist",
			args: map[string]interface{}{
				"name":      "test-secret",
				"file_path": nonExistentFile,
			},
			setupMocks: func(_ *configmocks.MockProvider) {},
			wantErr:    false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
		{
			name: "empty file content",
			args: map[string]interface{}{
				"name":      "test-secret",
				"file_path": emptyFile,
			},
			setupMocks: func(_ *configmocks.MockProvider) {},
			wantErr:    false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
		{
			name: "file too large",
			args: map[string]interface{}{
				"name":      "test-secret",
				"file_path": largeFile,
			},
			setupMocks: func(_ *configmocks.MockProvider) {},
			wantErr:    false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
		{
			name: "secrets not setup",
			args: map[string]interface{}{
				"name":      "test-secret",
				"file_path": validFile,
			},
			setupMocks: func(configProvider *configmocks.MockProvider) {
				// Mock config setup - not completed
				cfg := &config.Config{
					Secrets: config.Secrets{
						SetupCompleted: false,
					},
				}
				configProvider.EXPECT().GetConfig().Return(cfg).AnyTimes()
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mocks
			mockRegistry := registrymocks.NewMockProvider(ctrl)
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockConfigProvider := configmocks.NewMockProvider(ctrl)

			// Setup mocks
			if tt.setupMocks != nil {
				tt.setupMocks(mockConfigProvider)
			}

			handler := &Handler{
				ctx:              context.Background(),
				workloadManager:  mockWorkloadManager,
				registryProvider: mockRegistry,
				configProvider:   mockConfigProvider,
			}

			// Marshal arguments to JSON
			argsJSON, _ := json.Marshal(tt.args)

			request := &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name:      "set_secret",
					Arguments: argsJSON,
				},
			}

			result, err := handler.SetSecret(context.Background(), request)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}
