// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestTokenOnlyDCRRejectsAuthorizationCodeClient(t *testing.T) {
	t.Parallel()

	configured, _, _ := handlerTestSetup(t)
	handler, err := NewHandler(configured.provider, configured.config, configured.storage, nil)
	require.NoError(t, err)

	body, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.RegisterClientHandler(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "token-only authorization servers")
}

func TestTokenOnlyCapabilityBehaviorUsesTokenOnlyField(t *testing.T) {
	t.Parallel()

	handler, _, _ := handlerTestSetup(t)
	handler.tokenOnly = true

	discovery := httptest.NewRecorder()
	handler.OIDCDiscoveryHandler(discovery, httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil))
	require.Equal(t, http.StatusOK, discovery.Code)
	var metadata map[string]any
	require.NoError(t, json.NewDecoder(discovery.Body).Decode(&metadata))
	assert.NotContains(t, metadata, "authorization_endpoint")
	assert.NotContains(t, metadata, "code_challenge_methods_supported")

	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"code"},
		"state":         {"test-state"},
	}
	authorize := httptest.NewRecorder()
	handler.AuthorizeHandler(authorize, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))
	require.Equal(t, http.StatusSeeOther, authorize.Code)
	location, err := url.Parse(authorize.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "unsupported_response_type", location.Query().Get("error"))

	body, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
	})
	require.NoError(t, err)
	dcr := httptest.NewRecorder()
	dcrRequest := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body))
	dcrRequest.Header.Set("Content-Type", "application/json")
	handler.RegisterClientHandler(dcr, dcrRequest)
	require.Equal(t, http.StatusBadRequest, dcr.Code)
	assert.Contains(t, dcr.Body.String(), "token-only authorization servers")
}

func TestTokenOnlyHandlerConstructedWithoutUpstreams(t *testing.T) {
	t.Parallel()

	configured, _, _ := handlerTestSetup(t)
	handler, err := NewHandler(configured.provider, configured.config, configured.storage, nil)
	require.NoError(t, err)

	for name, serve := range map[string]http.HandlerFunc{
		"OAuth":      handler.OAuthDiscoveryHandler,
		"OIDC alias": handler.OIDCDiscoveryHandler,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			serve(rec, httptest.NewRequest(http.MethodGet, "/.well-known/metadata", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			var metadata map[string]any
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&metadata))
			assert.NotContains(t, metadata, "authorization_endpoint")
			assert.NotContains(t, metadata, "response_types_supported")
			assert.NotContains(t, metadata, "code_challenge_methods_supported")
			assert.NotContains(t, metadata, "subject_types_supported")
			assert.NotContains(t, metadata, "id_token_signing_alg_values_supported")
			assert.NotContains(t, metadata["grant_types_supported"], oauthproto.GrantTypeAuthorizationCode)
			assert.NotContains(t, metadata["grant_types_supported"], oauthproto.GrantTypeRefreshToken)
			assert.Contains(t, metadata["grant_types_supported"], oauthproto.GrantTypeTokenExchange)
		})
	}

	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"code"},
		"state":         {"test-state"},
	}
	rec := httptest.NewRecorder()
	handler.AuthorizeHandler(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "unsupported_response_type", location.Query().Get("error"))
	assert.Equal(t, "test-state", location.Query().Get("state"))
}
