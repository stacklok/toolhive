// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// newDCRCapTestServer builds a server on stor with the given registration
// rate limit (nil keeps the default) and closes it, and with it stor, at
// cleanup.
func newDCRCapTestServer(t *testing.T, stor storage.Storage, rateLimit *RegistrationRateLimitRunConfig) *server {
	t.Helper()
	mockUpstream := upstreammocks.NewMockOAuth2Provider(gomock.NewController(t))
	cfg := Config{
		Issuer:                "https://example.com",
		KeyProvider:           keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets:           &servercrypto.HMACSecrets{Current: validHMACSecret()},
		Upstreams:             []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
		AllowedAudiences:      []string{"https://mcp.example.com"},
		RegistrationRateLimit: rateLimit,
		UpstreamFactory: func(_ context.Context, _ *UpstreamConfig) (upstream.OAuth2Provider, error) {
			return mockUpstream, nil
		},
	}
	srv, err := newServer(context.Background(), cfg, stor)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// TestNewServer_BackfillsDCRClientIndex pins that server construction runs
// the DCR client index backfill, so an upgraded deployment counts the DCR
// rows written before the index existed.
func TestNewServer_BackfillsDCRClientIndex(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	stor := storage.NewRedisStorageWithClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}),
		storage.DeriveKeyPrefix("ns", "server"))

	client, err := registration.New(registration.Config{
		ID:                      "legacy",
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone,
		RedirectURIs:            []string{"http://127.0.0.1/callback"},
	})
	require.NoError(t, err)
	require.NoError(t, stor.RegisterClient(context.Background(), client))
	// Drop the index, leaving the row as a binary predating the index wrote it.
	indexKey := storage.DeriveKeyPrefix("ns", "server") + storage.KeyTypeDCRClientIdx
	mr.Del(indexKey)

	newDCRCapTestServer(t, stor, nil)

	members, err := mr.ZMembers(indexKey)
	require.NoError(t, err)
	assert.Equal(t, []string{"legacy"}, members)
}

// TestNewServer_StartsWhenBackfillFails pins that a failed DCR client index
// backfill does not stop the server from starting: skipping it only leaves the
// cap undercounting until the next restart.
func TestNewServer_StartsWhenBackfillFails(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	prefix := storage.DeriveKeyPrefix("ns", "server")
	stor := storage.NewRedisStorageWithClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}), prefix)

	client, err := registration.New(registration.Config{
		ID:                      "legacy",
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone,
		RedirectURIs:            []string{"http://127.0.0.1/callback"},
	})
	require.NoError(t, err)
	require.NoError(t, stor.RegisterClient(context.Background(), client))
	// Replace the index with a string, so the backfill's ZADD fails WRONGTYPE.
	indexKey := prefix + storage.KeyTypeDCRClientIdx
	mr.Del(indexKey)
	require.NoError(t, mr.Set(indexKey, "not a sorted set"))

	newDCRCapTestServer(t, stor, nil)

	got, err := mr.Get(indexKey)
	require.NoError(t, err)
	assert.Equal(t, "not a sorted set", got, "the backfill must have failed rather than rebuilt the index")
}

// TestNewServer_RegistrationRateLimit pins that registration_rate_limit
// reaches the /oauth/register limiter, and that leaving it unset keeps the
// default burst of 5.
func TestNewServer_RegistrationRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		rateLimit      *RegistrationRateLimitRunConfig
		wantAdmitted   int
		wantRetryAfter string
	}{
		{name: "default", wantAdmitted: 5, wantRetryAfter: "1"},
		{
			name:           "configured",
			rateLimit:      &RegistrationRateLimitRunConfig{RequestsPerSecond: 0.001, Burst: 2},
			wantAdmitted:   2,
			wantRetryAfter: "1000",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			routes := newDCRCapTestServer(t, storage.NewMemoryStorage(), tc.rateLimit).Handler()

			for i := range tc.wantAdmitted {
				assert.Equal(t, http.StatusCreated, postRegistration(t, routes).Code, "request %d", i)
			}
			w := postRegistration(t, routes)
			assert.Equal(t, http.StatusTooManyRequests, w.Code)
			assert.Equal(t, tc.wantRetryAfter, w.Header().Get("Retry-After"))
		})
	}
}

func postRegistration(t *testing.T, routes http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)
	return w
}
