// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWarmUpstreamConnections verifies the warmup burst fires the expected
// number of side-effect-free GET requests and tolerates a backend that
// rejects them (the real-world case: no Mcp-Session-Id means the streamable-
// HTTP server returns 400, but the connection is still established).
func TestWarmUpstreamConnections(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	var sawSessionHeader atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Mcp-Session-Id") != "" {
			sawSessionHeader.Store(true)
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	warmUpstreamConnections(http.DefaultTransport, server.URL)

	assert.EqualValues(t, upstreamMaxIdleConnsPerHost, requests.Load(),
		"expected one warmup request per configured idle connection")
	assert.False(t, sawSessionHeader.Load(), "warmup requests must not carry a session ID")
}
