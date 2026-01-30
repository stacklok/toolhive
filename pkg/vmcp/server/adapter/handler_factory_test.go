// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
)

func TestNewDefaultHandlerFactory(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockClient := vmcpmocks.NewMockBackendClient(ctrl)

	factory := adapter.NewDefaultHandlerFactory(mockRouter, mockClient)

	assert.NotNil(t, factory, "factory should not be nil")
}

func TestDefaultHandlerFactory_CreateToolHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		toolName    string
		setupMocks  func(*routermocks.MockRouter, *vmcpmocks.MockBackendClient)
		request     mcp.CallToolRequest
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name:     "successful tool call",
			toolName: "test_tool",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID:   "backend1",
					WorkloadName: "Backend 1",
					BaseURL:      "http://backend1:8080",
				}
				expectedResult := map[string]any{
					"output": "success",
					"status": "ok",
				}

				mockRouter.EXPECT().
					RouteTool(gomock.Any(), "test_tool").
					Return(target, nil)

				mockClient.EXPECT().
					CallTool(gomock.Any(), target, "test_tool", map[string]any{
						"input": "test",
						"count": 42,
					}, gomock.Any()).
					Return(&vmcp.ToolCallResult{StructuredContent: expectedResult}, nil)
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "test_tool",
					Arguments: map[string]any{
						"input": "test",
						"count": 42,
					},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.False(t, result.IsError)
				assert.Equal(t, map[string]any{
					"output": "success",
					"status": "ok",
				}, result.StructuredContent)
			},
		},
		{
			name:     "routing error returns error result for tool not found",
			toolName: "nonexistent_tool",
			setupMocks: func(mockRouter *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {
				mockRouter.EXPECT().
					RouteTool(gomock.Any(), "nonexistent_tool").
					Return(nil, router.ErrToolNotFound)
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "nonexistent_tool",
					Arguments: map[string]any{},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.True(t, result.IsError)
				textContent := result.Content[0].(mcp.TextContent)
				assert.Contains(t, textContent.Text, "not found")
				assert.Contains(t, textContent.Text, "nonexistent_tool")
			},
		},
		{
			name:     "routing error returns error result for other errors",
			toolName: "test_tool",
			setupMocks: func(mockRouter *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {
				mockRouter.EXPECT().
					RouteTool(gomock.Any(), "test_tool").
					Return(nil, errors.New("routing service unavailable"))
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "test_tool",
					Arguments: map[string]any{},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "Routing error")
			},
		},
		{
			name:     "invalid arguments type returns error result",
			toolName: "test_tool",
			setupMocks: func(mockRouter *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				mockRouter.EXPECT().
					RouteTool(gomock.Any(), "test_tool").
					Return(target, nil)
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "test_tool",
					Arguments: "invalid_string_argument",
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "invalid input")
				assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "arguments must be object")
			},
		},
		{
			name:     "backend tool execution failure returns error result",
			toolName: "test_tool",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				mockRouter.EXPECT().
					RouteTool(gomock.Any(), "test_tool").
					Return(target, nil)

				mockClient.EXPECT().
					CallTool(gomock.Any(), target, "test_tool", map[string]any{"input": "test"}, gomock.Any()).
					Return(&vmcp.ToolCallResult{
						Content: []vmcp.Content{
							{Type: "text", Text: "tool execution failed"},
						},
						IsError: true,
					}, nil)
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "test_tool",
					Arguments: map[string]any{"input": "test"},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "tool execution failed")
			},
		},
		{
			name:     "backend unavailable returns error result",
			toolName: "test_tool",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				mockRouter.EXPECT().
					RouteTool(gomock.Any(), "test_tool").
					Return(target, nil)

				mockClient.EXPECT().
					CallTool(gomock.Any(), target, "test_tool", map[string]any{"input": "test"}, gomock.Any()).
					Return(nil, vmcp.ErrBackendUnavailable)
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "test_tool",
					Arguments: map[string]any{"input": "test"},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "Backend unavailable")
			},
		},
		{
			name:     "backend other error returns error result",
			toolName: "test_tool",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				mockRouter.EXPECT().
					RouteTool(gomock.Any(), "test_tool").
					Return(target, nil)

				mockClient.EXPECT().
					CallTool(gomock.Any(), target, "test_tool", map[string]any{"input": "test"}, gomock.Any()).
					Return(nil, errors.New("unknown backend error"))
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "test_tool",
					Arguments: map[string]any{"input": "test"},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.True(t, result.IsError)
				assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "Tool call failed")
			},
		},
		{
			name:     "name translation for conflict resolution",
			toolName: "backend1_fetch",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID:             "backend1",
					OriginalCapabilityName: "fetch",
				}

				expectedResult := map[string]any{"status": "ok"}

				mockRouter.EXPECT().
					RouteTool(gomock.Any(), "backend1_fetch").
					Return(target, nil)

				// Handler factory now passes the client-facing name (backend1_fetch)
				// Backend client handles translation to original name (fetch)
				mockClient.EXPECT().
					CallTool(gomock.Any(), target, "backend1_fetch", map[string]any{"url": "https://example.com"}, gomock.Any()).
					Return(&vmcp.ToolCallResult{StructuredContent: expectedResult}, nil)
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "backend1_fetch",
					Arguments: map[string]any{"url": "https://example.com"},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.False(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRouter := routermocks.NewMockRouter(ctrl)
			mockClient := vmcpmocks.NewMockBackendClient(ctrl)

			tt.setupMocks(mockRouter, mockClient)

			factory := adapter.NewDefaultHandlerFactory(mockRouter, mockClient)
			handler := factory.CreateToolHandler(tt.toolName)

			result, err := handler(context.Background(), tt.request)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

func TestDefaultHandlerFactory_CreateResourceHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		uri         string
		setupMocks  func(*routermocks.MockRouter, *vmcpmocks.MockBackendClient)
		setupCtx    func() context.Context
		request     mcp.ReadResourceRequest
		wantErr     bool
		checkResult func(*testing.T, []mcp.ResourceContents, error)
	}{
		{
			name: "successful resource read",
			uri:  "file:///path/to/resource.json",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID:   "backend1",
					WorkloadName: "Backend 1",
				}

				resourceData := []byte(`{"key": "value"}`)

				mockRouter.EXPECT().
					RouteResource(gomock.Any(), "file:///path/to/resource.json").
					Return(target, nil)

				mockClient.EXPECT().
					ReadResource(gomock.Any(), target, "file:///path/to/resource.json").
					Return(&vmcp.ResourceReadResult{Contents: resourceData, MimeType: "application/json"}, nil)
			},
			setupCtx: func() context.Context {
				caps := &aggregator.AggregatedCapabilities{
					Resources: []vmcp.Resource{
						{
							URI:      "file:///path/to/resource.json",
							MimeType: "application/json",
						},
					},
					RoutingTable: &vmcp.RoutingTable{
						Tools:     make(map[string]*vmcp.BackendTarget),
						Resources: make(map[string]*vmcp.BackendTarget),
						Prompts:   make(map[string]*vmcp.BackendTarget),
					},
				}
				return discovery.WithDiscoveredCapabilities(context.Background(), caps)
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///path/to/resource.json",
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.NoError(t, err)
				require.Len(t, contents, 1)
				textContent := contents[0].(mcp.TextResourceContents)
				assert.Equal(t, "file:///path/to/resource.json", textContent.URI)
				assert.Equal(t, "application/json", textContent.MIMEType)
				assert.Equal(t, `{"key": "value"}`, textContent.Text)
			},
		},
		{
			name:       "no capabilities in context returns error",
			uri:        "file:///test",
			setupMocks: func(_ *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {},
			setupCtx: func() context.Context {
				return context.Background()
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///test",
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "capabilities not discovered")
				assert.Nil(t, contents)
			},
		},
		{
			name: "routing error for resource not found",
			uri:  "file:///nonexistent",
			setupMocks: func(mockRouter *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {
				mockRouter.EXPECT().
					RouteResource(gomock.Any(), "file:///nonexistent").
					Return(nil, router.ErrResourceNotFound)
			},
			setupCtx: func() context.Context {
				caps := &aggregator.AggregatedCapabilities{
					Resources: []vmcp.Resource{},
					RoutingTable: &vmcp.RoutingTable{
						Tools:     make(map[string]*vmcp.BackendTarget),
						Resources: make(map[string]*vmcp.BackendTarget),
						Prompts:   make(map[string]*vmcp.BackendTarget),
					},
				}
				return discovery.WithDiscoveredCapabilities(context.Background(), caps)
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///nonexistent",
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.Error(t, err)
				assert.True(t, errors.Is(err, vmcp.ErrNotFound))
				assert.Contains(t, err.Error(), "file:///nonexistent")
				assert.Nil(t, contents)
			},
		},
		{
			name: "routing error for other errors",
			uri:  "file:///test",
			setupMocks: func(mockRouter *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {
				mockRouter.EXPECT().
					RouteResource(gomock.Any(), "file:///test").
					Return(nil, errors.New("routing service unavailable"))
			},
			setupCtx: func() context.Context {
				caps := &aggregator.AggregatedCapabilities{
					Resources: []vmcp.Resource{},
					RoutingTable: &vmcp.RoutingTable{
						Tools:     make(map[string]*vmcp.BackendTarget),
						Resources: make(map[string]*vmcp.BackendTarget),
						Prompts:   make(map[string]*vmcp.BackendTarget),
					},
				}
				return discovery.WithDiscoveredCapabilities(context.Background(), caps)
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///test",
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "routing error")
				assert.Nil(t, contents)
			},
		},
		{
			name: "backend unavailable returns error",
			uri:  "file:///test",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				mockRouter.EXPECT().
					RouteResource(gomock.Any(), "file:///test").
					Return(target, nil)

				mockClient.EXPECT().
					ReadResource(gomock.Any(), target, "file:///test").
					Return(nil, vmcp.ErrBackendUnavailable)
			},
			setupCtx: func() context.Context {
				caps := &aggregator.AggregatedCapabilities{
					Resources: []vmcp.Resource{
						{URI: "file:///test", MimeType: "text/plain"},
					},
					RoutingTable: &vmcp.RoutingTable{
						Tools:     make(map[string]*vmcp.BackendTarget),
						Resources: make(map[string]*vmcp.BackendTarget),
						Prompts:   make(map[string]*vmcp.BackendTarget),
					},
				}
				return discovery.WithDiscoveredCapabilities(context.Background(), caps)
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///test",
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "backend unavailable")
				assert.Nil(t, contents)
			},
		},
		{
			name: "backend other error returns error",
			uri:  "file:///test",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				mockRouter.EXPECT().
					RouteResource(gomock.Any(), "file:///test").
					Return(target, nil)

				mockClient.EXPECT().
					ReadResource(gomock.Any(), target, "file:///test").
					Return(nil, errors.New("read failed"))
			},
			setupCtx: func() context.Context {
				caps := &aggregator.AggregatedCapabilities{
					Resources: []vmcp.Resource{
						{URI: "file:///test", MimeType: "text/plain"},
					},
					RoutingTable: &vmcp.RoutingTable{
						Tools:     make(map[string]*vmcp.BackendTarget),
						Resources: make(map[string]*vmcp.BackendTarget),
						Prompts:   make(map[string]*vmcp.BackendTarget),
					},
				}
				return discovery.WithDiscoveredCapabilities(context.Background(), caps)
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///test",
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "resource read failed")
				assert.Nil(t, contents)
			},
		},
		{
			name: "mime type found in capabilities",
			uri:  "file:///test.json",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				resourceData := []byte(`{"test": "data"}`)

				mockRouter.EXPECT().
					RouteResource(gomock.Any(), "file:///test.json").
					Return(target, nil)

				mockClient.EXPECT().
					ReadResource(gomock.Any(), target, "file:///test.json").
					Return(&vmcp.ResourceReadResult{Contents: resourceData, MimeType: "application/json"}, nil)
			},
			setupCtx: func() context.Context {
				caps := &aggregator.AggregatedCapabilities{
					Resources: []vmcp.Resource{
						{
							URI:      "file:///test.json",
							MimeType: "application/json",
						},
					},
					RoutingTable: &vmcp.RoutingTable{
						Tools:     make(map[string]*vmcp.BackendTarget),
						Resources: make(map[string]*vmcp.BackendTarget),
						Prompts:   make(map[string]*vmcp.BackendTarget),
					},
				}
				return discovery.WithDiscoveredCapabilities(context.Background(), caps)
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///test.json",
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.NoError(t, err)
				require.Len(t, contents, 1)
				textContent := contents[0].(mcp.TextResourceContents)
				assert.Equal(t, "application/json", textContent.MIMEType)
			},
		},
		{
			name: "mime type not found defaults to octet-stream",
			uri:  "file:///test.bin",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				resourceData := []byte{0x01, 0x02, 0x03}

				mockRouter.EXPECT().
					RouteResource(gomock.Any(), "file:///test.bin").
					Return(target, nil)

				mockClient.EXPECT().
					ReadResource(gomock.Any(), target, "file:///test.bin").
					Return(&vmcp.ResourceReadResult{Contents: resourceData, MimeType: ""}, nil)
			},
			setupCtx: func() context.Context {
				caps := &aggregator.AggregatedCapabilities{
					Resources: []vmcp.Resource{
						{
							URI:      "file:///test.bin",
							MimeType: "",
						},
					},
					RoutingTable: &vmcp.RoutingTable{
						Tools:     make(map[string]*vmcp.BackendTarget),
						Resources: make(map[string]*vmcp.BackendTarget),
						Prompts:   make(map[string]*vmcp.BackendTarget),
					},
				}
				return discovery.WithDiscoveredCapabilities(context.Background(), caps)
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///test.bin",
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.NoError(t, err)
				require.Len(t, contents, 1)
				textContent := contents[0].(mcp.TextResourceContents)
				assert.Equal(t, "application/octet-stream", textContent.MIMEType)
			},
		},
		{
			name: "uri translation for conflict resolution",
			uri:  "file:///backend1/resource",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID:             "backend1",
					OriginalCapabilityName: "file:///resource",
				}

				resourceData := []byte("test data")

				mockRouter.EXPECT().
					RouteResource(gomock.Any(), "file:///backend1/resource").
					Return(target, nil)

				mockClient.EXPECT().
					ReadResource(gomock.Any(), target, "file:///resource").
					Return(&vmcp.ResourceReadResult{Contents: resourceData, MimeType: "application/json"}, nil)
			},
			setupCtx: func() context.Context {
				caps := &aggregator.AggregatedCapabilities{
					Resources: []vmcp.Resource{
						{
							URI:      "file:///backend1/resource",
							MimeType: "text/plain",
						},
					},
					RoutingTable: &vmcp.RoutingTable{
						Tools:     make(map[string]*vmcp.BackendTarget),
						Resources: make(map[string]*vmcp.BackendTarget),
						Prompts:   make(map[string]*vmcp.BackendTarget),
					},
				}
				return discovery.WithDiscoveredCapabilities(context.Background(), caps)
			},
			request: mcp.ReadResourceRequest{
				Params: mcp.ReadResourceParams{
					URI: "file:///backend1/resource",
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, contents []mcp.ResourceContents, err error) {
				t.Helper()
				require.NoError(t, err)
				require.Len(t, contents, 1)
				textContent := contents[0].(mcp.TextResourceContents)
				assert.Equal(t, "file:///backend1/resource", textContent.URI)
				assert.Equal(t, "test data", textContent.Text)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRouter := routermocks.NewMockRouter(ctrl)
			mockClient := vmcpmocks.NewMockBackendClient(ctrl)

			tt.setupMocks(mockRouter, mockClient)

			factory := adapter.NewDefaultHandlerFactory(mockRouter, mockClient)
			handler := factory.CreateResourceHandler(tt.uri)

			ctx := tt.setupCtx()
			contents, err := handler(ctx, tt.request)

			if tt.checkResult != nil {
				tt.checkResult(t, contents, err)
			}
		})
	}
}

