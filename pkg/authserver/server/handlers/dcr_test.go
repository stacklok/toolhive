// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestRegisterClientHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		requestBody     any
		storageErr      error
		expectedStatus  int
		expectedError   string // DCR error code; empty means expect success
		expectedErrDesc string // substring match on error_description
	}{
		{
			name: "success",
			requestBody: oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
				ClientName:   "Test Client",
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name:           "invalid JSON body",
			requestBody:    "not-valid-json",
			expectedStatus: http.StatusBadRequest,
			expectedError:  registration.DCRErrorInvalidClientMetadata,
		},
		{
			name: "validation error propagated",
			requestBody: oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://example.com/callback"},
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  registration.DCRErrorInvalidRedirectURI,
		},
		{
			name: "storage failure returns 500",
			requestBody: oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
			},
			storageErr:      errors.New("disk full"),
			expectedStatus:  http.StatusInternalServerError,
			expectedError:   "server_error",
			expectedErrDesc: "failed to register client",
		},
		{
			name:           "oversized body rejected",
			requestBody:    strings.Repeat("x", 65*1024), // 65KB exceeds 64KB limit
			expectedStatus: http.StatusBadRequest,
			expectedError:  registration.DCRErrorInvalidClientMetadata,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			stor := mocks.NewMockStorage(ctrl)
			stor.EXPECT().RegisterClient(gomock.Any(), gomock.Any()).Return(tc.storageErr).AnyTimes()
			cfg := &server.AuthorizationServerConfig{
				Config:          &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
				ScopesSupported: registration.DefaultScopes,
			}
			handler := &Handler{storage: stor, config: cfg}

			var body []byte
			if s, ok := tc.requestBody.(string); ok {
				body = []byte(s)
			} else {
				body, _ = json.Marshal(tc.requestBody)
			}

			req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			handler.RegisterClientHandler(w, req)

			assert.Equal(t, tc.expectedStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			if tc.expectedError != "" {
				var errResp registration.DCRError
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
				assert.Equal(t, tc.expectedError, errResp.Error)
				if tc.expectedErrDesc != "" {
					assert.Contains(t, errResp.ErrorDescription, tc.expectedErrDesc)
				}
			} else {
				var resp oauthproto.DynamicClientRegistrationResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp.ClientID)
				assert.NotZero(t, resp.ClientIDIssuedAt)
				assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				assert.Equal(t, "no-cache", w.Header().Get("Pragma"))
			}
		})
	}
}

func TestRegisterClientHandler_ScopeInResponse(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	stor := mocks.NewMockStorage(ctrl)
	stor.EXPECT().RegisterClient(gomock.Any(), gomock.Any()).Return(nil)

	handler := &Handler{
		storage: stor,
		config: &server.AuthorizationServerConfig{
			Config:          &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
			ScopesSupported: registration.DefaultScopes,
		},
	}

	reqBody, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.RegisterClientHandler(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp oauthproto.DynamicClientRegistrationResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, []string(registration.DefaultScopes), []string(resp.Scopes),
		"DCR response should include granted scopes per RFC 7591 Section 3.2.1")
}

