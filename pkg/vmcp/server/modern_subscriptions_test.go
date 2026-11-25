// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
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
			// Named for what it asserts: everything is advertised, only tools is
			// wanted, so ONLY tools comes back -- the three advertised-but-unwanted
			// types must be absent. Exact equality is what pins that.
			name:       "only the wanted subset is honored when all are advertised",
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

	// Only strict-parse an actual stream; a rejected listen answers with a plain
	// JSON error envelope and has no frames.
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		return rec, nil
	}
	return rec, parseSSEStrict(t, rec.Body.String())
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

	// NOTE: there is deliberately no "capability resolution failure" case here.
	// The M2 pre-check skips core.Discover entirely while no push capability is
	// advertised, so the fan-out -- and its error path -- is unreachable from this
	// handler today. Testing it would require a capability flag that
	// newModernCapabilities never sets, and asserting a branch the code cannot
	// reach would be theatre. TestNewModernCapabilities_AdvertisesNoPushCapability
	// is what pins the precondition that makes it unreachable; if a flag is ever
	// flipped, the error path becomes live and wants a case here.
	tests := []struct {
		name       string
		params     any
		wantCode   float64
		wantStatus int
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec, _ := listenSSE(t, &modernFakeCore{}, tt.params, 7)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.NotEqual(t, "text/event-stream", rec.Header().Get("Content-Type"),
				"a rejected listen must not open a stream")

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok, "expected a JSON-RPC error envelope")
			assert.Equal(t, tt.wantCode, errObj["code"])
		})
	}
}

// parseSSEStrict parses an SSE body the way a compliant client does, and is
// deliberately STRICT where the previous helper was lenient.
//
// That leniency hid a real gap: it split on "\n\n" and then fell back to matching
// any line beginning "data: ", so replacing the blank-line event terminator with
// a single "\n" left the whole suite green -- including the real-client
// integration suite -- even though a conformant parser would see one
// never-terminated event and dispatch neither frame. The ack-then-result contract
// was therefore unverifiable.
//
// Rules enforced here, per the SSE grammar: events are separated by a blank
// line, every event must be terminated (the body ends with a blank line), and
// each event carries exactly one "data:" field.
func parseSSEStrict(t *testing.T, body string) []map[string]any {
	t.Helper()

	require.NotEmpty(t, body, "expected an SSE body")
	require.True(t, strings.HasSuffix(body, "\n\n"),
		"SSE body must end with a blank line; an unterminated final event is never dispatched by a client")

	var frames []map[string]any
	for _, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		require.NotEmpty(t, block, "empty SSE event block: two consecutive blank lines")

		var data string
		seenData := false
		for _, line := range strings.Split(block, "\n") {
			field, value, ok := strings.Cut(line, ":")
			require.True(t, ok, "malformed SSE line %q: no field separator", line)
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				assert.Equal(t, "message", value, "MCP SSE frames must use the 'message' event type")
			case "data":
				require.False(t, seenData, "more than one data field in a single SSE event")
				data, seenData = value, true
			default:
				t.Fatalf("unexpected SSE field %q", field)
			}
		}
		require.True(t, seenData, "SSE event carried no data field")

		var frame map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &frame), "SSE data must be valid JSON")
		frames = append(frames, frame)
	}
	return frames
}

// TestDispatchModernSubscriptionsListen_WireContract pins the literal strings and
// envelope fields the protocol requires. Every one of these was previously free:
// mutating the method name, the acknowledgement name, or the subscriptionId key
// left the whole package green, and those last two are precisely the constants
// whose provenance was least certain.
func TestDispatchModernSubscriptionsListen_WireContract(t *testing.T) {
	t.Parallel()

	// Literal, not the constant: asserting against the constant would pass even if
	// the constant itself were mistyped, which is the mutation that escaped.
	assert.Equal(t, "subscriptions/listen", methodSubscriptionsListen)
	assert.Equal(t, "notifications/subscriptions/acknowledged", notificationSubscriptionsAcked)
	assert.Equal(t, "io.modelcontextprotocol/subscriptionId", modernSubscriptionIDKey)

	rec, frames := listenSSE(t, &modernFakeCore{}, subscriptionsListenParams{
		Notifications: &notificationSubscriptions{ToolsListChanged: true},
	}, "sub-wire")

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, frames, 2)

	// Both frames must declare JSON-RPC 2.0 -- unasserted before, and a protocol
	// violation if dropped.
	for i, frame := range frames {
		assert.Equal(t, "2.0", frame["jsonrpc"], "frame %d must carry jsonrpc 2.0", i)
	}

	// The acknowledgement must be FIRST and must be the ack method: schema.ts
	// makes both a MUST ("This notification MUST be the first message the server
	// sends carrying the subscription's ID").
	assert.Equal(t, "notifications/subscriptions/acknowledged", frames[0]["method"],
		"the first frame must be the acknowledgement, by literal name")
	assert.NotContains(t, frames[0], "id", "a notification must not carry an id")

	// The result frame closes the subscription and echoes the request id.
	assert.Equal(t, "sub-wire", frames[1]["id"])
	assert.NotContains(t, frames[1], "method", "a response must not carry a method")

	// The subscriptionId key, by literal string, on both frames.
	ackParams, ok := frames[0]["params"].(map[string]any)
	require.True(t, ok)
	ackMeta, ok := ackParams["_meta"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "sub-wire", ackMeta["io.modelcontextprotocol/subscriptionId"])

	resultBody, ok := frames[1]["result"].(map[string]any)
	require.True(t, ok)
	resultMeta, ok := resultBody["_meta"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "sub-wire", resultMeta["io.modelcontextprotocol/subscriptionId"])
}

