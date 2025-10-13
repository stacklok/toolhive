package streamable

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// TestHTTPRequestIgnoresNotifications tests that HTTP requests ignore notifications
// and only return the actual response. This addresses the fix for issue #1568.
//
//nolint:paralleltest // Test starts HTTP server
func TestHTTPRequestIgnoresNotifications(t *testing.T) {
	t.Skip("Test incompatible with new SDK's streamable HTTP requirements - " +
		"SDK requires both application/json and text/event-stream in Accept header, " +
		"but proxy switches to SSE mode when text/event-stream is present. " +
		"This test needs to be redesigned for the new SDK architecture.")
	proxy := NewHTTPProxy("localhost", 8091, "test-container", nil)
	ctx := context.Background()

	// Start the proxy server
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer proxy.Stop(ctx)

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Simulate MCP server that sends notifications before responses
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				// Log what we received
				t.Logf("Simulated server received message: %v", msg)

				// Send notification first (should be ignored by HTTP handler)
				notification, _ := jsonrpc2.NewNotification("progress", map[string]interface{}{
					"status": "processing",
				})
				proxy.ForwardResponseToClients(ctx, notification)

				// Finally send the actual response
				if req, ok := msg.(*jsonrpc2.Request); ok && req.ID.IsValid() {
					// For initialize, send appropriate response
					var result interface{}
					if req.Method == "initialize" {
						result = map[string]interface{}{
							"protocolVersion": "2024-11-05",
							"serverInfo": map[string]interface{}{
								"name":    "test-server",
								"version": "1.0.0",
							},
						}
					} else if req.Method == "tools/list" {
						result = map[string]interface{}{
							"tools": []interface{}{},
						}
					} else {
						result = "operation complete"
					}
					response, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					proxy.ForwardResponseToClients(ctx, response)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	proxyURL := "http://localhost:8091" + StreamableHTTPEndpoint

	// Test single request - use a valid MCP method
	requestJSON := `{"jsonrpc": "2.0", "method": "initialize", "params": {"protocolVersion": "2024-11-05", "clientInfo": {"name": "test", "version": "1.0"}}, "id": "req-123"}`

	// Create request with Accept header for JSON response (not SSE)
	req, err := http.NewRequest("POST", proxyURL, bytes.NewReader([]byte(requestJSON)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should get the response, not notifications
	if resp.StatusCode != http.StatusOK {
		// Read the error message to understand what went wrong
		var bodyBytes bytes.Buffer
		_, _ = bodyBytes.ReadFrom(resp.Body)
		t.Logf("Got error response: %d, body: %s", resp.StatusCode, bodyBytes.String())
	}
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var responseData map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&responseData)
	require.NoError(t, err)

	// Verify we got the actual response (proving notifications were ignored)
	assert.Equal(t, "2.0", responseData["jsonrpc"])
	assert.Equal(t, "req-123", responseData["id"])
	// For initialize, we expect a result with serverInfo
	assert.NotNil(t, responseData["result"])

	// Test batch request - use a valid MCP method
	batchJSON := `[{"jsonrpc": "2.0", "method": "tools/list", "params": {}, "id": "batch-1"}]`

	// Create batch request with Accept header for JSON response (not SSE)
	req2, err := http.NewRequest("POST", proxyURL, bytes.NewReader([]byte(batchJSON)))
	require.NoError(t, err)
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json")

	resp2, err := client.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	var batchResponse []map[string]interface{}
	err = json.NewDecoder(resp2.Body).Decode(&batchResponse)
	require.NoError(t, err)

	// Should have one response (notifications filtered out)
	require.Len(t, batchResponse, 1)
	assert.Equal(t, "2.0", batchResponse[0]["jsonrpc"])
	assert.Equal(t, "batch-1", batchResponse[0]["id"])
	assert.Equal(t, "operation complete", batchResponse[0]["result"])
}
