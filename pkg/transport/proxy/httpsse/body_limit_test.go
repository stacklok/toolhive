// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestHTTPSSEProxy_RejectsOversizedBody verifies the body-limit middleware in
// the SSE proxy's message-endpoint chain rejects an oversized request body with
// 413. This guards the httpsse transport's independent middleware mounting.
//
//nolint:paralleltest // starts an HTTP server
func TestHTTPSSEProxy_RejectsOversizedBody(t *testing.T) {
	const limit = 1024

	chain := []types.NamedMiddleware{
		{Name: bodylimit.MiddlewareType, Function: bodylimit.Middleware(limit)},
	}

	proxy := NewHTTPSSEProxy("127.0.0.1", 0, false, nil, chain)
	ctx := t.Context()
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() { _ = proxy.Stop(ctx) })
	time.Sleep(100 * time.Millisecond)

	url := fmt.Sprintf("http://%s%s", proxy.server.Addr, ssecommon.HTTPMessagesEndpoint)
	resp, err := http.Post(url, "application/json", bytes.NewReader(make([]byte, limit+1)))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}
