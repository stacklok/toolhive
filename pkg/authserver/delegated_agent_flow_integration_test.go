// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	// flowActIssuer is the agent trust domain: the namespace act.sub is assigned in.
	flowActIssuer = "spiffe://agents.example.com"
	// flowAgentID is an opaque SPIFFE ID in flowActIssuer's trust domain.
	flowAgentID = "spiffe://agents.example.com/agent/ag_7hq2x9"
	// flowValidatorErrorText is the text of the validator's internal error. It must
	// never reach a client.
	flowValidatorErrorText = "flow-validator-internal-detail"
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
			t.Run("grant expiry", func(t *testing.T) {
				t.Parallel()
				runGrantExpiry(t, backend)
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

		// A login-shaped token that carries the delegated-access
		// marker is refused, with a jti no grant has used. Nothing can mint a
		// marked token over HTTP, so it is signed with AS-1's own key.
		marked := signMarkedLoginToken(t, f.as1Env.embedded, sessionClaims)
		res = f.as1Env.requestRefreshToken(t, broker, marked, entryForm(oauthproto.TokenTypeIDJAG, nil))
		requireError(t, res, "invalid_request", "subject_token_delegated_access")
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

	t.Run("ID-JAG issuance", func(t *testing.T) {
		require.NotEmpty(t, f.session)
		require.NotEmpty(t, f.schedule)
		require.NotEmpty(t, f.run)
		a := f.actor
		wantAct := map[string]any{"iss": flowActIssuer, "sub": flowAgentID}

		// The session grant issues the first ID-JAG.
		mark := a.callCount()
		res := f.as1Env.requestIDJAGWith(t, broker, f.session, f.as2Issuer, func(form url.Values) {
			actorPairWith("accept")(form)
			form.Set("mecatl_operation", "op-1")
		})
		claims := assertIDJAGResponse(t, res, idJAGWant{
			Issuer: f.as1, Subject: f.sub, Target: f.as2Issuer, Act: wantAct,
			Lifetime: xrtIDJAGLifetime, Extra: []string{"sub_id", claimnames.UpstreamSubjectKey},
		})
		f.sessionIDJAG = res.str(t, "access_token")
		firstJTI := claims["jti"]

		// The validator is called once, with the session grant's owner.
		stored := f.as1Env.grant(t, f.session)
		calls := a.callsSince(mark)
		require.Len(t, calls, 1)
		assert.Equal(t, tokenexchange.ActorRequest{
			ActorToken: "accept", ActorTokenType: oauthproto.TokenTypeJWT, ClientID: cfgBrokerID,
			Target: tokenexchange.ActorTarget{Issuer: f.as2Issuer, Resource: xrtIDJAGResource},
			Grant: tokenexchange.GrantOwner{
				Subject: f.sub, SubjectIdentifier: stored.SubjectIdentifier,
				Upstream: tokenexchange.UpstreamIdentity{
					Issuer: f.oidc.Issuer(), Subject: realLoginUpstreamSubject,
					StableClaim: "sub", StableValue: realLoginUpstreamSubject,
				},
			},
		}, calls[0])

		// The run's ID-JAG is capped by the agent credential and takes the schedule's owner.
		// The credential must expire before the ID-JAG's own 60 s lifetime, or the cap
		// would not bind; 40 s leaves time for the redemption step below to run first.
		shortExp := time.Now().Add(40 * time.Second)
		a.setShortExp(shortExp)
		mark = a.callCount()
		res = f.as1Env.requestIDJAGWith(t, broker, f.run, f.as2Issuer, actorPairWith("accept-short"))
		claims = assertIDJAGResponse(t, res, idJAGWant{
			Issuer: f.as1, Subject: f.sub, Target: f.as2Issuer, Act: wantAct,
			Extra: []string{"sub_id", claimnames.UpstreamSubjectKey},
		})
		assert.Equal(t, shortExp.Unix(), intClaim(t, claims, "exp"), "the agent credential caps the ID-JAG")
		f.runIDJAG = res.str(t, "access_token")
		calls = a.callsSince(mark)
		require.Len(t, calls, 1)
		parent := f.as1Env.grant(t, f.schedule)
		var parentUpstream struct {
			Iss   string `json:"iss"`
			Claim string `json:"claim"`
			Value string `json:"value"`
		}
		require.NoError(t, json.Unmarshal([]byte(parent.UpstreamSubject), &parentUpstream))
		assert.Equal(t, parent.Subject, calls[0].Grant.Subject)
		assert.Equal(t, parent.SubjectIdentifier, calls[0].Grant.SubjectIdentifier)
		assert.Equal(t, tokenexchange.UpstreamIdentity{
			Issuer: parentUpstream.Iss, Subject: realLoginUpstreamSubject,
			StableClaim: parentUpstream.Claim, StableValue: parentUpstream.Value,
		}, calls[0].Grant.Upstream)

		// A second ID-JAG from the session grant gets a new jti.
		res = f.as1Env.requestIDJAGWith(t, broker, f.session, f.as2Issuer, actorPairWith("accept"))
		claims = assertIDJAGResponse(t, res, idJAGWant{
			Issuer: f.as1, Subject: f.sub, Target: f.as2Issuer, Act: wantAct,
			Lifetime: xrtIDJAGLifetime, Extra: []string{"sub_id", claimnames.UpstreamSubjectKey},
		})
		assert.NotEqual(t, firstJTI, claims["jti"])
		// assertIDJAGResponse pins the exact response members, so there is no refresh_token member.
		f.spareIDJAG = res.str(t, "access_token")

		// The schedule grant cannot issue an ID-JAG, and the validator is not called.
		mark = a.callCount()
		res = f.as1Env.requestIDJAGWith(t, broker, f.schedule, f.as2Issuer, actorPairWith("accept"))
		requireError(t, res, "invalid_request", "not_issuable")
		assert.Equal(t, mark, a.callCount(), "the validator is not called")

		// Actor denials map to invalid_request; an unexpected validator error is hidden.
		for _, tc := range []struct{ token, description string }{
			{"denied", "denied"},
			{"approval-required", "approval_required"},
			{"token-expired", "actor_token_expired"},
		} {
			t.Run(tc.token, func(t *testing.T) {
				res := f.as1Env.requestIDJAGWith(t, broker, f.session, f.as2Issuer, actorPairWith(tc.token))
				requireError(t, res, "invalid_request", tc.description)
			})
		}
		res = f.as1Env.requestIDJAGWith(t, broker, f.session, f.as2Issuer, actorPairWith("plain-error"))
		requireRefusal(t, res, http.StatusInternalServerError, "server_error")
		// The decoded body re-encoded is equivalent to the raw body for this
		// plain-ASCII marker.
		raw, err := json.Marshal(res.body)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), flowValidatorErrorText)

		// A grant without the claims its target needs is refused before the validator runs.
		upstream := map[string]any{"iss": "https://idp.example", "claim": "oid", "value": "obj-1"}
		for _, tc := range []struct {
			name, target, description string
			mutate                    func(*jwt.Claims, map[string]any)
		}{
			{"neither claim", xrtTargetUpstream, "upstream_subject_missing", nil},
			{"upstream_subject only", xrtTargetBoth, "sub_id_missing", func(_ *jwt.Claims, x map[string]any) {
				x[claimnames.UpstreamSubjectKey] = upstream
			}},
		} {
			grant := f.as1Env.requestRefreshToken(t, broker, f.as1Env.mint(t, tc.mutate), entryForm(oauthproto.TokenTypeIDJAG, nil))
			require.Equal(t, http.StatusOK, grant.status, "%s: %v", tc.name, grant.body)
			mark = a.callCount()
			res = f.as1Env.requestIDJAGWith(t, broker, grant.str(t, "access_token"), tc.target, actorPairWith("accept"))
			requireError(t, res, "invalid_request", tc.description)
			assert.Equal(t, mark, a.callCount(), "%s: the validator is not called", tc.name)
		}
	})

	t.Run("redemption", func(t *testing.T) {
		require.NotEmpty(t, f.sessionIDJAG)
		require.NotEmpty(t, f.runIDJAG)
		wantAct := map[string]any{"iss": flowActIssuer, "sub": flowAgentID}

		// Each ID-JAG redeems at AS-2 into an access token for its resource.
		for _, tc := range []struct{ name, idjag string }{
			{"session", f.sessionIDJAG},
			{"run", f.runIDJAG},
		} {
			res := f.as2.redeem(t, f.as2.client, tc.idjag)
			require.Equal(t, http.StatusOK, res.status, "%s: %v", tc.name, res.body)
			_, hasRefresh := res.body["refresh_token"]
			assert.False(t, hasRefresh, "%s: no refresh_token member", tc.name)
			token := res.str(t, "access_token")
			header, _ := decodeIDJAG(t, token)
			assert.Equal(t, "at+jwt", header["typ"], tc.name)

			claims := accessTokenClaims(t, f.as2.embedded, token)
			_, idjagClaims := decodeIDJAG(t, tc.idjag)
			assert.Equal(t, f.as2Issuer, claims["iss"], tc.name)
			assert.Equal(t, []any{xrtIDJAGResource}, claims["aud"], tc.name)
			assert.Equal(t, xrtTargetClientID, claims["client_id"], tc.name)
			assert.NotEmpty(t, claims["jti"], tc.name)
			assert.NotEmpty(t, claims["iat"], tc.name)
			assert.NotContains(t, claims, "tsid", tc.name)
			exp, ok := claims["exp"].(float64)
			require.True(t, ok, tc.name)
			assert.LessOrEqual(t, int64(exp), intClaim(t, idjagClaims, "exp"), "%s: exp is capped by the ID-JAG", tc.name)
			// That AS-2 copies act from the ID-JAG is pinned by
			// TestIDJAGRedemptionCarriesActorAcrossTwoServers. This checks the
			// value for the session grant's ID-JAG and for the scheduled run's
			// child grant.
			assert.Equal(t, wantAct, claims["act"], tc.name)
			// Until Delegated access: subject mapping at ID-JAG redemption (#6887)
			// is built, AS-2 has no local user for AS-1's subject, so sub is
			// the issuer-qualified AS-1 subject. That change flips this.
			assert.Equal(t, f.as1+"#"+f.sub, claims["sub"], tc.name)
		}

		// A downstream client expects invalid_assertion for a replayed
		// ID-JAG. ToolHive sends invalid_grant with a hint only; see
		// Authorization server: reason codes in error_description for ID-JAG
		// redemption refusals (#7030).
		res := f.as2.redeem(t, f.as2.client, f.sessionIDJAG)
		requireRefusal(t, res, http.StatusBadRequest, "invalid_grant")

		// This ID-JAG has the same issuer, client ID and resource as
		// a good one and differs only in aud (and in the subject claims its
		// target does not require, which AS-2 never reads), so the refusal can
		// only be AS-2's audience check (draft section 4.4.1).
		a := f.actor
		mark := a.callCount()
		res = f.as1Env.requestIDJAGWith(t, broker, f.session, xrtTargetPlain, actorPairWith("accept"))
		require.Equal(t, http.StatusOK, res.status, res.body)
		assert.Equal(t, mark+1, a.callCount())
		res = f.as2.redeem(t, f.as2.client, res.str(t, "access_token"))
		requireRefusal(t, res, http.StatusBadRequest, "invalid_grant")

		// AS-2 binds exactly one subject, f.sub. Another real user's ID-JAG from
		// the same AS-1 has the same issuer, client ID, audience and resource as
		// a good one and differs in its subject (and the subject claims that
		// follow from it), so the refusal can only be the subject binding.
		f.oidc.QueueUser(&mockoidc.MockUser{Subject: "other-upstream-subject", Email: "other@example.com"})
		otherLogin := realLoginAccessToken(t, f.as1Env)
		grant := f.as1Env.requestRefreshToken(t, broker, otherLogin, entryForm(oauthproto.TokenTypeIDJAG, nil))
		require.Equal(t, http.StatusOK, grant.status, grant.body)
		mark = a.callCount()
		res = f.as1Env.requestIDJAGWith(t, broker, grant.str(t, "access_token"), f.as2Issuer, actorPairWith("accept"))
		require.Equal(t, http.StatusOK, res.status, res.body)
		assert.Equal(t, mark+1, a.callCount())
		otherIDJAG := res.str(t, "access_token")
		_, otherClaims := decodeIDJAG(t, otherIDJAG)
		require.NotEqual(t, f.sub, otherClaims["sub"])
		res = f.as2.redeem(t, f.as2.client, otherIDJAG)
		requireRefusal(t, res, http.StatusBadRequest, "invalid_grant")

		// An AS-1 login access token is not an ID-JAG. Its typ is at+jwt, so the
		// plain JWT-bearer handler gets it and refuses the typ.
		res = f.as2.redeem(t, f.as2.client, f.sessionLogin)
		requireRefusal(t, res, http.StatusBadRequest, "invalid_grant")
	})

	t.Run("revoke", func(t *testing.T) {
		require.NotEmpty(t, f.session)
		require.NotEmpty(t, f.schedule)
		require.NotEmpty(t, f.run)
		require.NotEmpty(t, f.spareIDJAG)
		a := f.actor

		// A mint token carries neither sub_id nor upstream_subject, so
		// the target is xrtTargetPlain; f.as2Issuer requires both.
		res := f.as1Env.requestRefreshToken(t, broker, f.as1Env.mint(t, nil), entryForm(oauthproto.TokenTypeIDJAG, nil))
		require.Equal(t, http.StatusOK, res.status, res.body)
		s2 := res.str(t, "access_token")
		a.setRevokeTarget(s2)
		// Known gap, described in docs/arch/17 (Grant reuse and revocation): the grant is
		// not read again after the validator, so a revoke that lands during it still
		// issues this one ID-JAG. It is bounded: the grant was checked just before the
		// validator ran, the ID-JAG starts when the validator's answer is checked, and
		// it lives 60 s. This pins today's behaviour; re-reading the grant after the
		// validator would flip it. Nothing revokes a grant outside tests yet, so decide
		// the re-read with the revocation work: RFC 7009 token revocation endpoint
		// (#6966), Per-user revocation time for grants issued by token exchange
		// (#6972), and Revoke the grant a reused subject token created (#6973).
		res = f.as1Env.requestIDJAGWith(t, broker, s2, xrtTargetPlain, actorPairWith("revoke-during"))
		require.Equal(t, http.StatusOK, res.status, res.body)

		// A revoked grant's ID-JAG request is refused before the validator runs.
		mark := a.callCount()
		res = f.as1Env.requestIDJAGWith(t, broker, s2, xrtTargetPlain, actorPairWith("accept"))
		requireError(t, res, "invalid_request", "revoked")
		assert.Equal(t, mark, a.callCount(), "the validator is not called")

		// A revoked session grant is refused before the validator runs.
		f.as1Env.revoke(t, f.session)
		mark = a.callCount()
		res = f.as1Env.requestIDJAGWith(t, broker, f.session, f.as2Issuer, actorPairWith("accept"))
		requireError(t, res, "invalid_request", "revoked")
		assert.Equal(t, mark, a.callCount(), "the validator is not called")

		// A run under a revoked schedule is refused with parent_revoked.
		f.as1Env.revoke(t, f.schedule)
		mark = a.callCount()
		res = f.as1Env.requestIDJAGWith(t, broker, f.run, f.as2Issuer, actorPairWith("accept"))
		requireError(t, res, "invalid_request", "parent_revoked")
		assert.Equal(t, mark, a.callCount(), "the validator is not called")

		// Another client learns nothing from a revoked grant. It uses derive, because
		// the other client has no ID-JAG targets. The client check that runs before the
		// revoked and parent-revoked branches is shared by the derive and ID-JAG paths,
		// so this pins the ordering for both.
		for _, tc := range []struct{ name, grant string }{
			{"derive by another client: revoked session", f.session},
			{"derive by another client: run under a revoked schedule", f.run},
		} {
			t.Run(tc.name, func(t *testing.T) {
				res := f.as1Env.derive(t, f.as1Env.otherRefresh, tc.grant, oauthproto.TokenTypeIDJAG, nil)
				requireError(t, res, "invalid_request", "grant_invalid")
			})
		}

		// An ID-JAG issued before the revoke redeems until its own exp.
		res = f.as2.redeem(t, f.as2.client, f.spareIDJAG)
		require.Equal(t, http.StatusOK, res.status, res.body)

		// Later work and what each adds to this test:
		//   - RFC 7009 token revocation endpoint (#6966): the HTTP rows for
		//     revoking, in place of the store calls above.
		//   - Per-user revocation time for grants issued by token exchange
		//     (#6972): rows for revoking all of a user's grants at once.
		//   - Re-check the user's upstream refresh token when issuing an ID-JAG
		//     (#6978): the upstream_revoked and owner_action_required reasons.
	})
}

