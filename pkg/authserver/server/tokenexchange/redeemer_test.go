// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// These tests cover the redemption steps the JWT-bearer and ID-JAG handlers
// share (consume, lifetime, session, client and populate), exercised through
// both handlers' public methods.

// nonAccessRequest hides the *fosite.AccessRequest behind another type, so a
// type assertion to *fosite.AccessRequest fails.
type nonAccessRequest struct {
	*fosite.AccessRequest
}

// redemptionIssuers returns one resolved trusted issuer for idJAGTestIssuer
// that accepts both assertion types.
func redemptionIssuers(t *testing.T) []TrustedIssuer {
	t.Helper()
	resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{testRedemptionIssuer(idJAGTestIssuer, nil)})
	require.NoError(t, err)
	return resolved
}

func TestRedeemer_CompleteIssuesSession(t *testing.T) {
	t.Parallel()

	const fiveMinutes = 5 * time.Minute
	tests := []struct {
		name         string
		idJAG        bool
		noJTI        bool
		lifespan     time.Duration // 0 means one hour
		resolved     fosite.Client // plain: the client fosite resolved
		wrap         bool          // hide the *fosite.AccessRequest
		wantLifetime time.Duration
	}{
		{name: "plain with jti", wantLifetime: fiveMinutes},
		{name: "plain without jti", noJTI: true, wantLifetime: fiveMinutes},
		{
			name:         "plain replaces a public client fosite resolved",
			resolved:     &fosite.DefaultClient{ID: "resolved-public-client", Public: true},
			wantLifetime: fiveMinutes,
		},
		{name: "plain lifespan shorter than the assertion", lifespan: 2 * time.Minute, wantLifetime: 2 * time.Minute},
		{name: "ID-JAG", idJAG: true, wantLifetime: fiveMinutes},
		{name: "ID-JAG lifespan shorter than the assertion", idJAG: true, lifespan: 2 * time.Minute, wantLifetime: 2 * time.Minute},
		{name: "ID-JAG through a requester that is not a *fosite.AccessRequest", idJAG: true, wrap: true, wantLifetime: fiveMinutes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validIDJAGClaims()
			if tt.noJTI {
				claims.JWTID = ""
			}
			validator := &testJWTBearerAssertionValidator{claims: claims}
			deps := testIssuanceDeps(validator, redemptionIssuers(t))
			if tt.lifespan != 0 {
				deps.config.AccessTokenLifespan = tt.lifespan
			}

			var (
				req       *fosite.AccessRequest
				assertion string
			)
			if tt.idJAG {
				req = newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
				assertion = req.Form.Get("assertion")
			} else {
				assertion = signAssertionWithType(t, newTestJWKS(t), nil)
				req = newJWTBearerRequest(url.Values{"assertion": {assertion}, "resource": {idJAGTestResource}})
				req.Client = tt.resolved
			}
			var requester fosite.AccessRequester = req
			if tt.wrap {
				requester = nonAccessRequest{req}
			}

			wantPurpose, wantKey, wantClientID := idJAGReplayPurpose, claims.JWTID, idJAGTestClientID
			if !tt.idJAG {
				wantPurpose, wantClientID = jwtBearerReplayPurpose, jwtBearerClientID(idJAGTestIssuer, idJAGTestSubject)
				if tt.noJTI {
					segments, err := canonicalAssertionSegments(assertion)
					require.NoError(t, err)
					wantPurpose, wantKey = jwtBearerNoJTIReplayPurpose, assertionReplayKey(segments, "")
				}
			}
			consumer := storagemocks.NewMockAssertionJWTConsumer(gomock.NewController(t))
			consumer.EXPECT().
				ConsumeAssertionJWT(gomock.Any(), wantPurpose, idJAGTestIssuer, wantKey, claims.Expiry).
				Return(nil)
			deps.consumer = consumer
			var handler fosite.TokenEndpointHandler
			if tt.idJAG {
				handler = newTestIDJAGHandler(t, deps)
			} else {
				handler = newTestJWTBearerHandler(t, deps)
			}

			require.NoError(t, handler.HandleTokenEndpointRequest(context.Background(), requester))
			assert.Equal(t, []string{testTokenEndpoint}, validator.gotDefaults)

			issued, ok := req.GetSession().(*session.Session)
			require.True(t, ok, "expected the handler to install a *session.Session")
			assert.Equal(t, idJAGTestIssuer+"#"+idJAGTestSubject, issued.GetSubject())
			assert.Equal(t, wantClientID, issued.JWTClaims.Extra[session.ClientIDClaimKey])
			assert.Equal(t, wantClientID, req.GetClient().GetID())
			// A resource server relies on this marker to tell an intentionally
			// session-less token from an identity that carries no upstream
			// credentials; dropping it sends JWT-bearer requests to a hard deny
			// under a pinned Cedar provider.
			assert.Equal(t, true, issued.JWTClaims.Extra[claimnames.NoUpstreamSessionKey])
			assert.NotContains(t, issued.JWTClaims.Extra, claimnames.TokenSessionIDKey)
			if tt.idJAG {
				assert.Equal(t, claims.Extra["act"], issued.JWTClaims.Extra["act"])
			} else {
				require.Contains(t, claims.Extra, "act", "the fixture must carry an act claim")
				assert.NotContains(t, issued.JWTClaims.Extra, "act")
			}
			assert.WithinDuration(t, time.Now().Add(tt.wantLifetime), issued.GetExpiresAt(fosite.AccessToken), 10*time.Second)
			assert.Equal(t, fosite.Arguments{idJAGTestResource}, req.GetGrantedAudience())
		})
	}
}