func TestDefaultHandlerFactory_CreatePromptHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		promptName  string
		setupMocks  func(*routermocks.MockRouter, *vmcpmocks.MockBackendClient)
		request     mcp.GetPromptRequest
		wantErr     bool
		checkResult func(*testing.T, *mcp.GetPromptResult, error)
	}{
		{
			name:       "successful prompt request",
			promptName: "test_prompt",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID:   "backend1",
					WorkloadName: "Backend 1",
				}

				promptText := "Write tests for Go code about testing"

				expectedArgs := map[string]any{
					"topic":    "testing",
					"language": "Go",
				}

				mockRouter.EXPECT().
					RoutePrompt(gomock.Any(), "test_prompt").
					Return(target, nil)

				mockClient.EXPECT().
					GetPrompt(gomock.Any(), target, "test_prompt", expectedArgs).
					Return(&vmcp.PromptGetResult{Messages: promptText, Description: ""}, nil)
			},
			request: mcp.GetPromptRequest{
				Params: mcp.GetPromptParams{
					Name: "test_prompt",
					Arguments: map[string]string{
						"topic":    "testing",
						"language": "Go",
					},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.GetPromptResult, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Contains(t, result.Description, "test_prompt")
				require.Len(t, result.Messages, 1)
				assert.Equal(t, "assistant", string(result.Messages[0].Role))
				assert.Equal(t, "Write tests for Go code about testing", result.Messages[0].Content.(mcp.TextContent).Text)
			},
		},
		{
			name:       "routing error for prompt not found",
			promptName: "nonexistent_prompt",
			setupMocks: func(mockRouter *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {
				mockRouter.EXPECT().
					RoutePrompt(gomock.Any(), "nonexistent_prompt").
					Return(nil, router.ErrPromptNotFound)
			},
			request: mcp.GetPromptRequest{
				Params: mcp.GetPromptParams{
					Name:      "nonexistent_prompt",
					Arguments: map[string]string{},
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, result *mcp.GetPromptResult, err error) {
				t.Helper()
				require.Error(t, err)
				assert.True(t, errors.Is(err, vmcp.ErrNotFound))
				assert.Contains(t, err.Error(), "nonexistent_prompt")
				assert.Nil(t, result)
			},
		},
		{
			name:       "routing error for other errors",
			promptName: "test_prompt",
			setupMocks: func(mockRouter *routermocks.MockRouter, _ *vmcpmocks.MockBackendClient) {
				mockRouter.EXPECT().
					RoutePrompt(gomock.Any(), "test_prompt").
					Return(nil, errors.New("routing service unavailable"))
			},
			request: mcp.GetPromptRequest{
				Params: mcp.GetPromptParams{
					Name:      "test_prompt",
					Arguments: map[string]string{},
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, result *mcp.GetPromptResult, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "routing error")
				assert.Nil(t, result)
			},
		},
		{
			name:       "backend unavailable returns error",
			promptName: "test_prompt",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				expectedArgs := map[string]any{"input": "test"}

				mockRouter.EXPECT().
					RoutePrompt(gomock.Any(), "test_prompt").
					Return(target, nil)

				mockClient.EXPECT().
					GetPrompt(gomock.Any(), target, "test_prompt", expectedArgs).
					Return(nil, vmcp.ErrBackendUnavailable)
			},
			request: mcp.GetPromptRequest{
				Params: mcp.GetPromptParams{
					Name:      "test_prompt",
					Arguments: map[string]string{"input": "test"},
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, result *mcp.GetPromptResult, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "backend unavailable")
				assert.Nil(t, result)
			},
		},
		{
			name:       "backend other error returns error",
			promptName: "test_prompt",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				expectedArgs := map[string]any{"input": "test"}

				mockRouter.EXPECT().
					RoutePrompt(gomock.Any(), "test_prompt").
					Return(target, nil)

				mockClient.EXPECT().
					GetPrompt(gomock.Any(), target, "test_prompt", expectedArgs).
					Return(nil, errors.New("prompt rendering failed"))
			},
			request: mcp.GetPromptRequest{
				Params: mcp.GetPromptParams{
					Name:      "test_prompt",
					Arguments: map[string]string{"input": "test"},
				},
			},
			wantErr: true,
			checkResult: func(t *testing.T, result *mcp.GetPromptResult, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "prompt request failed")
				assert.Nil(t, result)
			},
		},
		{
			name:       "name translation for conflict resolution",
			promptName: "backend1_summarize",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID:             "backend1",
					OriginalCapabilityName: "summarize",
				}

				promptText := "Summary of test content"
				expectedArgs := map[string]any{"text": "test content"}

				mockRouter.EXPECT().
					RoutePrompt(gomock.Any(), "backend1_summarize").
					Return(target, nil)

				mockClient.EXPECT().
					GetPrompt(gomock.Any(), target, "summarize", expectedArgs).
					Return(&vmcp.PromptGetResult{Messages: promptText, Description: ""}, nil)
			},
			request: mcp.GetPromptRequest{
				Params: mcp.GetPromptParams{
					Name:      "backend1_summarize",
					Arguments: map[string]string{"text": "test content"},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.GetPromptResult, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, "Summary of test content", result.Messages[0].Content.(mcp.TextContent).Text)
			},
		},
		{
			name:       "empty arguments",
			promptName: "simple_prompt",
			setupMocks: func(mockRouter *routermocks.MockRouter, mockClient *vmcpmocks.MockBackendClient) {
				target := &vmcp.BackendTarget{
					WorkloadID: "backend1",
				}

				promptText := "Simple prompt response"
				emptyArgs := map[string]any{}

				mockRouter.EXPECT().
					RoutePrompt(gomock.Any(), "simple_prompt").
					Return(target, nil)

				mockClient.EXPECT().
					GetPrompt(gomock.Any(), target, "simple_prompt", emptyArgs).
					Return(&vmcp.PromptGetResult{Messages: promptText, Description: ""}, nil)
			},
			request: mcp.GetPromptRequest{
				Params: mcp.GetPromptParams{
					Name:      "simple_prompt",
					Arguments: map[string]string{},
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.GetPromptResult, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, result)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRouter := routermocks.NewMockRouter(ctrl)
			mockClient := vmcpmocks.NewMockBackendClient(ctrl)

			tt.setupMocks(mockRouter, mockClient)

			factory := adapter.NewDefaultHandlerFactory(mockRouter, mockClient)
			handler := factory.CreatePromptHandler(tt.promptName)

			result, err := handler(context.Background(), tt.request)

			if tt.checkResult != nil {
				tt.checkResult(t, result, err)
			}
		})
	}
}

