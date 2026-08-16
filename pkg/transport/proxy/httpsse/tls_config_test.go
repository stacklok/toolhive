// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTLSConfigClonesConfiguration(t *testing.T) {
	t.Parallel()

	original := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: "proxy.example.test",
	}
	proxy := NewHTTPSSEProxy("127.0.0.1", 0, false, nil, nil, WithTLSConfig(original))
	require.NotNil(t, proxy.tlsConfig)
	assert.NotSame(t, original, proxy.tlsConfig)
	assert.Equal(t, original.MinVersion, proxy.tlsConfig.MinVersion)
	assert.Equal(t, original.ServerName, proxy.tlsConfig.ServerName)

	original.MinVersion = tls.VersionTLS13
	original.ServerName = "changed.example.test"
	assert.Equal(t, uint16(tls.VersionTLS12), proxy.tlsConfig.MinVersion)
	assert.Equal(t, "proxy.example.test", proxy.tlsConfig.ServerName)
}

//nolint:paralleltest // Test starts and stops HTTP servers.
func TestHTTPSSEProxyTLSListener(t *testing.T) {
	serverTLS, tlsClient := newTestTLSConfiguration(t)

	tests := []struct {
		name      string
		tlsConfig *tls.Config
		scheme    string
		client    *http.Client
	}{
		{
			name:      "TLS configuration serves HTTPS",
			tlsConfig: serverTLS,
			scheme:    "https",
			client:    tlsClient,
		},
		{
			name:   "nil TLS configuration preserves HTTP",
			scheme: "http",
			client: &http.Client{Timeout: time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := []Option{WithPrefixHandlers(map[string]http.Handler{
				"/ready": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}),
			})}
			if tt.tlsConfig != nil {
				options = append(options, WithTLSConfig(tt.tlsConfig))
			}

			proxy := NewHTTPSSEProxy("127.0.0.1", 0, false, nil, nil, options...)
			require.NoError(t, proxy.Start(t.Context()))
			t.Cleanup(func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, proxy.Stop(stopCtx))
				tt.client.CloseIdleConnections()
			})

			url := fmt.Sprintf("%s://%s/ready", tt.scheme, proxy.server.Addr)
			var response *http.Response
			require.Eventually(t, func() bool {
				var err error
				response, err = tt.client.Get(url)
				return err == nil
			}, 2*time.Second, 10*time.Millisecond, "proxy should accept %s connections", tt.scheme)
			t.Cleanup(func() { _ = response.Body.Close() })
			assert.Equal(t, http.StatusNoContent, response.StatusCode)
		})
	}
}

func newTestTLSConfiguration(t *testing.T) (*tls.Config, *http.Client) {
	t.Helper()

	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := certificateServer.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())
	certificateServer.Close()

	return &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		}, &http.Client{
			Timeout: time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    roots,
			}},
		}
}
