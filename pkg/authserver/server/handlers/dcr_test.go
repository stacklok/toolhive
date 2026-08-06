// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	// DCR now stores the package's DCR-issued public client shape
	// (*registration.publicClient), which embeds the LoopbackClient behaviour.
	// Assert on the public surface rather than the unexported concrete type.
	assert.Equal(t, resp.ClientID, storedClient.GetID())
	assert.True(t, storedClient.IsPublic())
	assert.Equal(t, []string{"http://127.0.0.1:8080/callback"}, storedClient.GetRedirectURIs())
	assert.Equal(t, fosite.Arguments(allowedAudiences), storedClient.GetAudience(),
		"DCR client must inherit server's AllowedAudiences so refresh token requests with resource= succeed")

	// The stored client must carry the OIDC shape so the "none" auth method is
	// recorded and enforced at the token endpoint.
	oidc, ok := storedClient.(fosite.OpenIDConnectClient)
	require.True(t, ok, "stored DCR client must satisfy fosite.OpenIDConnectClient")
	assert.Equal(t, "none", oidc.GetTokenEndpointAuthMethod())
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

// confidentialClientSecretRegex matches the 43-char base64url (RawURLEncoding)
// secret produced by registration.GenerateClientSecret.
var confidentialClientSecretRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// confidentialConfig builds an AuthorizationServerConfig with
// AllowConfidentialClients set, matching the test pattern used elsewhere in
// this file.
func confidentialConfig(allowConfidential bool) *server.AuthorizationServerConfig {
	return &server.AuthorizationServerConfig{
		Config:                   &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
		ScopesSupported:          registration.DefaultScopes,
		AllowConfidentialClients: allowConfidential,
	}
}

// runDCR fires a single DCR request at a fresh handler with the given config
// and body, returning the recorded response. Storage is set up to capture the
// fosite.Client handed to RegisterClient (or nil when RegisterClient is not
// expected to be called).
func runDCR(t *testing.T, cfg *server.AuthorizationServerConfig, body string) (
	w *httptest.ResponseRecorder, capturedClient fosite.Client,
) {
	t.Helper()
	ctrl := gomock.NewController(t)
	stor := mocks.NewMockStorage(ctrl)
	stor.EXPECT().RegisterClient(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, c fosite.Client) error {
			capturedClient = c
			return nil
		}).AnyTimes()
	handler := &Handler{storage: stor, config: cfg}

	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.RegisterClientHandler(w, req)
	return w, capturedClient
}