// TestRedeemer_CompleteRefusalsConsumeTheAssertion covers the refusals that
// happen after the assertion is consumed, so a retry sees a replay.
func TestRedeemer_CompleteRefusalsConsumeTheAssertion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		claim func(*ValidatedClaims)
		wrap  bool
		check func(*testing.T, error)
	}{
		{
			name: "plain assertion expired at the lifetime step",
			claim: func(c *ValidatedClaims) {
				c.IssuedAt = time.Now().Add(-2 * time.Minute)
				c.Expiry = time.Now().Add(-time.Minute)
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				requireRefusal(t, err, "invalid_grant", "invalid_assertion: The JWT bearer assertion has expired.")
			},
		},
		{
			name: "plain requester is not a *fosite.AccessRequest",
			wrap: true,
			check: func(t *testing.T, err error) {
				t.Helper()
				want := fosite.ErrServerError.WithHint("The JWT-bearer grant requires a *fosite.AccessRequest.")
				requireRefusalStatus(t, err, want.CodeField, want.ErrorField, want.GetDescription())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validIDJAGClaims()
			if tt.claim != nil {
				tt.claim(claims)
			}
			consumer := &recordingAssertionConsumer{}
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: claims}, redemptionIssuers(t))
			deps.consumer = consumer
			handler := newTestJWTBearerHandler(t, deps)
			req := newJWTBearerRequest(url.Values{
				"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)},
				"resource":  {idJAGTestResource},
			})
			var requester fosite.AccessRequester = req
			if tt.wrap {
				requester = nonAccessRequest{req}
			}

			err := handler.HandleTokenEndpointRequest(context.Background(), requester)

			tt.check(t, err)
			assert.Equal(t, claims.JWTID, consumer.key)
		})
	}
}

func TestRedeemer_ConsumeErrors(t *testing.T) {
	t.Parallel()

	consumerErrs := []struct {
		name      string
		err       error
		wantErrIs error
		wantDesc  string
	}{
		{
			name: "replay is rejected as invalid_grant", err: fosite.ErrJTIKnown, wantErrIs: fosite.ErrInvalidGrant,
			wantDesc: "invalid_assertion: The JWT bearer assertion has already been used.",
		},
		{name: "storage failure surfaces as a server error", err: errors.New("redis: connection refused"), wantErrIs: fosite.ErrServerError},
	}
	for _, idJAG := range []bool{false, true} {
		handlerName := "plain"
		if idJAG {
			handlerName = "ID-JAG"
		}
		for _, ce := range consumerErrs {
			t.Run(handlerName+": "+ce.name, func(t *testing.T) {
				t.Parallel()
				deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: validIDJAGClaims()}, redemptionIssuers(t))
				deps.consumer = &recordingAssertionConsumer{err: ce.err}
				var (
					handler fosite.TokenEndpointHandler
					req     *fosite.AccessRequest
				)
				if idJAG {
					handler = newTestIDJAGHandler(t, deps)
					req = newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
				} else {
					handler = newTestJWTBearerHandler(t, deps)
					req = newJWTBearerRequest(url.Values{
						"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)},
						"resource":  {idJAGTestResource},
					})
				}

				err := handler.HandleTokenEndpointRequest(context.Background(), req)

				require.Error(t, err)
				assert.ErrorIs(t, err, ce.wantErrIs)
				if ce.wantDesc != "" {
					requireRefusal(t, err, "invalid_grant", ce.wantDesc)
				}
			})
		}
	}
}

