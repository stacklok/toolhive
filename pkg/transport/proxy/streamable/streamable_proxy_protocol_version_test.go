// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHandlePost_StrictProtocolValidation drives handlePost directly (no
// server/backend needed: the notification body used below returns 202
// without any upstream round-trip) to verify the MCP-Protocol-Version gate:
//   - strict mode rejects a present-but-unrecognized version with 400
//   - strict mode accepts a recognized version
//   - strict mode accepts an absent header (assume 2025-03-26 per spec)
//   - permissive mode (default, strict disabled) accepts any version,
//     preserving the proxy's original version-agnostic behavior
func TestHandlePost_StrictProtocolValidation(t *testing.T) {
	t.Parallel()

	// A notification (no id) is accepted and forwarded with 202 by
	// handleNotificationOrClientResponse without waiting on any backend
	// response, so these tests never need to start the proxy or a backend.
	const notificationBody = `{"jsonrpc":"2.0","method":"progress","params":{"pct":50}}`

	tests := []struct {
		name           string
		strict         bool
		protocolHeader string
		wantStatus     int
	}{
		{
			name:           "strict on, unsupported version rejected",
			strict:         true,
			protocolHeader: "1999-01-01",
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "strict on, supported version accepted",
			strict:         true,
			protocolHeader: "2025-11-25",
			wantStatus:     http.StatusAccepted,
		},
		{
			name:           "strict on, absent header accepted",
			strict:         true,
			protocolHeader: "",
			wantStatus:     http.StatusAccepted,
		},
		{
			name:           "strict off (default), unsupported version accepted",
			strict:         false,
			protocolHeader: "1999-01-01",
			wantStatus:     http.StatusAccepted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy := NewHTTPProxy("127.0.0.1", 0, nil, nil, WithStrictProtocolValidation(tt.strict))

			req := httptest.NewRequest(http.MethodPost, StreamableHTTPEndpoint, bytes.NewReader([]byte(notificationBody)))
			req.Header.Set("Content-Type", "application/json")
			if tt.protocolHeader != "" {
				req.Header.Set("MCP-Protocol-Version", tt.protocolHeader)
			}
			rec := httptest.NewRecorder()

			proxy.handlePost(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