func TestRegisterClientHandler_BaselineClientScopes(t *testing.T) {
	t.Parallel()

	// DefaultScopes is ["openid","profile","email","offline_access"].
	// Tests build ScopesSupported from DefaultScopes plus any extra scopes
	// required by the case.

	tests := []struct {
		name                 string
		requestScope         string
		baselineClientScopes []string
		extraScopesSupported []string // appended to DefaultScopes in ScopesSupported
		expectedScopes       []string
	}{
		{
			// When scope is empty, ValidateScopes returns DefaultScopes.
			// The baseline adds "custom:scope" (not in DefaultScopes), so the
			// union expands the set: DefaultScopes + ["custom:scope"].
			name:                 "empty client scope with non-empty baseline adds baseline scope",
			requestScope:         "",
			baselineClientScopes: []string{"custom:scope"},
			extraScopesSupported: []string{"custom:scope"},
			expectedScopes:       append(append([]string{}, registration.DefaultScopes...), "custom:scope"),
		},
		{
			// Requested scopes already contain the baseline; no expansion occurs.
			name:                 "baseline is subset of requested scopes no expansion",
			requestScope:         "openid profile email offline_access",
			baselineClientScopes: []string{"openid"},
			extraScopesSupported: nil,
			expectedScopes:       []string{"openid", "profile", "email", "offline_access"},
		},
		{
			// Partial overlap: baseline shares "openid" with the request but adds
			// "offline_access" not in the request. Exercises the dedup+append paths
			// of unionScopes in the same handler call.
			name:                 "partial overlap baseline appends only non-overlapping scopes",
			requestScope:         "openid profile",
			baselineClientScopes: []string{"openid", "offline_access"},
			extraScopesSupported: nil,
			expectedScopes:       []string{"openid", "profile", "offline_access"},
		},
		{
			// Canonical regression: client registers with "openid" only,
			// baseline adds "offline_access" → union is both.
			name:                 "disjoint baseline expands registered scope set",
			requestScope:         "openid",
			baselineClientScopes: []string{"offline_access"},
			extraScopesSupported: nil,
			expectedScopes:       []string{"openid", "offline_access"},
		},
		{
			// Nil baseline must not alter the registered scope set.
			name:                 "nil baseline preserves existing behavior",
			requestScope:         "openid",
			baselineClientScopes: nil,
			extraScopesSupported: nil,
			expectedScopes:       []string{"openid"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			stor := mocks.NewMockStorage(ctrl)
			var capturedClient fosite.Client
			stor.EXPECT().RegisterClient(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, c fosite.Client) error {
					capturedClient = c
					return nil
				})

			// Defensive copy of DefaultScopes (a package-level var) before extending,
			// so per-case extraScopesSupported never mutates the global.
			scopesSupported := append(append([]string{}, registration.DefaultScopes...), tc.extraScopesSupported...)
			cfg := &server.AuthorizationServerConfig{
				Config:               &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
				ScopesSupported:      scopesSupported,
				BaselineClientScopes: tc.baselineClientScopes,
			}
			handler := &Handler{storage: stor, config: cfg}

			reqBody, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
				Scopes:       oauthproto.ScopeList(strings.Fields(tc.requestScope)),
			})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			handler.RegisterClientHandler(w, req)

			require.Equal(t, http.StatusCreated, w.Code)

			var resp oauthproto.DynamicClientRegistrationResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tc.expectedScopes, []string(resp.Scopes),
				"DCR response scope must equal the union of requested and baseline scopes")

			require.NotNil(t, capturedClient, "storage was not called")
			assert.Equal(t, fosite.Arguments(tc.expectedScopes), capturedClient.GetScopes(),
				"the union of requested and baseline scopes must reach storage")
		})
	}
}

func TestRegisterClientHandler_ClientIsStored(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	stor := mocks.NewMockStorage(ctrl)
	var storedClient fosite.Client
	stor.EXPECT().RegisterClient(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, client fosite.Client) error {
			storedClient = client
			return nil
		})

	allowedAudiences := []string{"https://mcp.example.com"}
	cfg := &server.AuthorizationServerConfig{
		Config:           &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
		ScopesSupported:  registration.DefaultScopes,
		AllowedAudiences: allowedAudiences,
	}
	handler := &Handler{storage: stor, config: cfg}

	reqBody, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
		ClientName:   "Stored Client",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.RegisterClientHandler(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp oauthproto.DynamicClientRegistrationResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.NotNil(t, storedClient)
	loopbackClient, ok := storedClient.(*registration.LoopbackClient)
	require.True(t, ok, "expected *registration.LoopbackClient, got %T", storedClient)

	assert.Equal(t, resp.ClientID, loopbackClient.GetID())
	assert.True(t, loopbackClient.IsPublic())
	assert.Equal(t, []string{"http://127.0.0.1:8080/callback"}, loopbackClient.GetRedirectURIs())
	assert.Equal(t, fosite.Arguments(allowedAudiences), loopbackClient.GetAudience(),
		"DCR client must inherit server's AllowedAudiences so refresh token requests with resource= succeed")
}

// TestRegisterClientHandler_ScopeAsJSONArray verifies that the /oauth/register
// endpoint accepts the RFC 7591 array form of "scope". Prior to consolidating
// onto oauthproto.ScopeList, the handler only accepted space-delimited strings
// and would reject this body as a JSON decode error.
func TestRegisterClientHandler_ScopeAsJSONArray(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	stor := mocks.NewMockStorage(ctrl)
	var capturedClient fosite.Client
	stor.EXPECT().RegisterClient(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, c fosite.Client) error {
			capturedClient = c
			return nil
		})

	handler := &Handler{
		storage: stor,
		config: &server.AuthorizationServerConfig{
			Config:          &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
			ScopesSupported: registration.DefaultScopes,
		},
	}

	// Raw JSON body with scope as a JSON array — the form that
	// well-formed RFC 7591 clients can send and that the previous
	// space-delimited-only authserver implementation rejected.
	body := []byte(`{"redirect_uris":["http://127.0.0.1:8080/callback"],"scope":["openid","offline_access"]}`)
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.RegisterClientHandler(w, req)

	require.Equal(t, http.StatusCreated, w.Code,
		"array-form scope must be accepted per RFC 7591 §2 ambiguity")

	var resp oauthproto.DynamicClientRegistrationResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, []string{"openid", "offline_access"}, []string(resp.Scopes),
		"granted scopes must reflect the array-form request")

	require.NotNil(t, capturedClient, "storage was not called")
	assert.Equal(t, fosite.Arguments([]string{"openid", "offline_access"}), capturedClient.GetScopes(),
		"the array-form scopes must reach storage")
}
