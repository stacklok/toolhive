// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	authorizationDetailsClientID     = "authorization-details-client"
	authorizationDetailsClientSecret = "authorization-details-secret"
	authorizationDetailsSubject      = "authorization-details-subject"
)

const richAuthorizationDetails = `[{"type":"custom","amount":1234567890123456789012345678901234567890,"nested":{"value":null,"array":[1,2,3]}},{"type":"custom","nested":{"value":{"deep":[null,{"keep":"order"}]}}},{"type":"custom","amount":1e+100}]`

// accessTokenFormRecorder keeps the request form of every stored access-token
// session. It is the only way to observe, through the real server, what the
// token endpoint persists.
type accessTokenFormRecorder struct {
	storage.Storage
	storage.DCRCredentialStore
	forms []url.Values
}

func (s *accessTokenFormRecorder) CreateAccessTokenSession(
	ctx context.Context, signature string, request fosite.Requester,
) error {
	if err := s.Storage.CreateAccessTokenSession(ctx, signature, request); err != nil {
		return err
	}
	stored, err := s.GetAccessTokenSession(ctx, signature, nil)
	if err != nil {
		return err
	}
	s.forms = append(s.forms, stored.GetRequestForm().Clone())
	return nil
}

// newAuthorizationDetailsRegistry registers a "custom" type whose hook counts
// its invocations, fails with an untyped error for an entry with "deny":true
// and with a typed error for an entry with "reject":true.
func newAuthorizationDetailsRegistry(t *testing.T, calls *atomic.Int32) *authorizationdetails.Registry {
	t.Helper()
	registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		"custom": {
			GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
			Grant: func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
				calls.Add(1)
				for _, entry := range req.Entries {
					var object map[string]json.RawMessage
					if err := json.Unmarshal(entry, &object); err != nil {
						return authorizationdetails.Result{}, err
					}
					switch {
					case string(object["deny"]) == "true":
						return authorizationdetails.Result{}, fmt.Errorf("private validator detail that must not escape")
					case string(object["reject"]) == "true":
						return authorizationdetails.Result{}, authorizationdetails.Error{
							Code: authorizationdetails.CodeInvalidRequest, Reason: "rejected_by_policy",
						}
					}
				}
				return authorizationdetails.Result{Entries: req.Entries}, nil
			},
		},
	})
	require.NoError(t, err)
	return registry
}

