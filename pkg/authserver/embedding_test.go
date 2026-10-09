// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func TestNewRegistersIdentityModifiers(t *testing.T) {
	t.Parallel()

	// Registration is process-global. A fresh test process prevents another
	// constructor from masking a missing registration in this test.
	const childEnv = "TOOLHIVE_TEST_AUTHSERVER_MODIFIERS"
	if os.Getenv(childEnv) != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestNewRegistersIdentityModifiers$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return
	}

	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"upstream-user"}`))
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "e30." + payload + ".test-signature",
			"token_type":   "Bearer",
		})
	}))
	t.Cleanup(tokenServer.Close)

	upstreamCfg := validUpstreamConfig()
	upstreamCfg.AuthorizationEndpoint = tokenServer.URL + "/authorize"
	upstreamCfg.TokenEndpoint = tokenServer.URL + "/token"
	upstreamCfg.IdentityFromToken = &upstream.IdentityFromTokenConfig{
		SubjectPath: "access_token|@upstreamjwt|sub",
	}
	stor := storage.NewMemoryStorage()
	srv, err := New(t.Context(), Config{
		Issuer:           "https://example.com",
		KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		AllowedAudiences: []string{"https://mcp.example.com"},
		Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: upstreamCfg}},
		UpstreamFactory: func(_ context.Context, cfg *UpstreamConfig) (upstream.OAuth2Provider, error) {
			return upstream.NewOAuth2Provider(cfg.OAuth2Config, upstream.WithOAuth2HTTPClient(tokenServer.Client()))
		},
	}, stor)
	if err != nil {
		require.NoError(t, stor.Close())
	}
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, srv.Close()) })

	identity, err := srv.(*server).upstreams[0].Provider.ExchangeCodeForIdentity(t.Context(), "test-code", "", "")
	require.NoError(t, err)
	assert.Equal(t, "upstream-user", identity.Subject)
	assert.False(t, identity.Synthetic)
}

func TestNewFailureLeavesStorageCallerOwned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         Config
		errContains string
	}{
		{name: "validation failure", errContains: "issuer is required"},
		{
			name: "upstream construction failure",
			cfg: Config{
				Issuer:           "https://example.com",
				KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
				AllowedAudiences: []string{"https://mcp.example.com"},
				Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
				UpstreamFactory: func(context.Context, *UpstreamConfig) (upstream.OAuth2Provider, error) {
					return nil, errors.New("upstream construction failed")
				},
			},
			errContains: "upstream construction failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { assert.NoError(t, base.Close()) })
			var events []string
			stor := &eventRecordingStorage{MemoryStorage: base, record: func(event string) { events = append(events, event) }}
			srv, err := New(t.Context(), tt.cfg, stor)
			require.ErrorContains(t, err, tt.errContains)
			assert.Nil(t, srv)
			assert.Empty(t, events, "failed construction must not close caller storage")
		})
	}
}

func TestServerHandlerBodyLimit(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	srv, err := New(t.Context(), Config{
		Issuer:           "https://example.com",
		KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		AllowedAudiences: []string{"https://mcp.example.com"},
		Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
	}, stor)
	if err != nil {
		require.NoError(t, stor.Close())
	}
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
		{"declared oversized discovery body", "/.well-known/openid-configuration", handlers.MaxDCRBodySize + 1, handlers.MaxDCRBodySize + 1, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(strings.Repeat("x", tt.bodySize)))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.ContentLength = tt.contentLength
			response := httptest.NewRecorder()
			srv.Handler().ServeHTTP(response, req)
			assert.Equal(t, tt.wantStatus, response.Code, "%s", response.Body.String())
		})
	}
}
