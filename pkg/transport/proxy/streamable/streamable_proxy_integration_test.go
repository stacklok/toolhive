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
				// Send notification first (should be ignored by HTTP handler)
				notification, _ := jsonrpc2.NewNotification("progress", map[string]interface{}{
					"status": "processing",
				})
				proxy.ForwardResponseToClients(ctx, notification)

				// Finally send the actual response
				if req, ok := msg.(*jsonrpc2.Request); ok && req.ID.IsValid() {
					response, _ := jsonrpc2.NewResponse(req.ID, "operation complete", nil)
					proxy.ForwardResponseToClients(ctx, response)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	proxyURL := "http://localhost:8091" + StreamableHTTPEndpoint

	// Test single request
	requestJSON := `{"jsonrpc": "2.0", "method": "test.method", "id": "req-123"}`
	resp, err := http.Post(proxyURL, "application/json", bytes.NewReader([]byte(requestJSON)))
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should get the response, not notifications
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var responseData map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&responseData)
	require.NoError(t, err)

	// Verify we got the actual response (proving notifications were ignored)
	assert.Equal(t, "2.0", responseData["jsonrpc"])
	assert.Equal(t, "req-123", responseData["id"])
	assert.Equal(t, "operation complete", responseData["result"])

	// Test batch request
	batchJSON := `[{"jsonrpc": "2.0", "method": "test.batch", "id": "batch-1"}]`
	resp2, err := http.Post(proxyURL, "application/json", bytes.NewReader([]byte(batchJSON)))
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
