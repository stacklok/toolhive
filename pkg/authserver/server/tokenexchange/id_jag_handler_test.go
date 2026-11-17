// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
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
	return newIDJAGTestHandlerWithIssuer(t, validator, consumer, "")
}

func newIDJAGTestHandlerWithIssuer(
	t *testing.T, validator *testJWTBearerAssertionValidator, consumer *recordingAssertionConsumer, asIssuer string,
) *IDJAGHandler {
	t.Helper()
	handler, err := newIDJAGIssuanceHandler(
		validator,
		testTokenEndpoint,
		consumer,
		&fosite.Config{
			AccessTokenLifespan:      time.Hour,
			AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy,
			AccessTokenIssuer:        asIssuer,
		},
		&mockAccessTokenStrategy{},
		&mockAccessTokenStorage{},
		idJAGResolvedIssuers(t),
		nil, nil, nil,
	)
	require.NoError(t, err)
	return handler
}

func newIDJAGRequest(t *testing.T, tj *testJWKS, clientID string) *fosite.AccessRequest {
	t.Helper()
	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
	req.Form = url.Values{
		"assertion": {signAssertionWithType(t, tj, oauthproto.JWTTypeIDJAG)},
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
			assertion:   signAssertionWithType(t, tj, oauthproto.JWTTypeIDJAG),
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
			name:       "media-type ID-JAG typ is left for the unbound handler",
			grantTypes: fosite.Arguments{oauthproto.GrantTypeJWTBearer},
			assertion:  signAssertionWithType(t, tj, "application/"+oauthproto.JWTTypeIDJAG),
		},
		{
			name:       "other grant does not match",
			grantTypes: fosite.Arguments{"client_credentials"},
			assertion:  signAssertionWithType(t, tj, oauthproto.JWTTypeIDJAG),
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
		wantErrIs      error  // checked alongside wantErrContain
		wantErrContain string // substring of the reason, for refusals without a reason code
		wantDesc       string // exact error_description of an invalid_grant refusal
	}{
		{
			name:          "missing jti is rejected",
			mutateClaims:  func(c *ValidatedClaims) { c.JWTID = "" },
			requestClient: idJAGTestClientID,
			wantDesc:      "invalid_assertion: The ID-JAG assertion must carry a 'jti' claim.",
		},
		{
			name:          "missing client_id claim is rejected",
			mutateClaims:  func(c *ValidatedClaims) { c.ClientID = "" },
			requestClient: idJAGTestClientID,
			wantDesc:      "invalid_assertion: The ID-JAG assertion must carry a 'client_id' claim.",
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
			name:          "client mismatch is rejected",
			mutateClaims:  func(*ValidatedClaims) {},
			requestClient: "some-other-client",
			wantDesc:      "invalid_assertion: The ID-JAG assertion was not issued to the requesting client.",
		},
		{
			name: "aud array with two elements is rejected",
			mutateClaims: func(c *ValidatedClaims) {
				c.Audience = []string{idJAGTestASIssuer, testTokenEndpoint}
			},
			requestClient: idJAGTestClientID,
			wantErrIs:     fosite.ErrInvalidGrant,
			wantDesc:      "invalid_assertion: The ID-JAG assertion aud must name exactly one audience.",
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
			if tt.wantDesc != "" {
				requireRefusal(t, err, "invalid_grant", tt.wantDesc)
			}
			if tt.wantErrContain != "" {
				assert.ErrorIs(t, err, tt.wantErrIs)
				var rfcErr *fosite.RFC6749Error
				require.True(t, errors.As(err, &rfcErr))
				assert.Contains(t, rfcErr.Reason(), tt.wantErrContain)
			}
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
		client     fosite.Client // overrides the client built from grantTypes and audience
		wantErrIs  error         // for refusals without a reason code
		wantDesc   string        // exact error_description of an access_denied refusal
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
			wantDesc:   "resource_denied",
		},
		{
			name:       "client without audiences is refused",
			grantTypes: []string{oauthproto.GrantTypeJWTBearer},
			wantDesc:   "resource_denied",
		},
		{
			// Config validation, not the handler, forbids empty audiences with
			// jwt-bearer in real configs; this client is constructible only
			// because NewStaticDelegateClient leaves that rule to config.
			name:  "static client with jwt-bearer and no audiences is refused the resource",
			gated: true,
			client: func() fosite.Client {
				c, err := registration.NewStaticDelegateClient(registration.Config{
					ID: idJAGTestClientID, Secret: "s", Scopes: []string{"openid"},
					GrantTypes: []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer},
				})
				require.NoError(t, err)
				return c
			}(),
			wantDesc: "resource_denied",
		},
		{
			name:       "configured audience strategy is applied",
			grantTypes: []string{oauthproto.GrantTypeJWTBearer},
			audience:   []string{idJAGTestResource},
			strategy:   func(_, _ []string) error { return fosite.ErrInvalidRequest },
			wantDesc:   "resource_denied",
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
			if tt.client != nil {
				client = tt.client
			}
			if tt.dcrIssued {
				client = registration.MarkDCRIssued(client)
			}
			req.Client = client

			err := h.HandleTokenEndpointRequest(t.Context(), req)
			if tt.wantErrIs == nil && tt.wantDesc == "" {
				require.NoError(t, err)
				return
			}
			if tt.wantDesc != "" {
				requireRefusal(t, err, "access_denied", tt.wantDesc)
			} else {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
			assert.Empty(t, consumer.key, "a refused client must not consume the assertion")
		})
	}
}

