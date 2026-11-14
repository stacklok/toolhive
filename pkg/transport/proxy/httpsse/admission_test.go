// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmissionStandaloneSSE(t *testing.T) {
	t.Parallel()
	const valid = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"malformed_plain", `{"jsonrpc":`, "text/plain", http.StatusBadRequest},
		{"ambiguous_missing", `{"jsonrpc":"2.0","id":1,"method":"ping","method":"tools/call"}`, "", http.StatusBadRequest},
		{"mixed_json", `{"jsonrpc":"2.0","id":1,"method":"tools/call","result":{}}`, "application/json", http.StatusBadRequest},
		{"batch_missing", `[` + valid + `]`, "", http.StatusBadRequest},
		{"response_plain", `{"jsonrpc":"2.0","id":9007199254740993,"result":{}}`, "text/plain", http.StatusBadRequest},
		{"error_response_missing", `{"jsonrpc":"2.0","id":"server-call","error":{"code":-32603,"message":"client error"}}`, "", http.StatusBadRequest},
		{"valid_plain", valid, "text/plain", http.StatusAccepted},
		{"valid_missing", valid, "", http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarnessWithMiddleware(t, nil)
			client := h.connect("caller")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, strings.NewReader(tc.body))
			require.NoError(t, err)
			req.Header.Set("Authorization", client.token)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			resp, err := h.http.Do(req)
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, tc.status, resp.StatusCode)
			if tc.status == http.StatusAccepted {
				require.Len(t, h.proxy.messageCh, 1, "exactly one message must reach stdio")
				forwarded := h.backendRequest()
				assert.Equal(t, "tools/call", forwarded.Method)
				assert.JSONEq(t, `{"name":"echo"}`, string(forwarded.Params))
			} else {
				assert.Empty(t, h.proxy.messageCh, "rejected input must not reach stdio")
			}
		})
	}
}
