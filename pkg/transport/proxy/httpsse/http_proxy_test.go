package httpsse

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
)

const testClientID = "test-client"

// TestNewHTTPSSEProxy tests the creation of a new HTTP SSE proxy
//
//nolint:paralleltest // Test modifies shared proxy state
func TestNewHTTPSSEProxy(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	assert.NotNil(t, proxy)
	assert.Equal(t, "localhost", proxy.host)
	assert.Equal(t, 8080, proxy.port)
	assert.Equal(t, "test-container", proxy.containerName)
	assert.NotNil(t, proxy.messageCh)
	assert.NotNil(t, proxy.sessionManager)
	assert.NotNil(t, proxy.closedClients)
	assert.NotNil(t, proxy.healthChecker)
}

// TestGetMessageChannel tests getting the message channel
//
//nolint:paralleltest // Test modifies shared proxy state
func TestGetMessageChannel(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	ch := proxy.GetMessageChannel()
	assert.NotNil(t, ch)
	assert.Equal(t, proxy.messageCh, ch)
}

// TestSendMessageToDestination tests sending messages to the destination
//
//nolint:paralleltest // Test modifies shared proxy state
func TestSendMessageToDestination(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Create a test message
	msg, err := jsonrpc2.NewCall(jsonrpc2.StringID("test"), "test.method", nil)
	require.NoError(t, err)

	// Send the message
	err = proxy.SendMessageToDestination(msg)
	assert.NoError(t, err)

	// Verify the message was sent
	select {
	case receivedMsg := <-proxy.messageCh:
		assert.Equal(t, msg, receivedMsg)
	case <-time.After(1 * time.Second):
		t.Fatal("Message was not sent to channel")
	}
}

// TestSendMessageToDestination_ChannelFull tests sending when channel is full
//
//nolint:paralleltest // Test modifies shared proxy state
func TestSendMessageToDestination_ChannelFull(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Fill the channel
	for i := 0; i < 100; i++ {
		msg, _ := jsonrpc2.NewCall(jsonrpc2.StringID(fmt.Sprintf("test%d", i)), "test.method", nil)
		proxy.messageCh <- msg
	}

	// Try to send another message
	msg, _ := jsonrpc2.NewCall(jsonrpc2.StringID("overflow"), "test.method", nil)
	err := proxy.SendMessageToDestination(msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to send message to destination")
}

// TestRemoveClient tests the removeClient method for preventing double-close
//
//nolint:paralleltest // Test modifies shared proxy state
func TestRemoveClient(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Create a client session
	clientID := "test-client-1"
	clientInfo := &ssecommon.SSEClient{
		MessageCh: make(chan string, 10),
		CreatedAt: time.Now(),
	}

	// Add session to manager
	sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
	err := proxy.sessionManager.AddSession(sseSession)
	require.NoError(t, err)

	// Remove the client once
	proxy.removeClient(clientID)

	// Verify client was removed from session manager
	_, exists := proxy.sessionManager.Get(clientID)
	assert.False(t, exists)

	// Verify client is marked as closed
	proxy.closedClientsMutex.Lock()
	closed := proxy.closedClients[clientID]
	proxy.closedClientsMutex.Unlock()
	assert.True(t, closed)

	// Try to remove the same client again (should not panic)
	assert.NotPanics(t, func() {
		proxy.removeClient(clientID)
	})
}

// TestConcurrentClientRemoval tests concurrent removal of clients
//
//nolint:paralleltest // Test modifies shared proxy state
func TestConcurrentClientRemoval(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Create multiple client sessions
	numClients := 100
	for i := 0; i < numClients; i++ {
		clientID := fmt.Sprintf("client-%d", i)
		clientInfo := &ssecommon.SSEClient{
			MessageCh: make(chan string, 10),
			CreatedAt: time.Now(),
		}

		// Add session to manager
		sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
		err := proxy.sessionManager.AddSession(sseSession)
		require.NoError(t, err)
	}

	// Concurrently remove all clients from multiple goroutines
	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(2) // Two goroutines trying to remove the same client
		clientID := fmt.Sprintf("client-%d", i)

		go func(id string) {
			defer wg.Done()
			proxy.removeClient(id)
		}(clientID)

		go func(id string) {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond) // Small delay
			proxy.removeClient(id)
		}(clientID)
	}

	// Wait for all goroutines to complete
	assert.NotPanics(t, func() {
		wg.Wait()
	})

	// Verify all clients are removed
	assert.Equal(t, 0, proxy.sessionManager.Count())
}

