// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/oauthproto/cimd"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// serveCIMDDoc starts a server hosting a client metadata document at
// /metadata.json whose client_id is its own URL, and returns that URL.
func serveCIMDDoc(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metadata.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cimd.ClientMetadataDocument{
			ClientID:     "http://" + r.Host + r.URL.Path,
			RedirectURIs: []string{"http://localhost:8080/callback"},
		})
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/metadata.json"
}

// TestEmbeddedAuthServer_CIMD runs a real EmbeddedAuthServer built from a
// RunConfig and verifies that CIMD support is advertised in discovery and that
// a CIMD URL is accepted as client_id at /oauth/authorize, with no prior DCR
// registration, only when CIMD is enabled.
func TestEmbeddedAuthServer_CIMD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cimdCfg *authserver.CIMDRunConfig
		enabled bool
	}{
		{
			name:    "enabled",
			cimdCfg: &authserver.CIMDRunConfig{Enabled: true, CacheMaxSize: 16},
			enabled: true,
		},
		{
			name: "disabled",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			upstream := helpers.NewMockUpstreamIDP(t)
			cimdURL := serveCIMDDoc(t)

			opts := []helpers.AuthServerOption{}
			if tc.cimdCfg != nil {
				opts = append(opts, helpers.WithCIMD(tc.cimdCfg))
			}
			cfg := helpers.NewTestAuthServerConfig(t, upstream.URL(), opts...)
			authServer := helpers.NewEmbeddedAuthServer(context.Background(), t, cfg)
			server := httptest.NewServer(authServer.Handler())
			t.Cleanup(server.Close)

			// No RegisterClient call: a CIMD client must not need DCR.
			client := helpers.NewOAuthClient(server.URL)

			oauthMeta, statusCode, err := client.GetOAuthDiscovery()
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, statusCode)
			oidcMeta, statusCode, err := client.GetOIDCDiscovery()
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, statusCode)
			for name, meta := range map[string]map[string]any{"oauth": oauthMeta, "oidc": oidcMeta} {
				if tc.enabled {
					assert.Equal(t, true, meta["client_id_metadata_document_supported"],
						"%s discovery must advertise CIMD support", name)
				} else {
					assert.NotContains(t, meta, "client_id_metadata_document_supported",
						"%s discovery must not advertise CIMD support", name)
				}
			}

			challenge := servercrypto.ComputePKCEChallenge(servercrypto.GeneratePKCEVerifier())
			resp, err := client.StartAuthorization(url.Values{
				"response_type":         {"code"},
				"client_id":             {cimdURL},
				"redirect_uri":          {"http://localhost:8080/callback"},
				"code_challenge":        {challenge},
				"code_challenge_method": {"S256"},
				"state":                 {"test-state-cimd"},
				"resource":              {cfg.AllowedAudiences[0]},
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })

			if !tc.enabled {
				assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
					"a CIMD URL must be an unknown client_id when CIMD is disabled")
				assert.NotContains(t, resp.Header.Get("Location"), upstream.URL(),
					"authorize must not continue to the upstream IDP")
				return
			}

			require.Equal(t, http.StatusOK, resp.StatusCode, "a resolved CIMD client reaches consent")
			approved, err := client.ApproveConsent(resp, cfg.Issuer)
			require.NoError(t, err)
			t.Cleanup(func() { _ = approved.Body.Close() })
			assert.Equal(t, http.StatusSeeOther, approved.StatusCode)
			assert.Contains(t, approved.Header.Get("Location"), upstream.URL(),
				"consent approval must redirect to the upstream IDP")
		})
	}
}
