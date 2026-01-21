package adapter

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

// mockOptimizer implements optimizer.Optimizer for testing.
type mockOptimizer struct {
	findToolFunc func(ctx context.Context, input optimizer.FindToolInput) (*optimizer.FindToolOutput, error)
	callToolFunc func(ctx context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error)
}

func (m *mockOptimizer) FindTool(ctx context.Context, input optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
	if m.findToolFunc != nil {
		return m.findToolFunc(ctx, input)
	}
	return &optimizer.FindToolOutput{}, nil
}

func (m *mockOptimizer) CallTool(ctx context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error) {
	if m.callToolFunc != nil {
		return m.callToolFunc(ctx, input)
	}
	return mcp.NewToolResultText("ok"), nil
}

func TestCreateOptimizerTools(t *testing.T) {
	t.Parallel()

	opt := &mockOptimizer{}
	tools := CreateOptimizerTools(opt)

	require.Len(t, tools, 2)
	require.Equal(t, FindToolName, tools[0].Tool.Name)
	require.Equal(t, CallToolName, tools[1].Tool.Name)
}

func TestFindToolHandler(t *testing.T) {
	t.Parallel()

	opt := &mockOptimizer{
		findToolFunc: func(_ context.Context, input optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
			require.Equal(t, "read files", input.ToolDescription)
			return &optimizer.FindToolOutput{
				Tools: []optimizer.ToolMatch{
					{
						Name:        "read_file",
						Description: "Read a file",
						Score:       1.0,
					},
				},
			}, nil
		},
	}

	tools := CreateOptimizerTools(opt)
	handler := tools[0].Handler

	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"tool_description": "read files",
	}

	result, err := handler(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
}

func TestCallToolHandler(t *testing.T) {
	t.Parallel()

	opt := &mockOptimizer{
		callToolFunc: func(_ context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error) {
			require.Equal(t, "read_file", input.ToolName)
			require.Equal(t, "/etc/hosts", input.Parameters["path"])
			return mcp.NewToolResultText("file contents here"), nil
		},
	}

	tools := CreateOptimizerTools(opt)
	handler := tools[1].Handler

	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]any{
		"tool_name": "read_file",
		"parameters": map[string]any{
			"path": "/etc/hosts",
		},
	}

	result, err := handler(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
}