func TestIDJAGHandler_DefaultAudiencesWithoutIssuer(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	validator := &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}
	h := newIDJAGTestHandler(t, validator, &recordingAssertionConsumer{})

	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), newIDJAGRequest(t, tj, idJAGTestClientID)))
	assert.Equal(t, []string{testTokenEndpoint}, validator.gotDefaults)
}

func TestIDJAGHandler_IssuesBoundSession(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	claims := validIDJAGClaims()
	consumer := &recordingAssertionConsumer{}
	validator := &testJWTBearerAssertionValidator{claims: claims}
	h := newIDJAGTestHandlerWithIssuer(t, validator, consumer, idJAGTestASIssuer)
	req := newIDJAGRequest(t, tj, idJAGTestClientID)

	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))
	assert.Equal(t, []string{testTokenEndpoint, idJAGTestASIssuer}, validator.gotDefaults)

	// Replay consumption happened under the ID-JAG purpose with the JAG's jti —
	// never the plain handler's purpose or an assertion-hash fallback.
	assert.Equal(t, idJAGReplayPurpose, consumer.purpose)
	assert.Equal(t, idJAGTestIssuer, consumer.issuer)
	assert.Equal(t, claims.JWTID, consumer.key)

	issued, ok := req.GetSession().(*session.Session)
	require.True(t, ok, "expected the handler to install a *session.Session")
	assert.Equal(t, idJAGTestIssuer+"#"+idJAGTestSubject, issued.GetSubject())
	assert.Equal(t, true, issued.JWTClaims.Extra[claimnames.NoUpstreamSessionKey],
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
		wantDesc    string
	}{
		{
			name: "replay is rejected as invalid_grant", consumerErr: fosite.ErrJTIKnown, wantErrIs: fosite.ErrInvalidGrant,
			wantDesc: "invalid_assertion: The JWT bearer assertion has already been used.",
		},
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
			if tt.wantDesc != "" {
				requireRefusal(t, err, "invalid_grant", tt.wantDesc)
			}
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

func TestIDJAGHandler_MaxAgeIsCheckedBeforeResource(t *testing.T) {
	t.Parallel()

	claims := validIDJAGClaims()
	claims.IssuedAt = time.Now().Add(-2 * time.Hour)
	h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: claims}, &recordingAssertionConsumer{})
	req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
	delete(req.Form, "resource")

	err := h.HandleTokenEndpointRequest(context.Background(), req)
	requireRefusal(t, err, "invalid_grant", "invalid_assertion: The JWT bearer assertion exceeds the configured maximum age.")
	assert.NotErrorIs(t, err, fosite.ErrInvalidRequest)
}

const (
	idJAGTestASIssuer = "https://as.example.com"
	idJAGTestOtherAud = "https://other.example.com"
	// idJAGTestSecondResource is governed and bound like idJAGTestResource.
	idJAGTestSecondResource = "https://mcp2.example.com"
	// idJAGTestUnboundResource is governed but outside the subject binding.
	idJAGTestUnboundResource = "https://unbound.example.com"
	// idJAGTestForeignResource is not governed by the departure handler.
	idJAGTestForeignResource = "https://foreign.example.com"
	hintNoAcceptedAud        = "this server's issuer identifier is accepted by default; have the IdP use it as aud"
	hintAcceptedAudMissed    = "add this server's issuer identifier to jwt_bearer_grant.accepted_audiences"
	hintIdPUsesWrongAud      = "have the IdP use this server's issuer identifier as aud"
)

