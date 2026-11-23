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
	return newIDJAGTestHandlerWithIssuer(t, validator, consumer, "")
}

func newIDJAGTestHandlerWithIssuer(
	t *testing.T, validator *testJWTBearerAssertionValidator, consumer *recordingAssertionConsumer, asIssuer string,
) *IDJAGHandler {
	t.Helper()
	deps := testIssuanceDeps(validator, idJAGResolvedIssuers(t))
	deps.consumer = consumer
	deps.config.AccessTokenIssuer = asIssuer
	return newTestIDJAGHandler(t, deps)
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
			// A request with no client credentials may skip fosite's client
			// authentication whether or not this handler claims it: the handler
			// then refuses the placeholder client. Requests that carry
			// credentials are covered by TestIDJAGHandler_CanSkipClientAuth.
			assert.True(t, h.CanSkipClientAuth(ctx, req))
		})
	}
}

func TestIDJAGHandler_ClientBindingAndJTI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mutateClaims  func(*ValidatedClaims)
		requestClient string
		wantDesc      string // exact error_description of an invalid_grant refusal
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
			requireRefusal(t, err, "invalid_grant", tt.wantDesc)
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
				h.audienceStrategy = &fosite.Config{AudienceMatchingStrategy: tt.strategy}
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

func TestIDJAGHandler_PassesASIssuerAsDefaultAudience(t *testing.T) {
	t.Parallel()

	validator := &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}
	h := newIDJAGTestHandlerWithIssuer(t, validator, &recordingAssertionConsumer{}, idJAGTestASIssuer)

	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)))
	assert.Equal(t, []string{testTokenEndpoint, idJAGTestASIssuer}, validator.gotDefaults)
}

func TestIDJAGHandler_DefaultAudiencesWithoutIssuer(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	validator := &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}
	h := newIDJAGTestHandler(t, validator, &recordingAssertionConsumer{})

	require.NoError(t, h.HandleTokenEndpointRequest(context.Background(), newIDJAGRequest(t, tj, idJAGTestClientID)))
	assert.Equal(t, []string{testTokenEndpoint}, validator.gotDefaults)
}

