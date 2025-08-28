package httpsse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	logger.Initialize()
}

// TestIntegrationSSEProxyStressTest simulates the scenario from issue #1572
// where multiple clients connect, disconnect, and reconnect frequently
func TestIntegrationSSEProxyStressTest(t *testing.T) {
	t.Parallel()

	// Create proxy with a random port
	proxy := NewHTTPSSEProxy("localhost", 0, "test-container", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the proxy
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	}()

	// Get the actual port
	proxyURL := fmt.Sprintf("http://%s", proxy.server.Addr)
	t.Logf("Proxy started at %s", proxyURL)

	// Track statistics
	var (
		totalConnections    int32
		totalDisconnections int32
		totalMessages       int32
		totalErrors         int32
	)

	// Create a worker that processes messages from the proxy
	go func() {
		for msg := range proxy.GetMessageChannel() {
			// Echo the message back to clients
			_ = proxy.ForwardResponseToClients(ctx, msg)
			atomic.AddInt32(&totalMessages, 1)
		}
	}()

	// Number of concurrent clients and duration
	numClients := 10
	testDuration := 5 * time.Second
	messagesPerClient := 5

	// WaitGroup for all clients
	var wg sync.WaitGroup

	// Start multiple clients that connect and disconnect
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientNum int) {
			defer wg.Done()

			startTime := time.Now()
			reconnectCount := 0

			for time.Since(startTime) < testDuration {
				reconnectCount++
				atomic.AddInt32(&totalConnections, 1)

				// Connect to SSE endpoint
				sseResp, err := http.Get(proxyURL + "/sse")
				if err != nil {
					atomic.AddInt32(&totalErrors, 1)
					t.Logf("Client %d: Failed to connect: %v", clientNum, err)
					time.Sleep(100 * time.Millisecond)
					continue
				}

				// Extract session ID from the endpoint event
				sessionID, err := extractSessionID(sseResp.Body)
				if err != nil {
					atomic.AddInt32(&totalErrors, 1)
					t.Logf("Client %d: Failed to extract session ID: %v", clientNum, err)
					sseResp.Body.Close()
					time.Sleep(100 * time.Millisecond)
					continue
				}

				// Send some messages
				for j := 0; j < messagesPerClient; j++ {
					msg, _ := jsonrpc2.NewCall(
						jsonrpc2.StringID(fmt.Sprintf("client%d-msg%d", clientNum, j)),
						"test.method",
						map[string]interface{}{"data": fmt.Sprintf("test data %d", j)},
					)
					msgBytes, _ := jsonrpc2.EncodeMessage(msg)

					postResp, err := http.Post(
						fmt.Sprintf("%s/messages?session_id=%s", proxyURL, sessionID),
						"application/json",
						bytes.NewReader(msgBytes),
					)
					if err != nil {
						atomic.AddInt32(&totalErrors, 1)
						t.Logf("Client %d: Failed to send message: %v", clientNum, err)
						break
					}
					postResp.Body.Close()

					// Small delay between messages
					time.Sleep(10 * time.Millisecond)
				}

				// Close the SSE connection
				sseResp.Body.Close()
				atomic.AddInt32(&totalDisconnections, 1)

				// Random delay before reconnecting (simulating real client behavior)
				time.Sleep(time.Duration(100+clientNum*10) * time.Millisecond)
			}

			t.Logf("Client %d: Completed with %d reconnections", clientNum, reconnectCount)
		}(i)
	}

	// Wait for all clients to complete
	wg.Wait()

	// Give some time for final messages to be processed
	time.Sleep(500 * time.Millisecond)

	// Log statistics
	t.Logf("Test Statistics:")
	t.Logf("  Total Connections: %d", atomic.LoadInt32(&totalConnections))
	t.Logf("  Total Disconnections: %d", atomic.LoadInt32(&totalDisconnections))
	t.Logf("  Total Messages Processed: %d", atomic.LoadInt32(&totalMessages))
	t.Logf("  Total Errors: %d", atomic.LoadInt32(&totalErrors))

	// Verify no panics occurred and proxy is still functional
	assert.NotNil(t, proxy.server)

	// Verify we can still connect after the stress test
	finalResp, err := http.Get(proxyURL + "/sse")
	assert.NoError(t, err)
	if finalResp != nil {
		finalResp.Body.Close()
	}

	// Check that we processed a reasonable number of messages
	assert.Greater(t, atomic.LoadInt32(&totalMessages), int32(0), "Should have processed some messages")

	// Check that errors are within acceptable limits (less than 10% of connections)
	errorRate := float64(atomic.LoadInt32(&totalErrors)) / float64(atomic.LoadInt32(&totalConnections))
	assert.Less(t, errorRate, 0.1, "Error rate should be less than 10%")
}

