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
// headers. A remote upstream must not receive X-Forwarded-Host (third-party
// servers can echo it into redirect URLs, creating 307 loops back to the
// proxy), while X-Forwarded-For is always set so backends can see the client
// IP. Local backends receive the full set.
//
// X-Forwarded-Proto must reflect the real client/upstream scheme rather than
// the pod-local inbound HTTP hop: behind a TLS-terminating load balancer the
// inbound connection is always HTTP, and forwarding "http" to a TLS upstream
// that enforces HTTPS via the header causes infinite redirect loops (#5567).
func TestSetXForwardedHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		targetURI         string
		isRemote          bool
		trustProxyHeaders bool
		inboundProto      string
		wantHost          bool
		wantProto         string
	}{
		{
			name:      "local backend keeps X-Forwarded-Host and http proto",
			targetURI: "http://backend.svc:8080",
			isRemote:  false,
			wantHost:  true,
			wantProto: "http",
		},
		{
			name:      "remote https upstream omits X-Forwarded-Host and sets https proto",
			targetURI: "https://mcp.example.com/mcp",
			isRemote:  true,
			wantHost:  false,
			wantProto: "https",
		},
		{
			name:      "remote http upstream keeps http proto",
			targetURI: "http://mcp.example.com/mcp",
			isRemote:  true,
			wantHost:  false,
			wantProto: "http",
		},
		{
			name:              "remote upstream honors trusted inbound X-Forwarded-Proto",
			targetURI:         "https://mcp.example.com/mcp",
			isRemote:          true,
			trustProxyHeaders: true,
			inboundProto:      "https",
			wantHost:          false,
			wantProto:         "https",
		},
		{
			name:              "trusted inbound proto wins over upstream scheme",
			targetURI:         "http://mcp.example.com/mcp",
			isRemote:          true,
			trustProxyHeaders: true,
			inboundProto:      "https",
			wantHost:          false,
			wantProto:         "https",
		},
		{
			name:              "trust enabled but no inbound proto falls back to upstream scheme",
			targetURI:         "https://mcp.example.com/mcp",
			isRemote:          true,
			trustProxyHeaders: true,
			wantHost:          false,
			wantProto:         "https",
		},
		{
			name:         "untrusted inbound proto is ignored in favor of upstream scheme",
			targetURI:    "https://mcp.example.com/mcp",
			isRemote:     true,
			inboundProto: "http",
			wantHost:     false,
			wantProto:    "https",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := NewTransparentProxy(
				"127.0.0.1", 0, tt.targetURI, nil, nil, nil, false,
				tt.isRemote, "streamable-http", nil, nil, "", tt.trustProxyHeaders,
			)

			in := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", nil)
			if tt.inboundProto != "" {
				in.Header.Set("X-Forwarded-Proto", tt.inboundProto)
			}
			pr := &httputil.ProxyRequest{In: in, Out: in.Clone(in.Context())}

			p.setXForwardedHeaders(pr)

			if tt.wantHost {
				assert.Equal(t, "proxy.example.com", pr.Out.Header.Get("X-Forwarded-Host"))
			} else {
				assert.Empty(t, pr.Out.Header.Get("X-Forwarded-Host"),
					"remote upstreams must not see the proxy hostname")
			}
			assert.NotEmpty(t, pr.Out.Header.Get("X-Forwarded-For"),
				"client IP must be forwarded for both local and remote backends")
			assert.Equal(t, tt.wantProto, pr.Out.Header.Get("X-Forwarded-Proto"))
		})
	}
}