// newIDJAGDepartureHandler builds a handler whose AS issuer identifier is
// asIssuer, whose trusted issuers all carry acceptedAudiences, whose governed
// resources are idJAGTestResource, idJAGTestSecondResource and
// idJAGTestUnboundResource, and whose departure lines go to the returned
// buffer. The logger is handler-local, so
// the process-wide default logger is untouched and tests stay parallel.
func newIDJAGDepartureHandler(
	t *testing.T, claims *ValidatedClaims, asIssuer string, acceptedAudiences []string, trustedIssuerURLs ...string,
) (*IDJAGHandler, *recordingAssertionConsumer, *strings.Builder) {
	t.Helper()
	if len(trustedIssuerURLs) == 0 {
		trustedIssuerURLs = []string{idJAGTestIssuer}
	}
	trusted := make([]TrustedIssuer, 0, len(trustedIssuerURLs))
	for _, issuerURL := range trustedIssuerURLs {
		trusted = append(trusted, TrustedIssuer{
			IssuerURL:              issuerURL,
			AllowedDelegateClients: []string{anyDelegateClient},
			JWTBearerGrant: &JWTBearerGrantPolicy{
				MaxAssertionAge:   time.Hour.String(),
				AcceptedAudiences: acceptedAudiences,
				SubjectBindings: []JWTBearerSubjectBinding{
					{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource, idJAGTestSecondResource}},
				},
			},
		})
	}
	resolved, err := ResolveJWTBearerGrantPolicies(trusted)
	require.NoError(t, err)
	consumer := &recordingAssertionConsumer{}
	handler, err := newIDJAGIssuanceHandler(
		&testJWTBearerAssertionValidator{claims: claims},
		testTokenEndpoint,
		consumer,
		&fosite.Config{
			AccessTokenIssuer:        asIssuer,
			AccessTokenLifespan:      time.Hour,
			AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy,
		},
		&mockAccessTokenStrategy{},
		&mockAccessTokenStorage{},
		resolved,
		nil, []string{idJAGTestResource, idJAGTestSecondResource, idJAGTestUnboundResource}, nil,
	)
	require.NoError(t, err)
	var buf strings.Builder
	handler.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return handler, consumer, &buf
}

func logLinesAt(out *strings.Builder, level string) []string {
	var lines []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "level="+level+" ") {
			lines = append(lines, line)
		}
	}
	return lines
}