// TestIntegrationConcurrentClientsWithLongRunning tests a mix of short-lived and long-running clients
func TestIntegrationConcurrentClientsWithLongRunning(t *testing.T) {
	t.Parallel()

	// Create and start proxy
	proxy := NewHTTPSSEProxy("localhost", 0, "test-container", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	}()

	proxyURL := fmt.Sprintf("http://%s", proxy.server.Addr)

	// Message processor
	go func() {
		for msg := range proxy.GetMessageChannel() {
			// Echo messages back
			_ = proxy.ForwardResponseToClients(ctx, msg)
		}
	}()

	var wg sync.WaitGroup

	// Start long-running clients
	numLongRunning := 3
	for i := 0; i < numLongRunning; i++ {
		wg.Add(1)
		go func(clientNum int) {
			defer wg.Done()

			// Connect and stay connected
			resp, err := http.Get(proxyURL + "/sse")
			if err != nil {
				t.Logf("Long-running client %d: Failed to connect: %v", clientNum, err)
				return
			}
			defer resp.Body.Close()

			sessionID, err := extractSessionID(resp.Body)
			if err != nil {
				t.Logf("Long-running client %d: Failed to get session ID: %v", clientNum, err)
				return
			}

			// Send periodic messages
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()

			timeout := time.After(3 * time.Second)
			msgCount := 0

			for {
				select {
				case <-ticker.C:
					msg, _ := jsonrpc2.NewCall(
						jsonrpc2.StringID(fmt.Sprintf("long%d-msg%d", clientNum, msgCount)),
						"ping",
						nil,
					)
					msgBytes, _ := jsonrpc2.EncodeMessage(msg)

					postResp, err := http.Post(
						fmt.Sprintf("%s/messages?session_id=%s", proxyURL, sessionID),
						"application/json",
						bytes.NewReader(msgBytes),
					)
					if err != nil {
						t.Logf("Long-running client %d: Failed to send message: %v", clientNum, err)
						return
					}
					postResp.Body.Close()
					msgCount++

				case <-timeout:
					t.Logf("Long-running client %d: Sent %d messages", clientNum, msgCount)
					return
				}
			}
		}(i)
	}

	// Start short-lived clients that connect and disconnect frequently
	numShortLived := 20
	for i := 0; i < numShortLived; i++ {
		wg.Add(1)
		go func(clientNum int) {
			defer wg.Done()

			// Quick connect, send message, disconnect
			resp, err := http.Get(proxyURL + "/sse")
			if err != nil {
				t.Logf("Short-lived client %d: Failed to connect: %v", clientNum, err)
				return
			}

			sessionID, err := extractSessionID(resp.Body)
			resp.Body.Close() // Close immediately after getting session ID

			if err != nil {
				t.Logf("Short-lived client %d: Failed to get session ID: %v", clientNum, err)
				return
			}

			// Send a single message
			msg, _ := jsonrpc2.NewCall(
				jsonrpc2.StringID(fmt.Sprintf("short%d", clientNum)),
				"test",
				nil,
			)
			msgBytes, _ := jsonrpc2.EncodeMessage(msg)

			postResp, err := http.Post(
				fmt.Sprintf("%s/messages?session_id=%s", proxyURL, sessionID),
				"application/json",
				bytes.NewReader(msgBytes),
			)
			if err != nil {
				t.Logf("Short-lived client %d: Failed to send message: %v", clientNum, err)
				return
			}
			postResp.Body.Close()

			// Small random delay
			time.Sleep(time.Duration(50+clientNum*5) * time.Millisecond)
		}(i)
	}

	// Wait for all clients
	wg.Wait()

	// Verify proxy is still healthy
	assert.NotNil(t, proxy.server)

	// Check we can still connect
	finalResp, err := http.Get(proxyURL + "/sse")
	assert.NoError(t, err)
	if finalResp != nil {
		finalResp.Body.Close()
	}
}

// TestIntegrationMemoryLeakPrevention tests that the closedClients map doesn't grow unbounded
func TestIntegrationMemoryLeakPrevention(t *testing.T) {
	t.Parallel()
	proxy := NewHTTPSSEProxy("localhost", 0, "test-container", nil)
	ctx := context.Background()

	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	}()

	proxyURL := fmt.Sprintf("http://%s", proxy.server.Addr)

	// Create and remove many clients to trigger cleanup
	for i := 0; i < 1500; i++ {
		// Quick connect and disconnect
		resp, err := http.Get(proxyURL + "/sse")
		if err != nil {
			continue
		}

		// Extract session ID and immediately close
		sessionID, _ := extractSessionID(resp.Body)
		resp.Body.Close()

		// The disconnection should trigger removeClient
		if sessionID != "" {
			// Give time for the disconnect to be processed
			time.Sleep(1 * time.Millisecond)
		}

		// Check closedClients size periodically
		if i%100 == 0 {
			proxy.closedClientsMutex.Lock()
			size := len(proxy.closedClients)
			proxy.closedClientsMutex.Unlock()

			// Should have been reset when it hit 1000
			assert.Less(t, size, 1100, "closedClients map should be cleaned up")
			t.Logf("After %d clients, closedClients size: %d", i, size)
		}
	}

	// Final check
	proxy.closedClientsMutex.Lock()
	finalSize := len(proxy.closedClients)
	proxy.closedClientsMutex.Unlock()

	assert.Less(t, finalSize, 1000, "Final closedClients size should be less than 1000")
	t.Logf("Final closedClients size: %d", finalSize)
}

// Helper function to extract session ID from SSE response
func extractSessionID(body io.Reader) (string, error) {
	buf := make([]byte, 4096)
	n, err := body.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}

	// Look for the endpoint event which contains the session ID
	const prefix = "session_id="
	idx := bytes.Index(buf[:n], []byte(prefix))
	if idx == -1 {
		return "", fmt.Errorf("session_id not found in response")
	}

	// Extract session ID (UUID format)
	start := idx + len(prefix)
	end := start + 36 // UUID length
	if end > n {
		return "", fmt.Errorf("incomplete session_id")
	}

	return string(buf[start:end]), nil
}
