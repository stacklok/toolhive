// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cimd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validDoc returns a ClientMetadataDocument that is valid when fetched from serverURL.
func validDoc(serverURL string) ClientMetadataDocument {
	return ClientMetadataDocument{
		ClientID:     serverURL,
		RedirectURIs: []string{"https://example.com/callback"},
		ClientName:   "Test Client",
	}
}

func TestFetchClientMetadataDocument(t *testing.T) {
	t.Parallel()

	t.Run("valid document is fetched and parsed", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The client_id in the document must match the URL it is served from,
			// so we build the expected URL dynamically from the request.
			serverURL := "http://" + r.Host + r.URL.RequestURI()
			doc := validDoc(serverURL)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc) //nolint:errcheck
		}))
		t.Cleanup(server.Close)

		serverURL := server.URL + "/metadata.json"
		// Patch the handler so the doc client_id matches the URL we will request.
		server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			doc := validDoc(serverURL)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc) //nolint:errcheck
		})

		got, err := FetchClientMetadataDocument(context.Background(), serverURL)
		require.NoError(t, err)
		assert.Equal(t, serverURL, got.ClientID)
		assert.Equal(t, []string{"https://example.com/callback"}, got.RedirectURIs)
		assert.Equal(t, "Test Client", got.ClientName)
	})

	t.Run("non-HTTPS non-localhost URL is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := FetchClientMetadataDocument(context.Background(), "http://example.com/metadata.json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "https scheme")
	})

	t.Run("localhost subdomain bypass is rejected", func(t *testing.T) {
		t.Parallel()

		// http://localhost.evil.com/ must NOT be treated as loopback.
		_, err := FetchClientMetadataDocument(context.Background(), "http://localhost.evil.com/metadata.json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "https scheme")
	})

	t.Run("HTTP non-200 status returns error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)

		_, err := FetchClientMetadataDocument(context.Background(), server.URL+"/metadata.json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "404")
	})

	t.Run("body over 10KB is handled as truncated/malformed", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Write more than 10KB of data that is not valid JSON when truncated.
			w.WriteHeader(http.StatusOK)
			// Start a JSON object but pad it well beyond the limit so the decoder
			// receives a truncated, invalid document.
			fmt.Fprintf(w, `{"client_id":"x","redirect_uris":["https://a.example.com"],"padding":"`) //nolint:errcheck
			w.Write([]byte(strings.Repeat("x", 11*1024)))                                            //nolint:errcheck
			fmt.Fprintf(w, `"}`)                                                                     //nolint:errcheck
		}))
		t.Cleanup(server.Close)

		_, err := FetchClientMetadataDocument(context.Background(), server.URL+"/metadata.json")
		require.Error(t, err)
	})

	t.Run("Content-Type not application/json returns error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"client_id":"x"}`)) //nolint:errcheck
		}))
		t.Cleanup(server.Close)

		_, err := FetchClientMetadataDocument(context.Background(), server.URL+"/metadata.json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "content type")
	})

	t.Run("application/json subtype accepted", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverURL := "http://" + r.Host + "/metadata.json"
			doc := validDoc(serverURL)
			w.Header().Set("Content-Type", "application/ld+json")
			json.NewEncoder(w).Encode(doc) //nolint:errcheck
		}))
		t.Cleanup(server.Close)

		serverURL := server.URL + "/metadata.json"
		server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			doc := validDoc(serverURL)
			w.Header().Set("Content-Type", "application/ld+json")
			json.NewEncoder(w).Encode(doc) //nolint:errcheck
		})

		_, err := FetchClientMetadataDocument(context.Background(), serverURL)
		require.NoError(t, err)
	})

	t.Run("client_id mismatch returns validation error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			doc := ClientMetadataDocument{
				ClientID:     "https://different.example.com/metadata.json",
				RedirectURIs: []string{"https://example.com/callback"},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc) //nolint:errcheck
		}))
		t.Cleanup(server.Close)

		_, err := FetchClientMetadataDocument(context.Background(), server.URL+"/metadata.json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "client_id")
	})

	t.Run("missing redirect_uris returns validation error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverURL := "http://" + r.Host + "/metadata.json"
			doc := ClientMetadataDocument{
				ClientID:     serverURL,
				RedirectURIs: nil,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc) //nolint:errcheck
		}))
		t.Cleanup(server.Close)

		serverURL := server.URL + "/metadata.json"
		server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			doc := ClientMetadataDocument{
				ClientID:     serverURL,
				RedirectURIs: nil,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc) //nolint:errcheck
		})

		_, err := FetchClientMetadataDocument(context.Background(), serverURL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redirect_uris")
	})

	t.Run("SSRF: URL scheme check rejects non-loopback HTTP", func(t *testing.T) {
		t.Parallel()

		// This exercises validateCIMDClientURL, not the dial-guard.
		_, err := FetchClientMetadataDocument(context.Background(), "http://10.0.0.1/metadata.json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "https scheme")
	})

	t.Run("SSRF: dial-guard blocks private IP served via HTTPS hostname", func(t *testing.T) {
		t.Parallel()

		// Attempt to reach a private non-loopback IP over HTTPS — the DialContext
		// SSRF guard must reject it at dial time. We use the HTTPS scheme so the
		// URL passes validateCIMDClientURL and reaches the transport layer.
		//
		// 192.0.2.1 is a TEST-NET-1 documentation address (RFC5737): it is
		// guaranteed to be unreachable and is in our SSRF block list.
		_, err := FetchClientMetadataDocument(context.Background(), "https://192.0.2.1/metadata.json")
		require.Error(t, err)
		// The error must come from the dial guard, not a network timeout.
		assert.Contains(t, err.Error(), "private address")
	})

	t.Run("SSRF: HTTPS loopback addresses are rejected", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewTLSServer(http.NotFoundHandler())
		t.Cleanup(server.Close)

		_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
		require.NoError(t, err)

		tests := []struct {
			name string
			host string
		}{
			{name: "IPv4 loopback", host: "127.0.0.1"},
			{name: "IPv6 loopback", host: "::1"},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				rawURL := "https://" + net.JoinHostPort(tt.host, port) + "/metadata.json"

				_, err := FetchClientMetadataDocument(context.Background(), rawURL)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "private address")
			})
		}
	})
}

