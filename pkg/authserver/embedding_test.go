// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
