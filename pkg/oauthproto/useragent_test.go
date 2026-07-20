// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingRoundTripper captures the User-Agent header from every request it
// sees and returns a 200 response.
type recordingRoundTripper struct {
	userAgents []string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.userAgents = append(r.userAgents, req.Header.Get("User-Agent"))
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestUserAgentTransport_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		incomingUserAgent string
		wantUserAgent     string
	}{
		{
			name:              "sets User-Agent when missing",
			incomingUserAgent: "",
			wantUserAgent:     UserAgent,
		},
		{
			name:              "preserves caller-provided User-Agent",
			incomingUserAgent: "custom-client/2.5",
			wantUserAgent:     "custom-client/2.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			recorder := &recordingRoundTripper{}
			transport := &UserAgentTransport{Base: recorder}

			req, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
			require.NoError(t, err)
			if tt.incomingUserAgent != "" {
				req.Header.Set("User-Agent", tt.incomingUserAgent)
			}
			originalIncomingUA := req.Header.Get("User-Agent")

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.NotNil(t, resp)

			require.Len(t, recorder.userAgents, 1)
			assert.Equal(t, tt.wantUserAgent, recorder.userAgents[0])

			// The RoundTripper contract requires that RoundTrip not mutate
			// the caller's *http.Request. Verify the User-Agent the caller
			// set (or didn't set) is unchanged on the original request.
			assert.Equal(t, originalIncomingUA, req.Header.Get("User-Agent"), "caller's User-Agent was mutated")
		})
	}
}

// TestUserAgentTransport_NilBase verifies that a UserAgentTransport with no
// Base falls back to http.DefaultTransport. We exercise this by sending a real
// request through an httptest.Server (not just a recorder, since the recorder
// would short-circuit the Base lookup).
func TestUserAgentTransport_NilBase(t *testing.T) {
	t.Parallel()

	var got string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: &UserAgentTransport{}}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Equal(t, UserAgent, got)
}
