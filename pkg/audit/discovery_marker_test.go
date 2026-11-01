// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

func TestAddMetadata_DiscoveryFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		marker    *authorizers.DiscoveryMarker
		wantCount any
	}{
		{name: "no marker in context records nothing", marker: nil},
		{name: "marker without a fallback records nothing", marker: &authorizers.DiscoveryMarker{}},
		{
			name:      "fallback records the count",
			marker:    &authorizers.DiscoveryMarker{FallbackCount: 2},
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			auditor, err := NewAuditorWithTransport(&Config{}, "sse")
			require.NoError(t, err)

			event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
			rw := &responseWriter{
				ResponseWriter: httptest.NewRecorder(),
				body:           bytes.NewBufferString(""),
			}
			req := httptest.NewRequest("POST", "/mcp", nil)
			if tc.marker != nil {
				req = req.WithContext(authorizers.WithDiscoveryMarker(req.Context(), tc.marker))
			}

			auditor.addMetadata(event, req, 10*time.Millisecond, rw)

			if tc.wantCount == nil {
				assert.NotContains(t, event.Metadata.Extra, MetadataExtraKeyDiscoveryFallbackCount)
				return
			}
			assert.Equal(t, tc.wantCount, event.Metadata.Extra[MetadataExtraKeyDiscoveryFallbackCount])
		})
	}
}

// callOnlyAuthorizer grants tool:call and nothing else.
type callOnlyAuthorizer struct{}

func (callOnlyAuthorizer) AuthorizeWithJWTClaims(
	_ context.Context,
	_ authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	_ string,
	_ map[string]interface{},
) (bool, error) {
	return operation == authorizers.MCPOperationCall, nil
}

// TestDiscoveryFallback_FlowsFromDecoratorToEvent checks the whole path: the audit
// middleware injects the marker, the decorator counts a fallback in it, and the event
// carries the count.
func TestDiscoveryFallback_FlowsFromDecoratorToEvent(t *testing.T) {
	t.Parallel()

	auditor, err := NewAuditorWithTransport(&Config{}, "sse")
	require.NoError(t, err)

	req := ensureAuditContext(httptest.NewRequest("POST", "/mcp", nil))

	listed, err := authorizers.WithToolCallImpliesRead(callOnlyAuthorizer{}).AuthorizeWithJWTClaims(
		req.Context(), authorizers.MCPFeatureTool, authorizers.MCPOperationRead, "weather", nil)
	require.NoError(t, err)
	require.True(t, listed)

	event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
	rw := &responseWriter{
		ResponseWriter: httptest.NewRecorder(),
		body:           bytes.NewBufferString(""),
	}
	auditor.addMetadata(event, req, 10*time.Millisecond, rw)

	assert.Equal(t, 1, event.Metadata.Extra[MetadataExtraKeyDiscoveryFallbackCount])
}
