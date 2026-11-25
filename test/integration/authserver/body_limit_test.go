// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// TestEmbeddedAuthServer_RejectsOversizedBody verifies that the embedded auth
// server's endpoints — which are mounted outside the MCP middleware chain — cap
// request bodies and return 413 for oversized payloads. This prevents a
// memory-exhaustion DoS on the unauthenticated POST /oauth/token endpoint.
func TestEmbeddedAuthServer_RejectsOversizedBody(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	upstream := helpers.NewMockUpstreamIDP(t)
	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())
	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)

	// Served via Handler(), which the proxies and vMCP reach through
	// Routes()/RegisterHandlers, so the cap applies for every consumer.
	server := httptest.NewServer(authServer.Handler())
	defer server.Close()

	// A body one byte over the 64KB auth-server cap must be rejected with 413,
	// before fosite's form parsing buffers it.
	oversized := bytes.NewReader(make([]byte, (64*1024)+1))
	resp, err := http.Post(server.URL+"/oauth/token", "application/x-www-form-urlencoded", oversized)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}
