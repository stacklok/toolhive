// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/networking"
)

// secretPathQuery is appended to every target (and redirect Location) below.
// Neither "secret-path" nor "SECRET" may appear in a Ping error, because that
// text is served by the unauthenticated /health endpoint.
const secretPathQuery = "/secret-path?api_key=SECRET"

// TestPinger_Ping_RedactsTargetInErrors covers every Ping failure in which
// net/http puts a URL into the returned error, for both pingers.
func TestPinger_Ping_RedactsTargetInErrors(t *testing.T) {
	t.Parallel()

	pingers := []struct {
		name string
		new  func(targetURL string, timeout time.Duration) healthcheck.MCPPinger
	}{
		{name: "MCPPinger", new: NewMCPPingerWithTimeout},
		{name: "StatelessMCPPinger", new: NewStatelessMCPPingerWithTimeout},
	}

	tests := []struct {
		name string
		// setup returns the pinger target and the text the error must still
		// contain so it stays useful to an operator.
		setup        func(t *testing.T) (target, wantInError string)
		timeout      time.Duration
		wantDeadline bool
		wantErrIs    error
	}{
		{
			name: "unreachable target",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				// Start and immediately close a server so the port is closed.
				srv := httptest.NewServer(http.NotFoundHandler())
				srv.Close()
				return srv.URL + secretPathQuery, srv.Listener.Addr().String()
			},
			timeout: 2 * time.Second,
		},
		{
			name: "target hangs past the pinger timeout",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				release := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}))
				t.Cleanup(srv.Close)
				t.Cleanup(func() { close(release) })
				return srv.URL + secretPathQuery, srv.Listener.Addr().String()
			},
			timeout:      100 * time.Millisecond,
			wantDeadline: true,
		},
		{
			name: "cross-host redirect to a secret-bearing Location",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				other := httptest.NewServer(http.NotFoundHandler())
				t.Cleanup(other.Close)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Location", other.URL+secretPathQuery)
					w.WriteHeader(http.StatusTemporaryRedirect)
				}))
				t.Cleanup(srv.Close)
				return srv.URL + secretPathQuery, srv.Listener.Addr().String()
			},
			timeout:   2 * time.Second,
			wantErrIs: networking.ErrRedirectRefused,
		},
		{
			// net/http quotes an unparsable Location verbatim in its error.
			name: "redirect with an unparsable secret-bearing Location",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Location", "/secret-path%zz?api_key=SECRET")
					w.WriteHeader(http.StatusTemporaryRedirect)
				}))
				t.Cleanup(srv.Close)
				return srv.URL + secretPathQuery, srv.Listener.Addr().String()
			},
			timeout:   2 * time.Second,
			wantErrIs: errUnparsableRedirectLocation,
		},
		{
			// http.NewRequest's parse error quotes the raw target.
			name: "target that does not parse",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				return "http://127.0.0.1" + secretPathQuery + "\x7f", redactedTargetPlaceholder
			},
			timeout: 2 * time.Second,
		},
	}

	for _, pc := range pingers {
		for _, tc := range tests {
			t.Run(pc.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				target, wantInError := tc.setup(t)
				_, err := pc.new(target, tc.timeout).Ping(context.Background())
				require.Error(t, err)

				msg := err.Error()
				assert.NotContains(t, msg, "secret-path")
				assert.NotContains(t, msg, "SECRET")
				assert.Contains(t, msg, wantInError)

				if tc.wantDeadline {
					assert.ErrorIs(t, err, context.DeadlineExceeded)
					var urlErr *url.Error
					require.ErrorAs(t, err, &urlErr)
					assert.True(t, urlErr.Timeout(), "the rebuilt *url.Error must still report a timeout")
				}
				if tc.wantErrIs != nil {
					assert.ErrorIs(t, err, tc.wantErrIs)
				}
			})
		}
	}
}

func TestRedactTargetURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "path, query and fragment are dropped",
			target: "https://mcp.example.com/secret-path?api_key=SECRET#frag",
			want:   "https://mcp.example.com",
		},
		{
			name:   "port is kept and userinfo is dropped",
			target: "http://user:SECRET@127.0.0.1:8080/mcp",
			want:   "http://127.0.0.1:8080",
		},
		{
			name:   "IPv6 host keeps its brackets",
			target: "http://[::1]:8080/mcp?token=SECRET",
			want:   "http://[::1]:8080",
		},
		{
			name:   "parse failure yields the placeholder",
			target: "http://127.0.0.1/secret-path?api_key=SECRET\x7f",
			want:   redactedTargetPlaceholder,
		},
		{
			name:   "missing scheme and host yields the placeholder",
			target: "secret-path?api_key=SECRET",
			want:   redactedTargetPlaceholder,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, redactTargetURL(tc.target))
		})
	}
}

// TestRedactURLError_PassesThroughOtherErrors pins that an error which is not
// a *url.Error is returned unchanged rather than dropped.
func TestRedactURLError_PassesThroughOtherErrors(t *testing.T) {
	t.Parallel()

	orig := errors.New("boom")
	assert.Same(t, orig, redactURLError(orig, "http://host"))
}
