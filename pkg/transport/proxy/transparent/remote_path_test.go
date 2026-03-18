// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoteQueryForwarding verifies that the transparent proxy correctly
// forwards query parameters from the remote URL configuration to every
// outbound request.
//
// Scenario: remoteURL is https://mcp.datadoghq.com/mcp?toolsets=core,alerting
// Without this fix the query params are silently dropped and the remote
// server receives /mcp with no toolsets, returning only default tools.
func TestRemoteQueryForwarding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		remoteRawQuery   string // Query from registration URL
		clientRawQuery   string // Additional query from client request
		expectedRawQuery string // Query that should arrive at the remote server
		description      string
	}{
		{
			name:             "remote query only, no client query",
			remoteRawQuery:   "toolsets=core,alerting",
			clientRawQuery:   "",
			expectedRawQuery: "toolsets=core,alerting",
			description:      "Datadog case: remote query params forwarded when client sends none",
		},
		{
			name:             "remote query merged with client query",
			remoteRawQuery:   "toolsets=core,alerting",
			clientRawQuery:   "session=abc",
			expectedRawQuery: "toolsets=core,alerting&session=abc",
			description:      "Remote params take precedence, client params appended",
		},
		{
			name:             "no remote query, client query preserved",
			remoteRawQuery:   "",
			clientRawQuery:   "session=abc",
			expectedRawQuery: "session=abc",
			description:      "Without remote query, client query passes through unchanged",
		},
		{
			name:             "no remote query and no client query",
			remoteRawQuery:   "",
			clientRawQuery:   "",
			expectedRawQuery: "",
			description:      "No query params in either direction",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var receivedQuery atomic.Value

			remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedQuery.Store(r.URL.RawQuery)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"protocolVersion":"2024-11-05"}}`))
			}))
			defer remoteServer.Close()

			parsedRemote, err := url.Parse(remoteServer.URL)
			require.NoError(t, err)
			targetURI := (&url.URL{
				Scheme: parsedRemote.Scheme,
				Host:   parsedRemote.Host,
			}).String()

			var opts []Option
			if tt.remoteRawQuery != "" {
				opts = append(opts, WithRemoteRawQuery(tt.remoteRawQuery))
			}

			proxy := NewTransparentProxyWithOptions(
				"127.0.0.1", 0, targetURI,
				nil, nil, nil,
				false, true, "streamable-http",
				nil, nil,
				"", false,
				nil, // middlewares
				opts...,
			)

			ctx := context.Background()
			err = proxy.Start(ctx)
			require.NoError(t, err)
			defer func() {
				assert.NoError(t, proxy.Stop(context.Background()))
			}()

			addr := proxy.ListenerAddr()
			require.NotEmpty(t, addr)

			proxyURL := fmt.Sprintf("http://%s/mcp", addr)
			if tt.clientRawQuery != "" {
				proxyURL += "?" + tt.clientRawQuery
			}

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
			assert.Equal(t, tt.expectedRawQuery, actualQuery,
				"%s: remote server received wrong query string", tt.description)
		})
	}
}

// TestRemotePathForwarding verifies that the transparent proxy correctly
// forwards requests to the remote server's full path, not just the host.
//
// Scenario: remoteURL is https://mcp.asana.com/v2/mcp
// The proxy strips the path and uses https://mcp.asana.com as the target.
// When a client sends POST /mcp to the proxy, the request is forwarded to
// https://mcp.asana.com/mcp instead of https://mcp.asana.com/v2/mcp.
func TestRemotePathForwarding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		remoteURL    string // The configured remoteURL (e.g. https://mcp.asana.com/v2/mcp)
		clientPath   string // Path the MCP client sends (e.g. /mcp or /v2/mcp)
		expectedPath string // Path that should arrive at the remote server
		description  string
	}{
		{
			name:         "remote URL with path prefix, client sends default /mcp",
			remoteURL:    "/v2/mcp",
			clientPath:   "/mcp",
			expectedPath: "/v2/mcp",
			description:  "Asana case: client sends /mcp but remote expects /v2/mcp",
		},
		{
			name:         "remote URL with path prefix, client sends full path",
			remoteURL:    "/v2/mcp",
			clientPath:   "/v2/mcp",
			expectedPath: "/v2/mcp",
			description:  "Client correctly sends the full remote path",
		},
		{
			name:         "remote URL with no path, client sends /mcp",
			remoteURL:    "",
			clientPath:   "/mcp",
			expectedPath: "/mcp",
			description:  "GitHub case: no path prefix, /mcp goes to /mcp",
		},
		{
			name:         "remote URL with /v1/mcp path, client sends /mcp",
			remoteURL:    "/v1/mcp",
			clientPath:   "/mcp",
			expectedPath: "/v1/mcp",
			description:  "Atlassian case: client sends /mcp but remote expects /v1/mcp",
		},
		{
			name:         "remote URL with single path segment replaces client path",
			remoteURL:    "/api",
			clientPath:   "/mcp",
			expectedPath: "/api",
			description:  "Remote path /api replaces client path /mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Track what path the remote server actually receives
			var receivedPath atomic.Value

			remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedPath.Store(r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"protocolVersion":"2024-11-05"}}`))
			}))
			defer remoteServer.Close()

			// Construct the full remote URL with path
			remoteURL := remoteServer.URL + tt.remoteURL

			// Build target URI the same way http.go does (strip path, pass base path separately)
			parsedRemote, err := url.Parse(remoteURL)
			require.NoError(t, err)
			targetURI := (&url.URL{
				Scheme: parsedRemote.Scheme,
				Host:   parsedRemote.Host,
			}).String()
			remoteBasePath := parsedRemote.Path

			var opts []Option
			if remoteBasePath != "" {
				opts = append(opts, WithRemoteBasePath(remoteBasePath))
			}

			proxy := NewTransparentProxyWithOptions(
				"127.0.0.1", 0, targetURI,
				nil, nil, nil,
				false, true, "streamable-http",
				nil, nil,
				"", false,
				nil, // middlewares
				opts...,
			)

			ctx := context.Background()
			err = proxy.Start(ctx)
			require.NoError(t, err)
			defer func() {
				assert.NoError(t, proxy.Stop(context.Background()))
			}()

			addr := proxy.listener.Addr()
			require.NotNil(t, addr)

			// Send request with the client's path
			proxyURL := fmt.Sprintf("http://%s%s", addr.String(), tt.clientPath)
			body := `{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`
			req, err := http.NewRequest("POST", proxyURL, strings.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)

			actualPath, _ := receivedPath.Load().(string)
			assert.Equal(t, tt.expectedPath, actualPath,
				"%s: remote server received wrong path", tt.description)
		})
	}
}
