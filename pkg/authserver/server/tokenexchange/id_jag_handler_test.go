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

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
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
			AcceptedAssertionTypes: []JWTBearerAssertionType{JWTBearerAssertionTypeIDJAG},
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
		&fosite.Config{AccessTokenLifespan: time.Hour, AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy},
		&mockAccessTokenStrategy{},
		&mockAccessTokenStorage{},
		idJAGResolvedIssuers(t),
		nil,
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
		req.Client = &fosite.DefaultClient{ID: clientID, Audience: []string{idJAGTestResource}}
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

func TestIDJAGHandler_ClientGrantAndAudience(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		gated      bool
		grantTypes []string
		audience   []string
		strategy   fosite.AudienceMatchingStrategy // nil keeps the default
		dcrIssued  bool
		wantErrIs  error
	}{
		{
			name:       "gated client with jwt-bearer is allowed",
			gated:      true,
			grantTypes: []string{oauthproto.GrantTypeJWTBearer},
			audience:   []string{idJAGTestResource},
		},
		{
			name:       "gated client without jwt-bearer is refused",
			gated:      true,
			grantTypes: []string{oauthproto.GrantTypeTokenExchange},
			audience:   []string{idJAGTestResource},
			wantErrIs:  fosite.ErrUnauthorizedClient,
		},
		{
			name:       "ungated client is not held to its grants",
			grantTypes: []string{oauthproto.GrantTypeTokenExchange},
			audience:   []string{idJAGTestResource},
		},
		{
			name:       "resource outside the client audiences is refused",
			grantTypes: []string{oauthproto.GrantTypeJWTBearer},
			audience:   []string{"https://other.example.com"},
			wantErrIs:  server.ErrInvalidTarget,
		},
		{
			name:       "client without audiences is refused",
			grantTypes: []string{oauthproto.GrantTypeJWTBearer},
			wantErrIs:  server.ErrInvalidTarget,
		},
		{
			name:       "configured audience strategy is applied",
			grantTypes: []string{oauthproto.GrantTypeJWTBearer},
			audience:   []string{idJAGTestResource},
			strategy:   func(_, _ []string) error { return fosite.ErrInvalidRequest },
			wantErrIs:  server.ErrInvalidTarget,
		},
		{
			name:       "DCR client with a stale audience copy is not narrowed",
			grantTypes: []string{oauthproto.GrantTypeJWTBearer},
			audience:   []string{"https://other.example.com"},
			dcrIssued:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tj := newTestJWKS(t)
			consumer := &recordingAssertionConsumer{}
			h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}, consumer)
			if tt.gated {
				h.explicitGrantClients = map[string]struct{}{idJAGTestClientID: {}}
			}
			req := newIDJAGRequest(t, tj, idJAGTestClientID)
			if tt.strategy != nil {
				h.core.config.(*fosite.Config).AudienceMatchingStrategy = tt.strategy
			}
			var client fosite.Client = &fosite.DefaultClient{ID: idJAGTestClientID, GrantTypes: tt.grantTypes, Audience: tt.audience}
			if tt.dcrIssued {
				client = registration.MarkDCRIssued(client)
			}
			req.Client = client

			err := h.HandleTokenEndpointRequest(t.Context(), req)
			if tt.wantErrIs == nil {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErrIs)
			assert.Empty(t, consumer.key, "a refused client must not consume the assertion")
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

	_, err := IDJAGIssuanceFactory(idJAGResolvedIssuers(t), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shared validator")
}