// runGrantExpiry checks a grant whose bound passes while the actor
// validator runs. Its grants come from minted tokens, so it needs no login
// and no second server.
func runGrantExpiry(t *testing.T, backend string) {
	t.Helper()
	a := newFlowActor(t, gomock.NewController(t))
	env := startXRTServerWith(t, backend, func(cfg *authserver.RunConfig, _ string) {
		setBrokerActorProfile(cfg)
	}, withActorTestValidator(a.mock))
	a.setEnv(env)
	broker := env.broker

	exp := time.Now().Unix() + 4
	res := env.requestRefreshToken(t, broker, env.mint(t, nil), func(form url.Values) {
		form.Set("authorization_details", "["+xrtEntry(oauthproto.TokenTypeRefreshToken, &exp)+"]")
	})
	require.Equal(t, http.StatusOK, res.status, res.body)
	parent := res.str(t, "access_token")
	res = env.derive(t, broker, parent, oauthproto.TokenTypeIDJAG, nil)
	require.Equal(t, http.StatusOK, res.status, res.body)
	child := res.str(t, "access_token")

	a.setWaitUntil(time.Unix(exp, 0))
	res = env.requestIDJAGWith(t, broker, child, xrtTargetPlain, actorPairWith("wait-for-expiry"))
	requireError(t, res, "invalid_grant", "bound_elapsed")

	mark := a.callCount()
	res = env.requestIDJAGWith(t, broker, child, xrtTargetPlain, actorPairWith("accept"))
	requireError(t, res, "invalid_request", "grant_invalid")
	assert.Equal(t, mark, a.callCount(), "the validator is not called")

	res = env.derive(t, broker, parent, oauthproto.TokenTypeIDJAG, nil)
	requireError(t, res, "invalid_request", "grant_invalid")
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
	// sessionIDJAG, runIDJAG and spareIDJAG are ID-JAGs from session and run,
	// redeemed before and after the revokes.
	session      string
	schedule     string
	scheduleExp  int64
	run          string
	sessionIDJAG string
	runIDJAG     string
	spareIDJAG   string
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
	f.actor.setEnv(f.as1Env)
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

// signMarkedLoginToken re-signs a copy of a login token's claims, with a fresh
// jti and the delegated-access marker, using the server's own signing key.
func signMarkedLoginToken(t *testing.T, embedded *authserverrunner.EmbeddedAuthServer, loginClaims map[string]any) string {
	t.Helper()
	signingKey, err := embedded.KeyProvider().SigningKey(t.Context())
	require.NoError(t, err)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(signingKey.Algorithm), Key: signingKey.Key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", signingKey.KeyID),
	)
	require.NoError(t, err)
	claims := maps.Clone(loginClaims)
	claims["jti"] = "marked-" + time.Now().Format(time.RFC3339Nano)
	claims[claimnames.DelegatedAccessKey] = true
	// Preserve json.Number timestamps as JSON numbers when re-signing.
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	signed, err := signer.Sign(payload)
	require.NoError(t, err)
	token, err := signed.CompactSerialize()
	require.NoError(t, err)
	return token
}

