// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter_test

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter/mocks"
)

func TestCapabilityAdapter_ToSDKTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		tools       []vmcp.Tool
		setupMocks  func(*mocks.MockHandlerFactory)
		wantErr     bool
		wantNil     bool
		checkResult func(*testing.T, []server.ServerTool)
	}{
		{
			name: "successful conversion with single tool",
			tools: []vmcp.Tool{
				{
					Name:        "test_tool",
					Description: "Test tool description",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"input": map[string]any{"type": "string"},
						},
					},
					BackendID: "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateToolHandler("test_tool").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
			},
			wantErr: false,
			wantNil: false,
			checkResult: func(t *testing.T, result []server.ServerTool) {
				t.Helper()
				require.Len(t, result, 1)
				assert.Equal(t, "test_tool", result[0].Tool.Name)
				assert.Equal(t, "Test tool description", result[0].Tool.Description)
				assert.NotNil(t, result[0].Tool.RawInputSchema)
				assert.NotNil(t, result[0].Handler)

				// Verify schema is properly JSON-marshaled
				assert.Contains(t, string(result[0].Tool.RawInputSchema), `"type":"object"`)
				assert.Contains(t, string(result[0].Tool.RawInputSchema), `"properties"`)
			},
		},
		{
			name: "successful conversion with multiple tools",
			tools: []vmcp.Tool{
				{
					Name:        "tool_one",
					Description: "First tool",
					InputSchema: map[string]any{"type": "object"},
					BackendID:   "backend1",
				},
				{
					Name:        "tool_two",
					Description: "Second tool",
					InputSchema: map[string]any{"type": "string"},
					BackendID:   "backend2",
				},
				{
					Name:        "tool_three",
					Description: "Third tool",
					InputSchema: map[string]any{"type": "number"},
					BackendID:   "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateToolHandler("tool_one").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
				mf.EXPECT().CreateToolHandler("tool_two").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
				mf.EXPECT().CreateToolHandler("tool_three").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
			},
			wantErr: false,
			wantNil: false,
			checkResult: func(t *testing.T, result []server.ServerTool) {
				t.Helper()
				require.Len(t, result, 3)

				// Verify all tools converted correctly
				assert.Equal(t, "tool_one", result[0].Tool.Name)
				assert.Equal(t, "First tool", result[0].Tool.Description)
				assert.NotNil(t, result[0].Handler)

				assert.Equal(t, "tool_two", result[1].Tool.Name)
				assert.Equal(t, "Second tool", result[1].Tool.Description)
				assert.NotNil(t, result[1].Handler)

				assert.Equal(t, "tool_three", result[2].Tool.Name)
				assert.Equal(t, "Third tool", result[2].Tool.Description)
				assert.NotNil(t, result[2].Handler)
			},
		},
		{
			name:    "empty tools slice returns nil",
			tools:   []vmcp.Tool{},
			wantNil: true,
		},
		{
			// This test verifies that JSON Schema fields from issue #2775
			// (description, default, required) are preserved when converting to MCP SDK format
			name: "preserves JSON Schema fields (issue #2775)",
			tools: []vmcp.Tool{
				{
					Name:        "deploy_app",
					Description: "Deploy an application",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"environment": map[string]any{
								"type":        "string",
								"description": "Target deployment environment",
								"default":     "staging",
							},
							"replicas": map[string]any{
								"type":        "integer",
								"description": "Number of pod replicas",
								"default":     3,
							},
						},
						"required": []any{"environment"},
					},
					BackendID: "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateToolHandler("deploy_app").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
			},
			wantErr: false,
			wantNil: false,
			checkResult: func(t *testing.T, result []server.ServerTool) {
				t.Helper()
				require.Len(t, result, 1)

				schema := string(result[0].Tool.RawInputSchema)

				// Verify description fields are preserved
				assert.Contains(t, schema, `"description":"Target deployment environment"`,
					"environment description should be preserved")
				assert.Contains(t, schema, `"description":"Number of pod replicas"`,
					"replicas description should be preserved")

				// Verify default fields are preserved
				assert.Contains(t, schema, `"default":"staging"`,
					"environment default should be preserved")
				assert.Contains(t, schema, `"default":3`,
					"replicas default should be preserved")

				// Verify required array is preserved
				assert.Contains(t, schema, `"required":["environment"]`,
					"required array should be preserved")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockFactory := mocks.NewMockHandlerFactory(ctrl)
			if tt.setupMocks != nil {
				tt.setupMocks(mockFactory)
			}

			adapter := adapter.NewCapabilityAdapter(mockFactory)
			result, err := adapter.ToSDKTools(tt.tools)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			if tt.wantNil {
				assert.Nil(t, result)
			} else if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

func TestCapabilityAdapter_ToSDKResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		resources   []vmcp.Resource
		setupMocks  func(*mocks.MockHandlerFactory)
		wantNil     bool
		checkResult func(*testing.T, []server.ServerResource)
	}{
		{
			name: "successful conversion with single resource",
			resources: []vmcp.Resource{
				{
					URI:         "file:///path/to/resource.txt",
					Name:        "Test Resource",
					Description: "A test resource",
					MimeType:    "text/plain",
					BackendID:   "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateResourceHandler("file:///path/to/resource.txt").Return(func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
					return []mcp.ResourceContents{}, nil
				})
			},
			wantNil: false,
			checkResult: func(t *testing.T, result []server.ServerResource) {
				t.Helper()
				require.Len(t, result, 1)
				assert.Equal(t, "file:///path/to/resource.txt", result[0].Resource.URI)
				assert.Equal(t, "Test Resource", result[0].Resource.Name)
				assert.Equal(t, "A test resource", result[0].Resource.Description)
				assert.Equal(t, "text/plain", result[0].Resource.MIMEType)
				assert.NotNil(t, result[0].Handler)
			},
		},
		{
			name: "successful conversion with multiple resources",
			resources: []vmcp.Resource{
				{
					URI:         "file:///data/file1.json",
					Name:        "JSON File",
					Description: "JSON data file",
					MimeType:    "application/json",
					BackendID:   "backend1",
				},
				{
					URI:         "http://example.com/api/data",
					Name:        "API Data",
					Description: "Remote API resource",
					MimeType:    "application/xml",
					BackendID:   "backend2",
				},
				{
					URI:         "file:///docs/readme.md",
					Name:        "README",
					Description: "Documentation file",
					MimeType:    "text/markdown",
					BackendID:   "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateResourceHandler("file:///data/file1.json").Return(func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
					return []mcp.ResourceContents{}, nil
				})
				mf.EXPECT().CreateResourceHandler("http://example.com/api/data").Return(func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
					return []mcp.ResourceContents{}, nil
				})
				mf.EXPECT().CreateResourceHandler("file:///docs/readme.md").Return(func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
					return []mcp.ResourceContents{}, nil
				})
			},
			wantNil: false,
			checkResult: func(t *testing.T, result []server.ServerResource) {
				t.Helper()
				require.Len(t, result, 3)

				// Verify all resources converted correctly
				assert.Equal(t, "file:///data/file1.json", result[0].Resource.URI)
				assert.Equal(t, "JSON File", result[0].Resource.Name)
				assert.Equal(t, "application/json", result[0].Resource.MIMEType)
				assert.NotNil(t, result[0].Handler)

				assert.Equal(t, "http://example.com/api/data", result[1].Resource.URI)
				assert.Equal(t, "API Data", result[1].Resource.Name)
				assert.Equal(t, "application/xml", result[1].Resource.MIMEType)
				assert.NotNil(t, result[1].Handler)

				assert.Equal(t, "file:///docs/readme.md", result[2].Resource.URI)
				assert.Equal(t, "README", result[2].Resource.Name)
				assert.Equal(t, "text/markdown", result[2].Resource.MIMEType)
				assert.NotNil(t, result[2].Handler)
			},
		},
		{
			name:      "empty resources slice returns nil",
			resources: []vmcp.Resource{},
			wantNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockFactory := mocks.NewMockHandlerFactory(ctrl)
			if tt.setupMocks != nil {
				tt.setupMocks(mockFactory)
			}

			adapter := adapter.NewCapabilityAdapter(mockFactory)
			result := adapter.ToSDKResources(tt.resources)

			if tt.wantNil {
				assert.Nil(t, result)
			} else if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

func TestCapabilityAdapter_ToSDKPrompts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prompts     []vmcp.Prompt
		setupMocks  func(*mocks.MockHandlerFactory)
		wantNil     bool
		checkResult func(*testing.T, []server.ServerPrompt)
	}{
		{
			name: "successful conversion with single prompt",
			prompts: []vmcp.Prompt{
				{
					Name:        "test_prompt",
					Description: "Test prompt description",
					Arguments: []vmcp.PromptArgument{
						{
							Name:        "topic",
							Description: "The topic to write about",
							Required:    true,
						},
					},
					BackendID: "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreatePromptHandler("test_prompt").Return(func(context.Context, mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
					return &mcp.GetPromptResult{}, nil
				})
			},
			wantNil: false,
			checkResult: func(t *testing.T, result []server.ServerPrompt) {
				t.Helper()
				require.Len(t, result, 1)
				assert.Equal(t, "test_prompt", result[0].Prompt.Name)
				assert.Equal(t, "Test prompt description", result[0].Prompt.Description)
				assert.NotNil(t, result[0].Handler)

				// Verify arguments converted correctly
				require.Len(t, result[0].Prompt.Arguments, 1)
				assert.Equal(t, "topic", result[0].Prompt.Arguments[0].Name)
				assert.Equal(t, "The topic to write about", result[0].Prompt.Arguments[0].Description)
				assert.True(t, result[0].Prompt.Arguments[0].Required)
			},
		},
		{
			name: "successful conversion with multiple prompts",
			prompts: []vmcp.Prompt{
				{
					Name:        "prompt_one",
					Description: "First prompt",
					Arguments: []vmcp.PromptArgument{
						{Name: "arg1", Description: "Arg 1", Required: true},
					},
					BackendID: "backend1",
				},
				{
					Name:        "prompt_two",
					Description: "Second prompt",
					Arguments: []vmcp.PromptArgument{
						{Name: "arg2", Description: "Arg 2", Required: false},
					},
					BackendID: "backend2",
				},
				{
					Name:        "prompt_three",
					Description: "Third prompt",
					Arguments:   []vmcp.PromptArgument{},
					BackendID:   "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreatePromptHandler("prompt_one").Return(func(context.Context, mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
					return &mcp.GetPromptResult{}, nil
				})
				mf.EXPECT().CreatePromptHandler("prompt_two").Return(func(context.Context, mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
					return &mcp.GetPromptResult{}, nil
				})
				mf.EXPECT().CreatePromptHandler("prompt_three").Return(func(context.Context, mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
					return &mcp.GetPromptResult{}, nil
				})
			},
			wantNil: false,
			checkResult: func(t *testing.T, result []server.ServerPrompt) {
				t.Helper()
				require.Len(t, result, 3)

				// Verify all prompts converted correctly
				assert.Equal(t, "prompt_one", result[0].Prompt.Name)
				assert.Equal(t, "First prompt", result[0].Prompt.Description)
				assert.NotNil(t, result[0].Handler)

				assert.Equal(t, "prompt_two", result[1].Prompt.Name)
				assert.Equal(t, "Second prompt", result[1].Prompt.Description)
				assert.NotNil(t, result[1].Handler)

				assert.Equal(t, "prompt_three", result[2].Prompt.Name)
				assert.Equal(t, "Third prompt", result[2].Prompt.Description)
				assert.NotNil(t, result[2].Handler)
			},
		},
		{
			name:    "empty prompts slice returns nil",
			prompts: []vmcp.Prompt{},
			wantNil: true,
		},
		{
			name: "prompt with no arguments",
			prompts: []vmcp.Prompt{
				{
					Name:        "no_args_prompt",
					Description: "Prompt without arguments",
					Arguments:   []vmcp.PromptArgument{},
					BackendID:   "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreatePromptHandler("no_args_prompt").Return(func(context.Context, mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
					return &mcp.GetPromptResult{}, nil
				})
			},
			wantNil: false,
			checkResult: func(t *testing.T, result []server.ServerPrompt) {
				t.Helper()
				require.Len(t, result, 1)
				assert.Equal(t, "no_args_prompt", result[0].Prompt.Name)
				assert.Empty(t, result[0].Prompt.Arguments)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockFactory := mocks.NewMockHandlerFactory(ctrl)
			if tt.setupMocks != nil {
				tt.setupMocks(mockFactory)
			}

			adapter := adapter.NewCapabilityAdapter(mockFactory)
			result := adapter.ToSDKPrompts(tt.prompts)

			if tt.wantNil {
				assert.Nil(t, result)
			} else if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

func TestCapabilityAdapter_ToCompositeToolSDKTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tools     []vmcp.Tool
		executors map[string]adapter.WorkflowExecutor
		wantErr   string
	}{
		{
			name:      "empty tools",
			tools:     []vmcp.Tool{},
			executors: map[string]adapter.WorkflowExecutor{},
		},
		{
			name:      "single tool",
			tools:     []vmcp.Tool{{Name: "deploy", InputSchema: map[string]any{"type": "object"}}},
			executors: map[string]adapter.WorkflowExecutor{"deploy": &mockWorkflowExecutor{}},
		},
		{
			name: "multiple tools",
			tools: []vmcp.Tool{
				{Name: "deploy", InputSchema: map[string]any{"type": "object"}},
				{Name: "rollback", InputSchema: map[string]any{"type": "object"}},
			},
			executors: map[string]adapter.WorkflowExecutor{"deploy": &mockWorkflowExecutor{}, "rollback": &mockWorkflowExecutor{}},
		},
		{
			name:      "missing executor",
			tools:     []vmcp.Tool{{Name: "deploy", InputSchema: map[string]any{"type": "object"}}},
			executors: map[string]adapter.WorkflowExecutor{},
			wantErr:   "workflow executor not found",
		},
		{
			name:      "invalid schema",
			tools:     []vmcp.Tool{{Name: "bad", InputSchema: map[string]any{"ch": make(chan int)}}},
			executors: map[string]adapter.WorkflowExecutor{"bad": &mockWorkflowExecutor{}},
			wantErr:   "failed to marshal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockFactory := mocks.NewMockHandlerFactory(ctrl)

			if tt.wantErr == "" {
				for _, tool := range tt.tools {
					mockFactory.EXPECT().CreateCompositeToolHandler(tool.Name, gomock.Any()).
						Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
							return mcp.NewToolResultStructuredOnly(map[string]any{}), nil
						})
				}
			}

			adapter := adapter.NewCapabilityAdapter(mockFactory)
			result, err := adapter.ToCompositeToolSDKTools(tt.tools, tt.executors)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				if len(tt.tools) == 0 {
					assert.Nil(t, result)
				} else {
					assert.Len(t, result, len(tt.tools))
				}
			}
		})
	}
}
