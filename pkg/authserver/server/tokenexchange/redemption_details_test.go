// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	testDetailType         = "https://example.com/test_detail"
	testDetailEntry        = `{"type":"` + testDetailType + `","note":"first"}`
	testOtherDetailEntry   = `{"type":"` + testDetailType + `","note":"second"}`
	testMalformedEntry     = `{"type":"` + testDetailType + `"}`
	testExchangeGrantEntry = `{"type":"` + exchangeGrantType + `","issuable_token_types":["` + oauthproto.TokenTypeIDJAG + `"]}`
)

// newCallDetailsRegistry registers a made-up type for the JWT-bearer grant
// whose hook accepts exactly one entry carrying a non-empty "note" member. When
// seen is not nil, the hook stores the request it received there.
func newCallDetailsRegistry(t *testing.T, seen ...*authorizationdetails.Request) *authorizationdetails.Registry {
	t.Helper()
	refuse := func(reason string) error {
		return authorizationdetails.Error{Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: reason}
	}
	registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		testDetailType: {
			GrantTypes: []string{oauthproto.GrantTypeJWTBearer},
			Grant: func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
				if len(seen) > 0 {
					*seen[0] = req
				}
				if len(req.Entries) != 1 {
					return authorizationdetails.Result{}, refuse("single_entry_required")
				}
				var entry struct {
					Note string `json:"note"`
				}
				if err := json.Unmarshal(req.Entries[0], &entry); err != nil || entry.Note == "" {
					return authorizationdetails.Result{}, refuse("malformed_entry")
				}
				return authorizationdetails.Result{Entries: req.Entries}, nil
			},
		},
	})
	require.NoError(t, err)
	return registry
}

func TestRedemptionDetailsClaim_Refusals(t *testing.T) {
	t.Parallel()

	exchangeOnly, err := newExchangeGrantRegistry()
	require.NoError(t, err)

	tests := []struct {
		name     string
		registry *authorizationdetails.Registry
		values   []string
		code     string
		reason   string
	}{
		{name: "no registry refuses every entry", values: []string{"[" + testDetailEntry + "]"},
			code: "invalid_authorization_details", reason: "malformed_entry"},
		{name: "type registered for token exchange only", registry: exchangeOnly,
			values: []string{"[" + testExchangeGrantEntry + "]"}, code: "invalid_authorization_details", reason: "malformed_entry"},
		{name: "unknown type", registry: newCallDetailsRegistry(t), values: []string{`[{"type":"https://example.com/other"}]`},
			code: "invalid_authorization_details", reason: "malformed_entry"},
		{name: "empty array", registry: newCallDetailsRegistry(t), values: []string{"[]"},
			code: "invalid_authorization_details", reason: "malformed_entry"},
		{name: "hook refusal is passed on", registry: newCallDetailsRegistry(t),
			values: []string{"[" + testDetailEntry + "," + testDetailEntry + "]"},
			code:   "invalid_authorization_details", reason: "single_entry_required"},
		{name: "repeated parameter", registry: newCallDetailsRegistry(t),
			values: []string{"[" + testDetailEntry + "]", "[" + testDetailEntry + "]"},
			code:   "invalid_request", reason: "repeated_parameter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, err := newJWTBearerHandler(&testJWTBearerAssertionValidator{}, testTokenEndpoint)
			require.NoError(t, err)
			handler.detailsRegistry = tt.registry
			_, err = handler.redemptionDetailsClaim(
				context.Background(), url.Values{"authorization_details": tt.values}, &ValidatedClaims{}, "client", "https://mcp.example.com")
			requireRefusal(t, err, tt.code, tt.reason)
		})
	}
}

