// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestFactoryStripConsentCookieChangesLiveProxyBehavior(t *testing.T) {
	t.Parallel()

	for _, transportType := range []types.TransportType{types.TransportTypeSSE, types.TransportTypeStreamableHTTP} {
		for _, strip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/strip=%t", transportType, strip), func(t *testing.T) {
				t.Parallel()

				backendCookies := make(chan string, 1)
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					select {
					case backendCookies <- r.Header.Get("Cookie"):
					case <-r.Context().Done():
					}
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(backend.Close)

				transport, err := NewFactory().Create(types.Config{
					Type:               transportType,
					Host:               LocalhostIPv4,
					StripConsentCookie: strip,
				})
				require.NoError(t, err)
				httpTransport, ok := transport.(*HTTPTransport)
				require.True(t, ok, "factory should create an HTTPTransport")
				httpTransport.SetRemoteURL(backend.URL + "/mcp")
				require.NoError(t, httpTransport.Start(t.Context()))
				t.Cleanup(func() {
					stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					require.NoError(t, httpTransport.Stop(stopCtx))
				})

				proxy, ok := httpTransport.proxy.(*transparent.TransparentProxy)
				require.True(t, ok, "HTTPTransport should start a transparent proxy")
				addr := proxy.ListenerAddr()
				require.NotEmpty(t, addr)
				requestCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				t.Cleanup(cancel)
				req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+addr+"/mcp", nil)
				require.NoError(t, err)
				req.AddCookie(&http.Cookie{Name: "backend", Value: "one"})
				req.AddCookie(&http.Cookie{Name: "__Host-thv_consent_test", Value: "remembered"})
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				t.Cleanup(func() { _ = resp.Body.Close() })
				require.Equal(t, http.StatusOK, resp.StatusCode)
				_, err = io.Copy(io.Discard, resp.Body)
				require.NoError(t, err)

				want := "backend=one; __Host-thv_consent_test=remembered"
				if strip {
					want = "backend=one"
				}
				select {
				case got := <-backendCookies:
					require.Equal(t, want, got)
				case <-requestCtx.Done():
					t.Fatal("timed out waiting for backend cookie observation")
				}
			})
		}
	}
}
