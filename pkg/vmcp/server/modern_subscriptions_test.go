// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// pushCaps builds an mcp.ServerCapabilities advertising the given push flags.
// The anonymous struct types must be spelled out to match mcp.ServerCapabilities
// field-for-field; this helper keeps that noise out of the table below and lets
// tests construct advertisements the live newModernCapabilities cannot currently
// produce (every push flag it emits is false).
func pushCaps(toolsLC, promptsLC, resourcesLC, subscribe bool) mcp.ServerCapabilities {
	var caps mcp.ServerCapabilities
	caps.Tools = &struct {
		ListChanged bool `json:"listChanged,omitempty"`
	}{ListChanged: toolsLC}
	caps.Prompts = &struct {
		ListChanged bool `json:"listChanged,omitempty"`
	}{ListChanged: promptsLC}
	caps.Resources = &struct {
		Subscribe   bool `json:"subscribe,omitempty"`
		ListChanged bool `json:"listChanged,omitempty"`
	}{Subscribe: subscribe, ListChanged: resourcesLC}
	return caps
}

// allWanted is a client asking for every subscribable type there is.
func allWanted() notificationSubscriptions {
	return notificationSubscriptions{
		ToolsListChanged:      true,
		PromptsListChanged:    true,
		ResourcesListChanged:  true,
		ResourceSubscriptions: []string{"file:///a", "file:///b"},
	}
}

// TestHonoredSubscriptions is the central assertion of this change: a
// notification type is honored if and only if the client asked for it AND the
// matching capability is advertised. That intersection is the whole of SEP-2575's
// per-type/per-URI opt-in filtering, and it is what guarantees the
// acknowledgement can never promise more than server/discover advertises.
//
// The advertised side is a parameter rather than read from a constant, so this
// covers the honoring combinations that the live advertisement cannot currently
// reach -- which is exactly what makes the handler correct-by-construction the
// day a capability flag flips.
func TestHonoredSubscriptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		want       notificationSubscriptions
		advertised mcp.ServerCapabilities
		expect     notificationSubscriptions
	}{
		{
			name:       "live advertisement honors nothing even when everything is requested",
			want:       allWanted(),
			advertised: newModernCapabilities(true, true, true, true),
			expect:     notificationSubscriptions{},
		},
		{
			name:       "all advertised and all wanted honors everything",
			want:       allWanted(),
			advertised: pushCaps(true, true, true, true),
			expect:     allWanted(),
		},
		{
			name:       "nothing wanted honors nothing even when all advertised",
			want:       notificationSubscriptions{},
			advertised: pushCaps(true, true, true, true),
			expect:     notificationSubscriptions{},
		},
		{
			name:       "advertised but not wanted is not honored",
			want:       notificationSubscriptions{ToolsListChanged: true},
			advertised: pushCaps(true, true, true, true),
			expect:     notificationSubscriptions{ToolsListChanged: true},
		},
		{
			name:       "wanted but not advertised is dropped per type",
			want:       allWanted(),
			advertised: pushCaps(true, false, false, false),
			expect:     notificationSubscriptions{ToolsListChanged: true},
		},
		{
			name:       "resource subscriptions are gated by subscribe, not by resources listChanged",
			want:       notificationSubscriptions{ResourceSubscriptions: []string{"file:///a"}},
			advertised: pushCaps(false, false, true, false),
			expect:     notificationSubscriptions{},
		},
		{
			name:       "resource subscriptions survive when subscribe is advertised",
			want:       notificationSubscriptions{ResourceSubscriptions: []string{"file:///a"}},
			advertised: pushCaps(false, false, false, true),
			expect:     notificationSubscriptions{ResourceSubscriptions: []string{"file:///a"}},
		},
		{
			name:       "absent capability pointers honor nothing",
			want:       allWanted(),
			advertised: mcp.ServerCapabilities{},
			expect:     notificationSubscriptions{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expect, honoredSubscriptions(tt.want, tt.advertised))
		})
	}
}

// listenSSE drives dispatchModern for a subscriptions/listen request and splits
// the SSE body into its decoded JSON-RPC frames. It goes through dispatchModern
// rather than calling the handler directly so the method-switch wiring is
// covered too.
func listenSSE(t *testing.T, fakeCore *modernFakeCore, params any, id any) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()

	raw, err := json.Marshal(params)
	require.NoError(t, err)

	s := &Server{
		config: &Config{Name: testServerName, Version: testServerVersion},
		core:   fakeCore,
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(context.Background())
	rec := httptest.NewRecorder()

	s.dispatchModern(rec, req, &mcpparser.ParsedMCPRequest{
		ID:     id,
		Method: methodSubscriptionsListen,
		Params: raw,
	})

	var frames []map[string]any
	for _, block := range strings.Split(rec.Body.String(), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var frame map[string]any
			require.NoError(t, json.Unmarshal([]byte(data), &frame))
			frames = append(frames, frame)
		}
	}
	return rec, frames
}