func nonEmptyLines(out *strings.Builder) []string {
	var lines []string
	for _, line := range strings.Split(out.String(), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestIDJAGHandler_LogsDraftDepartures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		asIssuer string
		accepted []string
		audience []string
		want     []draftDeparture
		wantHint string // expected hint on the audience_not_issuer line; empty means no hint attr
	}{
		{
			name:     "conforming ID-JAG logs nothing",
			asIssuer: idJAGTestASIssuer,
			audience: []string{idJAGTestASIssuer},
		},
		{
			name:     "audience is the token endpoint only, accepted_audiences empty",
			asIssuer: idJAGTestASIssuer,
			audience: []string{testTokenEndpoint},
			want:     []draftDeparture{departureAudienceNotIssuer},
			wantHint: hintNoAcceptedAud,
		},
		{
			name:     "accepted_audiences lacks the issuer",
			asIssuer: idJAGTestASIssuer,
			accepted: []string{idJAGTestOtherAud},
			audience: []string{idJAGTestOtherAud},
			want:     []draftDeparture{departureAudienceNotIssuer},
			wantHint: hintAcceptedAudMissed,
		},
		{
			name:     "accepted_audiences has the issuer but the IdP does not use it",
			asIssuer: idJAGTestASIssuer,
			accepted: []string{idJAGTestASIssuer, idJAGTestOtherAud},
			audience: []string{idJAGTestOtherAud},
			want:     []draftDeparture{departureAudienceNotIssuer},
			wantHint: hintIdPUsesWrongAud,
		},
		{
			name:     "unknown AS issuer skips the audience check",
			audience: []string{testTokenEndpoint},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tj := newTestJWKS(t)
			claims := validIDJAGClaims()
			claims.Audience = tt.audience
			h, consumer, out := newIDJAGDepartureHandler(t, claims, tt.asIssuer, tt.accepted)

			redeem := func() {
				req := newIDJAGRequest(t, tj, idJAGTestClientID)
				require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))
				// The outcome is the one an unlogged redemption produces.
				assert.Equal(t, fosite.Arguments{idJAGTestResource}, req.GetGrantedAudience())
				issued, ok := req.GetSession().(*session.Session)
				require.True(t, ok)
				assert.Equal(t, idJAGTestIssuer+"#"+idJAGTestSubject, issued.GetSubject())
				assert.Equal(t, claims.JWTID, consumer.key)
			}

			redeem()
			warns := logLinesAt(out, "WARN")
			require.Len(t, warns, len(tt.want))
			for i, finding := range tt.want {
				assert.Contains(t, warns[i], "finding="+string(finding))
				assert.Contains(t, warns[i], "issuer="+idJAGTestIssuer)
				assert.Contains(t, warns[i], "client_id="+idJAGTestClientID)
				if finding == departureAudienceNotIssuer {
					assert.Contains(t, warns[i], `hint="`+tt.wantHint+`"`)
				} else {
					assert.NotContains(t, warns[i], "hint=")
				}
			}
			assert.Empty(t, logLinesAt(out, "DEBUG"))

			// The same issuer and finding again is only a Debug line.
			redeem()
			assert.Len(t, logLinesAt(out, "WARN"), len(tt.want))
			assert.Len(t, logLinesAt(out, "DEBUG"), len(tt.want))

			for _, secret := range []string{
				idJAGTestSubject, claims.JWTID, testTokenEndpoint, idJAGTestASIssuer, idJAGTestResource, idJAGTestOtherAud,
			} {
				// Hints name the config keys, never values; the AS issuer value is
				// absent from every line.
				assert.NotContains(t, out.String(), secret)
			}
		})
	}
}

func TestIDJAGHandler_RefusedRedemptionLogsNoDeparture(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		prepare      func(claims *ValidatedClaims, consumer *recordingAssertionConsumer, req *fosite.AccessRequest)
		wantErr      error
		wantDesc     string // exact error_description, when set
		wantConsumed bool
	}{
		{
			name: "resource the client may not target",
			prepare: func(_ *ValidatedClaims, _ *recordingAssertionConsumer, req *fosite.AccessRequest) {
				req.Client = &fosite.DefaultClient{ID: idJAGTestClientID, Audience: []string{idJAGTestOtherAud}}
			},
			wantDesc: "resource_denied",
		},
		{
			name: "replayed assertion",
			prepare: func(_ *ValidatedClaims, consumer *recordingAssertionConsumer, _ *fosite.AccessRequest) {
				consumer.err = fosite.ErrJTIKnown
			},
			wantErr: fosite.ErrInvalidGrant,
		},
		{
			name: "assertion expired by the lifetime check",
			prepare: func(claims *ValidatedClaims, _ *recordingAssertionConsumer, _ *fosite.AccessRequest) {
				claims.Expiry = time.Now().Add(-time.Minute)
			},
			wantErr:      fosite.ErrInvalidGrant,
			wantConsumed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each ID-JAG would log an audience departure if it were accepted.
			claims := validIDJAGClaims()
			claims.Audience = []string{testTokenEndpoint}
			h, consumer, out := newIDJAGDepartureHandler(t, claims, idJAGTestASIssuer, nil)
			req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			tt.prepare(claims, consumer, req)

			err := h.HandleTokenEndpointRequest(t.Context(), req)

			if tt.wantDesc != "" {
				requireRefusal(t, err, "access_denied", tt.wantDesc)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
			assert.Empty(t, out.String())
			if tt.wantConsumed {
				assert.Equal(t, claims.JWTID, consumer.key, "the lifetime check must run after the assertion is consumed")
			}
		})
	}
}

