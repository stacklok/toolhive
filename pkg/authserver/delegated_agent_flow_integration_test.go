// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/oauth2-proxy/mockoidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	// flowActIssuer is the agent trust domain: the namespace act.sub is assigned in.
	flowActIssuer = "spiffe://agents.example.com"
	// flowAgentID is an opaque SPIFFE ID in flowActIssuer's trust domain.
	flowAgentID = "spiffe://agents.example.com/agent/ag_7hq2x9"
)

// TestDelegatedAgentFlow runs the session path and the schedule path of a
// delegated agent over HTTP against two in-process authorization servers, on
// both storage backends. Steps run as serial subtests inside a run, because
// postToken derives the client assertion's jti from the clock.
func TestDelegatedAgentFlow(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			t.Run("flow", func(t *testing.T) {
				t.Parallel()
				runDelegatedAgentFlow(t, startFlow(t, backend))
			})
		})
	}
}

func runDelegatedAgentFlow(t *testing.T, f *flow) {
	t.Helper()

	// The logins are checked in startFlow.

	broker := f.as1Env.broker
	entryForm := func(tokenType string, expiresAt *int64) func(url.Values) {
		return func(form url.Values) {
			form.Set("authorization_details", "["+xrtEntry(tokenType, expiresAt)+"]")
		}
	}

	t.Run("open grants", func(t *testing.T) {
		_, sessionClaims := decodeIDJAG(t, f.sessionLogin)
		_, scheduleClaims := decodeIDJAG(t, f.scheduleLogin)

		// Session open, no horizon.
		before := time.Now()
		res := f.as1Env.requestRefreshToken(t, broker, f.sessionLogin, entryForm(oauthproto.TokenTypeIDJAG, nil))
		after := time.Now()
		require.Equal(t, http.StatusOK, res.status, res.body)
		sessionEntry := assertRefreshTokenResponse(t, res, oauthproto.TokenTypeIDJAG, before, after, xrtIDJAGCap)
		f.session = res.str(t, "access_token")
		sessionExp, ok := sessionEntry["expires_at"].(float64)
		require.True(t, ok)

		// Schedule setup, a horizon inside the cap, echoed exactly. It is
		// shorter than xrtIDJAGCap, so a child grant without the parent's bound
		// would expire later than this and the child grant check below can fail.
		horizon := time.Now().Add(6 * time.Hour).Unix()
		before = time.Now()
		res = f.as1Env.requestRefreshToken(t, broker, f.scheduleLogin, entryForm(oauthproto.TokenTypeRefreshToken, &horizon))
		after = time.Now()
		require.Equal(t, http.StatusOK, res.status, res.body)
		scheduleEntry := assertRefreshTokenResponse(t, res, oauthproto.TokenTypeRefreshToken, before, after, xrtRefreshCap)
		f.schedule = res.str(t, "access_token")
		scheduleExp, ok := scheduleEntry["expires_at"].(float64)
		require.True(t, ok)
		require.Equal(t, horizon, int64(scheduleExp), "the horizon is echoed exactly")
		f.scheduleExp = int64(scheduleExp)

		// What the server stored for each grant.
		for _, tc := range []struct {
			name      string
			token     string
			claims    map[string]any
			tokenType string
			expiresAt int64
		}{
			{"session", f.session, sessionClaims, oauthproto.TokenTypeIDJAG, int64(sessionExp)},
			{"schedule", f.schedule, scheduleClaims, oauthproto.TokenTypeRefreshToken, f.scheduleExp},
		} {
			t.Run(tc.name, func(t *testing.T) {
				g := f.as1Env.grant(t, tc.token)
				assert.Equal(t, cfgBrokerID, g.ClientID, tc.name)
				assert.Equal(t, f.sub, g.Subject, tc.name)
				subID, _ := tc.claims["sub_id"].(map[string]any)
				upstream, _ := tc.claims[claimnames.UpstreamSubjectKey].(map[string]any)
				assertJSONEqual(t, subID, g.SubjectIdentifier)
				assertJSONEqual(t, upstream, g.UpstreamSubject)
				assert.Equal(t, []string{tc.tokenType}, g.IssuableTokenTypes, tc.name)
				assert.Equal(t, f.as1, g.SourceIssuer, tc.name)
				assert.Equal(t, tc.claims["jti"], g.SourceJTI, tc.name)
				assert.Empty(t, g.ParentSignature, tc.name)
				assert.False(t, g.CreatedAt.IsZero(), tc.name)
				assert.Equal(t, tc.expiresAt, g.ExpiresAt.Unix(), tc.name)
				assert.True(t, g.RevokedAt.IsZero(), tc.name)
			})
		}

		// A reused login token is refused. The ID-JAG issuance block relies on the
		// first grant staying live. The last assertion is the one that "Revoke the
		// grant a reused subject token created" (#6973) flips.
		res = f.as1Env.requestRefreshToken(t, broker, f.sessionLogin, entryForm(oauthproto.TokenTypeIDJAG, nil))
		requireError(t, res, "invalid_request", "token_used")
		assert.True(t, f.as1Env.grant(t, f.session).RevokedAt.IsZero(), "a reused login token leaves the first grant live")
	})

	t.Run("child grant", func(t *testing.T) {
		require.NotEmpty(t, f.schedule)

		// A child grant is derived from the schedule grant.
		before := time.Now()
		res := f.as1Env.derive(t, broker, f.schedule, oauthproto.TokenTypeIDJAG, nil)
		after := time.Now()
		require.Equal(t, http.StatusOK, res.status, res.body)
		entry := assertRefreshTokenResponse(t, res, oauthproto.TokenTypeIDJAG, before, after, xrtIDJAGCap)
		expiresAt, ok := entry["expires_at"].(float64)
		require.True(t, ok)
		assert.Equal(t, f.scheduleExp, int64(expiresAt), "the child expires with its parent, which is shorter than the cap")
		f.run = res.str(t, "access_token")

		// The child records its parent's signature and copies the parent's owner.
		parent := f.as1Env.grant(t, f.schedule)
		child := f.as1Env.grant(t, f.run)
		assert.Equal(t, tokenSignature(f.schedule), child.ParentSignature)
		assert.Equal(t, parent.Subject, child.Subject)
		assert.Equal(t, parent.SubjectIdentifier, child.SubjectIdentifier)
		assert.Equal(t, parent.UpstreamSubject, child.UpstreamSubject)
		assert.Equal(t, []string{oauthproto.TokenTypeIDJAG}, child.IssuableTokenTypes)
	})
}