// TestRegisterClientHandler_ConfidentialDCR covers the confidential-client DCR
// acceptance criteria: flag-gating of client_secret_basic/post, the
// server-minted secret and its expiry in the raw response body, uniqueness,
// suppression of an attacker-supplied client_secret, redirect-URI restrictions
// for confidential clients, and rejection of unsupported auth methods.
func TestRegisterClientHandler_ConfidentialDCR(t *testing.T) {
	t.Parallel()

	t.Run("flag off rejects client_secret_basic with exact error", func(t *testing.T) {
		t.Parallel()
		w, _ := runDCR(t, confidentialConfig(false),
			`{"redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)

		require.Equal(t, http.StatusBadRequest, w.Code)
		var errResp registration.DCRError
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
		assert.Equal(t, registration.DCRErrorInvalidClientMetadata, errResp.Error)
		// The description names the server's limitation, not the client's
		// posture: the client did not declare itself public.
		assert.Equal(t, "this authorization server only supports token_endpoint_auth_method 'none'", errResp.ErrorDescription)
		// Raw body must not carry a client_secret key.
		var raw map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
		_, hasSecret := raw["client_secret"]
		assert.False(t, hasSecret, "rejected registration must not return a client_secret")
	})

	t.Run("flag on method omitted stays public", func(t *testing.T) {
		t.Parallel()
		w, _ := runDCR(t, confidentialConfig(true),
			`{"redirect_uris":["http://127.0.0.1:8080/callback"]}`)

		require.Equal(t, http.StatusCreated, w.Code)
		// Decode to a raw map so omitempty cannot mask a regression: a public
		// registration must emit neither client_secret nor
		// client_secret_expires_at.
		var raw map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
		_, hasSecret := raw["client_secret"]
		assert.False(t, hasSecret, "public registration must not return client_secret")
		_, hasExp := raw["client_secret_expires_at"]
		assert.False(t, hasExp, "public registration must not return client_secret_expires_at")
		assert.Equal(t, oauthproto.TokenEndpointAuthMethodNone, raw["token_endpoint_auth_method"])
	})

	t.Run("flag on client_secret_basic mints secret and raw expiry", func(t *testing.T) {
		t.Parallel()
		w, _ := runDCR(t, confidentialConfig(true),
			`{"redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)

		require.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

		// Assert on the RAW JSON bytes, not the unmarshalled struct, so an
		// omitempty drop on client_secret_expires_at is caught.
		var raw map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))

		secret, ok := raw["client_secret"].(string)
		require.True(t, ok, "client_secret must be a present string")
		assert.Regexp(t, confidentialClientSecretRegex, secret,
			"client_secret must be 43-char base64url (RawURLEncoding)")

		issuedAt, ok := raw["client_id_issued_at"].(float64)
		require.True(t, ok, "client_id_issued_at must be present")
		assert.NotZero(t, issuedAt)
		expiresAt, ok := raw["client_secret_expires_at"].(float64)
		require.True(t, ok, "client_secret_expires_at must be present in raw body")
		assert.Equal(t, float64(0), expiresAt,
			"client_secret_expires_at must be 0 (does not expire): RenewClientTTL keeps an actively used registration alive indefinitely, so advertising a real expiry would be false and would make ToolHive's own DCR client re-register against itself")
	})

	t.Run("two consecutive registrations yield different secrets", func(t *testing.T) {
		t.Parallel()
		cfg := confidentialConfig(true)
		w1, _ := runDCR(t, cfg,
			`{"redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)
		require.Equal(t, http.StatusCreated, w1.Code)
		var r1 map[string]any
		require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &r1))

		w2, _ := runDCR(t, cfg,
			`{"redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)
		require.Equal(t, http.StatusCreated, w2.Code)
		var r2 map[string]any
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &r2))

		s1, _ := r1["client_secret"].(string)
		s2, _ := r2["client_secret"].(string)
		require.NotEmpty(t, s1)
		require.NotEmpty(t, s2)
		assert.NotEqual(t, s1, s2, "server-generated secrets must be unique per registration")
	})

	t.Run("attacker-supplied client_secret is ignored", func(t *testing.T) {
		t.Parallel()
		w, _ := runDCR(t, confidentialConfig(true),
			`{"redirect_uris":["https://app.example/cb"],`+
				`"token_endpoint_auth_method":"client_secret_basic","client_secret":"attacker-chosen"}`)

		require.Equal(t, http.StatusCreated, w.Code)
		var resp oauthproto.DynamicClientRegistrationResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotEqual(t, "attacker-chosen", resp.ClientSecret,
			"a client_secret in the request must be ignored; the server always mints its own")
		assert.Regexp(t, confidentialClientSecretRegex, resp.ClientSecret)
	})

	// Redirect URIs that identify a public client (loopback or private scheme)
	// must be rejected for confidential registrations but still accepted for
	// public (none) registrations — a secret must not ship inside a distributed
	// native binary. Loopback URIs pass strict redirect-URI validation, so the
	// rejection names the real field (invalid_client_metadata on
	// token_endpoint_auth_method) to keep loopback clients self-correcting on a
	// single retry; a private scheme fails strict validation first and stays
	// invalid_redirect_uri.
	for _, tc := range []struct {
		name      string
		uris      []string
		wantError string
	}{
		{"loopback localhost", []string{"http://localhost:1234/cb"}, registration.DCRErrorInvalidClientMetadata},
		{"loopback 127.0.0.1", []string{"http://127.0.0.1/cb"}, registration.DCRErrorInvalidClientMetadata},
		// Passes RedirectURIPolicyStrict and is caught only by the isLoopbackURI
		// check (bracket-stripping + IsLoopback, three indirections deep).
		{"IPv6 loopback", []string{"https://[::1]/cb"}, registration.DCRErrorInvalidClientMetadata},
		// Every URI in the list must be checked, not just redirectURIs[0].
		{"mixed list with loopback second", []string{"https://app.example/cb", "http://127.0.0.1/cb"},
			registration.DCRErrorInvalidClientMetadata},
		// Plaintext non-loopback exercises the ValidateRedirectURI strict branch
		// on a non-private-scheme input.
		{"plaintext non-loopback", []string{"http://app.example/cb"}, registration.DCRErrorInvalidRedirectURI},
		// A private scheme fails strict validation first and stays
		// invalid_redirect_uri.
		{"private scheme", []string{"cursor://cb"}, registration.DCRErrorInvalidRedirectURI},
	} {
		tc := tc
		t.Run("confidential rejects "+tc.name, func(t *testing.T) {
			t.Parallel()
			urisJSON, err := json.Marshal(tc.uris)
			require.NoError(t, err)
			body := `{"redirect_uris":` + string(urisJSON) + `,"token_endpoint_auth_method":"client_secret_basic"}`
			w, _ := runDCR(t, confidentialConfig(true), body)
			require.Equal(t, http.StatusBadRequest, w.Code)
			var errResp registration.DCRError
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
			assert.Equal(t, tc.wantError, errResp.Error)
			if tc.wantError == registration.DCRErrorInvalidClientMetadata {
				assert.Contains(t, errResp.ErrorDescription, "token_endpoint_auth_method",
					"the error must name the field the client can fix")
				assert.Contains(t, errResp.ErrorDescription, "'none'",
					"the error must point loopback clients at the public method")
			}
		})
	}

	// The same loopback/private URIs are accepted for public (none) registrations.
	for _, uri := range []string{
		"http://localhost:1234/cb",
		"http://127.0.0.1/cb",
		"cursor://cb",
	} {
		uri := uri
		t.Run("public accepts the same loopback/private URI: "+uri, func(t *testing.T) {
			t.Parallel()
			body := `{"redirect_uris":["` + uri + `"],"token_endpoint_auth_method":"none"}`
			w, _ := runDCR(t, confidentialConfig(true), body)
			require.Equal(t, http.StatusCreated, w.Code)
		})
	}

	// Auth methods outside the accepted set (none / client_secret_basic /
	// client_secret_post) are rejected regardless of the flag.
	rejectedMethods := []string{
		"client_secret_jwt",
		"private_key_jwt",
		"tls_client_auth",
		"not-a-real-method",
	}
	for _, method := range rejectedMethods {
		method := method
		for _, flag := range []bool{true, false} {
			flag := flag
			t.Run(fmt.Sprintf("rejected method %s flag=%v", method, flag), func(t *testing.T) {
				t.Parallel()
				body := `{"redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"` + method + `"}`
				w, _ := runDCR(t, confidentialConfig(flag), body)
				require.Equal(t, http.StatusBadRequest, w.Code)
				var errResp registration.DCRError
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
				assert.Equal(t, registration.DCRErrorInvalidClientMetadata, errResp.Error)
			})
		}
	}
}