func TestRedeemer_Populate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		idJAG     bool // the handler under test
		reqIDJAG  bool // the request carries an ID-JAG assertion
		expiresIn time.Duration
		wantErr   func(*testing.T, error)
	}{
		{name: "plain: session expiry decides expires_in", expiresIn: 5 * time.Minute},
		{name: "ID-JAG: session expiry decides expires_in", idJAG: true, reqIDJAG: true, expiresIn: 5 * time.Minute},
		{
			name: "ID-JAG: elapsed session expiry", idJAG: true, reqIDJAG: true, expiresIn: -time.Second,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				requireRefusal(t, err, "invalid_grant", "invalid_assertion: The JWT bearer assertion has expired.")
			},
		},
		{
			name: "plain: elapsed session expiry", expiresIn: -time.Second,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				requireRefusal(t, err, "invalid_grant", "invalid_assertion: The JWT bearer assertion has expired.")
			},
		},
		{
			name: "plain: ignores an ID-JAG request", reqIDJAG: true,
			wantErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, fosite.ErrUnknownRequest)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tokenStorage := &mockAccessTokenStorage{}
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: validIDJAGClaims()}, redemptionIssuers(t))
			deps.tokenStorage = tokenStorage
			var handler fosite.TokenEndpointHandler
			if tt.idJAG {
				handler = newTestIDJAGHandler(t, deps)
			} else {
				handler = newTestJWTBearerHandler(t, deps)
			}
			var typ any
			if tt.reqIDJAG {
				typ = oauthproto.JWTTypeIDJAG
			}
			sess := session.New("ext-user", "", "client", session.UserClaims{})
			sess.SetExpiresAt(fosite.AccessToken, time.Now().Add(tt.expiresIn))
			req := fosite.NewAccessRequest(sess)
			req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
			req.Form = url.Values{"assertion": {signAssertionWithType(t, newTestJWKS(t), typ)}}
			responder := fosite.NewAccessResponse()

			err := handler.PopulateTokenEndpointResponse(context.Background(), req, responder)

			if tt.wantErr != nil {
				tt.wantErr(t, err)
				assert.Empty(t, responder.GetAccessToken())
				assert.Empty(t, tokenStorage.createdSig)
				return
			}
			require.NoError(t, err)
			assert.NotEmpty(t, responder.GetAccessToken())
			// The one-hour lifespan does not stretch a session that ends sooner.
			assert.InDelta(t, int64(tt.expiresIn.Seconds()), responder.GetExtra("expires_in"), 10)
		})
	}
}

// TestRedeemer_ClientAccessTokenLifespan covers the per-client lifespan end to
// end: the token's exp and the response's expires_in both come from it.
func TestRedeemer_ClientAccessTokenLifespan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		plain           bool          // redeem a plain assertion instead of an ID-JAG
		assertionLeft   time.Duration // the assertion's remaining validity
		serverLifespan  time.Duration // 0 means one hour
		clientLifespans map[string]time.Duration
		want            time.Duration
	}{
		{
			name:            "client lifespan shorter than the assertion",
			assertionLeft:   5 * time.Minute,
			clientLifespans: map[string]time.Duration{idJAGTestClientID: 30 * time.Second},
			want:            30 * time.Second,
		},
		{
			name:            "client lifespan longer than the assertion is shortened",
			assertionLeft:   20 * time.Second,
			clientLifespans: map[string]time.Duration{idJAGTestClientID: time.Minute},
			want:            20 * time.Second,
		},
		{
			name:            "client lifespan longer than the server lifespan",
			assertionLeft:   5 * time.Minute,
			serverLifespan:  time.Minute,
			clientLifespans: map[string]time.Duration{idJAGTestClientID: 4 * time.Minute},
			want:            4 * time.Minute,
		},
		{
			name:            "a client without a lifespan gets the server lifespan",
			assertionLeft:   5 * time.Minute,
			serverLifespan:  2 * time.Minute,
			clientLifespans: map[string]time.Duration{"other-client": 10 * time.Second},
			want:            2 * time.Minute,
		},
		{
			// A pin, not a probe: the plain handler has no client lifespans to
			// consult, so its redemption leaves clientLifespan zero and keeps
			// the server lifespan. The ID-JAG handler built from the same deps
			// has a lifespan for the client_id the plain token carries, so a
			// change that routed plain redemption through it would fail here.
			name:           "plain grant ignores it",
			plain:          true,
			assertionLeft:  5 * time.Minute,
			serverLifespan: 2 * time.Minute,
			clientLifespans: map[string]time.Duration{
				jwtBearerClientID(idJAGTestIssuer, idJAGTestSubject): 10 * time.Second,
			},
			want: 2 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validIDJAGClaims()
			claims.Expiry = time.Now().Add(tt.assertionLeft)
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: claims}, redemptionIssuers(t))
			if tt.serverLifespan != 0 {
				deps.config.AccessTokenLifespan = tt.serverLifespan
			}
			idJAGHandler := newTestIDJAGHandler(t, deps)
			idJAGHandler.clientLifespans = tt.clientLifespans
			handler, req := fosite.TokenEndpointHandler(idJAGHandler), newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			if tt.plain {
				handler = newTestJWTBearerHandler(t, deps)
				req = newJWTBearerRequest(url.Values{
					"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)},
					"resource":  {idJAGTestResource},
				})
			}

			require.NoError(t, handler.HandleTokenEndpointRequest(context.Background(), req))
			resp := fosite.NewAccessResponse()
			require.NoError(t, handler.PopulateTokenEndpointResponse(context.Background(), req, resp))

			assert.WithinDuration(t, time.Now().Add(tt.want), req.GetSession().GetExpiresAt(fosite.AccessToken), 5*time.Second)
			assert.InDelta(t, int64(tt.want.Seconds()), resp.GetExtra("expires_in"), 5, "expires_in must match exp")
		})
	}
}