// TestIDJAGIssuanceHandler_RegistersWithNoIDJAGIssuers proves the
// diagnostics fix: newIDJAGIssuanceHandler must still construct successfully
// when zero trusted issuers accept id_jag assertions, so a well-formed
// "oauth-id-jag+jwt" assertion is claimed and rejected with the precise
// "issuer is not enabled for this grant" hint rather than falling through
// every registered fosite handler to the generic invalid_request.
func TestIDJAGIssuanceHandler_RegistersWithNoIDJAGIssuers(t *testing.T) {
	t.Parallel()

	resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{{
		IssuerURL:              idJAGTestIssuer,
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
			},
			// jwt_bearer only, so no issuer accepts id_jag and the resolved
			// policy map handed to newIDJAGIssuanceHandler is empty.
			AcceptedAssertionTypes: []JWTBearerAssertionType{JWTBearerAssertionTypeJWTBearer},
		},
	}})
	require.NoError(t, err)

	handler, err := newIDJAGIssuanceHandler(
		&testJWTBearerAssertionValidator{claims: validIDJAGClaims()},
		testTokenEndpoint,
		&recordingAssertionConsumer{},
		&fosite.Config{AccessTokenLifespan: time.Hour},
		&mockAccessTokenStrategy{},
		&mockAccessTokenStorage{},
		IssuersAcceptingAssertionType(resolved, JWTBearerAssertionTypeIDJAG),
		nil,
	)
	require.NoError(t, err, "the bound handler must register even with no id_jag issuers")

	tj := newTestJWKS(t)
	req := newIDJAGRequest(t, tj, idJAGTestClientID)
	assert.True(t, handler.CanHandleTokenEndpointRequest(context.Background(), req),
		"a recognized ID-JAG assertion must still be claimed by the registered handler")
	assertIssuerNotEnabled(t, handler.HandleTokenEndpointRequest(context.Background(), req))
}

// assertIssuerNotEnabled asserts err is the "issuer not enabled for this
// grant" invalid_grant rejection both handlers return for an issuer left out
// of their policy map.
func assertIssuerNotEnabled(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, fosite.ErrInvalidGrant)
	var rfcErr *fosite.RFC6749Error
	require.True(t, errors.As(err, &rfcErr))
	assert.Contains(t, rfcErr.Reason(), "not enabled for this grant")
}

