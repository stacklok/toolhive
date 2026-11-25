// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// fakeSDKNotifier captures the (method, params) the adapter forwards and returns
// a canned error, letting us assert the adapter's domain -> mcp-go mapping and
// its best-effort no-session handling.
type fakeSDKNotifier struct {
	gotMethod string
	gotParams map[string]any
	err       error
}

func (f *fakeSDKNotifier) SendNotificationToClient(_ context.Context, method string, params map[string]any) error {
	f.gotMethod = method
	f.gotParams = params
	return f.err
}

func TestSDKNotifierAdapter_NotifyProgress(t *testing.T) {
	t.Parallel()

	t.Run("includes total and message when set", func(t *testing.T) {
		t.Parallel()

		fake := &fakeSDKNotifier{}
		adapter := &sdkNotifierAdapter{mcpServer: fake}

		err := adapter.NotifyProgress(context.Background(), vmcp.ProgressNotification{
			ProgressToken: "tok-1",
			Progress:      0.5,
			Total:         1.0,
			Message:       "halfway",
		})
		require.NoError(t, err)
		assert.Equal(t, vmcp.MethodProgressNotification, fake.gotMethod)
		assert.Equal(t, "tok-1", fake.gotParams["progressToken"])
		assert.Equal(t, 0.5, fake.gotParams["progress"])
		assert.Equal(t, 1.0, fake.gotParams["total"])
		assert.Equal(t, "halfway", fake.gotParams["message"])
	})

	t.Run("omits total and message when unset", func(t *testing.T) {
		t.Parallel()

		fake := &fakeSDKNotifier{}
		adapter := &sdkNotifierAdapter{mcpServer: fake}

		err := adapter.NotifyProgress(context.Background(), vmcp.ProgressNotification{
			ProgressToken: "tok-1",
			Progress:      0.5,
		})
		require.NoError(t, err)
		_, hasTotal := fake.gotParams["total"]
		_, hasMessage := fake.gotParams["message"]
		assert.False(t, hasTotal)
		assert.False(t, hasMessage)
	})
}

func TestSDKNotifierAdapter_NotifyLog(t *testing.T) {
	t.Parallel()

	fake := &fakeSDKNotifier{}
	adapter := &sdkNotifierAdapter{mcpServer: fake}

	err := adapter.NotifyLog(context.Background(), vmcp.LogMessage{
		Level:  "info",
		Logger: "backend",
		Data:   "something happened",
	})
	require.NoError(t, err)
	assert.Equal(t, vmcp.MethodLogNotification, fake.gotMethod)
	assert.Equal(t, "info", fake.gotParams["level"])
	assert.Equal(t, "backend", fake.gotParams["logger"])
	assert.Equal(t, "something happened", fake.gotParams["data"])
}

// TestSDKNotifierAdapter_NoSessionIsNoOp verifies that a missing downstream
// session (ErrNoActiveSession) is swallowed so forwarding stays best-effort.
func TestSDKNotifierAdapter_NoSessionIsNoOp(t *testing.T) {
	t.Parallel()

	fake := &fakeSDKNotifier{err: server.ErrNoActiveSession}
	adapter := &sdkNotifierAdapter{mcpServer: fake}

	assert.NoError(t, adapter.NotifyProgress(context.Background(), vmcp.ProgressNotification{Progress: 1}))
	assert.NoError(t, adapter.NotifyLog(context.Background(), vmcp.LogMessage{Level: "info"}))
}

// TestSDKNotifierAdapter_OtherErrorIsPropagated verifies that a non-session
// error is surfaced (not swallowed).
func TestSDKNotifierAdapter_OtherErrorIsPropagated(t *testing.T) {
	t.Parallel()

	fake := &fakeSDKNotifier{err: errors.New("transport closed")}
	adapter := &sdkNotifierAdapter{mcpServer: fake}

	require.Error(t, adapter.NotifyProgress(context.Background(), vmcp.ProgressNotification{Progress: 1}))
	require.Error(t, adapter.NotifyLog(context.Background(), vmcp.LogMessage{Level: "info"}))
}

func TestSDKNotifierAdapter_Integration(t *testing.T) {
	t.Parallel()

	mcpServer := server.NewMCPServer("test", "1.0.0")
	adapter := NewSDKNotifierAdapter(mcpServer)
	assert.NotNil(t, adapter)
}