// TestForwardResponseToClients tests forwarding responses to connected clients
//
//nolint:paralleltest // Test modifies shared proxy state
func TestForwardResponseToClients(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)
	ctx := context.Background()

	// Create a client session
	clientID := testClientID
	messageCh := make(chan string, 10)
	clientInfo := &ssecommon.SSEClient{
		MessageCh: messageCh,
		CreatedAt: time.Now(),
	}

	// Add session to manager
	sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
	err := proxy.sessionManager.AddSession(sseSession)
	require.NoError(t, err)

	// Create a test response
	response, err := jsonrpc2.NewResponse(jsonrpc2.StringID("test"), "test result", nil)
	require.NoError(t, err)

	// Forward the response
	err = proxy.ForwardResponseToClients(ctx, response)
	assert.NoError(t, err)

	// Check if the message was received
	select {
	case msg := <-messageCh:
		assert.Contains(t, msg, "event: message")
		assert.Contains(t, msg, "test result")
	case <-time.After(1 * time.Second):
		t.Fatal("Message was not forwarded to client")
	}
}

// TestForwardResponseToClients_NoClients tests forwarding when no clients are connected
//
//nolint:paralleltest // Test modifies shared proxy state
func TestForwardResponseToClients_NoClients(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)
	ctx := context.Background()

	// Create a test response
	response, err := jsonrpc2.NewResponse(jsonrpc2.StringID("test"), "test result", nil)
	require.NoError(t, err)

	// Forward the response (should queue it)
	err = proxy.ForwardResponseToClients(ctx, response)
	assert.NoError(t, err)

	// Verify the message was queued
	proxy.pendingMutex.Lock()
	assert.Len(t, proxy.pendingMessages, 1)
	proxy.pendingMutex.Unlock()
}

// TestSendSSEEvent_ChannelFull tests handling of full client channels
//
//nolint:paralleltest // Test modifies shared proxy state
func TestSendSSEEvent_ChannelFull(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Create a client session with a small buffer
	clientID := testClientID
	messageCh := make(chan string, 1)
	clientInfo := &ssecommon.SSEClient{
		MessageCh: messageCh,
		CreatedAt: time.Now(),
	}

	// Add session to manager
	sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
	err := proxy.sessionManager.AddSession(sseSession)
	require.NoError(t, err)

	// Fill the channel
	messageCh <- "blocking message"

	// Try to send another message
	msg := ssecommon.NewSSEMessage("test", "test data")
	err2 := proxy.sendSSEEvent(msg)
	assert.NoError(t, err2)

	// In the improved implementation, we don't remove clients with full channels
	// We just skip sending to them and let the disconnect monitor handle cleanup
	_, exists := proxy.sessionManager.Get(clientID)
	assert.True(t, exists, "Client should still exist even with full channel")

	// Clean up
	proxy.removeClient(clientID)
}

// TestProcessPendingMessages tests processing of pending messages
//
//nolint:paralleltest // Test modifies shared proxy state
func TestProcessPendingMessages(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Add pending messages
	for i := 0; i < 5; i++ {
		msg := ssecommon.NewSSEMessage("test", fmt.Sprintf("data-%d", i))
		proxy.pendingMutex.Lock()
		proxy.pendingMessages = append(proxy.pendingMessages, ssecommon.NewPendingSSEMessage(msg))
		proxy.pendingMutex.Unlock()
	}

	// Create a client channel
	clientID := testClientID
	messageCh := make(chan string, 10)

	// Process pending messages
	proxy.processPendingMessages(clientID, messageCh)

	// Verify all messages were sent
	assert.Len(t, messageCh, 5)

	// Verify pending messages were cleared
	proxy.pendingMutex.Lock()
	assert.Empty(t, proxy.pendingMessages)
	proxy.pendingMutex.Unlock()
}

// TestHandleSSEConnection tests the SSE connection handler
//
//nolint:paralleltest // Test uses HTTP test server
func TestHandleSSEConnection(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.handleSSEConnection(w, r)
	}))
	defer server.Close()

	// Make a request
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Check headers
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "keep-alive", resp.Header.Get("Connection"))

	// Verify a client was registered
	time.Sleep(100 * time.Millisecond) // Give time for registration
	assert.Equal(t, 1, proxy.sessionManager.Count())
}