func TestRedemptionDetailsClaim_RedemptionPaths(t *testing.T) {
	t.Parallel()

	const (
		resource = "https://mcp.example.com"
		subject  = "ext-user"
		bigEntry = `{"type":"` + testDetailType + `","note":"big","limit":9007199254740993}`
	)
	type attempt struct {
		details url.Values
		extra   map[string]any
		seen    *authorizationdetails.Request
	}
	type outcome struct {
		req      *fosite.AccessRequest
		resp     fosite.AccessResponder
		consumer *recordingAssertionConsumer
		err      error
	}
	paths := []struct {
		name        string
		wantClient  string
		wantSubject string
		redeem      func(t *testing.T, a attempt) outcome
	}{
		{name: "jwt bearer", wantClient: "", wantSubject: subject, redeem: func(t *testing.T, a attempt) outcome {
			t.Helper()
			resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{{
				IssuerURL:              idJAGTestIssuer,
				AllowedDelegateClients: []string{anyDelegateClient},
				JWTBearerGrant: &JWTBearerGrantPolicy{
					MaxAssertionAge: time.Hour.String(),
					SubjectBindings: []JWTBearerSubjectBinding{{Subject: subject, AllowedResources: []string{resource}}},
				},
			}})
			require.NoError(t, err)
			now := time.Now()
			consumer := &recordingAssertionConsumer{}
			registry := newCallDetailsRegistry(t, a.seen)
			handler, err := newJWTBearerIssuanceHandler(
				&testJWTBearerAssertionValidator{claims: &ValidatedClaims{
					Issuer: idJAGTestIssuer, Subject: subject, JWTID: "jti", IssuedAt: now, Expiry: now.Add(time.Minute),
					Extra: a.extra,
				}},
				testTokenEndpoint, consumer, &fosite.Config{AccessTokenLifespan: time.Hour},
				&mockAccessTokenStrategy{}, &mockAccessTokenStorage{}, resolved,
				WithAuthorizationDetailsRegistry(registry))
			require.NoError(t, err)
			form := url.Values{"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)}, "resource": {resource}}
			for k, v := range a.details {
				form[k] = v
			}
			req, resp := newJWTBearerRequest(form), fosite.NewAccessResponse()
			return outcome{req, resp, consumer, runRedemption(handler, req, resp)}
		}},
		{name: "id-jag", wantClient: idJAGTestClientID, wantSubject: idJAGTestSubject,
			redeem: func(t *testing.T, a attempt) outcome {
				t.Helper()
				consumer := &recordingAssertionConsumer{}
				claims := validIDJAGClaims()
				for k, v := range a.extra {
					claims.Extra[k] = v
				}
				handler := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: claims}, consumer,
					WithAuthorizationDetailsRegistry(newCallDetailsRegistry(t, a.seen)))
				req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
				for k, v := range a.details {
					req.Form[k] = v
				}
				resp := fosite.NewAccessResponse()
				return outcome{req, resp, consumer, runRedemption(handler, req, resp)}
			}},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			t.Parallel()

			base := path.redeem(t, attempt{seen: &authorizationdetails.Request{}})
			require.NoError(t, base.err)
			baseSession := base.req.GetSession().(*session.Session)
			assert.NotContains(t, baseSession.JWTClaims.Extra, "authorization_details")
			assert.Nil(t, base.resp.GetExtra("authorization_details"))

			for _, entry := range []string{testDetailEntry, testOtherDetailEntry, bigEntry} {
				var seen authorizationdetails.Request
				got := path.redeem(t, attempt{
					details: url.Values{"authorization_details": {"[" + entry + "]"}}, seen: &seen})
				require.NoError(t, got.err)

				issued := got.req.GetSession().(*session.Session)
				claim, err := json.Marshal(issued.JWTClaims.Extra["authorization_details"])
				require.NoError(t, err)
				assert.Equal(t, "["+entry+"]", string(claim))
				member, err := json.Marshal(got.resp.GetExtra("authorization_details"))
				require.NoError(t, err)
				assert.Equal(t, "["+entry+"]", string(member))

				assert.Equal(t, base.req.GetGrantedAudience(), got.req.GetGrantedAudience())
				assert.Equal(t, base.req.GetGrantedScopes(), got.req.GetGrantedScopes())
				assert.Equal(t, baseSession.GetSubject(), issued.GetSubject())

				assert.Equal(t, oauthproto.GrantTypeJWTBearer, seen.GrantType)
				assert.Equal(t, path.wantClient, seen.ClientID)
				assert.Equal(t, idJAGTestIssuer, seen.Subject.Issuer)
				assert.Equal(t, path.wantSubject, seen.Subject.Subject)
				assert.True(t, seen.Subject.External)
				assert.Equal(t, []string(got.req.GetGrantedAudience()), seen.GrantedAudience)
			}

			malformed := path.redeem(t, attempt{
				details: url.Values{"authorization_details": {"[" + testMalformedEntry + "]"}}, seen: &authorizationdetails.Request{}})
			requireRefusal(t, malformed.err, "invalid_authorization_details", "malformed_entry")
			assert.Empty(t, malformed.consumer.key, "a refused entry must not consume the assertion")

			signed := path.redeem(t, attempt{
				extra: map[string]any{"authorization_details": []any{}}, seen: &authorizationdetails.Request{}})
			requireRefusal(t, signed.err, "invalid_grant",
				"invalid_assertion: The assertion must not carry an 'authorization_details' claim.")
			assert.Empty(t, signed.consumer.key, "an assertion carrying authorization_details must not be consumed")
		})
	}
}

// runRedemption runs both phases of a redemption handler.
func runRedemption(handler fosite.TokenEndpointHandler, req *fosite.AccessRequest, resp fosite.AccessResponder) error {
	if err := handler.HandleTokenEndpointRequest(context.Background(), req); err != nil {
		return err
	}
	return handler.PopulateTokenEndpointResponse(context.Background(), req, resp)
}
