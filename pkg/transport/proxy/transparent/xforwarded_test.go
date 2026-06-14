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
// proxy), while X-Forwarded-For and X-Forwarded-Proto are always set so
// backends can still see the client IP and scheme. Local backends receive
// the full set.
func TestSetXForwardedHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		isRemote bool
		wantHost bool
	}{
		{name: "local backend keeps X-Forwarded-Host", isRemote: false, wantHost: true},
		{name: "remote upstream omits X-Forwarded-Host", isRemote: true, wantHost: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := NewTransparentProxy(
				"127.0.0.1", 0, "", nil, nil, nil, false,
				tt.isRemote, "streamable-http", nil, nil, "", false,
			)

			in := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", nil)
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
			assert.Equal(t, "http", pr.Out.Header.Get("X-Forwarded-Proto"))
		})
	}
}
