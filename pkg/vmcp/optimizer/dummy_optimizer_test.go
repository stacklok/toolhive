package optimizer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
)

func TestDummyOptimizer_FindTool(t *testing.T) {
	t.Parallel()

	tools := []vmcp.Tool{
		{
			Name:        "fetch_url",
			Description: "Fetch content from a URL",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string"},
				},
			},
			BackendID: "backend1",
		},
		{
			Name:        "read_file",
			Description: "Read a file from the filesystem",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
			BackendID: "backend2",
		},
		{
			Name:        "write_file",
			Description: "Write content to a file",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
			},
			BackendID: "backend2",
		},
	}

	ctrl := gomock.NewController(t)
	mockRouter := routermocks.NewMockRouter(ctrl)
	mockClient := vmcpmocks.NewMockBackendClient(ctrl)

	opt := NewDummyOptimizer(tools, mockRouter, mockClient)

	tests := []struct {
		name          string
		input         FindToolInput
		expectedNames []string
		expectedError bool
		errorContains string
	}{
		{
			name: "find by exact name",
			input: FindToolInput{
				ToolDescription: "fetch_url",
			},
			expectedNames: []string{"fetch_url"},
		},
		{
			name: "find by description substring",
			input: FindToolInput{
				ToolDescription: "file",
			},
			expectedNames: []string{"read_file", "write_file"},
		},
		{
			name: "case insensitive search",
			input: FindToolInput{
				ToolDescription: "FETCH",
			},
			expectedNames: []string{"fetch_url"},
		},
		{
			name: "no matches",
			input: FindToolInput{
				ToolDescription: "nonexistent",
			},
			expectedNames: []string{},
		},
		{
			name:          "empty description",
			input:         FindToolInput{},
			expectedError: true,
			errorContains: "tool_description is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := opt.FindTool(context.Background(), tc.input)

			if tc.expectedError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorContains)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			// Extract names from results
			var names []string
			for _, match := range result.Tools {
				names = append(names, match.Name)
			}

			assert.ElementsMatch(t, tc.expectedNames, names)
		})
	}
}

func TestDummyOptimizer_CallTool(t *testing.T) {
	t.Parallel()

	tools := []vmcp.Tool{
		{
			Name:        "test_tool",
			Description: "A test tool",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{"type": "string"},
				},
			},
			BackendID: "backend1",
		},
	}

	tests := []struct {
		name           string
		input          CallToolInput
		setupMocks     func(*routermocks.MockRouter, *vmcpmocks.MockBackendClient)
		expectedResult map[string]any
		expectedError  bool
		errorContains  string
	}{
		{
			name: "successful tool call",
			input: CallToolInput{
				ToolName:   "test_tool",
				Parameters: map[string]any{"input": "hello"},
			},
			setupMocks: func(r *routermocks.MockRouter, c *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID:   "backend1",
					WorkloadName: "backend1",
					BaseURL:      "http://localhost:8080",
				}
				r.EXPECT().RouteTool(gomock.Any(), "test_tool").Return(target, nil)
				c.EXPECT().CallTool(gomock.Any(), target, "test_tool", map[string]any{"input": "hello"}).
					Return(map[string]any{
						"content": []any{
							map[string]any{"type": "text", "text": "Hello, World!"},
						},
					}, nil)
			},
			expectedResult: map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "Hello, World!"},
				},
			},
		},
		{
			name: "tool not found",
			input: CallToolInput{
				ToolName:   "nonexistent",
				Parameters: map[string]any{},
			},
			setupMocks:    func(_ *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {},
			expectedError: true,
			errorContains: "tool not found: nonexistent",
		},
		{
			name: "empty tool name",
			input: CallToolInput{
				Parameters: map[string]any{},
			},
			setupMocks:    func(_ *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {},
			expectedError: true,
			errorContains: "tool_name is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockRouter := routermocks.NewMockRouter(ctrl)
			mockClient := vmcpmocks.NewMockBackendClient(ctrl)

			tc.setupMocks(mockRouter, mockClient)

			opt := NewDummyOptimizer(tools, mockRouter, mockClient)
			result, err := opt.CallTool(context.Background(), tc.input)

			if tc.expectedError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}
