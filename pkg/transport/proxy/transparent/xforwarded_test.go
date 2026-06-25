// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSetXForwardedHeaders verifies the remote/local split for X-Forwarded-*
// headers: a remote upstream must not receive X-Forwarded-Host (third-party
// servers can echo it into redirect URLs, creating 307 loops back to the
// proxy), while X-Forwarded-For is always set so backends can still see the
// client IP. Local backends receive the full set with X-Forwarded-Proto
// derived from the inbound connection.
//
// For remote upstreams X-Forwarded-Proto is rewritten to the scheme of the
// upstream connection (targetURI). The inbound connection is plain HTTP behind
// a TLS-terminating load balancer even when the upstream is HTTPS, so deriving
// the header from the inbound hop made HTTPS upstreams that redirect on
// X-Forwarded-Proto != https loop forever (see issue #5567).
func TestSetXForwardedHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		isRemote  bool
		targetURI string
		scheme    string
		wantHost  bool
		wantProto string
	}{
		{
			name:      "local backend keeps X-Forwarded-Host and inbound proto",
			isRemote:  false,
			targetURI: "http://127.0.0.1:8080",
			scheme:    "http",
			wantHost:  true,
			wantProto: "http",
		},
		{
			name:      "remote https upstream omits X-Forwarded-Host and forces https proto",
			isRemote:  true,
			targetURI: "https://mcp.example.com/mcp",
			scheme:    "https",
			wantHost:  false,
			wantProto: "https",
		},
		{
			name:      "remote http upstream omits X-Forwarded-Host and keeps http proto",
			isRemote:  true,
			targetURI: "http://mcp.example.com/mcp",
			scheme:    "http",
			wantHost:  false,
			wantProto: "http",
		},
		{
			name:      "remote upstream with empty scheme retains inbound proto",
			isRemote:  true,
			targetURI: "http://mcp.example.com/mcp",
			scheme:    "",
			wantHost:  false,
			wantProto: "http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := NewTransparentProxy(
				"127.0.0.1", 0, tt.targetURI, nil, nil, nil, false,
				tt.isRemote, "streamable-http", nil, nil, "", false,
			)

			// Inbound connection is plain HTTP, mirroring a pod reached over
			// HTTP behind a TLS-terminating load balancer.
			in := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", nil)
			pr := &httputil.ProxyRequest{In: in, Out: in.Clone(in.Context())}

			p.setXForwardedHeaders(pr, tt.scheme)

			if tt.wantHost {
				assert.Equal(t, "proxy.example.com", pr.Out.Header.Get("X-Forwarded-Host"))
			} else {
				assert.Empty(t, pr.Out.Header.Get("X-Forwarded-Host"),
					"remote upstreams must not see the proxy hostname")
			}
			assert.NotEmpty(t, pr.Out.Header.Get("X-Forwarded-For"),
				"client IP must be forwarded for both local and remote backends")
			assert.Equal(t, tt.wantProto, pr.Out.Header.Get("X-Forwarded-Proto"),
				"X-Forwarded-Proto must reflect the upstream scheme for remote proxies")
		})
	}
}