// flow is the state one delegated-agent run carries from step to step.
type flow struct {
	as1, as2Issuer string // issuers, known before either server starts
	as1Env         *xrtEnv
	as2            *as2Env
	oidc           *mockoidc.MockOIDC
	actor          *flowActor

	sub           string // AS-1's local subject for the user (one user, two logins)
	sessionLogin  string // login 1: opens the session grant
	scheduleLogin string // login 2: opens the schedule grant

	// session issues ID-JAGs; schedule issues refresh tokens, with scheduleExp
	// as its echoed expires_at; run is schedule's child and issues ID-JAGs.
	session     string
	schedule    string
	scheduleExp int64
	run         string
}

// startFlow starts both servers and signs the user in twice. AS-1 gives the
// user a random subject at the first login and resolves the second login to
// the same one. AS-2 binds that subject exactly, so it starts after the logins.
func startFlow(t *testing.T, backend string) *flow {
	t.Helper()
	f := &flow{}
	ctrl := gomock.NewController(t)
	f.actor = newFlowActor(t, ctrl)

	l1, l2 := listenLoopback(t), listenLoopback(t)
	f.as1 = "http://" + l1.Addr().String()
	f.as2Issuer = "http://" + l2.Addr().String()

	f.oidc = startRealLoginUpstream(t)
	// Each login consumes one queued user.
	f.oidc.QueueUser(&mockoidc.MockUser{Subject: realLoginUpstreamSubject, Email: realLoginUpstreamEmail})

	f.as1Env = startXRTServerOn(t, backend, l1, func(cfg *authserver.RunConfig, issuer string) {
		cfg.Upstreams = []authserver.UpstreamRunConfig{realLoginUpstream(t, f.oidc, issuer)}
		setBrokerActorProfile(cfg)
		for i := range cfg.InboundGrants.TokenExchange.DelegateClients {
			client := &cfg.InboundGrants.TokenExchange.DelegateClients[i]
			if client.ClientID != cfgBrokerID {
				continue
			}
			client.TokenExchange.IDJAG.Targets = append(client.TokenExchange.IDJAG.Targets,
				xrtIDJAGTarget(f.as2Issuer, xrtUpstreamSubject, "sub_id"))
		}
	},
		authserverrunner.WithAccessTokenType("at+jwt"),
		// act.iss is the agent trust domain, the namespace act.sub is assigned
		// in, not the issuer of the ID-JAG: ToolHive refuses an ActIssuer equal
		// to the server's own issuer at startup.
		authserverrunner.WithActorTokenValidators(map[string]tokenexchange.ActorValidatorRegistration{
			actorTestValidator: {Validator: f.actor.mock, ActIssuer: flowActIssuer},
		}),
	)
	require.Equal(t, f.as1, f.as1Env.issuer)

	login1 := realLoginAccessToken(t, f.as1Env)
	login2 := realLoginAccessToken(t, f.as1Env)

	// The logins are preconditions of every later step.
	header1, _ := decodeIDJAG(t, login1)
	header2, _ := decodeIDJAG(t, login2)
	require.Equal(t, "at+jwt", header1["typ"])
	require.Equal(t, "at+jwt", header2["typ"])

	claims1 := accessTokenClaims(t, f.as1Env.embedded, login1)
	claims2 := accessTokenClaims(t, f.as1Env.embedded, login2)
	require.Equal(t, f.as1, claims1["iss"])
	require.Equal(t, []any{cfgAudienceA}, claims1["aud"])
	require.Equal(t, xrtSourceApp, claims1["client_id"])
	for _, name := range []string{"jti", "iat", "exp"} {
		require.NotNil(t, claims1[name], "login 1 has %s", name)
	}
	sub, _ := claims1["sub"].(string)
	require.NotEmpty(t, sub)
	wantSubID := map[string]any{"format": "iss_sub", "iss": f.oidc.Issuer(), "sub": realLoginUpstreamSubject}
	wantUpstream := map[string]any{"iss": f.oidc.Issuer(), "claim": "sub", "value": realLoginUpstreamSubject}
	require.Equal(t, wantSubID, claims1["sub_id"])
	require.Equal(t, wantUpstream, claims1[claimnames.UpstreamSubjectKey])

	require.Equal(t, sub, claims2["sub"], "the second login resolves to the same user")
	require.Equal(t, wantSubID, claims2["sub_id"])
	require.Equal(t, wantUpstream, claims2[claimnames.UpstreamSubjectKey])
	require.NotEqual(t, claims1["jti"], claims2["jti"])

	f.sub, f.sessionLogin, f.scheduleLogin = sub, login1, login2

	f.as2 = startAS2(t, l2, f.as1, f.sub, authserverrunner.WithAccessTokenType("at+jwt"))
	require.Equal(t, f.as2Issuer, f.as2.issuer)
	return f
}

// flowActor is the actor validator stub of one AS-1. The actor_token value
// picks the behaviour. It runs in the validator's goroutine, so it never calls
// require or any helper that does: it reports with t.Errorf and returns an
// error.
type flowActor struct {
	mock *mocks.MockActorTokenValidator
	t    *testing.T
}

func newFlowActor(t *testing.T, ctrl *gomock.Controller) *flowActor {
	t.Helper()
	a := &flowActor{mock: mocks.NewMockActorTokenValidator(ctrl), t: t}
	a.mock.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(a.validate).AnyTimes()
	return a
}

func (a *flowActor) validate(_ context.Context, req tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
	switch req.ActorToken {
	case "accept":
		return tokenexchange.ActorResult{AgentID: flowAgentID, ExpiresAt: time.Now().Add(time.Hour)}, nil
	default:
		a.t.Errorf("unexpected actor_token of length %d", len(req.ActorToken))
		return tokenexchange.ActorResult{}, errors.New("unexpected actor_token")
	}
}
