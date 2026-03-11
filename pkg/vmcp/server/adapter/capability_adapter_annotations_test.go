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

func boolPtr(b bool) *bool { return &b }

func TestCapabilityAdapter_ToSDKTools_Annotations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		tools       []vmcp.Tool
		setupMocks  func(*mocks.MockHandlerFactory)
		checkResult func(*testing.T, []server.ServerTool)
	}{
		{
			name: "preserves Annotations and OutputSchema in SDK output",
			tools: []vmcp.Tool{
				{
					Name:        "annotated_tool",
					Description: "Tool with annotations",
					InputSchema: map[string]any{"type": "object"},
					OutputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"result": map[string]any{"type": "string"},
						},
					},
					Annotations: &vmcp.ToolAnnotations{
						Title:           "Annotated Tool",
						ReadOnlyHint:    boolPtr(true),
						DestructiveHint: boolPtr(false),
					},
					BackendID: "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateToolHandler("annotated_tool").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
			},
			checkResult: func(t *testing.T, result []server.ServerTool) {
				t.Helper()
				require.Len(t, result, 1)
				tool := result[0].Tool
				assert.Equal(t, "annotated_tool", tool.Name)

				// Verify annotations are set
				assert.Equal(t, "Annotated Tool", tool.Annotations.Title)
				require.NotNil(t, tool.Annotations.ReadOnlyHint)
				assert.True(t, *tool.Annotations.ReadOnlyHint)
				require.NotNil(t, tool.Annotations.DestructiveHint)
				assert.False(t, *tool.Annotations.DestructiveHint)
				assert.Nil(t, tool.Annotations.IdempotentHint)
				assert.Nil(t, tool.Annotations.OpenWorldHint)

				// Verify output schema is set
				assert.NotNil(t, tool.RawOutputSchema)
				assert.Contains(t, string(tool.RawOutputSchema), `"result"`)
			},
		},
		{
			name: "nil Annotations produces zero-valued SDK Annotations",
			tools: []vmcp.Tool{
				{
					Name:        "simple_tool",
					Description: "Tool without annotations",
					InputSchema: map[string]any{"type": "object"},
					BackendID:   "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateToolHandler("simple_tool").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
			},
			checkResult: func(t *testing.T, result []server.ServerTool) {
				t.Helper()
				require.Len(t, result, 1)
				tool := result[0].Tool
				// nil vmcp.ToolAnnotations -> zero-valued mcp.ToolAnnotation
				assert.Empty(t, tool.Annotations.Title)
				assert.Nil(t, tool.Annotations.ReadOnlyHint)
				assert.Nil(t, tool.RawOutputSchema)
			},
		},
		{
			name: "all annotation hints populated",
			tools: []vmcp.Tool{
				{
					Name:        "full_annotations_tool",
					Description: "Tool with all annotation hints",
					InputSchema: map[string]any{"type": "object"},
					Annotations: &vmcp.ToolAnnotations{
						Title:           "Full Hints",
						ReadOnlyHint:    boolPtr(false),
						DestructiveHint: boolPtr(true),
						IdempotentHint:  boolPtr(true),
						OpenWorldHint:   boolPtr(false),
					},
					BackendID: "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateToolHandler("full_annotations_tool").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
			},
			checkResult: func(t *testing.T, result []server.ServerTool) {
				t.Helper()
				require.Len(t, result, 1)
				tool := result[0].Tool

				assert.Equal(t, "Full Hints", tool.Annotations.Title)
				require.NotNil(t, tool.Annotations.ReadOnlyHint)
				assert.False(t, *tool.Annotations.ReadOnlyHint)
				require.NotNil(t, tool.Annotations.DestructiveHint)
				assert.True(t, *tool.Annotations.DestructiveHint)
				require.NotNil(t, tool.Annotations.IdempotentHint)
				assert.True(t, *tool.Annotations.IdempotentHint)
				require.NotNil(t, tool.Annotations.OpenWorldHint)
				assert.False(t, *tool.Annotations.OpenWorldHint)
			},
		},
		{
			name: "OutputSchema without Annotations",
			tools: []vmcp.Tool{
				{
					Name:        "schema_only_tool",
					Description: "Tool with output schema but no annotations",
					InputSchema: map[string]any{"type": "object"},
					OutputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"status": map[string]any{"type": "string"},
						},
					},
					BackendID: "backend1",
				},
			},
			setupMocks: func(mf *mocks.MockHandlerFactory) {
				mf.EXPECT().CreateToolHandler("schema_only_tool").Return(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				})
			},
			checkResult: func(t *testing.T, result []server.ServerTool) {
				t.Helper()
				require.Len(t, result, 1)
				tool := result[0].Tool

				// No annotations
				assert.Empty(t, tool.Annotations.Title)
				assert.Nil(t, tool.Annotations.ReadOnlyHint)

				// Output schema should be set
				assert.NotNil(t, tool.RawOutputSchema)
				assert.Contains(t, string(tool.RawOutputSchema), `"status"`)
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

			a := adapter.NewCapabilityAdapter(mockFactory)
			result, err := a.ToSDKTools(tt.tools)
			require.NoError(t, err)

			if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}
