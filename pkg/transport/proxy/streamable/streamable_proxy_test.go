// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	proxy := NewHTTPProxy("localhost", 8080, nil)

	assert.NotNil(t, proxy)
	assert.Equal(t, "localhost", proxy.host)
	assert.Equal(t, 8080, proxy.port)
	assert.NotNil(t, proxy.messageCh)
	assert.NotNil(t, proxy.responseCh)
}

// TestProxyChannelCommunication tests basic proxy channel communication
//
//nolint:paralleltest // Test modifies shared proxy state
func TestProxyChannelCommunication(t *testing.T) {
	proxy := NewHTTPProxy("localhost", 8080, nil)
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
	proxy := NewHTTPProxy("localhost", 8080, nil)

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
	proxy := NewHTTPProxy("localhost", 8080, nil)

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
	proxy := NewHTTPProxy("localhost", 0, nil) // Use port 0 for auto-assignment
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

// TestResolveRequestTimeout tests the resolveRequestTimeout function.
func TestResolveRequestTimeout(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     time.Duration
	}{
		{
			name:     "no env var returns default",
			envValue: "",
			want:     defaultRequestTimeout,
		},
		{
			name:     "valid duration string",
			envValue: "10m",
			want:     10 * time.Minute,
		},
		{
			name:     "valid short duration",
			envValue: "45s",
			want:     45 * time.Second,
		},
		{
			name:     "invalid string returns default",
			envValue: "garbage",
			want:     defaultRequestTimeout,
		},
		{
			name:     "zero duration returns default",
			envValue: "0s",
			want:     defaultRequestTimeout,
		},
		{
			name:     "negative duration returns default",
			envValue: "-5m",
			want:     defaultRequestTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Always call t.Setenv to ensure the env var is in a known state,
			// even for the "unset" case (empty string is treated as unset).
			t.Setenv(proxyRequestTimeoutEnv, tt.envValue)
			got := resolveRequestTimeout()
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestNewHTTPProxyUsesResolvedTimeout verifies the constructor wires the
// resolved timeout into the proxy struct.
func TestNewHTTPProxyUsesResolvedTimeout(t *testing.T) {
	t.Setenv(proxyRequestTimeoutEnv, "7m")
	proxy := NewHTTPProxy("localhost", 0, nil)
	assert.Equal(t, 7*time.Minute, proxy.requestTimeout)
}

// TestNewHTTPProxyDefaultTimeout verifies the default timeout when no env var
// is set. Not parallel because t.Setenv is needed to clear any ambient
// TOOLHIVE_PROXY_REQUEST_TIMEOUT from the test runner's environment.
func TestNewHTTPProxyDefaultTimeout(t *testing.T) { //nolint:paralleltest
	t.Setenv(proxyRequestTimeoutEnv, "")
	proxy := NewHTTPProxy("localhost", 0, nil)
	assert.Equal(t, defaultRequestTimeout, proxy.requestTimeout)
}