func TestIDJAGHandler_PopulateGatesOnOwnMatcher(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}, &recordingAssertionConsumer{})

	// A plain assertion is not this handler's request, even with an
	// otherwise-complete session. The redeemer is a named field, not an
	// embedded type, so no method is promoted that could answer for it.
	req := newJWTBearerRequest(map[string][]string{"assertion": {signAssertionWithType(t, tj, nil)}})
	err := h.PopulateTokenEndpointResponse(context.Background(), req, fosite.NewAccessResponse())
	assert.ErrorIs(t, err, fosite.ErrUnknownRequest)
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
	deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: claims}, resolved)
	deps.consumer = consumer
	deps.config.AccessTokenIssuer = asIssuer
	deps.allowedAudiences = []string{idJAGTestResource, idJAGTestSecondResource, idJAGTestUnboundResource}
	handler, err := newIDJAGIssuanceHandler(deps, nil, nil)
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
			deps := testIssuanceDeps(validator, resolved)
			deps.config.AccessTokenIssuer = idJAGTestASIssuer
			deps.allowedAudiences = tt.allowed
			h, err := newIDJAGIssuanceHandler(deps, nil,
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

func TestIDJAGHandler_RealValidatorAudience(t *testing.T) {
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
	tests := []struct {
		name        string
		audience    jwt.Audience
		wantRefusal string
	}{
		{name: "issuer as the single aud is redeemed", audience: jwt.Audience{idJAGTestASIssuer}},
		{
			name:        "aud array with two values is refused and not consumed",
			audience:    jwt.Audience{idJAGTestASIssuer, testTokenEndpoint},
			wantRefusal: "invalid_assertion: The ID-JAG assertion aud must name exactly one audience.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			consumer := &recordingAssertionConsumer{}
			deps := testIssuanceDeps(validator, resolved)
			deps.consumer = consumer
			deps.config.AccessTokenIssuer = idJAGTestASIssuer
			deps.allowedAudiences = []string{idJAGTestResource}
			h := newTestIDJAGHandler(t, deps)

			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: externalJWKS.jwk},
				(&jose.SignerOptions{}).WithHeader(jose.HeaderType, oauthproto.JWTTypeIDJAG))
			require.NoError(t, err)
			claims := jwtBearerExternalClaims()
			claims.Audience = tt.audience
			raw, err := jwt.Signed(signer).Claims(claims).Claims(map[string]any{"client_id": idJAGTestClientID}).Serialize()
			require.NoError(t, err)

			req := newIDJAGRequest(t, externalJWKS, idJAGTestClientID)
			req.Form.Set("assertion", raw)
			err = h.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantRefusal != "" {
				requireRefusal(t, err, "invalid_grant", tt.wantRefusal)
				assert.Empty(t, consumer.key, "a refused assertion must not be consumed")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "jwt-bearer-jti", consumer.key)
		})
	}
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
			// jwt_bearer only, so no issuer accepts id_jag: the constructor
			// filters this issuer out itself, leaving an empty policy map.
			AcceptedAssertionTypes: []JWTBearerAssertionType{JWTBearerAssertionTypeJWTBearer},
		},
	}})
	require.NoError(t, err)

	handler := newTestIDJAGHandler(t, testIssuanceDeps(&testJWTBearerAssertionValidator{claims: validIDJAGClaims()}, resolved))

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
	assert.Contains(t, rfcErr.GetDescription(), "issuer is not enabled for this grant")
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
		handler := newTestJWTBearerHandler(t, testIssuanceDeps(&testJWTBearerAssertionValidator{claims: claims}, resolved))
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
		handler := newTestIDJAGHandler(t, testIssuanceDeps(&testJWTBearerAssertionValidator{claims: claims}, resolved))
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
// production JWTBearerIssuanceFactory and pins end to end that an id_jag-only
// issuer stays off the plain (client-auth-skippable) path. The filtering
// itself lives in the two issuance constructors, through issuanceDeps.policiesFor.
func TestJWTBearerIssuanceFactory_GatesOnAcceptedAssertionTypes(t *testing.T) {
	t.Parallel()

	const (
		plainIssuer  = "https://plain-issuer.example.com"
		idJAGIssuer  = "https://id-jag-only-issuer.example.com"
		bearerIssuer = "https://bearer-only-issuer.example.com"
	)
	issuer := func(issuerURL string, types ...JWTBearerAssertionType) TrustedIssuer {
		return testRedemptionIssuer(issuerURL, []string{"https://auth.example.com"}, types...)
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

// TestIDJAGIssuanceFactory_PassesInputsToHandler drives the production
// IDJAGIssuanceFactory and pins that its inputs reach the handler.
func TestIDJAGIssuanceFactory_PassesInputsToHandler(t *testing.T) {
	t.Parallel()

	const explicitID = "explicit-grant-client"
	issuers := []TrustedIssuer{testRedemptionIssuer(idJAGTestIssuer, []string{"https://auth.example.com"})}
	build := func(t *testing.T, explicitGrantClientIDs []string) *IDJAGHandler {
		t.Helper()
		cfg := buildTestAuthServerConfig(t)
		shared, err := NewSharedTrustedIssuerValidator(cfg, issuers)
		require.NoError(t, err)
		t.Cleanup(func() { _ = shared.Close() })

		f, err := IDJAGIssuanceFactory(issuers, shared, explicitGrantClientIDs)
		require.NoError(t, err)
		result, err := f(cfg, &jwtBearerFactoryStorage{
			fakeFactoryStorage:         fakeFactoryStorage{mockAccessTokenStorage: &mockAccessTokenStorage{}},
			recordingAssertionConsumer: &recordingAssertionConsumer{},
		}, &mockAccessTokenStrategy{})
		require.NoError(t, err)
		handler, ok := result.(*IDJAGHandler)
		require.True(t, ok, "factory closure must return *IDJAGHandler, got %T", result)
		// The factory fixes the validator to the shared real one; swap in a
		// fake so the claims are controlled.
		handler.validator = &testJWTBearerAssertionValidator{claims: validIDJAGClaims()}
		return handler
	}
	redeem := func(t *testing.T, handler *IDJAGHandler, clientID string) error {
		t.Helper()
		req := newIDJAGRequest(t, newTestJWKS(t), "")
		req.Client = &fosite.DefaultClient{ID: clientID, Audience: []string{idJAGTestResource}}
		return handler.HandleTokenEndpointRequest(context.Background(), req)
	}

	t.Run("explicit grant clients", func(t *testing.T) {
		t.Parallel()
		handler := build(t, []string{explicitID})

		requireRefusal(t, redeem(t, handler, explicitID), "unauthorized_client",
			fosite.ErrUnauthorizedClient.WithHint("The client is not registered for the jwt-bearer grant.").GetDescription())
		assert.NoError(t, redeem(t, handler, idJAGTestClientID), "a client that is not listed is unaffected")
	})

	t.Run("allowed audiences become the governed resources", func(t *testing.T) {
		t.Parallel()
		handler := build(t, nil)
		handler.validator = &testJWTBearerAssertionValidator{claims: func() *ValidatedClaims {
			claims := validIDJAGClaims()
			claims.Extra["resource"] = idJAGTestForeignResource
			return claims
		}()}

		requireRefusal(t, redeem(t, handler, idJAGTestClientID), "invalid_grant",
			"invalid_assertion: The ID-JAG assertion resource claim names a resource this server does not govern.")
	})
}