// TestNewModernCapabilities_AdvertisesNoPushCapability is the guard the whole
// empty-honored-set design rests on.
//
// The argument for this handler being conformant rather than a silent no-op has
// two halves: (1) the advertisement and the honored set are computed from one
// source, which the code enforces, and (2) that source advertises nothing
// pushable -- which, before this test, was enforced by nothing at all. The safety
// property was "no line of source sets these true", and a runtime WARN only fires
// after such a line ships, in production, per request, naming a developer action
// an operator cannot remedy.
//
// So it is asserted here, at build time, for every combination of the presence
// flags.
//
// IF YOU FLIP ONE OF THESE: you are promising to deliver that notification type.
// Nothing in vMCP can keep that promise yet -- dispatchModern creates no session,
// so the list_changed machinery never runs for a Modern client, and backend
// resources/updated is not forwarded even on Legacy. Implement delivery in the
// same change (#5743), including the schema MUST that every notification on a
// listen stream carries io.modelcontextprotocol/subscriptionId, and update this
// test deliberately rather than to make it pass.
//
// FLIPPING A FLAG ALSO RE-ENABLES CODE THAT IS CURRENTLY UNREACHABLE, and it
// carries no test coverage today precisely because it cannot be entered. Restore
// coverage for all three when you flip:
//
//   - the core.Discover call in dispatchModernSubscriptionsListen (the ceiling
//     pre-check skips it entirely while nothing can be honored), AND
//   - that call's error path, which maps a fan-out failure to -32603. Its test was
//     removed rather than left asserting an unenterable branch.
//   - the handler's non-empty-honored branch, including its WARN.
func TestNewModernCapabilities_AdvertisesNoPushCapability(t *testing.T) {
	t.Parallel()

	for _, hasTools := range []bool{false, true} {
		for _, hasResources := range []bool{false, true} {
			for _, hasTemplates := range []bool{false, true} {
				for _, hasPrompts := range []bool{false, true} {
					caps := newModernCapabilities(hasTools, hasResources, hasTemplates, hasPrompts)

					if caps.Tools != nil {
						assert.False(t, caps.Tools.ListChanged,
							"tools.listChanged must stay false until delivery exists (#5743)")
					}
					if caps.Prompts != nil {
						assert.False(t, caps.Prompts.ListChanged,
							"prompts.listChanged must stay false until delivery exists (#5743)")
					}
					if caps.Resources != nil {
						assert.False(t, caps.Resources.ListChanged,
							"resources.listChanged must stay false until delivery exists (#5743)")
						assert.False(t, caps.Resources.Subscribe,
							"resources.subscribe must stay false until delivery exists (#5743)")
					}

					// And the consequence: a client asking for everything is
					// honored nothing, whatever it can reach.
					assert.True(t, honoredSubscriptions(allWanted(), caps).isEmpty(),
						"no capability combination may honor a subscription")
				}
			}
		}
	}
}

// TestDispatchModernSubscriptionsListen_SkipsFanOutWhenNothingHonorable pins the
// pre-check: core.Discover is an un-rate-limited backend fan-out whose result
// cannot change the answer while no push capability is advertised, so it must not
// be called at all. Without this, N no-op listens cost N fan-outs.
//
// discoverCalls counting is the point -- the response is identical either way, so
// only the call count distinguishes the optimisation being present from absent.
func TestDispatchModernSubscriptionsListen_SkipsFanOutWhenNothingHonorable(t *testing.T) {
	t.Parallel()

	fakeCore := &modernFakeCore{discoverCaps: core.DiscoverCapabilities{
		HasTools: true, HasResources: true, HasResourceTemplates: true, HasPrompts: true,
	}}

	_, frames := listenSSE(t, fakeCore, subscriptionsListenParams{
		Notifications: &notificationSubscriptions{
			ToolsListChanged:      true,
			PromptsListChanged:    true,
			ResourcesListChanged:  true,
			ResourceSubscriptions: []string{"file:///a", "file:///b"},
		},
	}, "sub-nofanout")

	require.Len(t, frames, 2, "the response must be unaffected by the pre-check")
	assert.Zero(t, fakeCore.discoverCalls,
		"core.Discover must not be called when no push capability is advertised")
}
