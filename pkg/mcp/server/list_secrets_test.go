package server

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	configmocks "github.com/stacklok/toolhive/pkg/config/mocks"
	registrymocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func TestHandler_ListSecrets(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	tests := []struct {
		name        string
		setupMocks  func(*configmocks.MockProvider)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "secrets not setup",
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

			request := &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name:      "list_secrets",
					Arguments: []byte("{}"),
				},
			}

			result, err := handler.ListSecrets(context.Background(), request)

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
