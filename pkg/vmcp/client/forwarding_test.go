// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// ctxKey is a private context key used to prove that the forwarder relays with
// the captured per-call downstream context (callCtx), not the receive-loop
// handler context the go-sdk supplies.
type ctxKey struct{}

func TestNewElicitationForwarder_ForwardsWithCapturedCtx(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	req := mocks.NewMockElicitationRequester(ctrl)

	callCtx := context.WithValue(t.Context(), ctxKey{}, "downstream-session")

	req.EXPECT().
		RequestElicitation(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, r vmcp.ElicitationRequest) (*vmcp.ElicitationResult, error) {
			// The captured downstream ctx is used, not the handler ctx.
			assert.Equal(t, "downstream-session", ctx.Value(ctxKey{}))
			// SDK -> domain conversion carried the request fields.
			assert.Equal(t, "Confirm?", r.Message)
			assert.Equal(t, map[string]any{"type": "object"}, r.RequestedSchema)
			return &vmcp.ElicitationResult{Action: "accept", Content: map[string]any{"ok": true}}, nil
		})

	handler := newElicitationForwarder(callCtx, req)
	res, err := handler.Elicit(t.Context(), mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Message:         "Confirm?",
			RequestedSchema: map[string]any{"type": "object"},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, res)
	// domain -> SDK result conversion.
	assert.Equal(t, mcp.ElicitationResponseActionAccept, res.Action)
	assert.Equal(t, map[string]any{"ok": true}, res.Content)
}

func TestNewElicitationForwarder_PropagatesError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	req := mocks.NewMockElicitationRequester(ctrl)
	req.EXPECT().
		RequestElicitation(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("no active session"))

	handler := newElicitationForwarder(t.Context(), req)
	res, err := handler.Elicit(t.Context(), mcp.ElicitationRequest{})
	require.Error(t, err)
	assert.Nil(t, res)
}

func TestNewSamplingForwarder_ForwardsWithCapturedCtx(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	req := mocks.NewMockSamplingRequester(ctrl)

	callCtx := context.WithValue(t.Context(), ctxKey{}, "downstream-session")

	req.EXPECT().
		RequestSampling(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, r vmcp.SamplingRequest) (*vmcp.SamplingResult, error) {
			assert.Equal(t, "downstream-session", ctx.Value(ctxKey{}))
			require.Len(t, r.Messages, 1)
			assert.Equal(t, "user", r.Messages[0].Role)
			assert.Equal(t, 128, r.MaxTokens)
			return &vmcp.SamplingResult{
				Role:       "assistant",
				Content:    map[string]any{"type": "text", "text": "hi"},
				Model:      "m",
				StopReason: "endTurn",
			}, nil
		})

	handler := newSamplingForwarder(callCtx, req)
	res, err := handler.CreateMessage(t.Context(), mcp.CreateMessageRequest{
		CreateMessageParams: mcp.CreateMessageParams{
			Messages: []mcp.SamplingMessage{
				{Role: mcp.Role("user"), Content: map[string]any{"type": "text", "text": "hello"}},
			},
			MaxTokens: 128,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, mcp.Role("assistant"), res.Role)
	assert.Equal(t, "m", res.Model)
	assert.Equal(t, "endTurn", res.StopReason)
}

func TestNewSamplingForwarder_PropagatesError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	req := mocks.NewMockSamplingRequester(ctrl)
	req.EXPECT().
		RequestSampling(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("session does not support sampling"))

	handler := newSamplingForwarder(t.Context(), req)
	res, err := handler.CreateMessage(t.Context(), mcp.CreateMessageRequest{})
	require.Error(t, err)
	assert.Nil(t, res)
}

func TestNewNotificationForwarder_ForwardsProgress(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	notifier := mocks.NewMockClientNotifier(ctrl)

	callCtx := context.WithValue(t.Context(), ctxKey{}, "downstream-session")

	notifier.EXPECT().
		NotifyProgress(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, n vmcp.ProgressNotification) error {
			assert.Equal(t, "downstream-session", ctx.Value(ctxKey{}))
			assert.Equal(t, "tok-1", n.ProgressToken)
			assert.Equal(t, 0.5, n.Progress)
			assert.Equal(t, 1.0, n.Total)
			assert.Equal(t, "halfway", n.Message)
			return nil
		})

	handler := newNotificationForwarder(callCtx, notifier)
	handler(progressNotification("tok-1", 0.5, 1.0, "halfway"))
}