// TestIntegration_AuthorizationDetailsOverHTTP covers what only the real HTTP
// server shows: form parsing, client authentication ordering, the serialized
// response member and what reaches the JWT and the store. Parsing and hook
// semantics are covered by the unit tests in the authorizationdetails and
// tokenexchange packages.
//
//nolint:paralleltest,tparallel // The subtests share the recorder and call counter, so they run in order.
func TestIntegration_AuthorizationDetailsOverHTTP(t *testing.T) {
	t.Parallel()

	client, err := registration.New(registration.Config{
		ID:                      authorizationDetailsClientID,
		Secret:                  authorizationDetailsClientSecret,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretPost,
		GrantTypes:              []string{oauthproto.GrantTypeTokenExchange},
		Scopes:                  registration.DefaultScopes,
		Audience:                []string{testAudience},
	})
	require.NoError(t, err)

	var calls atomic.Int32
	recorder := &accessTokenFormRecorder{}
	ts := setupTestServer(t,
		withExtraClient(client),
		withAuthorizationDetailsRegistry(newAuthorizationDetailsRegistry(t, &calls)),
		withStorageDecorator(func(stor storage.Storage) storage.Storage {
			recorder.Storage = stor
			dcrStore, ok := stor.(storage.DCRCredentialStore)
			require.True(t, ok)
			recorder.DCRCredentialStore = dcrStore
			return recorder
		}),
	)

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: ts.PrivateKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	require.NoError(t, err)
	now := time.Now()
	subjectToken, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: testIssuer, Subject: authorizationDetailsSubject,
		Audience: jwt.Audience{testAudience}, Expiry: jwt.NewNumericDate(now.Add(30 * time.Minute)),
		IssuedAt: jwt.NewNumericDate(now),
	}).Claims(map[string]any{"client_id": authorizationDetailsClientID}).Serialize()
	require.NoError(t, err)

	tests := []struct {
		name            string
		details         []string
		mutate          func(url.Values)
		wantStatus      int
		wantError       string
		wantDescription string
		wantCalls       int32
		wantEcho        string
		wantNoEcho      bool
	}{
		{name: "rich array is echoed as a JSON array and kept out of the JWT", details: []string{richAuthorizationDetails}, wantStatus: http.StatusOK, wantCalls: 1, wantEcho: richAuthorizationDetails},
		{name: "empty array is echoed", details: []string{"[]"}, wantStatus: http.StatusOK, wantEcho: "[]"},
		{name: "absent parameter is not echoed", wantStatus: http.StatusOK, wantNoEcho: true},
		{name: "empty parameter is not echoed", details: []string{""}, wantStatus: http.StatusOK, wantNoEcho: true},
		{name: "repeated form values are rejected before the hook", details: []string{`[{"type":"custom"}]`, `[{"type":"custom"}]`}, wantStatus: http.StatusBadRequest, wantError: "invalid_request", wantDescription: "The authorization_details parameter must not be repeated."},
		{name: "typed hook error keeps its code and reason", details: []string{`[{"type":"custom","reject":true}]`}, wantStatus: http.StatusBadRequest, wantError: "invalid_request", wantDescription: "rejected_by_policy", wantCalls: 1},
		{name: "untyped hook error is a server_error without its text", details: []string{`[{"type":"custom","deny":true}]`}, wantStatus: http.StatusInternalServerError, wantError: "server_error", wantDescription: "The authorization_details could not be processed.", wantCalls: 1},
		{
			name:    "refresh-token output is refused before the hook runs",
			details: []string{`[{"type":"custom"}]`},
			mutate: func(form url.Values) {
				form.Set("requested_token_type", "urn:ietf:params:oauth:token-type:refresh_token")
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_request",
		},
		{
			name:       "client authentication runs before the hook",
			details:    []string{`[{"type":"custom"}]`},
			mutate:     func(form url.Values) { form.Set("client_id", "unregistered-client") },
			wantStatus: http.StatusUnauthorized,
			wantError:  "invalid_client",
		},
	}

	// The subtests share the recorder and the call counter, so they run in order.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := calls.Load()
			form := url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"subject_token":      {subjectToken},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
				"client_id":          {authorizationDetailsClientID},
				"client_secret":      {authorizationDetailsClientSecret},
			}
			if tt.details != nil {
				form["authorization_details"] = tt.details
			}
			if tt.mutate != nil {
				tt.mutate(form)
			}

			response := makeTokenRequest(t, ts.Server.URL, form)
			t.Cleanup(func() { _ = response.Body.Close() })
			rawBody, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rawBody, &body), string(rawBody))

			assert.Equal(t, tt.wantCalls, calls.Load()-before)
			assert.Equal(t, tt.wantStatus, response.StatusCode, string(rawBody))
			if tt.wantError != "" {
				assert.Equal(t, fmt.Sprintf("%q", tt.wantError), string(body["error"]))
				if tt.wantDescription != "" {
					assert.Equal(t, fmt.Sprintf("%q", tt.wantDescription), string(body["error_description"]))
				}
				assert.NotContains(t, string(rawBody), "private validator detail")
				assert.Empty(t, body["access_token"])
				return
			}

			assert.NotEmpty(t, body["access_token"])
			assert.Empty(t, body["refresh_token"], "token exchange must not issue a refresh token")
			if tt.wantNoEcho {
				assert.Empty(t, body["authorization_details"])
			}
			if tt.wantEcho == "" {
				return
			}
			assert.Equal(t, tt.wantEcho, string(body["authorization_details"]))

			var accessToken string
			require.NoError(t, json.Unmarshal(body["access_token"], &accessToken))
			parsed, err := jwt.ParseSigned(accessToken, []jose.SignatureAlgorithm{jose.RS256})
			require.NoError(t, err)
			var claims map[string]any
			require.NoError(t, parsed.Claims(ts.PrivateKey.Public(), &claims))
			assert.NotContains(t, claims, "authorization_details", "details belong in the response, not the access-token JWT")
		})
	}

	require.NotEmpty(t, recorder.forms, "successful token exchange should persist an access-token record")
	for _, form := range recorder.forms {
		assert.NotContains(t, form, "authorization_details", "authorization_details must not be persisted with access tokens")
	}
}

func TestIntegration_AuthorizationDetailsIgnoredOnOtherGrants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		form func(ts *testServerWithUpstream, signAssertion func(string, time.Time, time.Time, string) string) url.Values
	}{
		{
			name: "jwt-bearer",
			form: func(_ *testServerWithUpstream, signAssertion func(string, time.Time, time.Time, string) string) url.Values {
				now := time.Now()
				return url.Values{
					"grant_type": {oauthproto.GrantTypeJWTBearer},
					"assertion":  {signAssertion("external-subject", now, now.Add(2*time.Minute), "authorization-details-jwt-bearer")},
					"resource":   {testAudience},
				}
			},
		},
		{
			name: "ID-JAG",
			form: func(ts *testServerWithUpstream, _ func(string, time.Time, time.Time, string) string) url.Values {
				now := time.Now()
				return url.Values{
					"grant_type": {oauthproto.GrantTypeJWTBearer},
					"assertion":  {ts.signIDJAGAssertion("external-subject", now, now.Add(2*time.Minute), "authorization-details-id-jag")},
					"client_id":  {testClientID},
					"resource":   {testAudience},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			ts, signAssertion := setupJWTBearerGrantTestServer(t,
				withAuthorizationDetailsRegistry(newAuthorizationDetailsRegistry(t, &calls)))
			form := tt.form(ts, signAssertion)
			form.Set("authorization_details", `[{"type":"unknown"}]`)

			response := makeTokenRequest(t, ts.Server.URL, form)
			t.Cleanup(func() { _ = response.Body.Close() })
			body := parseTokenResponse(t, response)

			require.Equal(t, http.StatusOK, response.StatusCode, body)
			assert.NotEmpty(t, body["access_token"])
			assert.Zero(t, calls.Load(), "the registry must remain specific to RFC 8693 token exchange")
		})
	}
}
