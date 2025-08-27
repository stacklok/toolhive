package streamable

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// TestNewHTTPProxy tests the creation of a new HTTP proxy
//
//nolint:paralleltest // Test modifies shared proxy state
func TestNewHTTPProxy(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 8080, "test-container", nil)

	assert.NotNil(t, proxy)
	assert.Equal(t, "localhost", proxy.host)
	assert.Equal(t, 8080, proxy.port)
	assert.Equal(t, "test-container", proxy.containerName)
	assert.NotNil(t, proxy.messageCh)
	assert.NotNil(t, proxy.responseCh)
}

// TestProxyChannelCommunication tests basic proxy channel communication
//
//nolint:paralleltest // Test modifies shared proxy state
func TestProxyChannelCommunication(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 8080, "test-container", nil)
	ctx := context.Background()

	// Test that we can send a message to the destination
	request, err := jsonrpc2.NewCall(jsonrpc2.StringID("test"), "test.method", nil)
	require.NoError(t, err)

	err = proxy.SendMessageToDestination(request)
	assert.NoError(t, err)

	// Verify message was received
	select {
	case msg := <-proxy.GetMessageChannel():
		assert.Equal(t, request, msg)
	case <-time.After(1 * time.Second):
		t.Fatal("Message not received")
	}

	// Test that we can forward a response
	response, err := jsonrpc2.NewResponse(jsonrpc2.StringID("test"), "result", nil)
	require.NoError(t, err)

	err = proxy.ForwardResponseToClients(ctx, response)
	assert.NoError(t, err)

	// Verify response was forwarded
	select {
	case msg := <-proxy.GetResponseChannel():
		assert.Equal(t, response, msg)
	case <-time.After(1 * time.Second):
		t.Fatal("Response not forwarded")
	}
}

// TestSendMessageToDestination tests sending messages to the destination
//
//nolint:paralleltest // Test modifies shared proxy state
func TestSendMessageToDestination(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 8080, "test-container", nil)

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
	proxy := NewHTTPProxy("localhost", 8080, "test-container", nil)

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

// TestStartStop tests starting and stopping the proxy
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestStartStop(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 0, "test-container", nil) // Use port 0 for auto-assignment
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
