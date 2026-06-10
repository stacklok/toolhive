// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bytes"
	"context"
	"fmt"
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
//nolint:paralleltest // starts an HTTP server
func TestTransparentProxy_RejectsOversizedBody(t *testing.T) {
	const limit = 1024

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
	time.Sleep(100 * time.Millisecond)

	url := fmt.Sprintf("http://%s/", proxy.listener.Addr().String())
	resp, err := http.Post(url, "application/json", bytes.NewReader(make([]byte, limit+1)))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}