// TestRegisterClientHandler_ConfidentialClientStored covers the storage-layer
// shape of a confidential registration: it must be a non-loopback OIDC client
// carrying the registered auth method and the server's AllowedAudiences.
func TestRegisterClientHandler_ConfidentialClientStored(t *testing.T) {
	t.Parallel()

	t.Run("stored without LoopbackClient wrapper and carries auth method", func(t *testing.T) {
		t.Parallel()
		cfg := confidentialConfig(true)
		w, captured := runDCR(t, cfg,
			`{"redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)
		require.Equal(t, http.StatusCreated, w.Code)

		require.NotNil(t, captured, "storage.RegisterClient must be called")
		oidc, ok := captured.(fosite.OpenIDConnectClient)
		require.True(t, ok, "confidential client must satisfy fosite.OpenIDConnectClient")
		assert.Equal(t, oauthproto.TokenEndpointAuthMethodClientSecretBasic, oidc.GetTokenEndpointAuthMethod())
		assert.False(t, captured.IsPublic(), "confidential client must not be public")
		assert.Equal(t, []string{"https://app.example/cb"}, captured.GetRedirectURIs())

		// A secret-holding client must NOT get the LoopbackClient dynamic-port
		// matching wrapper.
		_, isLoopback := captured.(*registration.LoopbackClient)
		assert.False(t, isLoopback,
			"confidential client must not be a *registration.LoopbackClient")
	})

	t.Run("audience is preserved on stored confidential client", func(t *testing.T) {
		t.Parallel()
		allowedAudiences := []string{"https://mcp.example.com", "https://api.example.com"}
		cfg := &server.AuthorizationServerConfig{
			Config:                   &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
			ScopesSupported:          registration.DefaultScopes,
			AllowedAudiences:         allowedAudiences,
			AllowConfidentialClients: true,
		}
		w, captured := runDCR(t, cfg,
			`{"redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)
		require.Equal(t, http.StatusCreated, w.Code)

		require.NotNil(t, captured, "storage.RegisterClient must be called")
		assert.Equal(t, fosite.Arguments(allowedAudiences), captured.GetAudience(),
			"confidential client must inherit server AllowedAudiences")
	})
}
