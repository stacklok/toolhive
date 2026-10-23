// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	idJAGTestIssuer   = "https://idp.example.com"
	idJAGTestSubject  = "okta-user"
	idJAGTestResource = "https://mcp.example.com"
	idJAGTestClientID = "agent-client"
)

// recordingAssertionConsumer captures the arguments of the last consume call
// so tests can pin the replay-key namespace, not just that consumption happened.
type recordingAssertionConsumer struct {
	err     error
	purpose string
	issuer  string
	key     string
}

func (c *recordingAssertionConsumer) ConsumeAssertionJWT(
	_ context.Context, purpose, issuer, key string, _ time.Time,
) error {
	c.purpose = purpose
	c.issuer = issuer
	c.key = key
	return c.err
}

func idJAGResolvedIssuers(t *testing.T) []TrustedIssuer {
	t.Helper()
	resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{{
		IssuerURL:              idJAGTestIssuer,
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
			},
		},
	}})
	require.NoError(t, err)
	return resolved
}

func validIDJAGClaims() *ValidatedClaims {
	now := time.Now()
	return &ValidatedClaims{
		Issuer:   idJAGTestIssuer,
		Subject:  idJAGTestSubject,
		ClientID: idJAGTestClientID,
		JWTID:    "id-jag-jti",
		IssuedAt: now,
		Expiry:   now.Add(5 * time.Minute),
		Extra: map[string]any{
			"act":        map[string]any{"sub": "okta-agent-client"},
			"aud_tenant": idJAGTestIssuer,
		},
	}
}

func newIDJAGTestHandler(
	t *testing.T, validator *testJWTBearerAssertionValidator, consumer *recordingAssertionConsumer,
) *IDJAGHandler {
	t.Helper()
	handler, err := newIDJAGIssuanceHandler(
		validator,
		testTokenEndpoint,
		consumer,
		&fosite.Config{AccessTokenLifespan: time.Hour},
		&mockAccessTokenStrategy{},
		&mockAccessTokenStorage{},
		idJAGResolvedIssuers(t),
	)
	require.NoError(t, err)
	return handler
}

func newIDJAGRequest(t *testing.T, tj *testJWKS, clientID string) *fosite.AccessRequest {
	t.Helper()
	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
	req.Form = url.Values{
		"assertion": {signAssertionWithType(t, tj, idJAGJWTType)},
		"resource":  {idJAGTestResource},
	}
	if clientID != "" {
		req.Client = &fosite.DefaultClient{ID: clientID}
	}
	return req
}

func TestIDJAGHandler_MatchingAndClientAuth(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{}, &recordingAssertionConsumer{})

	tests := []struct {
		name        string
		grantTypes  fosite.Arguments
		assertion   string
		wantHandles bool
	}{
		{
			name:        "ID-JAG assertion matches",
			grantTypes:  fosite.Arguments{oauthproto.GrantTypeJWTBearer},
			assertion:   signAssertionWithType(t, tj, idJAGJWTType),
			wantHandles: true,
		},
		{
			name:       "plain assertion is left for the unbound handler",
			grantTypes: fosite.Arguments{oauthproto.GrantTypeJWTBearer},
			assertion:  signAssertionWithType(t, tj, nil),
		},
		{
			name:       "JWT typ is left for the unbound handler",
			grantTypes: fosite.Arguments{oauthproto.GrantTypeJWTBearer},
			assertion:  signAssertionWithType(t, tj, "JWT"),
		},
		{
			name:       "other grant does not match",
			grantTypes: fosite.Arguments{"client_credentials"},
			assertion:  signAssertionWithType(t, tj, idJAGJWTType),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := newJWTBearerRequest(map[string][]string{"assertion": {tt.assertion}})
			req.GrantTypes = tt.grantTypes
			ctx := contextWithHTTPRequest(t, "", "")
			assert.Equal(t, tt.wantHandles, h.CanHandleTokenEndpointRequest(ctx, req))
			// Bound means bound: client authentication is never skippable, with or
			// without credentials in the request, whether or not this handler even
			// claims the request.
			assert.False(t, h.CanSkipClientAuth(ctx, req))
		})
	}
}