// TestJWTBearerAssertionTypeGating proves AcceptedAssertionTypes decides, per
// issuer, which assertion form(s) the jwt-bearer grant accepts: unset accepts
// both (the pre-field behavior every existing config relies on), {id_jag}
// refuses plain assertions, and {jwt_bearer} refuses ID-JAG.
func TestJWTBearerAssertionTypeGating(t *testing.T) {
	t.Parallel()

	const (
		defaultIssuer   = "https://default-issuer.example.com"
		acceptedIssuer  = "https://id-jag-only-issuer.example.com"
		plainOnlyIssuer = "https://plain-only-issuer.example.com"
	)
	resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{
		{
			IssuerURL:              defaultIssuer,
			AllowedDelegateClients: []string{anyDelegateClient},
			JWTBearerGrant: &JWTBearerGrantPolicy{
				MaxAssertionAge: time.Hour.String(),
				SubjectBindings: []JWTBearerSubjectBinding{
					{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
				},
				// AcceptedAssertionTypes deliberately left unset.
			},
		},
		{
			IssuerURL:              acceptedIssuer,
			AllowedDelegateClients: []string{anyDelegateClient},
			JWTBearerGrant: &JWTBearerGrantPolicy{
				MaxAssertionAge: time.Hour.String(),
				SubjectBindings: []JWTBearerSubjectBinding{
					{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
				},
				AcceptedAssertionTypes: []JWTBearerAssertionType{JWTBearerAssertionTypeIDJAG},
			},
		},
		{
			IssuerURL:              plainOnlyIssuer,
			AllowedDelegateClients: []string{anyDelegateClient},
			JWTBearerGrant: &JWTBearerGrantPolicy{
				MaxAssertionAge: time.Hour.String(),
				SubjectBindings: []JWTBearerSubjectBinding{
					{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
				},
				AcceptedAssertionTypes: []JWTBearerAssertionType{JWTBearerAssertionTypeJWTBearer},
			},
		},
	})
	require.NoError(t, err)

	tj := newTestJWKS(t)
	newPlainHandler := func(t *testing.T, issuer string) *JWTBearerHandler {
		t.Helper()
		claims := &ValidatedClaims{
			Issuer: issuer, Subject: idJAGTestSubject,
			IssuedAt: time.Now(), Expiry: time.Now().Add(5 * time.Minute),
		}
		handler, err := newJWTBearerIssuanceHandler(
			&testJWTBearerAssertionValidator{claims: claims},
			testTokenEndpoint, &recordingAssertionConsumer{},
			&fosite.Config{AccessTokenLifespan: time.Hour},
			&mockAccessTokenStrategy{}, &mockAccessTokenStorage{},
			IssuersAcceptingAssertionType(resolved, JWTBearerAssertionTypeJWTBearer),
		)
		require.NoError(t, err)
		return handler
	}
	// The request form doesn't carry the issuer; newPlainHandler already bound
	// it into the validator's returned claims, which is what policy lookup
	// keys on.
	newPlainRequest := func() *fosite.AccessRequest {
		req := fosite.NewAccessRequest(&session.Session{})
		req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
		req.Form = url.Values{
			"assertion": {signAssertionWithType(t, tj, nil)},
			"resource":  {idJAGTestResource},
		}
		return req
	}
	newIDJAGHandlerFor := func(t *testing.T, issuer string) *IDJAGHandler {
		t.Helper()
		claims := validIDJAGClaims()
		claims.Issuer = issuer
		handler, err := newIDJAGIssuanceHandler(
			&testJWTBearerAssertionValidator{claims: claims},
			testTokenEndpoint, &recordingAssertionConsumer{},
			&fosite.Config{AccessTokenLifespan: time.Hour},
			&mockAccessTokenStrategy{}, &mockAccessTokenStorage{},
			resolved,
			nil,
		)
		require.NoError(t, err)
		return handler
	}

	t.Run("nil AcceptedAssertionTypes accepts both plain and ID-JAG", func(t *testing.T) {
		t.Parallel()
		plain := newPlainHandler(t, defaultIssuer)
		assert.NoError(t, plain.HandleTokenEndpointRequest(context.Background(), newPlainRequest()))

		idJAG := newIDJAGHandlerFor(t, defaultIssuer)
		req := newIDJAGRequest(t, tj, idJAGTestClientID)
		assert.NoError(t, idJAG.HandleTokenEndpointRequest(context.Background(), req))
	})

	t.Run("jwt_bearer-only AcceptedAssertionTypes accepts plain and rejects ID-JAG", func(t *testing.T) {
		t.Parallel()
		plain := newPlainHandler(t, plainOnlyIssuer)
		assert.NoError(t, plain.HandleTokenEndpointRequest(context.Background(), newPlainRequest()))

		idJAG := newIDJAGHandlerFor(t, plainOnlyIssuer)
		req := newIDJAGRequest(t, tj, idJAGTestClientID)
		assertIssuerNotEnabled(t, idJAG.HandleTokenEndpointRequest(context.Background(), req))
	})

	t.Run("id_jag-only AcceptedAssertionTypes rejects plain and accepts ID-JAG", func(t *testing.T) {
		t.Parallel()
		plain := newPlainHandler(t, acceptedIssuer)
		assertIssuerNotEnabled(t, plain.HandleTokenEndpointRequest(context.Background(), newPlainRequest()))

		idJAG := newIDJAGHandlerFor(t, acceptedIssuer)
		req := newIDJAGRequest(t, tj, idJAGTestClientID)
		assert.NoError(t, idJAG.HandleTokenEndpointRequest(context.Background(), req))
	})
}

// jwtBearerFactoryStorage satisfies the factory closure's storage parameter:
// fosite.Storage, oauth2.AccessTokenStorage and storage.AssertionJWTConsumer.
type jwtBearerFactoryStorage struct {
	fakeFactoryStorage
	*recordingAssertionConsumer
}

// TestJWTBearerIssuanceFactory_GatesOnAcceptedAssertionTypes drives the
// production JWTBearerIssuanceFactory, whose filtering is the only thing that
// keeps an id_jag-only issuer off the plain (client-auth-skippable) path.
func TestJWTBearerIssuanceFactory_GatesOnAcceptedAssertionTypes(t *testing.T) {
	t.Parallel()

	const (
		plainIssuer  = "https://plain-issuer.example.com"
		idJAGIssuer  = "https://id-jag-only-issuer.example.com"
		bearerIssuer = "https://bearer-only-issuer.example.com"
	)
	issuer := func(issuerURL string, types ...JWTBearerAssertionType) TrustedIssuer {
		return TrustedIssuer{
			IssuerURL:              issuerURL,
			ExpectedAudience:       idJAGTestResource,
			AllowedDelegateClients: []string{anyDelegateClient},
			JWTBearerGrant: &JWTBearerGrantPolicy{
				MaxAssertionAge:   time.Hour.String(),
				AcceptedAudiences: []string{"https://auth.example.com"},
				SubjectBindings: []JWTBearerSubjectBinding{
					{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
				},
				AcceptedAssertionTypes: types,
			},
		}
	}

	tests := []struct {
		name         string
		issuers      []TrustedIssuer
		acceptIssuer string
		rejectIssuer string
	}{
		{
			name: "mixed issuers keep the id_jag-only issuer off the plain path",
			issuers: []TrustedIssuer{
				issuer(plainIssuer),
				issuer(bearerIssuer, JWTBearerAssertionTypeJWTBearer),
				issuer(idJAGIssuer, JWTBearerAssertionTypeIDJAG),
			},
			acceptIssuer: plainIssuer,
			rejectIssuer: idJAGIssuer,
		},
		{
			name:         "only id_jag issuers builds a plain handler that rejects every issuer",
			issuers:      []TrustedIssuer{issuer(idJAGIssuer, JWTBearerAssertionTypeIDJAG)},
			rejectIssuer: idJAGIssuer,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := buildTestAuthServerConfig(t)
			shared, err := NewSharedTrustedIssuerValidator(cfg, tt.issuers)
			require.NoError(t, err)
			t.Cleanup(func() { _ = shared.Close() })

			f, err := JWTBearerIssuanceFactory(tt.issuers, shared)
			require.NoError(t, err)
			result, err := f(cfg, &jwtBearerFactoryStorage{
				fakeFactoryStorage:         fakeFactoryStorage{mockAccessTokenStorage: &mockAccessTokenStorage{}},
				recordingAssertionConsumer: &recordingAssertionConsumer{},
			}, &mockAccessTokenStrategy{})
			require.NoError(t, err)
			handler, ok := result.(*JWTBearerHandler)
			require.True(t, ok, "factory closure must return *JWTBearerHandler, got %T", result)

			tj := newTestJWKS(t)
			plainRequest := func() *fosite.AccessRequest {
				req := fosite.NewAccessRequest(&session.Session{})
				req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
				req.Form = url.Values{
					"assertion": {signAssertionWithType(t, tj, nil)},
					"resource":  {idJAGTestResource},
				}
				return req
			}
			claimsFor := func(iss string) *testJWTBearerAssertionValidator {
				return &testJWTBearerAssertionValidator{claims: &ValidatedClaims{
					Issuer: iss, Subject: idJAGTestSubject,
					IssuedAt: time.Now(), Expiry: time.Now().Add(5 * time.Minute),
				}}
			}

			// The factory fixes the validator to the shared real one; swap in
			// a fake so claims (and so the issuer policy lookup) are controlled.
			if tt.acceptIssuer != "" {
				handler.validator = claimsFor(tt.acceptIssuer)
				assert.NoError(t, handler.HandleTokenEndpointRequest(context.Background(), plainRequest()))
			}

			handler.validator = claimsFor(tt.rejectIssuer)
			assertIssuerNotEnabled(t, handler.HandleTokenEndpointRequest(context.Background(), plainRequest()))
		})
	}
}
