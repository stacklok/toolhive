package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

func TestToolFilterMiddleware_Handler(t *testing.T) {
	t.Parallel()

	// Create a mock middleware function
	mockMiddlewareFunc := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Mock implementation
			next.ServeHTTP(w, r)
		})
	}

	// Create middleware instance
	middleware := &ToolFilterMiddleware{
		middleware: mockMiddlewareFunc,
	}

	// Test that Handler returns the correct middleware function
	handlerFunc := middleware.Handler()

	// Verify that the handler function is not nil
	assert.NotNil(t, handlerFunc)
	// Verify it returns the stored middleware function by checking if it's the same function
	assert.Equal(t, fmt.Sprintf("%p", mockMiddlewareFunc), fmt.Sprintf("%p", handlerFunc))
}

func TestToolFilterMiddleware_Close(t *testing.T) {
	t.Parallel()

	middleware := &ToolFilterMiddleware{}

	// Test that Close returns nil (no cleanup needed)
	err := middleware.Close()
	assert.NoError(t, err)
}

func TestCreateToolFilterMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *types.MiddlewareConfig
		setupMock     func(*mocks.MockMiddlewareRunner)
		expectedError bool
		errorContains string
	}{
		{
			name: "success with full parameters",
			config: func() *types.MiddlewareConfig {
				params := ToolFilterMiddlewareParams{
					FilterTools: []string{"tool1", "tool2"},
					ToolsOverride: map[string]ToolOverride{
						"tool1": {
							Name:        "tool1",
							Description: "Description for tool1",
						},
					},
				}
				paramsJSON, _ := json.Marshal(params)
				return &types.MiddlewareConfig{
					Type:       ToolFilterMiddlewareType,
					Parameters: paramsJSON,
				}
			}(),
			setupMock: func(mockRunner *mocks.MockMiddlewareRunner) {
				mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Do(func(_ string, mw types.Middleware) {
					_, ok := mw.(*ToolFilterMiddleware)
					assert.True(t, ok, "Expected middleware to be of type *ToolFilterMiddleware")
				})
			},
			expectedError: false,
		},
		{
			name: "success with empty parameters",
			config: func() *types.MiddlewareConfig {
				params := ToolFilterMiddlewareParams{
					FilterTools: []string{"default-tool"},
				}
				paramsJSON, _ := json.Marshal(params)
				return &types.MiddlewareConfig{
					Type:       ToolFilterMiddlewareType,
					Parameters: paramsJSON,
				}
			}(),
			setupMock: func(mockRunner *mocks.MockMiddlewareRunner) {
				mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Do(func(_ string, mw types.Middleware) {
					_, ok := mw.(*ToolFilterMiddleware)
					assert.True(t, ok, "Expected middleware to be of type *ToolFilterMiddleware")
				})
			},
			expectedError: false,
		},
		{
			name: "invalid parameters",
			config: &types.MiddlewareConfig{
				Type:       ToolFilterMiddlewareType,
				Parameters: []byte(`{"invalid": json`), // Invalid JSON
			},
			setupMock: func(_ *mocks.MockMiddlewareRunner) {
				// No expectations for invalid parameters
			},
			expectedError: true,
			errorContains: "failed to unmarshal tool filter middleware parameters",
		},
		{
			name: "nil parameters",
			config: &types.MiddlewareConfig{
				Type:       ToolFilterMiddlewareType,
				Parameters: nil,
			},
			setupMock: func(_ *mocks.MockMiddlewareRunner) {
				// No expectations for nil parameters
			},
			expectedError: true,
			errorContains: "failed to unmarshal tool filter middleware parameters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
			tt.setupMock(mockRunner)

			err := CreateToolFilterMiddleware(tt.config, mockRunner)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCreateToolCallFilterMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *types.MiddlewareConfig
		setupMock     func(*mocks.MockMiddlewareRunner)
		expectedError bool
		errorContains string
	}{
		{
			name: "success with full parameters",
			config: func() *types.MiddlewareConfig {
				params := ToolFilterMiddlewareParams{
					FilterTools: []string{"tool1", "tool2"},
					ToolsOverride: map[string]ToolOverride{
						"tool1": {
							Name:        "tool1",
							Description: "Description for tool1",
						},
					},
				}
				paramsJSON, _ := json.Marshal(params)
				return &types.MiddlewareConfig{
					Type:       ToolCallFilterMiddlewareType,
					Parameters: paramsJSON,
				}
			}(),
			setupMock: func(mockRunner *mocks.MockMiddlewareRunner) {
				mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Do(func(_ string, mw types.Middleware) {
					_, ok := mw.(*ToolFilterMiddleware)
					assert.True(t, ok, "Expected middleware to be of type *ToolFilterMiddleware")
				})
			},
			expectedError: false,
		},
		{
			name: "success with empty parameters",
			config: func() *types.MiddlewareConfig {
				params := ToolFilterMiddlewareParams{
					FilterTools: []string{"default-tool"},
				}
				paramsJSON, _ := json.Marshal(params)
				return &types.MiddlewareConfig{
					Type:       ToolCallFilterMiddlewareType,
					Parameters: paramsJSON,
				}
			}(),
			setupMock: func(mockRunner *mocks.MockMiddlewareRunner) {
				mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Do(func(_ string, mw types.Middleware) {
					_, ok := mw.(*ToolFilterMiddleware)
					assert.True(t, ok, "Expected middleware to be of type *ToolFilterMiddleware")
				})
			},
			expectedError: false,
		},
		{
			name: "invalid parameters",
			config: &types.MiddlewareConfig{
				Type:       ToolCallFilterMiddlewareType,
				Parameters: []byte(`{"invalid": json`), // Invalid JSON
			},
			setupMock: func(_ *mocks.MockMiddlewareRunner) {
				// No expectations for invalid parameters
			},
			expectedError: true,
			errorContains: "failed to unmarshal tool call filter middleware parameters",
		},
		{
			name: "nil parameters",
			config: &types.MiddlewareConfig{
				Type:       ToolCallFilterMiddlewareType,
				Parameters: nil,
			},
			setupMock: func(_ *mocks.MockMiddlewareRunner) {
				// No expectations for nil parameters
			},
			expectedError: true,
			errorContains: "failed to unmarshal tool call filter middleware parameters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
			tt.setupMock(mockRunner)

			err := CreateToolCallFilterMiddleware(tt.config, mockRunner)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestToolOverride_JSON(t *testing.T) {
	t.Parallel()

	// Test JSON marshaling/unmarshaling of ToolOverride
	original := ToolOverride{
		Name:        "test-tool",
		Description: "Test tool description",
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(original)
	require.NoError(t, err)

	// Unmarshal from JSON
	var unmarshaled ToolOverride
	err = json.Unmarshal(jsonData, &unmarshaled)
	require.NoError(t, err)

	// Verify the data is preserved
	assert.Equal(t, original.Name, unmarshaled.Name)
	assert.Equal(t, original.Description, unmarshaled.Description)
}

func TestToolFilterMiddlewareParams_JSON(t *testing.T) {
	t.Parallel()

	// Test JSON marshaling/unmarshaling of ToolFilterMiddlewareParams
	original := ToolFilterMiddlewareParams{
		FilterTools: []string{"tool1", "tool2", "tool3"},
		ToolsOverride: map[string]ToolOverride{
			"tool1": {
				Name:        "tool1",
				Description: "Description for tool1",
			},
			"tool2": {
				Name:        "tool2",
				Description: "Description for tool2",
			},
		},
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(original)
	require.NoError(t, err)

	// Unmarshal from JSON
	var unmarshaled ToolFilterMiddlewareParams
	err = json.Unmarshal(jsonData, &unmarshaled)
	require.NoError(t, err)

	// Verify the data is preserved
	assert.Equal(t, original.FilterTools, unmarshaled.FilterTools)
	assert.Equal(t, len(original.ToolsOverride), len(unmarshaled.ToolsOverride))

	// Verify specific tool overrides
	assert.Equal(t, original.ToolsOverride["tool1"].Name, unmarshaled.ToolsOverride["tool1"].Name)
	assert.Equal(t, original.ToolsOverride["tool1"].Description, unmarshaled.ToolsOverride["tool1"].Description)
	assert.Equal(t, original.ToolsOverride["tool2"].Name, unmarshaled.ToolsOverride["tool2"].Name)
	assert.Equal(t, original.ToolsOverride["tool2"].Description, unmarshaled.ToolsOverride["tool2"].Description)
}

func TestMiddleware_InterfaceCompliance(t *testing.T) {
	t.Parallel()

	// Test that ToolFilterMiddleware implements the types.Middleware interface
	var _ types.Middleware = (*ToolFilterMiddleware)(nil)
}