func TestIDJAGHandler_ClientBindingAndJTI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mutateClaims   func(*ValidatedClaims)
		requestClient  string
		wantErrIs      error
		wantErrContain string
	}{
		{
			name:           "missing jti is rejected",
			mutateClaims:   func(c *ValidatedClaims) { c.JWTID = "" },
			requestClient:  idJAGTestClientID,
			wantErrIs:      fosite.ErrInvalidGrant,
			wantErrContain: "'jti'",
		},
		{
			name:           "missing client_id claim is rejected",
			mutateClaims:   func(c *ValidatedClaims) { c.ClientID = "" },
			requestClient:  idJAGTestClientID,
			wantErrIs:      fosite.ErrInvalidGrant,
			wantErrContain: "'client_id'",
		},
		{
			// fosite initializes an unauthenticated request's client to an
			// ID-less placeholder; the binding check must fail closed on it
			// rather than compare against the empty ID.
			name:           "unresolved client fails closed",
			mutateClaims:   func(*ValidatedClaims) {},
			wantErrIs:      fosite.ErrInvalidClient,
			wantErrContain: "authenticated or identified client",
		},
		{
			name:           "client mismatch is rejected",
			mutateClaims:   func(*ValidatedClaims) {},
			requestClient:  "some-other-client",
			wantErrIs:      fosite.ErrInvalidGrant,
			wantErrContain: "not issued to the requesting client",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tj := newTestJWKS(t)
			claims := validIDJAGClaims()
			tt.mutateClaims(claims)
			consumer := &recordingAssertionConsumer{}
			h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: claims}, consumer)
			req := newIDJAGRequest(t, tj, tt.requestClient)

			err := h.HandleTokenEndpointRequest(context.Background(), req)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErrIs)
			var rfcErr *fosite.RFC6749Error
			require.True(t, errors.As(err, &rfcErr))
			assert.Contains(t, rfcErr.Reason(), tt.wantErrContain)
			// None of these rejections may consume the assertion: they are all
			// caller-fixable, and a consumed jti would turn a retry with the
			// right client into a replay rejection.
			assert.Empty(t, consumer.key)
		})
	}
}

func TestIDJAGHandler_IssuesBoundSession(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	claims := validIDJAGClaims()
	consumer := &recordingAssertionConsumer{}
	h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: claims}, consumer)
	req := newIDJAGRequest(t, tj, idJAGTestClientID)

	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))

	// Replay consumption happened under the ID-JAG purpose with the JAG's jti —
	// never the plain handler's purpose or an assertion-hash fallback.
	assert.Equal(t, idJAGReplayPurpose, consumer.purpose)
	assert.Equal(t, idJAGTestIssuer, consumer.issuer)
	assert.Equal(t, claims.JWTID, consumer.key)

	issued, ok := req.GetSession().(*session.Session)
	require.True(t, ok, "expected the handler to install a *session.Session")
	assert.Equal(t, idJAGTestIssuer+"#"+idJAGTestSubject, issued.GetSubject())
	assert.Equal(t, true, issued.JWTClaims.Extra[session.NoUpstreamSessionClaimKey],
		"ID-JAG tokens must declare that they have no upstream session")
	assert.Equal(t, claims.Extra["act"], issued.JWTClaims.Extra["act"],
		"the assertion's actor chain must survive into the issued token")
	assert.Equal(t, idJAGTestClientID, issued.JWTClaims.Extra[session.ClientIDClaimKey])

	// The authenticated client stays on the request — no synthetic
	// replacement, unlike the clientless plain handler.
	assert.Equal(t, idJAGTestClientID, req.GetClient().GetID())
	assert.Equal(t, fosite.Arguments{idJAGTestResource}, req.GetGrantedAudience())

	// The session expiry is capped by the assertion's remaining validity
	// (5 minutes here), not the configured one-hour token lifespan.
	expiry := issued.GetExpiresAt(fosite.AccessToken)
	assert.WithinDuration(t, claims.Expiry, expiry, 10*time.Second)
}

func TestIDJAGHandler_ConsumeAssertionJWTErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		consumerErr error
		wantErrIs   error
	}{
		{name: "replay is rejected as invalid_grant", consumerErr: fosite.ErrJTIKnown, wantErrIs: fosite.ErrInvalidGrant},
		{name: "storage failure surfaces as a server error", consumerErr: errors.New("redis: connection refused"), wantErrIs: fosite.ErrServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tj := newTestJWKS(t)
			h := newIDJAGTestHandler(t,
				&testJWTBearerAssertionValidator{claims: validIDJAGClaims()},
				&recordingAssertionConsumer{err: tt.consumerErr})
			req := newIDJAGRequest(t, tj, idJAGTestClientID)

			err := h.HandleTokenEndpointRequest(context.Background(), req)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErrIs)
		})
	}
}

func TestIDJAGHandler_PopulateGatesOnOwnMatcher(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}, &recordingAssertionConsumer{})

	// A plain assertion is not this handler's request, even with an
	// otherwise-complete session — the promoted-method trap this type's
	// core field exists to avoid would have failed the opposite way.
	req := newJWTBearerRequest(map[string][]string{"assertion": {signAssertionWithType(t, tj, nil)}})
	err := h.PopulateTokenEndpointResponse(context.Background(), req, fosite.NewAccessResponse())
	assert.ErrorIs(t, err, fosite.ErrUnknownRequest)

	// An ID-JAG request with a prepared session issues a token.
	req = newIDJAGRequest(t, tj, idJAGTestClientID)
	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))
	resp := fosite.NewAccessResponse()
	require.NoError(t, h.PopulateTokenEndpointResponse(context.Background(), req, resp))
	assert.NotEmpty(t, resp.GetAccessToken())
}

func TestIDJAGIssuanceFactory_RequiresSharedValidator(t *testing.T) {
	t.Parallel()

	_, err := IDJAGIssuanceFactory(idJAGResolvedIssuers(t), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shared validator")
}