func TestIDJAGHandler_ReplayAfterAcceptanceLogsNothingNew(t *testing.T) {
	t.Parallel()

	claims := validIDJAGClaims()
	claims.Audience = []string{testTokenEndpoint}
	h, consumer, out := newIDJAGDepartureHandler(t, claims, idJAGTestASIssuer, nil)
	tj := newTestJWKS(t)

	require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), newIDJAGRequest(t, tj, idJAGTestClientID)))
	before := len(nonEmptyLines(out))
	require.Equal(t, 1, before)

	consumer.err = fosite.ErrJTIKnown
	err := h.HandleTokenEndpointRequest(t.Context(), newIDJAGRequest(t, tj, idJAGTestClientID))

	require.ErrorIs(t, err, fosite.ErrInvalidGrant)
	assert.Len(t, nonEmptyLines(out), before)
}

func TestIDJAGHandler_WarnOnceKeyIsIssuer(t *testing.T) {
	t.Parallel()

	const (
		issuerA = idJAGTestIssuer
		issuerB = "https://idp-b.example.com"
	)
	// The same validator claims are changed between redemptions.
	claims := validIDJAGClaims()
	h, _, out := newIDJAGDepartureHandler(t, claims, idJAGTestASIssuer, nil, issuerA, issuerB)
	tj := newTestJWKS(t)

	redeem := func(issuer, jti string) {
		claims.Issuer = issuer
		claims.JWTID = jti
		claims.Audience = []string{testTokenEndpoint}
		require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), newIDJAGRequest(t, tj, idJAGTestClientID)))
	}

	redeem(issuerA, "jti-1")
	require.Len(t, logLinesAt(out, "WARN"), 1)
	assert.Contains(t, logLinesAt(out, "WARN")[0], "issuer="+issuerA)

	redeem(issuerB, "jti-2")
	require.Len(t, logLinesAt(out, "WARN"), 2)
	assert.Contains(t, logLinesAt(out, "WARN")[1], "issuer="+issuerB)

	redeem(issuerA, "jti-3")
	assert.Len(t, logLinesAt(out, "WARN"), 2)
	require.Len(t, logLinesAt(out, "DEBUG"), 1)
	assert.Contains(t, logLinesAt(out, "DEBUG")[0], "finding="+string(departureAudienceNotIssuer))
}

const requestedResourceHint = "Exactly one resource parameter is required for the JWT-bearer grant."

// jsonNull marks a resource claim that is present with a JSON null value.
type jsonNull struct{}

