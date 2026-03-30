// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
)

const testClientID = "eeeeeeee-0001-0001-0001-000000000001"

// TestNewHTTPSSEProxy tests the creation of a new HTTP SSE proxy
//
//nolint:paralleltest // Test modifies shared proxy state
func TestNewHTTPSSEProxy(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	assert.NotNil(t, proxy)
	assert.Equal(t, "localhost", proxy.host)
	assert.Equal(t, 8080, proxy.port)
	assert.NotNil(t, proxy.messageCh)
	assert.NotNil(t, proxy.sessionManager)
	assert.NotNil(t, proxy.healthChecker)
}

// TestGetMessageChannel tests getting the message channel
//
//nolint:paralleltest // Test modifies shared proxy state
func TestGetMessageChannel(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	ch := proxy.GetMessageChannel()
	assert.NotNil(t, ch)
	assert.Equal(t, proxy.messageCh, ch)
}

// TestSendMessageToDestination tests sending messages to the destination
//
//nolint:paralleltest // Test modifies shared proxy state
func TestSendMessageToDestination(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

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
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

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
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	// Create a client session
	clientID := "eeeeeeee-0002-0002-0002-000000000002"
	clientInfo := &ssecommon.SSEClient{
		MessageCh: make(chan string, 10),
		CreatedAt: time.Now(),
	}

	// Add session to manager and live map
	sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
	err := proxy.sessionManager.AddSession(sseSession)
	require.NoError(t, err)
	proxy.liveSSESessions.Store(clientID, sseSession)

	// Remove the client once
	proxy.removeClient(clientID)

	// Verify client was removed from session manager
	_, exists := proxy.sessionManager.Get(clientID)
	assert.False(t, exists)

	// Verify client was removed from live sessions
	_, live := proxy.liveSSESessions.Load(clientID)
	assert.False(t, live)

	// Try to remove the same client again (should not panic)
	assert.NotPanics(t, func() {
		proxy.removeClient(clientID)
	})
}

// TestConcurrentClientRemoval tests concurrent removal of clients
//
//nolint:paralleltest // Test modifies shared proxy state
func TestConcurrentClientRemoval(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	// Create multiple client sessions
	numClients := 100
	clientIDs := make([]string, numClients)
	for i := 0; i < numClients; i++ {
		clientIDs[i] = uuid.New().String()
		clientInfo := &ssecommon.SSEClient{
			MessageCh: make(chan string, 10),
			CreatedAt: time.Now(),
		}

		// Add session to manager
		sseSession := session.NewSSESessionWithClient(clientIDs[i], clientInfo)
		err := proxy.sessionManager.AddSession(sseSession)
		require.NoError(t, err)
	}

	// Concurrently remove all clients from multiple goroutines
	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(2) // Two goroutines trying to remove the same client
		clientID := clientIDs[i]

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
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)
	ctx := context.Background()

	// Create a client session
	clientID := testClientID
	messageCh := make(chan string, 10)
	clientInfo := &ssecommon.SSEClient{
		MessageCh: messageCh,
		CreatedAt: time.Now(),
	}

	// Add session to manager and live-connection registry
	sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
	err := proxy.sessionManager.AddSession(sseSession)
	require.NoError(t, err)
	proxy.liveSSESessions.Store(clientID, sseSession)

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
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)
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
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	// Create a client session with a small buffer
	clientID := testClientID
	messageCh := make(chan string, 1)
	clientInfo := &ssecommon.SSEClient{
		MessageCh: messageCh,
		CreatedAt: time.Now(),
	}

	// Add session to manager and live-connection registry
	sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
	err := proxy.sessionManager.AddSession(sseSession)
	require.NoError(t, err)
	proxy.liveSSESessions.Store(clientID, sseSession)

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
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

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

// TestProcessPendingMessages_ChannelFull tests partial delivery when channel is full
//
//nolint:paralleltest // Test modifies shared proxy state
func TestProcessPendingMessages_ChannelFull(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	// Add 10 pending messages
	for i := 0; i < 10; i++ {
		msg := ssecommon.NewSSEMessage("test", fmt.Sprintf("data-%d", i))
		proxy.pendingMutex.Lock()
		proxy.pendingMessages = append(proxy.pendingMessages, ssecommon.NewPendingSSEMessage(msg))
		proxy.pendingMutex.Unlock()
	}

	// Create a client channel that can only hold 3 messages
	messageCh := make(chan string, 3)

	// Process pending messages
	proxy.processPendingMessages("client-1", messageCh)

	// Verify only 3 messages were sent
	assert.Len(t, messageCh, 3)

	// Verify 7 messages remain pending for reconnection
	proxy.pendingMutex.Lock()
	assert.Len(t, proxy.pendingMessages, 7)
	proxy.pendingMutex.Unlock()

	// Reconnected client should receive the remaining messages
	messageCh2 := make(chan string, 10)
	proxy.processPendingMessages("client-1", messageCh2)

	assert.Len(t, messageCh2, 7)

	// Verify all pending messages are now cleared
	proxy.pendingMutex.Lock()
	assert.Empty(t, proxy.pendingMessages)
	proxy.pendingMutex.Unlock()
}