// TestFetchClientMetadataDocument_ErrDocumentUnavailable pins which fetch
// failures carry ErrDocumentUnavailable: those in which the host delivered no
// usable response (no connection, a refusal by the dial-time SSRF guard, a
// timeout, a 200 body cut short or stalled) or answered 5xx, 408, or 429. A
// host that answers any other non-200 status, whatever happened to the body,
// a body that is not a document, and a document that fails validation are
// definitive.
func TestFetchClientMetadataDocument_ErrDocumentUnavailable(t *testing.T) {
	t.Parallel()

	serveStatus := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
	}
	serveJSON := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}
	// truncatedBody declares a longer body than it writes, so the client's
	// read ends with io.ErrUnexpectedEOF after the status arrived.
	truncatedBody := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"client_id":"x"`))
		}
	}
	// stalledBody sends the status and part of the body, then holds the
	// connection open until the fetch gives up.
	stalledBody := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"client_id":"x"`))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}

	tests := []struct {
		name string
		// handler answers the fetch; nil means the host is closed before the fetch.
		handler http.HandlerFunc
		// ctxTimeout, when set, bounds the fetch so a handler that never answers times out.
		ctxTimeout time.Duration
		// rawURL, when set, is fetched instead of a test server's URL; no server is started.
		rawURL          string
		wantUnavailable bool
	}{
		{name: "connection refused", handler: nil, wantUnavailable: true},
		{name: "SSRF guard refuses a private address", rawURL: "https://10.255.255.1/metadata.json", wantUnavailable: true},
		{name: "200 whose body is cut short", handler: truncatedBody(http.StatusOK), wantUnavailable: true},
		{name: "200 whose body stalls past the fetch timeout", handler: stalledBody(http.StatusOK),
			ctxTimeout: time.Second, wantUnavailable: true},
		{name: "fetch times out", handler: func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
			w.WriteHeader(http.StatusOK)
		}, ctxTimeout: 100 * time.Millisecond, wantUnavailable: true},
		{name: "500 internal server error", handler: serveStatus(http.StatusInternalServerError), wantUnavailable: true},
		{name: "502 bad gateway", handler: serveStatus(http.StatusBadGateway), wantUnavailable: true},
		{name: "503 service unavailable", handler: serveStatus(http.StatusServiceUnavailable), wantUnavailable: true},
		{name: "504 gateway timeout", handler: serveStatus(http.StatusGatewayTimeout), wantUnavailable: true},
		{name: "503 whose body is cut short", handler: truncatedBody(http.StatusServiceUnavailable), wantUnavailable: true},
		{name: "408 request timeout", handler: serveStatus(http.StatusRequestTimeout), wantUnavailable: true},
		{name: "429 too many requests", handler: serveStatus(http.StatusTooManyRequests), wantUnavailable: true},
		{name: "404 not found", handler: serveStatus(http.StatusNotFound)},
		{name: "403 forbidden", handler: serveStatus(http.StatusForbidden)},
		{name: "410 gone", handler: serveStatus(http.StatusGone)},
		{name: "404 whose body is cut short", handler: truncatedBody(http.StatusNotFound)},
		{name: "410 whose body stalls past the fetch timeout", handler: stalledBody(http.StatusGone), ctxTimeout: time.Second},
		{name: "302 redirect", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://example.com/moved.json")
			w.WriteHeader(http.StatusFound)
		}},
		{name: "unexpected content type", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		}},
		{name: "malformed body", handler: serveJSON(`{"client_id":`)},
		{name: "document fails validation", handler: serveJSON(`{"client_id":"https://other.example.com/meta.json","redirect_uris":["https://example.com/cb"]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rawURL := tt.rawURL
			if rawURL == "" {
				server := httptest.NewServer(tt.handler)
				t.Cleanup(server.Close)
				rawURL = server.URL + "/metadata.json"
				if tt.handler == nil {
					server.Close()
				}
			}

			ctx := context.Background()
			if tt.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.ctxTimeout)
				t.Cleanup(cancel)
			}

			_, err := FetchClientMetadataDocument(ctx, rawURL)
			require.Error(t, err)
			assert.Equal(t, tt.wantUnavailable, errors.Is(err, ErrDocumentUnavailable),
				"ErrDocumentUnavailable classification for %q: %v", tt.name, err)
		})
	}

	t.Run("invalid client_id URL is definitive", func(t *testing.T) {
		t.Parallel()
		_, err := FetchClientMetadataDocument(context.Background(), "http://example.com/metadata.json")
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrDocumentUnavailable))
	})
}

func TestValidateClientMetadataDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		doc         *ClientMetadataDocument
		fetchedFrom string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid document passes",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{"https://app.example.com/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     false,
		},
		{
			name: "valid document with loopback redirect passes",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{"http://localhost:4000/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     false,
		},
		{
			name: "valid document with IPv4 loopback redirect passes",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{"http://127.0.0.1:4000/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     false,
		},
		{
			name: "valid document with IPv6 loopback redirect passes",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{"http://[::1]:4000/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     false,
		},
		{
			name: "empty client_id fails",
			doc: &ClientMetadataDocument{
				ClientID:     "",
				RedirectURIs: []string{"https://example.com/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     true,
			errContains: "client_id",
		},
		{
			name: "empty redirect_uris fails",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     true,
			errContains: "redirect_uris",
		},
		{
			name: "nil redirect_uris fails",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: nil,
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     true,
			errContains: "redirect_uris",
		},
		{
			name: "client_id mismatch fails (strict string comparison)",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{"https://example.com/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json/",
			wantErr:     true,
			errContains: "client_id",
		},
		{
			name: "redirect_uri with ftp:// scheme fails",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{"ftp://example.com/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     true,
			errContains: "redirect_uri",
		},
		{
			name: "redirect_uri with http://example.com (non-loopback) fails",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{"http://example.com/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     true,
			errContains: "redirect_uri",
		},
		{
			name: "redirect_uri with http://192.168.1.1 (private non-loopback) fails",
			doc: &ClientMetadataDocument{
				ClientID:     "https://example.com/metadata.json",
				RedirectURIs: []string{"http://192.168.1.1/callback"},
			},
			fetchedFrom: "https://example.com/metadata.json",
			wantErr:     true,
			errContains: "redirect_uri",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateClientMetadataDocument(tt.doc, tt.fetchedFrom)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