func TestIDJAGHandler_ResourceDefaultsFromClaim(t *testing.T) {
	t.Parallel()

	const (
		bound      = idJAGTestResource
		bound2     = idJAGTestSecondResource
		unbound    = idJAGTestUnboundResource
		foreign    = idJAGTestForeignResource
		notGoverns = "invalid_assertion: The ID-JAG assertion resource claim names a resource this server does not govern."
		malformed  = "invalid_assertion: The ID-JAG assertion resource claim is malformed."
	)
	tests := []struct {
		name         string
		form         func(url.Values)
		claim        any // nil leaves the claim absent; jsonNull{} sets it to JSON null
		clientAud    []string
		wantErrIs    error
		wantCode     string // OAuth error code, for errors that are not sentinel values
		wantDesc     string // exact error_description, when set
		wantAudience string
	}{
		{
			name:         "absent param and string claim is accepted",
			claim:        bound,
			wantAudience: bound,
		},
		{
			name:         "absent param and one-element array claim is accepted",
			claim:        []any{bound},
			wantAudience: bound,
		},
		{name: "absent param and two-value claim is refused", claim: []any{bound, unbound}, wantErrIs: fosite.ErrInvalidRequest},
		{name: "absent param and no claim is refused", wantErrIs: fosite.ErrInvalidRequest},
		{
			name:     "malformed claim is an invalid assertion whatever the param",
			form:     func(f url.Values) { f.Set("resource", bound) },
			claim:    jsonNull{},
			wantCode: "invalid_grant",
			wantDesc: malformed,
		},
		{
			name:     "malformed claim without a param is an invalid assertion",
			claim:    jsonNull{},
			wantCode: "invalid_grant",
			wantDesc: malformed,
		},
		{
			name:     "governed param outside a string claim is invalid_target",
			form:     func(f url.Values) { f.Set("resource", unbound) },
			claim:    bound,
			wantCode: "invalid_target",
			wantDesc: server.ErrInvalidTarget.DescriptionField,
		},
		{
			name:         "param covered by an array claim is accepted",
			form:         func(f url.Values) { f.Set("resource", bound) },
			claim:        []any{unbound, bound},
			wantAudience: bound,
		},
		{
			name:     "claim with a value this server does not govern is refused even for a governed param",
			form:     func(f url.Values) { f.Set("resource", bound) },
			claim:    []any{bound, foreign},
			wantCode: "invalid_grant",
			wantDesc: notGoverns,
		},
		{
			name:     "claim with a non-URI value is refused without quoting it",
			form:     func(f url.Values) { f.Set("resource", bound) },
			claim:    []any{bound, "not-a-uri"},
			wantCode: "invalid_grant",
			wantDesc: notGoverns,
		},
		{
			name:     "string claim this server does not govern is refused for a governed param",
			form:     func(f url.Values) { f.Set("resource", bound) },
			claim:    foreign,
			wantCode: "invalid_grant",
			wantDesc: notGoverns,
		},
		{
			name:     "string claim this server does not govern is refused without a param",
			claim:    foreign,
			wantCode: "invalid_grant",
			wantDesc: notGoverns,
		},
		{
			name:      "param listed by the claim but outside the subject binding is refused",
			form:      func(f url.Values) { f.Set("resource", unbound) },
			claim:     []any{bound, unbound},
			clientAud: []string{bound, unbound},
			wantCode:  "access_denied",
			wantDesc:  "resource_denied",
		},
		{
			name:         "param listed by a bound two-value claim is accepted with only that audience",
			form:         func(f url.Values) { f.Set("resource", bound2) },
			claim:        []any{bound, bound2},
			clientAud:    []string{bound2},
			wantAudience: bound2,
		},
		{
			name:      "empty param wins over the claim",
			form:      func(f url.Values) { f.Set("resource", "") },
			claim:     bound,
			wantErrIs: fosite.ErrInvalidRequest,
		},
		{
			name:      "repeated params win over the claim",
			form:      func(f url.Values) { f["resource"] = []string{bound, bound} },
			claim:     bound,
			wantErrIs: fosite.ErrInvalidRequest,
		},
		{
			name:      "claim outside the subject binding is refused as a requested one is",
			claim:     unbound,
			clientAud: []string{unbound},
			wantCode:  "access_denied",
			wantDesc:  "resource_denied",
		},
		{
			name:      "claim outside the client audiences is refused",
			claim:     bound,
			clientAud: []string{idJAGTestOtherAud},
			wantCode:  "access_denied",
			wantDesc:  "resource_denied",
		},
		{
			name:     "claim that is not an absolute URI is refused without quoting it",
			claim:    "not-a-uri",
			wantCode: "invalid_grant",
			wantDesc: notGoverns,
		},
		{
			name:     "claim that does not parse as a URI is refused without quoting it",
			claim:    "https://secret-claim-value.example.com/%zz",
			wantCode: "invalid_grant",
			wantDesc: notGoverns,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validIDJAGClaims()
			switch tt.claim.(type) {
			case nil:
			case jsonNull:
				claims.Extra["resource"] = nil
			default:
				claims.Extra["resource"] = tt.claim
			}
			h, consumer, _ := newIDJAGDepartureHandler(t, claims, idJAGTestASIssuer, nil)
			req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			delete(req.Form, "resource")
			if tt.form != nil {
				tt.form(req.Form)
			}
			if tt.clientAud != nil {
				req.Client = &fosite.DefaultClient{ID: idJAGTestClientID, Audience: tt.clientAud}
			}

			err := h.HandleTokenEndpointRequest(t.Context(), req)
			if tt.wantErrIs == nil && tt.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, fosite.Arguments{tt.wantAudience}, req.GetGrantedAudience())
				assert.NotEmpty(t, consumer.key, "an accepted redemption consumes the assertion")
				return
			}
			require.Error(t, err)
			switch {
			case tt.wantCode != "":
				var oauthErr *fosite.RFC6749Error
				require.True(t, errors.As(err, &oauthErr))
				assert.Equal(t, tt.wantCode, oauthErr.ErrorField)
				assert.Equal(t, http.StatusBadRequest, oauthErr.CodeField)
				if tt.wantDesc != "" {
					assert.Equal(t, tt.wantDesc, oauthErr.GetDescription())
				}
			default:
				assert.ErrorIs(t, err, tt.wantErrIs)
				if errors.Is(tt.wantErrIs, fosite.ErrInvalidRequest) {
					var oauthErr *fosite.RFC6749Error
					require.True(t, errors.As(err, &oauthErr))
					assert.Equal(t, requestedResourceHint, oauthErr.HintField)
				}
			}
			assert.Empty(t, consumer.key, "a refused redemption must not consume the assertion")
		})
	}
}