func TestDefaultHandlerFactory_CreateCompositeToolHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		toolName  string
		setupMock func(*mockWorkflowExecutor)
		request   mcp.CallToolRequest
		wantError bool
		contains  string
	}{
		{
			name:     "successful workflow execution",
			toolName: "deploy",
			setupMock: func(m *mockWorkflowExecutor) {
				m.executeFunc = func(_ context.Context, params map[string]any) (*adapter.WorkflowResult, error) {
					return &adapter.WorkflowResult{
						Output: map[string]any{"deployed": true, "pr": params["pr_number"]},
					}, nil
				}
			},
			request: mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{"pr_number": 123},
				},
			},
			wantError: false,
		},
		{
			name:     "workflow execution error",
			toolName: "failing",
			setupMock: func(m *mockWorkflowExecutor) {
				m.executeFunc = func(context.Context, map[string]any) (*adapter.WorkflowResult, error) {
					return nil, errors.New("step timeout")
				}
			},
			request:   mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{}}},
			wantError: true,
			contains:  "Workflow execution failed",
		},
		{
			name:     "workflow result with error",
			toolName: "error_result",
			setupMock: func(m *mockWorkflowExecutor) {
				m.executeFunc = func(context.Context, map[string]any) (*adapter.WorkflowResult, error) {
					return &adapter.WorkflowResult{Error: errors.New("backend unavailable")}, nil
				}
			},
			request:   mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{}}},
			wantError: true,
			contains:  "backend unavailable",
		},
		{
			name:      "invalid arguments type",
			toolName:  "test",
			setupMock: func(*mockWorkflowExecutor) {},
			request:   mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: "invalid"}},
			wantError: true,
			contains:  "arguments must be object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockRouter := routermocks.NewMockRouter(ctrl)
			mockClient := vmcpmocks.NewMockBackendClient(ctrl)
			mockWorkflow := &mockWorkflowExecutor{}
			tt.setupMock(mockWorkflow)

			factory := adapter.NewDefaultHandlerFactory(mockRouter, mockClient)
			handler := factory.CreateCompositeToolHandler(tt.toolName, mockWorkflow)

			result, err := handler(context.Background(), tt.request)

			assert.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, tt.wantError, result.IsError)
			if tt.contains != "" {
				textContent := result.Content[0].(mcp.TextContent)
				assert.Contains(t, textContent.Text, tt.contains)
			}
		})
	}
}

type mockWorkflowExecutor struct {
	executeFunc func(context.Context, map[string]any) (*adapter.WorkflowResult, error)
}

func (m *mockWorkflowExecutor) ExecuteWorkflow(
	ctx context.Context,
	params map[string]any,
) (*adapter.WorkflowResult, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, params)
	}
	return nil, errors.New("not implemented")
}