// requireRefusal checks status and error only, for refusals whose
// error_description is a hint clients should not parse.
func requireRefusal(t *testing.T, res tokenResult, status int, code string) {
	t.Helper()
	require.Equal(t, status, res.status, "error=%v error_description=%v", res.body["error"], res.body["error_description"])
	assert.Equal(t, code, res.body["error"])
}

// flowActor is the actor validator stub of one AS-1. The actor_token value
// picks the behaviour. It runs in the validator's goroutine, so it never calls
// require or any helper that does: it reports with t.Errorf and returns an
// error, and every wait honours ctx. The server does not wait for a validator
// call after the validator timeout, so a call can outlive its request and the
// test: it must not report once ctx is cancelled.
type flowActor struct {
	mock *mocks.MockActorTokenValidator
	t    *testing.T

	mu           sync.Mutex
	calls        []tokenexchange.ActorRequest
	waitUntil    time.Time
	revokeTarget string
	shortExp     time.Time
	env          *xrtEnv
}

func newFlowActor(t *testing.T, ctrl *gomock.Controller) *flowActor {
	t.Helper()
	a := &flowActor{mock: mocks.NewMockActorTokenValidator(ctrl), t: t}
	a.mock.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(a.validate).AnyTimes()
	return a
}

func (a *flowActor) validate(ctx context.Context, req tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
	a.mu.Lock()
	a.calls = append(a.calls, req)
	waitUntil, revokeTarget, shortExp, env := a.waitUntil, a.revokeTarget, a.shortExp, a.env
	a.mu.Unlock()

	accept := tokenexchange.ActorResult{AgentID: flowAgentID, ExpiresAt: time.Now().Add(time.Hour)}
	switch req.ActorToken {
	case "accept":
		return accept, nil
	case "accept-short":
		return tokenexchange.ActorResult{AgentID: flowAgentID, ExpiresAt: shortExp}, nil
	case "denied":
		return tokenexchange.ActorResult{}, &tokenexchange.ActorDeniedError{Reason: tokenexchange.ActorDeniedReasonDenied}
	case "approval-required":
		return tokenexchange.ActorResult{}, &tokenexchange.ActorDeniedError{Reason: tokenexchange.ActorDeniedReasonApprovalRequired}
	case "token-expired":
		return tokenexchange.ActorResult{}, &tokenexchange.ActorDeniedError{Reason: tokenexchange.ActorDeniedReasonActorTokenExpired}
	case "plain-error":
		return tokenexchange.ActorResult{}, errors.New(flowValidatorErrorText)
	case "wait-for-expiry":
		select {
		case <-time.After(time.Until(waitUntil)):
			return accept, nil
		case <-ctx.Done():
			return tokenexchange.ActorResult{}, ctx.Err()
		}
	case "revoke-during":
		if env == nil {
			a.report(ctx, "revoke-during before the actor has an env")
			return tokenexchange.ActorResult{}, errors.New("flow actor has no env")
		}
		grants, ok := storage.Unwrap(env.store).(storage.ExchangeGrantStorage)
		if !ok {
			a.report(ctx, "the store does not hold exchange grants")
			return tokenexchange.ActorResult{}, errors.New("no exchange grant storage")
		}
		if err := grants.RevokeExchangeGrant(ctx, tokenSignature(revokeTarget), time.Now()); err != nil {
			a.report(ctx, "revoke during the validator: %v", err)
			return tokenexchange.ActorResult{}, err
		}
		return accept, nil
	default:
		a.report(ctx, "unexpected actor_token of length %d", len(req.ActorToken))
		return tokenexchange.ActorResult{}, errors.New("unexpected actor_token")
	}
}

// report fails the test from the validator's goroutine. It stays silent once ctx
// is cancelled: the server does not wait for a validator call after the validator
// timeout, so a late call can run after the test has finished, and t.Errorf then
// panics. The request's own failure reports the problem in that case.
func (a *flowActor) report(ctx context.Context, format string, args ...any) {
	if ctx.Err() == nil {
		a.t.Errorf(format, args...)
	}
}

// callCount is the number of validator calls so far. Take it before a request
// and compare it after, to assert the validator was or was not called.
func (a *flowActor) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// callsSince returns the requests the validator saw after callCount was mark.
func (a *flowActor) callsSince(mark int) []tokenexchange.ActorRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]tokenexchange.ActorRequest(nil), a.calls[mark:]...)
}

func (a *flowActor) setEnv(env *xrtEnv) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.env = env
}

func (a *flowActor) setWaitUntil(at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.waitUntil = at
}

func (a *flowActor) setRevokeTarget(grant string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revokeTarget = grant
}

func (a *flowActor) setShortExp(at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.shortExp = at
}