func TestNewNotificationForwarder_ForwardsLog(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	notifier := mocks.NewMockClientNotifier(ctrl)

	notifier.EXPECT().
		NotifyLog(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, n vmcp.LogMessage) error {
			assert.Equal(t, "info", n.Level)
			assert.Equal(t, "backend", n.Logger)
			assert.Equal(t, "hello", n.Data)
			return nil
		})

	handler := newNotificationForwarder(t.Context(), notifier)
	handler(mcp.JSONRPCNotification{
		Notification: mcp.Notification{
			Method: vmcp.MethodLogNotification,
			Params: mcp.NotificationParams{
				AdditionalFields: map[string]any{"level": "info", "logger": "backend", "data": "hello"},
			},
		},
	})
}

// TestNewNotificationForwarder_IgnoresUnknownMethod verifies that a notification
// method the forwarder does not relay does not reach the notifier.
func TestNewNotificationForwarder_IgnoresUnknownMethod(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	notifier := mocks.NewMockClientNotifier(ctrl)
	// No EXPECT calls: any invocation fails the test.

	handler := newNotificationForwarder(t.Context(), notifier)
	handler(mcp.JSONRPCNotification{
		Notification: mcp.Notification{Method: "notifications/tools/list_changed"},
	})
}

// TestNewNotificationForwarder_SwallowsError verifies that a notifier error
// (e.g. best-effort no-op returning an error) does not panic the receive loop.
func TestNewNotificationForwarder_SwallowsError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	notifier := mocks.NewMockClientNotifier(ctrl)
	notifier.EXPECT().
		NotifyProgress(gomock.Any(), gomock.Any()).
		Return(errors.New("transport closed"))

	handler := newNotificationForwarder(t.Context(), notifier)
	assert.NotPanics(t, func() {
		handler(progressNotification("tok", 1, 0, ""))
	})
}

func TestBindForwarders_StoresSnapshot(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	elicit := mocks.NewMockElicitationRequester(ctrl)
	sampling := mocks.NewMockSamplingRequester(ctrl)
	notifier := mocks.NewMockClientNotifier(ctrl)

	h := &httpBackendClient{}
	assert.Nil(t, h.forwarders.Load())

	h.BindForwarders(elicit, sampling, notifier)

	fwd := h.forwarders.Load()
	require.NotNil(t, fwd)
	assert.Same(t, elicit, fwd.elicitation)
	assert.Same(t, sampling, fwd.sampling)
	assert.Same(t, notifier, fwd.notifier)
}

// TestDeriveForwardCtx_CancelsOnHandlerCancel verifies that the derived context
// (rooted at the captured downstream ctx) is cancelled when the handler ctx is
// cancelled, so a backend-side cancellation aborts the forwarded round-trip.
func TestDeriveForwardCtx_CancelsOnHandlerCancel(t *testing.T) {
	t.Parallel()

	base := context.WithValue(t.Context(), ctxKey{}, "downstream-session")
	handlerCtx, cancelHandler := context.WithCancel(t.Context())

	ctx, cancel := deriveForwardCtx(base, handlerCtx)
	defer cancel()

	// The derived ctx still resolves the downstream session value from base.
	assert.Equal(t, "downstream-session", ctx.Value(ctxKey{}))

	select {
	case <-ctx.Done():
		t.Fatal("derived ctx cancelled prematurely")
	default:
	}

	cancelHandler()
	select {
	case <-ctx.Done():
	case <-t.Context().Done():
		t.Fatal("derived ctx not cancelled after handler cancellation")
	}
}

// progressNotification builds an mcp.JSONRPCNotification for notifications/progress
// with the wire fields the go-sdk client surfaces via OnNotification.
func progressNotification(token any, progress, total float64, message string) mcp.JSONRPCNotification {
	fields := map[string]any{"progressToken": token, "progress": progress}
	if total != 0 {
		fields["total"] = total
	}
	if message != "" {
		fields["message"] = message
	}
	return mcp.JSONRPCNotification{
		Notification: mcp.Notification{
			Method: vmcp.MethodProgressNotification,
			Params: mcp.NotificationParams{AdditionalFields: fields},
		},
	}
}
