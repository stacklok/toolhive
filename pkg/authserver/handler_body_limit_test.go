// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestServerHandlerBodyLimit(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { assert.NoError(t, stor.Close()) })
	srv, err := New(t.Context(), Config{
		Issuer:           "https://example.com",
		KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		AllowedAudiences: []string{"https://mcp.example.com"},
		Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
	}, stor)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, srv.Close()) })

	tests := []struct {
		name          string
		path          string
		bodySize      int
		contentLength int64
		wantStatus    int
	}{
		{"declared oversized token body", "/oauth/token", handlers.MaxDCRBodySize + 1, handlers.MaxDCRBodySize + 1, http.StatusRequestEntityTooLarge},
		{"unknown length token body", "/oauth/token", handlers.MaxDCRBodySize + 1, -1, http.StatusRequestEntityTooLarge},
		{"understated token body", "/oauth/token", handlers.MaxDCRBodySize + 1, 1, http.StatusRequestEntityTooLarge},
		{"exact limit token body", "/oauth/token", handlers.MaxDCRBodySize, handlers.MaxDCRBodySize, http.StatusBadRequest},
		{"unknown length exact limit token body", "/oauth/token", handlers.MaxDCRBodySize, -1, http.StatusBadRequest},
		{"unknown path", "/unknown", 0, 0, http.StatusNotFound},
		{"declared oversized unknown path body", "/unknown", handlers.MaxDCRBodySize + 1, handlers.MaxDCRBodySize + 1, http.StatusRequestEntityTooLarge},
		{"unsupported discovery method", "/.well-known/openid-configuration", 0, 0, http.StatusMethodNotAllowed},
		{"declared oversized discovery body", "/.well-known/openid-configuration", handlers.MaxDCRBodySize + 1, handlers.MaxDCRBodySize + 1, http.StatusRequestEntityTooLarge},
	}
	parent := chi.NewRouter()
	parent.Mount("/auth", srv.Handler())
	for _, setup := range []struct {
		name    string
		prefix  string
		handler http.Handler
	}{
		{"direct", "", srv.Handler()},
		{"mounted", "/auth", parent},
	} {
		t.Run(setup.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					req := httptest.NewRequest(http.MethodPost, setup.prefix+tt.path, strings.NewReader(strings.Repeat("x", tt.bodySize)))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					req.ContentLength = tt.contentLength
					response := httptest.NewRecorder()
					setup.handler.ServeHTTP(response, req)
					assert.Equal(t, tt.wantStatus, response.Code, "%s", response.Body.String())
				})
			}
		})
	}
}
