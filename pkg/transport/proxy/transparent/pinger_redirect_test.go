// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMCPPinger_Ping_RefusesCrossHostRedirect is a regression test for a blind
// SSRF: the pinger's target may be a remote, untrusted MCP endpoint, and prior
// to installing redirectPolicy the underlying http.Client followed a redirect
// to any host. A malicious or compromised remote could point the pinger at a
// loopback, private, or otherwise internal address purely by responding with a
// 3xx -- the request never touches the configured endpoint at all.
//
// internalHit proves the point directly: it is the thing an attacker would be
// reaching for (an address the caller never named), so the assertion is that
// it is never contacted, not just that Ping returns an error.
func TestMCPPinger_Ping_RefusesCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var internalHit bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	// 307 preserves method, matching the shape a redirect-based attack would
	// use against the stateless (POST) pinger; using it here too keeps both
	// tests testing the same redirect code.
	redirectOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, internal.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectOrigin.Close()

	pinger := NewMCPPingerWithTimeout(redirectOrigin.URL, 2*time.Second)
	_, err := pinger.Ping(context.Background())

	require.Error(t, err, "a cross-host redirect must fail the ping rather than being followed")
	assert.False(t, internalHit, "the redirect target must never be contacted")
}

// TestStatelessMCPPinger_Ping_RefusesCrossHostRedirect mirrors
// TestMCPPinger_Ping_RefusesCrossHostRedirect for the POST-based pinger used
// against stateless streamable-HTTP servers.
func TestStatelessMCPPinger_Ping_RefusesCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var internalHit bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	redirectOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, internal.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectOrigin.Close()

	pinger := NewStatelessMCPPingerWithTimeout(redirectOrigin.URL, 2*time.Second)
	_, err := pinger.Ping(context.Background())

	require.Error(t, err, "a cross-host redirect must fail the ping rather than being followed")
	assert.False(t, internalHit, "the redirect target must never be contacted")
}

// TestMCPPinger_Ping_FollowsSameHostRedirect is the positive control: the fix
// must not break a target that legitimately redirects within itself (a common
// pattern for e.g. a trailing-slash or path-canonicalization redirect).
func TestMCPPinger_Ping_FollowsSameHostRedirect(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	var redirectedRequestSeen bool
	mux.HandleFunc("/redirected", func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequestSeen = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	pinger := NewMCPPingerWithTimeout(server.URL, 2*time.Second)
	_, err := pinger.Ping(context.Background())

	require.NoError(t, err)
	assert.True(t, redirectedRequestSeen, "a same-host redirect must still be followed")
}

// TestStatelessMCPPinger_Ping_FollowsSameHostRedirect mirrors
// TestMCPPinger_Ping_FollowsSameHostRedirect for the POST-based pinger, so the
// positive control covers both constructors that redirectPolicy is wired into,
// not just one of them.
func TestStatelessMCPPinger_Ping_FollowsSameHostRedirect(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	var redirectedRequestSeen bool
	mux.HandleFunc("/redirected", func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequestSeen = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	pinger := NewStatelessMCPPingerWithTimeout(server.URL, 2*time.Second)
	_, err := pinger.Ping(context.Background())

	require.NoError(t, err)
	assert.True(t, redirectedRequestSeen, "a same-host redirect must still be followed")
}
