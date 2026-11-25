// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// remoteSpanContext returns a valid, sampled span context as if extracted from
// an inbound W3C traceparent header.
func remoteSpanContext(t *testing.T) trace.SpanContext {
	t.Helper()
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
}

func TestStampTraceContext(t *testing.T) {
	t.Parallel()

	t.Run("records trace and span IDs from a valid span context", func(t *testing.T) {
		t.Parallel()
		event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
		ctx := trace.ContextWithSpanContext(context.Background(), remoteSpanContext(t))

		stampTraceContext(ctx, event)

		assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", event.Metadata.Extra[MetadataExtraKeyTraceID])
		assert.Equal(t, "00f067aa0ba902b7", event.Metadata.Extra[MetadataExtraKeySpanID])
	})

	t.Run("adds nothing when the context carries no span", func(t *testing.T) {
		t.Parallel()
		event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")

		stampTraceContext(context.Background(), event)

		assert.NotContains(t, event.Metadata.Extra, MetadataExtraKeyTraceID)
		assert.NotContains(t, event.Metadata.Extra, MetadataExtraKeySpanID)
	})
}

func TestAddMetadata_RecordsTraceContext(t *testing.T) {
	t.Parallel()
	auditor, err := NewAuditorWithTransport(&Config{}, "http")
	require.NoError(t, err)

	event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
	rw := &responseWriter{ResponseWriter: httptest.NewRecorder(), body: bytes.NewBufferString("ok")}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = req.WithContext(trace.ContextWithSpanContext(req.Context(), remoteSpanContext(t)))

	auditor.addMetadata(event, req, 0, rw)

	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", event.Metadata.Extra[MetadataExtraKeyTraceID])
	assert.Equal(t, "00f067aa0ba902b7", event.Metadata.Extra[MetadataExtraKeySpanID])
}

// TestAuditorMiddleware_EmittedEventCarriesTraceID drives a request through the
// middleware and reads the event back from the audit log, so the assertion
// covers what an operator's log pipeline sees, not an in-memory struct.
func TestAuditorMiddleware_EmittedEventCarriesTraceID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		withSpan    bool
		wantTraceID string
	}{
		{name: "request inside a span", withSpan: true, wantTraceID: "4bf92f3577b34da6a3ce929d0e0e4736"},
		{name: "request without a span", withSpan: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logPath := filepath.Join(t.TempDir(), "audit.log")
			auditor, err := NewAuditorWithTransport(&Config{LogFile: logPath}, "streamable-http")
			require.NoError(t, err)
			t.Cleanup(func() { _ = auditor.Close() })

			handler := auditor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search"}}`))
			if tt.withSpan {
				req = req.WithContext(trace.ContextWithSpanContext(req.Context(), remoteSpanContext(t)))
			}

			handler.ServeHTTP(httptest.NewRecorder(), req)

			raw, err := os.ReadFile(logPath)
			require.NoError(t, err)
			var record struct {
				Metadata struct {
					Extra map[string]any `json:"extra"`
				} `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(raw), &record), "audit log: %s", raw)

			if !tt.withSpan {
				assert.NotContains(t, record.Metadata.Extra, MetadataExtraKeyTraceID)
				return
			}
			assert.Equal(t, tt.wantTraceID, record.Metadata.Extra[MetadataExtraKeyTraceID])
			assert.Equal(t, "00f067aa0ba902b7", record.Metadata.Extra[MetadataExtraKeySpanID])
		})
	}
}

func TestLogSSEConnectionEvent_CarriesTraceID(t *testing.T) {
	t.Parallel()
	logPath := filepath.Join(t.TempDir(), "audit.log")
	auditor, err := NewAuditorWithTransport(&Config{LogFile: logPath}, "sse")
	require.NoError(t, err)
	t.Cleanup(func() { _ = auditor.Close() })

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	req = req.WithContext(trace.ContextWithSpanContext(req.Context(), remoteSpanContext(t)))

	auditor.logSSEConnectionEvent(req, http.StatusOK)

	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	var record struct {
		Type     string `json:"type"`
		Metadata struct {
			Extra map[string]any `json:"extra"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(raw), &record), "audit log: %s", raw)
	assert.Equal(t, EventTypeSSEConnection, record.Type)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", record.Metadata.Extra[MetadataExtraKeyTraceID])
}
