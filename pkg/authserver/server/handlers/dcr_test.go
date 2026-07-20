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
		requestScopes        []string
		baselineClientScopes []string
		extraScopesSupported []string // appended to DefaultScopes in ScopesSupported
		expectedScopes       []string
	}{
		{
			// When scope is empty, ValidateScopes returns DefaultScopes.
			// The baseline adds "custom:scope" (not in DefaultScopes), so the
			// union expands the set: DefaultScopes + ["custom:scope"].
			name:                 "empty client scope with non-empty baseline adds baseline scope",
			requestScopes:        nil,
			baselineClientScopes: []string{"custom:scope"},
			extraScopesSupported: []string{"custom:scope"},
			expectedScopes:       append(append([]string{}, registration.DefaultScopes...), "custom:scope"),
		},
		{
			// Requested scopes already contain the baseline; no expansion occurs.
			name:                 "baseline is subset of requested scopes no expansion",
			requestScopes:        []string{"openid", "profile", "email", "offline_access"},
			baselineClientScopes: []string{"openid"},
			extraScopesSupported: nil,
			expectedScopes:       []string{"openid", "profile", "email", "offline_access"},
		},
		{
			// Partial overlap: baseline shares "openid" with the request but adds
			// "offline_access" not in the request. Exercises the dedup+append paths
			// of unionScopes in the same handler call.
			name:                 "partial overlap baseline appends only non-overlapping scopes",
			requestScopes:        []string{"openid", "profile"},
			baselineClientScopes: []string{"openid", "offline_access"},
			extraScopesSupported: nil,
			expectedScopes:       []string{"openid", "profile", "offline_access"},
		},
		{
			// Canonical regression: client registers with "openid" only,
			// baseline adds "offline_access" → union is both.
			name:                 "disjoint baseline expands registered scope set",
			requestScopes:        []string{"openid"},
			baselineClientScopes: []string{"offline_access"},
			extraScopesSupported: nil,
			expectedScopes:       []string{"openid", "offline_access"},
		},
		{
			// Nil baseline must not alter the registered scope set.
			name:                 "nil baseline preserves existing behavior",
			requestScopes:        []string{"openid"},
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
				Scopes:       oauthproto.ScopeList(tc.requestScopes),
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
// and would reject these bodies as a JSON decode error.
//
// Each case sends a raw JSON body (not a Go literal that round-trips through
// ScopeList.MarshalJSON) so the dual-format UnmarshalJSON path is exercised
// end-to-end at the handler boundary.
func TestRegisterClientHandler_ScopeAsJSONArray(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		body             string
		expectedStatus   int
		expectedScopes   []string // ignored when expectedStatus != 201
		expectedWireFrag string   // ignored when expectedStatus != 201
		expectedError    string   // DCR error code when expectedStatus != 201
	}{
		{
			name:             "happy path: array form is accepted and granted",
			body:             `{"redirect_uris":["http://127.0.0.1:8080/callback"],"scope":["openid","offline_access"]}`,
			expectedStatus:   http.StatusCreated,
			expectedScopes:   []string{"openid", "offline_access"},
			expectedWireFrag: `"scope":"openid offline_access"`,
		},
		{
			// Mirrors the string-form unsupported-scope path from
			// TestValidateScopes ("unknown scope rejected") at the handler
			// boundary, but routed through the array-form decoder.
			name:           "array form with unsupported scope is rejected",
			body:           `{"redirect_uris":["http://127.0.0.1:8080/callback"],"scope":["openid","sneaky_admin"]}`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  registration.DCRErrorInvalidClientMetadata,
		},
		{
			// Empty-array case. ScopeList.UnmarshalJSON normalizes [] to
			// nil, ValidateScopes then falls back to DefaultScopes.
			// Documented intentional behavior change: pre-consolidation the
			// authserver returned 400 for this body because the
			// space-delimited-only decoder could not consume a JSON array.
			name:             "empty array falls back to DefaultScopes",
			body:             `{"redirect_uris":["http://127.0.0.1:8080/callback"],"scope":[]}`,
			expectedStatus:   http.StatusCreated,
			expectedScopes:   registration.DefaultScopes,
			expectedWireFrag: `"scope":"openid profile email offline_access"`,
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
				}).AnyTimes()

			handler := &Handler{
				storage: stor,
				config: &server.AuthorizationServerConfig{
					Config:          &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
					ScopesSupported: registration.DefaultScopes,
				},
			}

			req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			handler.RegisterClientHandler(w, req)

			require.Equal(t, tc.expectedStatus, w.Code)

			if tc.expectedStatus != http.StatusCreated {
				var errResp registration.DCRError
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
				assert.Equal(t, tc.expectedError, errResp.Error)
				return
			}

			// Pin the response wire form: RFC 7591 §3.2.1 requires a
			// space-delimited scope string, not a JSON array. A raw-body
			// assertion catches a regression that the dual-format
			// ScopeList.UnmarshalJSON would otherwise mask when decoding
			// resp.Scopes back into a slice.
			assert.Contains(t, w.Body.String(), tc.expectedWireFrag,
				"RFC 7591 §3.2.1: response scope must be space-delimited string, not JSON array")

			var resp oauthproto.DynamicClientRegistrationResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tc.expectedScopes, []string(resp.Scopes),
				"granted scopes must reflect the array-form request")

			require.NotNil(t, capturedClient, "storage was not called")
			assert.Equal(t, fosite.Arguments(tc.expectedScopes), capturedClient.GetScopes(),
				"the array-form scopes must reach storage")
		})
	}
}
