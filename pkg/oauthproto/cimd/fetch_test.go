// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cimd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

		// Spin up a real server on loopback so we have a valid port, then
		// attempt to reach a private non-loopback IP over HTTPS — the DialContext
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