// TestHandlePostRequest tests handling of POST requests
//
//nolint:paralleltest // Test modifies shared proxy state
func TestHandlePostRequest(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Create a client session
	sessionID := "test-session"
	clientInfo := &ssecommon.SSEClient{
		MessageCh: make(chan string, 10),
		CreatedAt: time.Now(),
	}

	// Add session to manager
	sseSession := session.NewSSESessionWithClient(sessionID, clientInfo)
	err := proxy.sessionManager.AddSession(sseSession)
	require.NoError(t, err)

	// Create a valid JSON-RPC message
	msg, err := jsonrpc2.NewCall(jsonrpc2.StringID("test"), "test.method", nil)
	require.NoError(t, err)
	msgBytes, err := jsonrpc2.EncodeMessage(msg)
	require.NoError(t, err)

	// Create a test request with the JSON-RPC message
	req := httptest.NewRequest("POST", fmt.Sprintf("/messages?session_id=%s", sessionID), bytes.NewReader(msgBytes))
	w := httptest.NewRecorder()

	// Handle the request
	proxy.handlePostRequest(w, req)

	// Check response
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "Accepted", w.Body.String())

	// Verify the message was sent to the channel
	select {
	case receivedMsg := <-proxy.messageCh:
		assert.Equal(t, msg, receivedMsg)
	case <-time.After(1 * time.Second):
		t.Fatal("Message was not sent to channel")
	}
}

// TestHandlePostRequest_NoSessionID tests POST request without session ID
//
//nolint:paralleltest // Test modifies shared proxy state
func TestHandlePostRequest_NoSessionID(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Create a test request without session_id
	req := httptest.NewRequest("POST", "/messages", nil)
	w := httptest.NewRecorder()

	// Handle the request
	proxy.handlePostRequest(w, req)

	// Check response
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "session_id is required")
}

// TestHandlePostRequest_InvalidSession tests POST request with invalid session
//
//nolint:paralleltest // Test modifies shared proxy state
func TestHandlePostRequest_InvalidSession(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Create a test request with non-existent session_id
	req := httptest.NewRequest("POST", "/messages?session_id=invalid", nil)
	w := httptest.NewRecorder()

	// Handle the request
	proxy.handlePostRequest(w, req)

	// Check response
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "Could not find session")
}

// TestRWMutexUsage tests that RWMutex is used correctly for read operations
//
//nolint:paralleltest // Test modifies shared proxy state
func TestRWMutexUsage(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Add multiple client sessions
	for i := 0; i < 10; i++ {
		clientID := fmt.Sprintf("client-%d", i)
		clientInfo := &ssecommon.SSEClient{
			MessageCh: make(chan string, 10),
			CreatedAt: time.Now(),
		}

		// Add session to manager
		sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
		err := proxy.sessionManager.AddSession(sseSession)
		require.NoError(t, err)
	}

	// Test concurrent reads (should not block each other)
	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = proxy.sessionManager.Count()
			time.Sleep(10 * time.Millisecond) // Simulate some work
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	// If reads were serialized, this would take at least 1 second (100 * 10ms)
	// With RWMutex, it should be much faster
	assert.Less(t, elapsed, 200*time.Millisecond)
}

// TestClosedClientsCleanup tests the cleanup of closedClients map
//
//nolint:paralleltest // Test modifies shared proxy state
func TestClosedClientsCleanup(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, "test-container", nil)

	// Add many closed client sessions to trigger cleanup
	for i := 0; i < 1100; i++ {
		clientID := fmt.Sprintf("client-%d", i)
		clientInfo := &ssecommon.SSEClient{
			MessageCh: make(chan string, 1),
			CreatedAt: time.Now(),
		}

		// Add session to manager
		sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
		err := proxy.sessionManager.AddSession(sseSession)
		require.NoError(t, err)

		// Remove the client
		proxy.removeClient(clientID)
	}

	// Check that the closedClients map was reset
	proxy.closedClientsMutex.Lock()
	numClosed := len(proxy.closedClients)
	proxy.closedClientsMutex.Unlock()

	// Should be less than 1000 due to cleanup
	assert.Less(t, numClosed, 1000)
}

// TestStartStop tests starting and stopping the proxy
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestStartStop(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 0, "test-container", nil) // Use port 0 for auto-assignment
	ctx := context.Background()

	// Start the proxy
	err := proxy.Start(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, proxy.server)

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Stop the proxy
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err = proxy.Stop(stopCtx)
	assert.NoError(t, err)

	// Verify shutdown channel is closed
	select {
	case <-proxy.shutdownCh:
		// Good, channel is closed
	default:
		t.Fatal("Shutdown channel was not closed")
	}
}
