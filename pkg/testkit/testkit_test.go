package testkit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	toolsListRequest = `{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}`
	toolsCallRequest = `{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "test"}}`
)

// TestSSEServerEndpoints tests a simple MCP server with three endpoints
func TestSSEServerEndpoints(t *testing.T) {
	t.Parallel()

	opts := []TestMCPServerOption{
		WithTool("test", "A test tool", func() string { return "Tool call executed successfully" }),
	}

	t.Run("sse text/event-stream tools/list", func(t *testing.T) {
		t.Parallel()

		// Create SSE server for /command and /sse endpoints
		server, err := NewSSETestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		// Channel to receive SSE response
		sseResponseChan := make(chan string, 1)

		// Start SSE connection in a goroutine
		go func() {
			t.Helper()
			defer close(sseResponseChan)

			resp, err := http.Get(server.URL + "/sse")
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

			scanner := bufio.NewScanner(resp.Body)
			scanner.Split(NewSplitSSE(LFSep))

			for scanner.Scan() {
				require.NoError(t, scanner.Err())
				event := scanner.Text()
				sseResponseChan <- event
			}
		}()

		// Give the SSE goroutine a moment to start
		time.Sleep(10 * time.Millisecond)

		// Now send a command to /command endpoint
		commandResp, err := http.Post(server.URL+"/command", "application/json", bytes.NewBufferString(toolsListRequest))
		require.NoError(t, err)
		defer commandResp.Body.Close()

		assert.Equal(t, http.StatusAccepted, commandResp.StatusCode)

		commandBody, err := io.ReadAll(commandResp.Body)
		require.NoError(t, err)
		assert.Equal(t, "Accepted", string(commandBody))

		// Wait for SSE response
		select {
		case sseBody := <-sseResponseChan:
			scanner := bufio.NewScanner(bytes.NewReader([]byte(sseBody)))

			for scanner.Scan() {
				require.NoError(t, scanner.Err())

				// Check that the SSE response contains the tools/list response
				data, ok := strings.CutPrefix(scanner.Text(), "data:")
				if ok {
					var result map[string]any
					err = json.Unmarshal([]byte(data), &result)
					require.NoError(t, err)
					assert.Equal(t, "2.0", result["jsonrpc"])
					assert.Equal(t, float64(1), result["id"])

					// Check that it's a tools/list response
					toolCall, ok := result["result"].(map[string]any)
					require.True(t, ok, "Result should contain result array")
					assert.Len(t, toolCall["tools"], 1, "Should have one tool")
					return
				}
			}
		case <-time.After(1 * time.Second):
			t.Fatal("Timeout waiting for SSE response")
		}
	})

	t.Run("sse text/event-stream tools/call", func(t *testing.T) {
		t.Parallel()

		// Create SSE server for /command and /sse endpoints
		server, err := NewSSETestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		// Channel to receive SSE response
		sseResponseChan := make(chan string, 1)

		// Start SSE connection in a goroutine
		go func() {
			t.Helper()
			defer close(sseResponseChan)

			resp, err := http.Get(server.URL + "/sse")
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

			scanner := bufio.NewScanner(resp.Body)
			scanner.Split(NewSplitSSE(LFSep))

			for scanner.Scan() {
				require.NoError(t, scanner.Err())
				event := scanner.Text()
				sseResponseChan <- event
			}
		}()

		// Give the SSE goroutine a moment to start
		time.Sleep(10 * time.Millisecond)

		// Now send a command to /command endpoint
		commandResp, err := http.Post(server.URL+"/command", "application/json", bytes.NewBufferString(toolsCallRequest))
		require.NoError(t, err)
		defer commandResp.Body.Close()

		assert.Equal(t, http.StatusAccepted, commandResp.StatusCode)

		commandBody, err := io.ReadAll(commandResp.Body)
		require.NoError(t, err)
		assert.Equal(t, "Accepted", string(commandBody))

		// Wait for SSE response
		select {
		case sseBody := <-sseResponseChan:
			scanner := bufio.NewScanner(bytes.NewReader([]byte(sseBody)))

			for scanner.Scan() {
				require.NoError(t, scanner.Err())

				// Check that the SSE response contains the tools/call response
				data, ok := strings.CutPrefix(scanner.Text(), "data:")
				if ok {
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
					return
				}
			}
		case <-time.After(1 * time.Second):
			t.Fatal("Timeout waiting for SSE response")
		}
	})
}

func TestStreamableServerEndpoints(t *testing.T) {
	t.Parallel()

	opts := []TestMCPServerOption{
		WithTool("test", "A test tool", func() string { return "Tool call executed successfully" }),
	}

	t.Run("streamable application/json tools/list", func(t *testing.T) {
		t.Parallel()

		// Create streamable server for /mcp-json and /mcp-sse endpoints
		server, err := NewStreamableTestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		resp, err := http.Post(server.URL+"/mcp-json", "application/json", bytes.NewBufferString(toolsListRequest))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		body, err := io.ReadAll(resp.Body)
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

		// Create streamable server for /mcp-json and /mcp-sse endpoints
		server, err := NewStreamableTestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		resp, err := http.Post(server.URL+"/mcp-sse", "application/json", bytes.NewBufferString(toolsListRequest))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		scanner := bufio.NewScanner(resp.Body)
		scanner.Split(NewSplitSSE(LFSep))

		for scanner.Scan() {
			require.NoError(t, scanner.Err())

			lineScanner := bufio.NewScanner(bytes.NewReader([]byte(scanner.Text())))
			for lineScanner.Scan() {
				require.NoError(t, lineScanner.Err())

				if data, ok := strings.CutPrefix(lineScanner.Text(), "data:"); ok {
					var result map[string]any
					err = json.Unmarshal([]byte(data), &result)
					require.NoError(t, err)
					assert.Equal(t, "2.0", result["jsonrpc"])
					assert.Equal(t, float64(1), result["id"])

					// Check that it's a tools/list response
					resultData, ok := result["result"].(map[string]any)
					require.True(t, ok, "Result should contain a result object")

					tools, ok := resultData["tools"].([]any)
					require.True(t, ok, "Result should contain tools array")
					assert.Len(t, tools, 1, "Should have one tool")
					return
				}
			}
		}
	})

	t.Run("streamable application/json tools/call", func(t *testing.T) {
		t.Parallel()

		// Create streamable server for /mcp-json and /mcp-sse endpoints
		server, err := NewStreamableTestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		resp, err := http.Post(server.URL+"/mcp-json", "application/json", bytes.NewBufferString(toolsCallRequest))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		body, err := io.ReadAll(resp.Body)
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

		// Create streamable server for /mcp-json and /mcp-sse endpoints
		server, err := NewStreamableTestServer(opts...)
		require.NoError(t, err)
		defer server.Close()

		resp, err := http.Post(server.URL+"/mcp-sse", "application/json", bytes.NewBufferString(toolsCallRequest))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		scanner := bufio.NewScanner(resp.Body)
		scanner.Split(NewSplitSSE(LFSep))

		for scanner.Scan() {
			require.NoError(t, scanner.Err())

			lineScanner := bufio.NewScanner(bytes.NewReader([]byte(scanner.Text())))
			for lineScanner.Scan() {
				require.NoError(t, lineScanner.Err())

				if data, ok := strings.CutPrefix(lineScanner.Text(), "data:"); ok {
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
					return
				}
			}
		}
	})
}