func TestIDJAGHandler_IssuerAudienceSkippedWhenIssuerIsResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		allowed         []string
		wantDefaults    []string
		wantWarnIssuer  bool
		acceptedFromIdP []string
		// wantNotIssuer is whether the redemption, whose aud does not name the
		// issuer, logs audience_not_issuer. It is never logged when the issuer
		// is a resource audience, because no aud could name it.
		wantNotIssuer bool
	}{
		{name: "issuer is a resource audience", allowed: []string{idJAGTestASIssuer, idJAGTestResource},
			wantDefaults: []string{testTokenEndpoint}, wantWarnIssuer: true},
		{name: "issuer is not a resource audience", allowed: []string{idJAGTestResource},
			wantDefaults: []string{testTokenEndpoint, idJAGTestASIssuer}, wantNotIssuer: true},
		{name: "configured accepted_audiences need no warning", allowed: []string{idJAGTestASIssuer},
			wantDefaults: []string{testTokenEndpoint}, acceptedFromIdP: []string{idJAGTestOtherAud}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{{
				IssuerURL:              idJAGTestIssuer,
				AllowedDelegateClients: []string{anyDelegateClient},
				JWTBearerGrant: &JWTBearerGrantPolicy{
					MaxAssertionAge:   time.Hour.String(),
					AcceptedAudiences: tt.acceptedFromIdP,
					SubjectBindings: []JWTBearerSubjectBinding{
						{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
					},
				},
			}})
			require.NoError(t, err)
			var buf strings.Builder
			validator := &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}
			h, err := newIDJAGIssuanceHandler(validator, testTokenEndpoint, &recordingAssertionConsumer{},
				&fosite.Config{
					AccessTokenIssuer:        idJAGTestASIssuer,
					AccessTokenLifespan:      time.Hour,
					AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy,
				},
				&mockAccessTokenStrategy{}, &mockAccessTokenStorage{}, resolved, nil, tt.allowed,
				slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			require.NoError(t, err)

			warns := logLinesAt(&buf, "WARN")
			if tt.wantWarnIssuer {
				require.Len(t, warns, 1)
				assert.Contains(t, warns[0], idJAGTestASIssuer)
			} else {
				assert.Empty(t, warns)
			}
			buf.Reset()

			req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))
			assert.Equal(t, tt.wantDefaults, validator.gotDefaults)
			assert.Equal(t, tt.wantNotIssuer,
				strings.Contains(buf.String(), "finding="+string(departureAudienceNotIssuer)))
		})
	}
}

func TestIDJAGHandler_RealValidatorRedeemsIssuerAudience(t *testing.T) {
	t.Parallel()

	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	trusted := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: "ext-user-456", AllowedResources: []string{idJAGTestResource}},
			},
		},
	}}
	validator := newMultiValidator(t, newTestJWKS(t), trusted)
	resolved, err := ResolveJWTBearerGrantPolicies(trusted)
	require.NoError(t, err)
	consumer := &recordingAssertionConsumer{}
	h, err := newIDJAGIssuanceHandler(validator, testTokenEndpoint, consumer,
		&fosite.Config{
			AccessTokenIssuer:        idJAGTestASIssuer,
			AccessTokenLifespan:      time.Hour,
			AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy,
		},
		&mockAccessTokenStrategy{}, &mockAccessTokenStorage{}, resolved, nil, []string{idJAGTestResource}, nil)
	require.NoError(t, err)

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: externalJWKS.jwk},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderType, oauthproto.JWTTypeIDJAG))
	require.NoError(t, err)
	claims := jwtBearerExternalClaims()
	claims.Audience = jwt.Audience{idJAGTestASIssuer}
	raw, err := jwt.Signed(signer).Claims(claims).Claims(map[string]any{"client_id": idJAGTestClientID}).Serialize()
	require.NoError(t, err)

	req := newIDJAGRequest(t, externalJWKS, idJAGTestClientID)
	req.Form.Set("assertion", raw)
	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), req))
	assert.Equal(t, "jwt-bearer-jti", consumer.key)
}
