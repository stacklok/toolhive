// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestHTTPSession_RefusesCrossHostRedirect verifies the session connector installs
// SameHostRedirectPolicy: a backend returning a cross-host 302 on the initialize
// POST is refused before the second hop, so the RoundTripper-injected forwarded
// header never reaches the attacker host (credential-exfil vector, twin of
// pkg/vmcp/client.newBackendHTTPClient).
func TestHTTPSession_RefusesCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var attackerHits atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attackerHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(attacker.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, attacker.URL, http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	target := &vmcp.BackendTarget{
		WorkloadID:    "redirect-backend",
		WorkloadName:  "redirect-backend",
		BaseURL:       origin.URL,
		TransportType: "streamable-http",
		HeaderForward: &vmcp.HeaderForwardConfig{
			AddPlaintextHeaders: map[string]string{"X-Secret": "leak-me"},
		},
	}

	connector := NewHTTPConnector(newTestRegistry(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	sess, _, err := connector(ctx, target, nil, "", nil)
	if sess != nil {
		_ = sess.Close()
	}
	require.Error(t, err, "a cross-host redirect on initialize must fail the connection")
	assert.Zero(t, attackerHits.Load(), "credential-bearing request must not reach the cross-host redirect target")
}
