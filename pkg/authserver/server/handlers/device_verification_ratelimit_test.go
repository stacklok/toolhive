// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestPerIPLimiter_ScopedPerIP(t *testing.T) {
	t.Parallel()

	l := newPerIPLimiter(rate.Limit(1), 2)

	// IP A burns its burst of 2.
	assert.True(t, l.allow("10.0.0.1"))
	assert.True(t, l.allow("10.0.0.1"))
	assert.False(t, l.allow("10.0.0.1"), "third request from the same IP within the burst window must be throttled")

	// IP B is unaffected by IP A's exhausted bucket -- this is the whole
	// point of per-IP scoping over a single shared bucket.
	assert.True(t, l.allow("10.0.0.2"))
	assert.True(t, l.allow("10.0.0.2"))
}

func TestClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{name: "host and port", remoteAddr: "203.0.113.5:54321", want: "203.0.113.5"},
		{name: "IPv6 with port", remoteAddr: "[2001:db8::1]:54321", want: "2001:db8::1"},
		{
			name: "X-Forwarded-For is ignored", remoteAddr: "203.0.113.5:54321",
			xff: "1.2.3.4", want: "203.0.113.5",
		},
		{name: "no port falls back to RemoteAddr verbatim", remoteAddr: "not-a-valid-addr", want: "not-a-valid-addr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/oauth/device", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			assert.Equal(t, tt.want, clientIP(req))
		})
	}
}

func TestPerIPLimiter_EvictsIdleEntries(t *testing.T) {
	t.Parallel()

	l := newPerIPLimiter(rate.Limit(1), 1)
	for i := range perIPLimiterEvictionThreshold + 1 {
		require.True(t, l.allow(string(rune('a'+i%26))+string(rune('0'+i/26))))
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	// Every entry above was just touched, so none are idle yet -- this just
	// asserts the sweep didn't panic or drop live entries.
	assert.Len(t, l.limiters, perIPLimiterEvictionThreshold+1)
}
