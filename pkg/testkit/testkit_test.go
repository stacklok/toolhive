package testkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSEServerEndpoints tests a simple MCP server with three endpoints
func TestSSEServerEndpoints(t *testing.T) {
	t.Parallel()

	opts := []TestMCPServerOption{
		WithTool("test", "A test tool", func() string { return "Tool call executed successfully" }),
		WithSSEClientType(),
	}

	t.Run("sse text/event-stream tools/list", func(t *testing.T) {
		t.Parallel()

		// Create SSE server for /command and /sse endpoints
		server, client, err := NewSSETestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		data, err := client.ToolsList()
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal([]byte(data), &result)
		require.NoError(t, err)
		assert.Equal(t, "2.0", result["jsonrpc"])
		assert.Equal(t, float64(1), result["id"])

		// Check that it's a tools/list response
		toolCall, ok := result["result"].(map[string]any)
		require.True(t, ok, "Result should contain result array")
		assert.Len(t, toolCall["tools"], 1, "Should have one tool")
	})

	t.Run("sse text/event-stream tools/call", func(t *testing.T) {
		t.Parallel()

		// Create SSE server for /command and /sse endpoints
		server, client, err := NewSSETestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		data, err := client.ToolsCall("test")
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal([]byte(data), &result)
		require.NoError(t, err)
		assert.Equal(t, "2.0", result["jsonrpc"])
		assert.Equal(t, float64(1), result["id"])

		// Check that it's a tools/call response
		resultData, ok := result["result"].(map[string]any)
		require.True(t, ok, "Result should contain a result object")

		toolCall, ok := resultData["content"].([]any)
		require.True(t, ok, "Result should contain content array")
		assert.Len(t, toolCall, 1, "Should have one result")
	})
}

func TestStreamableServerEndpoints(t *testing.T) {
	t.Parallel()

	opts := []TestMCPServerOption{
		WithTool("test", "A test tool", func() string { return "Tool call executed successfully" }),
	}

	t.Run("streamable application/json tools/list", func(t *testing.T) {
		t.Parallel()

		opts := append(opts, WithJSONClientType())
		server, client, err := NewStreamableTestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		require.IsType(t, &streamableJSONClient{}, client)

		body, err := client.ToolsList()
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)
		assert.Equal(t, "2.0", result["jsonrpc"])
		assert.Equal(t, float64(1), result["id"])

		// Check that it's a tools/list response
		resultData, ok := result["result"].(map[string]any)
		require.True(t, ok, "Result should contain a result object")

		tools, ok := resultData["tools"].([]any)
		require.True(t, ok, "Result should contain tools array")
		assert.Len(t, tools, 1, "Should have one tool")
	})

	t.Run("streamable text/event-stream tools/list", func(t *testing.T) {
		t.Parallel()

		opts := append(opts, WithSSEClientType())
		server, client, err := NewStreamableTestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		require.IsType(t, &streamableEventStreamClient{}, client)

		body, err := client.ToolsList()
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)
		assert.Equal(t, "2.0", result["jsonrpc"])
		assert.Equal(t, float64(1), result["id"])

		// Check that it's a tools/list response
		resultData, ok := result["result"].(map[string]any)
		require.True(t, ok, "Result should contain a result object")

		tools, ok := resultData["tools"].([]any)
		require.True(t, ok, "Result should contain tools array")
		assert.Len(t, tools, 1, "Should have one tool")
	})

	t.Run("streamable application/json tools/call", func(t *testing.T) {
		t.Parallel()

		opts := append(opts, WithJSONClientType())
		server, client, err := NewStreamableTestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		require.IsType(t, &streamableJSONClient{}, client)

		body, err := client.ToolsCall("test")
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)
		assert.Equal(t, "2.0", result["jsonrpc"])
		assert.Equal(t, float64(1), result["id"])

		// Check that it's a tools/call response
		resultData, ok := result["result"].(map[string]any)
		require.True(t, ok, "Result should contain a result object")

		toolCall, ok := resultData["content"].([]any)
		require.True(t, ok, "Result should contain content array")
		assert.Len(t, toolCall, 1, "Should have one result")
	})

	t.Run("streamable text/event-stream tools/call", func(t *testing.T) {
		t.Parallel()

		opts := append(opts, WithSSEClientType())
		server, client, err := NewStreamableTestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		require.IsType(t, &streamableEventStreamClient{}, client)

		body, err := client.ToolsCall("test")
		require.NoError(t, err)

		var result map[string]any
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)
		assert.Equal(t, "2.0", result["jsonrpc"])
		assert.Equal(t, float64(1), result["id"])

		// Check that it's a tools/call response
		resultData, ok := result["result"].(map[string]any)
		require.True(t, ok, "Result should contain a result object")

		toolCall, ok := resultData["content"].([]any)
		require.True(t, ok, "Result should contain content array")
		assert.Len(t, toolCall, 1, "Should have one result")
	})
}