// TestDispatchModernSubscriptionsListen_AcknowledgesEmptyHonoredSet covers the
// full wire contract for the only path the live advertisement can produce: an
// SSE response carrying the mandatory acknowledgement first and the terminating
// result second, both tagged with the subscription id, and an honored set that
// is explicitly empty rather than absent.
//
// discoverCaps is all-true so the test proves the empty honored set comes from
// the capability advertisement (which advertises no push flags) and not merely
// from the identity having nothing to reach.
func TestDispatchModernSubscriptionsListen_AcknowledgesEmptyHonoredSet(t *testing.T) {
	t.Parallel()

	fakeCore := &modernFakeCore{discoverCaps: core.DiscoverCapabilities{
		HasTools: true, HasResources: true, HasResourceTemplates: true, HasPrompts: true,
	}}
	rec, frames := listenSSE(t, fakeCore, subscriptionsListenParams{
		Notifications: &notificationSubscriptions{
			ToolsListChanged:      true,
			PromptsListChanged:    true,
			ResourcesListChanged:  true,
			ResourceSubscriptions: []string{"file:///a"},
		},
	}, "sub-1")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Len(t, frames, 2, "expected an acknowledgement frame followed by a result frame")

	// Frame 1: the acknowledgement MUST come first, and MUST report an empty
	// honored set. An absent "notifications" object would be a different (and
	// wrong) statement -- "I did not answer" rather than "I honor none".
	ack := frames[0]
	assert.Equal(t, notificationSubscriptionsAcked, ack["method"])
	ackParams, ok := ack["params"].(map[string]any)
	require.True(t, ok, "acknowledgement must carry params")
	honored, ok := ackParams["notifications"].(map[string]any)
	require.True(t, ok, "acknowledgement must carry an explicit notifications object")
	assert.Empty(t, honored, "no push capability is advertised, so nothing may be honored")

	ackMeta, ok := ackParams["_meta"].(map[string]any)
	require.True(t, ok, "acknowledgement must be tagged with the subscription id")
	assert.Equal(t, "sub-1", ackMeta[modernSubscriptionIDKey])

	// Frame 2: the terminating result, same subscription id. Its presence is what
	// makes the stream honestly finite: there is nothing to deliver, so the
	// subscription ends immediately instead of idling open forever.
	result := frames[1]
	assert.Equal(t, "sub-1", result["id"])
	resultBody, ok := result["result"].(map[string]any)
	require.True(t, ok, "result frame must carry a result")
	assert.Equal(t, modernResultTypeComplete, resultBody["resultType"])
	resultMeta, ok := resultBody["_meta"].(map[string]any)
	require.True(t, ok, "result must be tagged with the subscription id")
	assert.Equal(t, "sub-1", resultMeta[modernSubscriptionIDKey])
}

// TestDispatchModernSubscriptionsListen_Errors covers the two rejections that
// must NOT open a stream: a missing required "notifications" field, and a failed
// capability resolution.
func TestDispatchModernSubscriptionsListen_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		params      any
		discoverErr error
		wantCode    float64
		wantStatus  int
	}{
		{
			name:       "missing notifications field",
			params:     map[string]any{},
			wantCode:   jsonRPCCodeInvalidParams,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "explicitly null notifications field",
			params:     map[string]any{"notifications": nil},
			wantCode:   jsonRPCCodeInvalidParams,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "capability resolution failure",
			params:      map[string]any{"notifications": map[string]any{"toolsListChanged": true}},
			discoverErr: errors.New("backend fan-out failed"),
			wantCode:    jsonRPCCodeInternalError,
			wantStatus:  http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeCore := &modernFakeCore{discoverErr: tt.discoverErr}
			rec, _ := listenSSE(t, fakeCore, tt.params, 7)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.NotEqual(t, "text/event-stream", rec.Header().Get("Content-Type"),
				"a rejected listen must not open a stream")

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok, "expected a JSON-RPC error envelope")
			assert.Equal(t, tt.wantCode, errObj["code"])

			// The generic internal-error text must not leak aggregation detail.
			if tt.discoverErr != nil {
				assert.NotContains(t, errObj["message"], "backend fan-out failed")
			}
		})
	}
}
