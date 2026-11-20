// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
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
// whose hook accepts exactly one entry carrying a non-empty "note" member and
// refuses the rest with "single_entry_required" or "note_required". When seen
// is not nil, the hook stores the request it received there.
func newCallDetailsRegistry(t *testing.T, seen *authorizationdetails.Request) *authorizationdetails.Registry {
	t.Helper()
	refuse := func(reason string) error {
		return authorizationdetails.Error{Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: reason}
	}
	registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		testDetailType: {
			GrantTypes: []string{oauthproto.GrantTypeJWTBearer},
			Grant: func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
				if seen != nil {
					*seen = req
				}
				if len(req.Entries) != 1 {
					return authorizationdetails.Result{}, refuse("single_entry_required")
				}
				var entry struct {
					Note string `json:"note"`
				}
				if err := json.Unmarshal(req.Entries[0], &entry); err != nil || entry.Note == "" {
					return authorizationdetails.Result{}, refuse("note_required")
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
		{name: "repeated parameter", registry: newCallDetailsRegistry(t, nil),
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
				context.Background(), url.Values{"authorization_details": tt.values}, &ValidatedClaims{}, "client", "client", "https://mcp.example.com")
			requireRefusal(t, err, tt.code, tt.reason)
		})
	}
}

func TestRedemptionDetailsClaim_RedemptionPaths(t *testing.T) {
	t.Parallel()

	const (
		resource  = "https://mcp.example.com"
		subject   = "ext-user"
		bigEntry  = `{"type":"` + testDetailType + `","note":"big","limit":9007199254740993}`
		signedMsg = "invalid_assertion: The assertion must not carry an 'authorization_details' claim."
	)
	type attempt struct {
		details  url.Values
		extra    map[string]any
		registry *authorizationdetails.Registry
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
			handler, err := newJWTBearerIssuanceHandler(
				&testJWTBearerAssertionValidator{claims: &ValidatedClaims{
					Issuer: idJAGTestIssuer, Subject: subject, JWTID: "jti", IssuedAt: now, Expiry: now.Add(time.Minute),
					Extra: a.extra,
				}},
				testTokenEndpoint, consumer, &fosite.Config{AccessTokenLifespan: time.Hour},
				&mockAccessTokenStrategy{}, &mockAccessTokenStorage{}, resolved,
				a.registry)
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
					WithAuthorizationDetailsRegistry(a.registry))
				req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
				for k, v := range a.details {
					req.Form[k] = v
				}
				resp := fosite.NewAccessResponse()
				return outcome{req, resp, consumer, runRedemption(handler, req, resp)}
			}},
	}

	failingHook := func(t *testing.T, _ *authorizationdetails.Request) *authorizationdetails.Registry {
		t.Helper()
		registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
			testDetailType: {
				GrantTypes: []string{oauthproto.GrantTypeJWTBearer},
				Grant: func(context.Context, authorizationdetails.Request) (authorizationdetails.Result, error) {
					return authorizationdetails.Result{}, errors.New("private validator detail that must not escape")
				},
			},
		})
		require.NoError(t, err)
		return registry
	}
	details := func(values ...string) url.Values { return url.Values{"authorization_details": values} }

	scenarios := []struct {
		name  string
		form  url.Values
		extra map[string]any
		// registry builds the registry for the run; nil means newCallDetailsRegistry.
		registry func(*testing.T, *authorizationdetails.Request) *authorizationdetails.Registry
		// wantEntry is the single entry the claim and the response member must carry.
		// Empty means the redemption succeeds without either.
		wantEntry string
		// wantCode and wantReason describe the refusal; serverError a failing hook,
		// which records no request.
		wantCode, wantReason string
		serverError          bool
		wantHook             bool
	}{
		{name: "absent"},
		{name: "empty value", form: details("")},
		{name: "first entry", form: details("[" + testDetailEntry + "]"), wantEntry: testDetailEntry, wantHook: true},
		{name: "second entry", form: details("[" + testOtherDetailEntry + "]"), wantEntry: testOtherDetailEntry, wantHook: true},
		{name: "integer beyond float64 precision", form: details("[" + bigEntry + "]"), wantEntry: bigEntry, wantHook: true},
		{name: "hook refuses an entry", form: details("[" + testMalformedEntry + "]"),
			wantCode: "invalid_authorization_details", wantReason: "note_required", wantHook: true},
		{name: "hook refuses two entries", form: details("[" + testDetailEntry + "," + testOtherDetailEntry + "]"),
			wantCode: "invalid_authorization_details", wantReason: "single_entry_required", wantHook: true},
		{name: "empty array", form: details("[]"),
			wantCode: "invalid_authorization_details", wantReason: "malformed_entry"},
		{name: "failing hook", form: details("[" + testDetailEntry + "]"), registry: failingHook,
			serverError: true},
		{name: "signed authorization_details claim", extra: map[string]any{"authorization_details": []any{}},
			wantCode: "invalid_grant", wantReason: signedMsg},
		{name: "signed marker and authorization_details claim",
			extra:    map[string]any{claimnames.DelegatedAccessKey: true, "authorization_details": []any{}},
			wantCode: "invalid_grant", wantReason: signedMsg},
	}

	for _, path := range paths {
		for _, sc := range scenarios {
			t.Run(path.name+"/"+sc.name, func(t *testing.T) {
				t.Parallel()

				var seen authorizationdetails.Request
				newRegistry := sc.registry
				if newRegistry == nil {
					newRegistry = newCallDetailsRegistry
				}
				got := path.redeem(t, attempt{details: sc.form, extra: sc.extra, registry: newRegistry(t, &seen)})
				assert.Equal(t, sc.wantHook, seen.GrantType != "", "hook invocation")

				switch {
				case sc.serverError:
					requireRefusalStatus(t, got.err, http.StatusInternalServerError, "server_error",
						"The authorization_details could not be processed.")
				case sc.wantCode != "":
					requireRefusal(t, got.err, sc.wantCode, sc.wantReason)
				}
				if got.err != nil {
					assert.Empty(t, got.consumer.key, "a refused redemption must not consume the assertion")
					return
				}
				require.NoError(t, got.err)

				issued := got.req.GetSession().(*session.Session)
				if sc.wantEntry == "" {
					assert.NotContains(t, issued.JWTClaims.Extra, "authorization_details")
					assert.Nil(t, got.resp.GetExtra("authorization_details"))
					return
				}
				claim, err := json.Marshal(issued.JWTClaims.Extra["authorization_details"])
				require.NoError(t, err)
				assert.Equal(t, "["+sc.wantEntry+"]", string(claim))
				member, err := json.Marshal(got.resp.GetExtra("authorization_details"))
				require.NoError(t, err)
				assert.Equal(t, "["+sc.wantEntry+"]", string(member))

				base := path.redeem(t, attempt{registry: newCallDetailsRegistry(t, nil)})
				require.NoError(t, base.err)
				assert.Equal(t, base.req.GetGrantedAudience(), got.req.GetGrantedAudience())
				assert.Equal(t, base.req.GetGrantedScopes(), got.req.GetGrantedScopes())
				assert.Equal(t, base.req.GetSession().GetSubject(), issued.GetSubject())

				assert.Equal(t, oauthproto.GrantTypeJWTBearer, seen.GrantType)
				assert.Equal(t, path.wantClient, seen.ClientID)
				assert.Equal(t, idJAGTestIssuer, seen.Subject.Issuer)
				assert.Equal(t, path.wantSubject, seen.Subject.Subject)
				assert.True(t, seen.Subject.External)
				assert.Equal(t, []string(got.req.GetGrantedAudience()), seen.GrantedAudience)
			})
		}
	}
}

// runRedemption runs both phases of a redemption handler.
func runRedemption(handler fosite.TokenEndpointHandler, req *fosite.AccessRequest, resp fosite.AccessResponder) error {
	if err := handler.HandleTokenEndpointRequest(context.Background(), req); err != nil {
		return err
	}
	return handler.PopulateTokenEndpointResponse(context.Background(), req, resp)
}
