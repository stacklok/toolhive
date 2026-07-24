// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestHTTPSession_CallTool_InjectsTraceparent asserts that the session-backed
// connector (pkg/vmcp/session/internal/backend) injects the current W3C trace
// context (traceparent) into outbound params._meta (SEP-414) for CallTool, and
// that it is omitted entirely when there is no active span. This mirrors the
// coverage in pkg/vmcp/client's TestOutboundMetaTraceContext for the
// session-side connector.
//
// ReadResource and GetPrompt are not covered here for the same reason
// documented on pkg/vmcp/client's TestOutboundMetaTraceContext: mcpcompat's
// non-resume client does not forward Params.Meta to the wire for those two
// operations (only CallTool does), so there is nothing to observe on fakeBackend
// today. See mcp_session.go's ReadResource/GetPrompt for the code that will
// start working once mcpcompat closes that gap.
//
// This test mutates the global OTEL propagator, so it must NOT run in
// parallel with the rest of this package's tests.
func TestHTTPSession_CallTool_InjectsTraceparent(t *testing.T) { //nolint:paralleltest // Mutates global OTEL propagator
	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(oldPropagator)

	fb := &fakeBackend{advertiseTools: true, tools: []mcp.Tool{{Name: "echo"}}}
	url := newFakeBackend(t, fb)

	target := &vmcp.BackendTarget{
		WorkloadID:    "trace-context-backend",
		WorkloadName:  "trace-context-backend",
		BaseURL:       url,
		TransportType: "streamable-http",
	}

	registry := newTestRegistry(t)
	connector := NewHTTPConnector(registry)

	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, _, err := connector(initCtx, target, nil, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	//nolint:paralleltest // sequential: shares the fakeBackend/session with the sibling subtests below
	t.Run("active span: traceparent on the wire carries this span's TraceID", func(t *testing.T) {
		spanCtx, span := tracer.Start(context.Background(), "test-span")
		defer span.End()

		_, err := sess.CallTool(spanCtx, "echo", map[string]any{}, nil)
		require.NoError(t, err)

		raw := fb.metaFor(string(mcp.MethodToolsCall))
		require.NotEmpty(t, raw, "expected params._meta to be sent")

		var meta map[string]any
		require.NoError(t, json.Unmarshal(raw, &meta))
		traceparent, ok := meta["traceparent"].(string)
		require.True(t, ok, "expected traceparent in captured _meta")

		// Prove span identity, not just presence: a stale or global context would
		// still yield a non-empty traceparent. The W3C format is
		// "<version>-<trace-id>-<span-id>-<flags>"; the trace-id field must match
		// the span active at the call site.
		wantTraceID := span.SpanContext().TraceID().String()
		assert.Contains(t, traceparent, wantTraceID,
			"traceparent on the wire must carry the active span's TraceID")
	})

	//nolint:paralleltest // sequential: shares the fakeBackend/session with the sibling subtests
	t.Run("progressToken and traceparent coexist on the wire", func(t *testing.T) {
		spanCtx, span := tracer.Start(context.Background(), "test-span")
		defer span.End()

		// progressToken serializes via Meta.ProgressToken while the injected
		// traceparent rides Meta.AdditionalFields — assert both survive the round
		// trip into the real serialized _meta bytes. progressToken is caller
		// _meta (the 4th CallTool arg), not a tool argument.
		_, err := sess.CallTool(spanCtx, "echo", map[string]any{}, map[string]any{"progressToken": "tok"})
		require.NoError(t, err)

		raw := fb.metaFor(string(mcp.MethodToolsCall))
		require.NotEmpty(t, raw, "expected params._meta to be sent")

		var meta map[string]any
		require.NoError(t, json.Unmarshal(raw, &meta))

		assert.Equal(t, "tok", meta["progressToken"], "progressToken must survive on the wire")
		traceparent, ok := meta["traceparent"].(string)
		require.True(t, ok, "expected traceparent alongside progressToken in captured _meta")
		assert.Contains(t, traceparent, span.SpanContext().TraceID().String(),
			"traceparent must carry the active span's TraceID when coexisting with progressToken")
	})

	//nolint:paralleltest // sequential: reuses the "tools/call" capture key populated above
	t.Run("no active span: _meta is omitted", func(t *testing.T) {
		_, err := sess.CallTool(context.Background(), "echo", map[string]any{}, nil)
		require.NoError(t, err)

		raw := fb.metaFor(string(mcp.MethodToolsCall))
		assert.Empty(t, raw, "expected no _meta to be sent without an active trace context")
	})
}