// TestHandleSSEConnection tests the SSE connection handler
//
//nolint:paralleltest // Test uses HTTP test server
func TestHandleSSEConnection(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

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

// TestHandleSSEConnection_WithTrustProxyHeaders tests SSE connection with trusted proxy headers
//
//nolint:paralleltest // Test uses HTTP test server
func TestHandleSSEConnection_WithTrustProxyHeaders(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, true, nil, nil)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.handleSSEConnection(w, r)
	}))
	defer server.Close()

	// Create a request with X-Forwarded headers
	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "public.example.com")
	req.Header.Set("X-Forwarded-Port", "443")
	req.Header.Set("X-Forwarded-Prefix", "/api/v1")

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Read the first SSE event (endpoint event)
	buf := make([]byte, 1024)
	n, err := resp.Body.Read(buf)
	require.NoError(t, err)

	endpointEvent := string(buf[:n])

	// Verify the endpoint URL uses the X-Forwarded headers
	assert.Contains(t, endpointEvent, "event: endpoint")
	assert.Contains(t, endpointEvent, "https://public.example.com:443/api/v1/messages")
}

// TestHandleSSEConnection_WithoutTrustProxyHeaders tests SSE connection ignores untrusted proxy headers
//
//nolint:paralleltest // Test uses HTTP test server
func TestHandleSSEConnection_WithoutTrustProxyHeaders(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.handleSSEConnection(w, r)
	}))
	defer server.Close()

	// Create a request with X-Forwarded headers (should be ignored)
	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "malicious.example.com")
	req.Header.Set("X-Forwarded-Port", "9999")

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Read the first SSE event (endpoint event)
	buf := make([]byte, 1024)
	n, err := resp.Body.Read(buf)
	require.NoError(t, err)

	endpointEvent := string(buf[:n])

	// Verify the endpoint URL does NOT use the X-Forwarded headers
	assert.Contains(t, endpointEvent, "event: endpoint")
	assert.NotContains(t, endpointEvent, "malicious.example.com")
	assert.NotContains(t, endpointEvent, ":9999")
}

// TestHandlePostRequest tests handling of POST requests
//
//nolint:paralleltest // Test modifies shared proxy state
func TestHandlePostRequest(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	// Create a client session
	sessionID := "eeeeeeee-0003-0003-0003-000000000003"
	clientInfo := &ssecommon.SSEClient{
		MessageCh: make(chan string, 10),
		CreatedAt: time.Now(),
	}

	// Add session to manager and to the live registry (mirrors what handleSSEConnection does)
	sseSession := session.NewSSESessionWithClient(sessionID, clientInfo)
	err := proxy.sessionManager.AddSession(sseSession)
	require.NoError(t, err)
	proxy.liveSSESessions.Store(sessionID, sseSession)

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
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

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
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	// Create a test request with non-existent session_id
	req := httptest.NewRequest("POST", "/messages?session_id=invalid", nil)
	w := httptest.NewRecorder()

	// Handle the request
	proxy.handlePostRequest(w, req)

	// Check response
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `"code":-32001`)
}

// TestRWMutexUsage tests that RWMutex is used correctly for read operations
//
//nolint:paralleltest // Test modifies shared proxy state
func TestRWMutexUsage(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	// Add multiple client sessions
	for i := 0; i < 10; i++ {
		clientInfo := &ssecommon.SSEClient{
			MessageCh: make(chan string, 10),
			CreatedAt: time.Now(),
		}

		// Add session to manager
		sseSession := session.NewSSESessionWithClient(uuid.New().String(), clientInfo)
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

// TestRemoveClientCleansLiveSessions verifies that removeClient removes entries
// from liveSSESessions so it does not grow unbounded.
//
//nolint:paralleltest // Test modifies shared proxy state
func TestRemoveClientCleansLiveSessions(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 8080, false, nil, nil)

	for i := 0; i < 100; i++ {
		clientID := uuid.New().String()
		clientInfo := &ssecommon.SSEClient{
			MessageCh: make(chan string, 1),
			CreatedAt: time.Now(),
		}

		sseSession := session.NewSSESessionWithClient(clientID, clientInfo)
		err := proxy.sessionManager.AddSession(sseSession)
		require.NoError(t, err)
		proxy.liveSSESessions.Store(clientID, sseSession)

		proxy.removeClient(clientID)
	}

	var liveCount int
	proxy.liveSSESessions.Range(func(_, _ interface{}) bool {
		liveCount++
		return true
	})
	assert.Equal(t, 0, liveCount, "liveSSESessions should be empty after all clients are removed")
}

// TestNewHTTPSSEProxyWithSessionStorage tests that WithSessionStorage option injects a custom storage backend.
func TestNewHTTPSSEProxyWithSessionStorage(t *testing.T) {
	t.Parallel()
	storage := session.NewLocalStorage()
	proxy := NewHTTPSSEProxy("localhost", 0, false, nil, nil, WithSessionStorage(storage))
	require.NotNil(t, proxy)
	require.NotNil(t, proxy.sessionManager)
}

// TestStartStop tests starting and stopping the proxy
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestStartStop(t *testing.T) {
	proxy := NewHTTPSSEProxy("localhost", 0, false, nil, nil) // Use port 0 for auto-assignment
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