func TestRedeemer_DelegatedAccessLifetime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		lifespan       time.Duration
		clientLifespan time.Duration
		remaining      time.Duration
		marked         bool
		want           time.Duration
		wantErr        bool
	}{
		{name: "marked, longer lifespan, longer validity", lifespan: time.Hour, remaining: 10 * time.Minute,
			marked: true, want: 5 * time.Minute},
		{name: "marked, short lifespan", lifespan: 2 * time.Minute, remaining: 10 * time.Minute,
			marked: true, want: 2 * time.Minute},
		{name: "marked, short validity", lifespan: time.Hour, remaining: time.Minute, marked: true, want: time.Minute},
		{name: "not marked is not capped", lifespan: time.Hour, remaining: 10 * time.Minute, want: 10 * time.Minute},
		{name: "marked and expired", lifespan: time.Hour, remaining: -time.Minute, marked: true, wantErr: true},
		{name: "marked, client lifespan above the cap", lifespan: time.Minute, clientLifespan: 10 * time.Minute,
			remaining: 10 * time.Minute, marked: true, want: 5 * time.Minute},
		{name: "marked, client lifespan below the cap", lifespan: time.Hour, clientLifespan: 30 * time.Second,
			remaining: 10 * time.Minute, marked: true, want: 30 * time.Second},
		{name: "marked, validity shorter than the client lifespan", lifespan: time.Hour, clientLifespan: 4 * time.Minute,
			remaining: time.Minute, marked: true, want: time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{}, redemptionIssuers(t))
			deps.config.AccessTokenLifespan = tt.lifespan
			r := newRedeemer(deps)
			d := redemption{
				claims:          &ValidatedClaims{Expiry: time.Now().Add(tt.remaining)},
				delegatedAccess: tt.marked,
				clientLifespan:  tt.clientLifespan,
			}

			got, err := r.assertionBoundedLifetime(context.Background(), d)

			if tt.wantErr {
				requireRefusal(t, err, "invalid_grant", "invalid_assertion: The JWT bearer assertion has expired.")
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.want.Seconds(), got.Seconds(), 5)
		})
	}
}

func TestNewRedemptionSession_ActIssuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		act             any
		delegatedAccess bool
		want            any
	}{
		{"marked token fills an absent iss", map[string]any{"sub": "agent"}, true,
			map[string]any{"sub": "agent", "iss": idJAGTestIssuer}},
		{"marked token keeps an explicit iss", map[string]any{"sub": "agent", "iss": "https://actor.example.com"}, true,
			map[string]any{"sub": "agent", "iss": "https://actor.example.com"}},
		{"marked token leaves a nested actor alone",
			map[string]any{"sub": "agent", "act": map[string]any{"sub": "inner"}}, true,
			map[string]any{"sub": "agent", "iss": idJAGTestIssuer, "act": map[string]any{"sub": "inner"}}},
		{"namespaced token is unchanged", map[string]any{"sub": "agent"}, false, map[string]any{"sub": "agent"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validIDJAGClaims()
			claims.Extra["act"] = tt.act
			before := maps.Clone(tt.act.(map[string]any))

			sess := newRedemptionSession(
				redemption{claims: claims, carryAct: true, delegatedAccess: tt.delegatedAccess}, idJAGTestClientID, time.Minute)

			assert.Equal(t, tt.want, sess.JWTClaims.Extra["act"])
			assert.Equal(t, before, claims.Extra["act"], "the assertion's claims must not change")
		})
	}
}
