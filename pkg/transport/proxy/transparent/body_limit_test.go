// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestTransparentProxy_RejectsOversizedBody verifies the body-limit middleware
// in the transparent proxy's chain rejects an oversized request body with 413
// before forwarding it to the backend. This guards the transparent transport's
// independent middleware mounting.
//
// Two distinct code paths are exercised:
//   - "content-length": a body with a Content-Length header that exceeds the
//     limit is rejected early by the middleware before forwarding.
//   - "chunked": a body sent WITHOUT a Content-Length bypasses the early
//     Content-Length rejection and instead trips http.MaxBytesReader inside the
//     transport's readRequestBody, which must still report 413 (not forward a
//     truncated body to the backend). Regression guard for readRequestBody's
//     MaxBytesError handling.
//
//nolint:paralleltest // starts an HTTP server
func TestTransparentProxy_RejectsOversizedBody(t *testing.T) {
	const limit = 1024

	tests := []struct {
		name string
		// body returns the request body for each case. io.NopCloser hides the
		// concrete reader type so net/http omits Content-Length and uses chunked
		// transfer, bypassing the middleware's early-reject path.
		body func() io.Reader
	}{
		{
			name: "content-length",
			body: func() io.Reader { return bytes.NewReader(make([]byte, limit+1)) },
		},
		{
			name: "chunked",
			body: func() io.Reader { return io.NopCloser(bytes.NewReader(make([]byte, limit*8))) },
		},
	}

	// Backend that would return 200 if the request were ever forwarded; the
	// body-limit middleware must reject the oversized request before this runs.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	chain := []types.NamedMiddleware{
		{Name: bodylimit.MiddlewareType, Function: bodylimit.Middleware(limit)},
	}

	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, backend.URL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		chain,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	require.NoError(t, proxy.Start(ctx))

	url := fmt.Sprintf("http://%s/", proxy.listener.Addr().String())

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, tc.body())
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
			assert.NotEqual(t, http.StatusOK, resp.StatusCode)
			assert.NotEqual(t, http.StatusInternalServerError, resp.StatusCode)
		})
	}
}
