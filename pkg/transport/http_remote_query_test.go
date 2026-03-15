// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestHTTPTransport_Start_RemoteURLQueryParams verifies that HTTPTransport.Start()
// correctly extracts the raw query from the remoteURL and wires it into the
// transparent proxy so every upstream request carries those query parameters.
func TestHTTPTransport_Start_RemoteURLQueryParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		remoteQuery   string // query string appended to the remote registration URL
		expectedQuery string // raw query the upstream server should receive
		description   string
	}{
		{
			name:          "query params from registration URL are forwarded to upstream",
			remoteQuery:   "toolsets=core,alerting",
			expectedQuery: "toolsets=core,alerting",
			description:   "Datadog case: toolset selection params must reach the upstream server",
		},
		{
			name:          "multiple query params are all forwarded to upstream",
			remoteQuery:   "toolsets=core,alerting&version=2",
			expectedQuery: "toolsets=core,alerting&version=2",
			description:   "Multiple params must all be forwarded, none dropped",
		},
		{
			name:          "no query params — upstream receives empty query string",
			remoteQuery:   "",
			expectedQuery: "",
			description:   "Without configured query params, upstream receives an empty query string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var receivedQuery atomic.Value

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedQuery.Store(r.URL.RawQuery)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"protocolVersion":"2024-11-05"}}`))
			}))
			defer upstream.Close()

			remoteURL := upstream.URL + "/mcp"
			if tt.remoteQuery != "" {
				remoteURL += "?" + tt.remoteQuery
			}

			// Use port 0 so the OS assigns a free port.
			transport := NewHTTPTransport(
				types.TransportTypeStreamableHTTP,
				LocalhostIPv4,
				0,     // proxyPort: OS-assigned
				0,     // targetPort: unused for remote
				nil,   // deployer: nil for remote
				false, // debug
				"",    // targetHost: unused for remote
				nil,   // authInfoHandler
				nil,   // prometheusHandler
				nil,   // prefixHandlers
				"",    // endpointPrefix
				false, // trustProxyHeaders
			)
			transport.SetRemoteURL(remoteURL)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			require.NoError(t, transport.Start(ctx))
			defer func() {
				assert.NoError(t, transport.Stop(context.Background()))
			}()

			// Retrieve the actual listening address from the underlying proxy.
			tp, ok := transport.proxy.(*transparent.TransparentProxy)
			require.True(t, ok, "proxy should be a TransparentProxy")
			addr := tp.ListenerAddr()
			require.NotEmpty(t, addr, "proxy should be listening")

			// POST to the clean proxy URL (no query params) so only the
			// proxy-configured remoteRawQuery is the source of upstream query params.
			proxyURL := fmt.Sprintf("http://%s/mcp", addr)
			body := `{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`
			req, err := http.NewRequest(http.MethodPost, proxyURL, strings.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)

			actualQuery, _ := receivedQuery.Load().(string)
			assert.Equal(t, tt.expectedQuery, actualQuery,
				"%s: upstream server received wrong query string", tt.description)
		})
	}
}
