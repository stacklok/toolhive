// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/actor"
	"github.com/stacklok/toolhive/pkg/authserver/actor/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
)

const (
	actorTestAllowedIssuer = "https://agents.example.com"
	actorTestOtherIssuer   = "https://other.example.com"
	actorTestSubject       = "secret-agent-sub"
	descActorDenied        = "actor_denied"
	descSubjectUnknown     = "subject_unknown"
)

// actorRedeem runs one redemption of claims against a handler built from
// policy and returns the replay consumer, the handler's log and the error.
func actorRedeem(
	t *testing.T, policy *JWTBearerGrantPolicy, claims *ValidatedClaims, prepare func(*IDJAGHandler),
) (*recordingAssertionConsumer, *strings.Builder, error) {
	t.Helper()
	_, consumer, buf, err := actorRedeemRequest(t, policy, claims, prepare)
	return consumer, buf, err
}

// actorRedeemRequest is actorRedeem that also returns the access request, so a
// test can read the session the redemption installed.
func actorRedeemRequest(
	t *testing.T, policy *JWTBearerGrantPolicy, claims *ValidatedClaims, prepare func(*IDJAGHandler),
) (fosite.AccessRequester, *recordingAssertionConsumer, *strings.Builder, error) {
	t.Helper()
	consumer := &recordingAssertionConsumer{}
	deps := subjectModeDeps(t, policy, consumer)
	deps.validator = &testJWTBearerAssertionValidator{claims: claims}
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h, err := newIDJAGIssuanceHandler(deps, nil, logger)
	require.NoError(t, err)
	if prepare != nil {
		prepare(h)
	}
	req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
	err = h.HandleTokenEndpointRequest(context.Background(), req)
	return req, consumer, &buf, err
}

func resolvePolicyWithActors(actorIssuers ...string) *JWTBearerGrantPolicy {
	policy := subjectModePolicy(SubjectModeResolve, nil)
	policy.ActorIssuers = actorIssuers
	return policy
}

func claimsWithAct(act any) *ValidatedClaims {
	claims := validIDJAGClaims()
	if act == nil {
		delete(claims.Extra, "act")
	} else {
		claims.Extra["act"] = act
	}
	return claims
}

func requireActorOutcome(t *testing.T, err error, consumer *recordingAssertionConsumer, wantDescription string) {
	t.Helper()
	requireRefusal(t, err, "access_denied", wantDescription)
	assert.Empty(t, consumer.key, "the ID-JAG must stay unused")
}

// requireActorLog asserts the log holds exactly one actor-refusal line at
// level, with the issuer, the client and wantReason, and returns it.
func requireActorLog(t *testing.T, buf *strings.Builder, level, wantReason string) string {
	t.Helper()
	var lines []string
	for _, line := range logLinesAt(buf, level) {
		if strings.Contains(line, "actor check failed") {
			lines = append(lines, line)
		}
	}
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "issuer="+idJAGTestIssuer)
	assert.Contains(t, lines[0], "client_id="+idJAGTestClientID)
	assert.Contains(t, lines[0], "reason="+wantReason)
	return lines[0]
}

func TestIDJAGActor_EffectiveActor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		act        any
		want       string
		wantReason string
	}{
		{"act missing", nil, descActorDenied, actor.ReasonNoActor},
		{"act is a string", "okta-agent", descActorDenied, reasonActorMalformed},
		{"act has no sub", map[string]any{"iss": idJAGTestIssuer}, descActorDenied, reasonActorMalformed},
		{"act.iss is a number", map[string]any{"sub": "a", "iss": 7}, descActorDenied, reasonActorMalformed},
		{"act.iss is empty", map[string]any{"sub": "a", "iss": ""}, descActorDenied, reasonActorMalformed},
		{"act without iss takes the ID-JAG iss", map[string]any{"sub": "a"}, descSubjectUnknown, ""},
		{
			"nested act does not decide",
			map[string]any{"sub": "a", "iss": actorTestAllowedIssuer, "act": map[string]any{"sub": "b", "iss": actorTestOtherIssuer}},
			descSubjectUnknown, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			consumer, buf, err := actorRedeem(t, resolvePolicyWithActors(actorTestAllowedIssuer), claimsWithAct(tt.act), nil)
			requireActorOutcome(t, err, consumer, tt.want)
			if tt.wantReason != "" {
				requireActorLog(t, buf, "DEBUG", tt.wantReason)
			}
		})
	}
}

func TestIDJAGActor_IssuerAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		actIssuer string
		// wantPolicyCalls is 0 when the issuer pre-check must refuse before the
		// policy is consulted.
		wantPolicyCalls int
		want            string
	}{
		{"act.iss is the ID-JAG's own iss", idJAGTestIssuer, 1, descSubjectUnknown},
		{"act.iss is in actor_issuers", actorTestAllowedIssuer, 1, descSubjectUnknown},
		{"act.iss is not listed", actorTestOtherIssuer, 0, descActorDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := mocks.NewMockPolicy(gomock.NewController(t))
			policy.EXPECT().Evaluate(gomock.Any(), gomock.Any()).Times(tt.wantPolicyCalls).
				Return(actor.Decision{Allow: true, Reason: actor.ReasonAllowed}, nil)
			claims := claimsWithAct(map[string]any{"sub": "a", "iss": tt.actIssuer})

			consumer, buf, err := actorRedeem(t, resolvePolicyWithActors(actorTestAllowedIssuer), claims,
				func(h *IDJAGHandler) { h.actorPolicies[idJAGTestIssuer] = policy })

			requireActorOutcome(t, err, consumer, tt.want)
			if tt.want == descActorDenied {
				requireActorLog(t, buf, "DEBUG", actor.ReasonIssuerNotAllowed)
			}
		})
	}
}

func TestIDJAGActor_Policy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		decision   actor.Decision
		err        error
		wantErr    string
		wantLevel  string
		wantReason string
	}{
		{
			name: "deny", decision: actor.Decision{Reason: actor.ReasonIssuerNotAllowed},
			wantErr: descActorDenied, wantLevel: "DEBUG", wantReason: actor.ReasonIssuerNotAllowed,
		},
		{name: "error", err: errors.New("directory down"), wantLevel: "WARN", wantReason: reasonActorPolicyErr},
		{name: "allow", decision: actor.Decision{Allow: true, Reason: actor.ReasonAllowed}, wantErr: descSubjectUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got actor.Request
			policy := mocks.NewMockPolicy(gomock.NewController(t))
			policy.EXPECT().Evaluate(gomock.Any(), gomock.Any()).Times(1).DoAndReturn(
				func(_ context.Context, req actor.Request) (actor.Decision, error) {
					got = req
					return tt.decision, tt.err
				})
			claims := claimsWithAct(map[string]any{"sub": actorTestSubject, "iss": actorTestAllowedIssuer})

			consumer, buf, err := actorRedeem(t, resolvePolicyWithActors(actorTestAllowedIssuer), claims,
				func(h *IDJAGHandler) { h.actorPolicies[idJAGTestIssuer] = policy })

			if tt.err != nil {
				requireRefusalStatus(t, err, http.StatusInternalServerError, "server_error",
					fosite.ErrServerError.WithHint(hintActorCheckFailed).GetDescription())
				assert.Empty(t, consumer.key)
			} else {
				requireActorOutcome(t, err, consumer, tt.wantErr)
			}
			if tt.wantReason != "" {
				line := requireActorLog(t, buf, tt.wantLevel, tt.wantReason)
				assert.NotContains(t, line, "error=")
				for _, secret := range []string{
					idJAGTestSubject, "id-jag-jti", actorTestSubject, actorTestAllowedIssuer, "directory down",
				} {
					assert.NotContains(t, line, secret)
				}
			}
			assert.Equal(t, idJAGTestIssuer, got.TokenIssuer)
			assert.Equal(t, idJAGTestClientID, got.ClientID)
			assert.Equal(t, idJAGTestResource, got.Resource)
			assert.Equal(t, idJAGTestSubject, got.User, "the policy gets the user's subject, not the actor's")
			require.NotNil(t, got.Actor)
			assert.Equal(t, actorTestAllowedIssuer, got.Actor.Issuer)
			assert.Equal(t, actorTestSubject, got.Actor.Subject)
		})
	}
}

func TestIDJAGActor_Scope(t *testing.T) {
	t.Parallel()

	otherAct := map[string]any{"sub": "a", "iss": actorTestOtherIssuer}
	tests := []struct {
		name string
		act  any
	}{
		{"act missing", nil},
		{"act.iss from an issuer a resolve issuer would refuse", otherAct},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := subjectModePolicy(SubjectModeNamespaced, []JWTBearerSubjectBinding{
				{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
			})

			req, consumer, _, err := actorRedeemRequest(t, policy, claimsWithAct(tt.act), nil)

			require.NoError(t, err)
			assert.Equal(t, "id-jag-jti", consumer.key)
			issued, ok := req.GetSession().(*session.Session)
			require.True(t, ok)
			assert.Equal(t, idJAGTestIssuer+"#"+idJAGTestSubject, issued.GetSubject())
			if tt.act != nil {
				assert.Equal(t, tt.act, issued.JWTClaims.Extra["act"], "act is still copied, unread")
			} else {
				assert.NotContains(t, issued.JWTClaims.Extra, "act")
			}
		})
	}
}

func TestNewActorPolicies(t *testing.T) {
	t.Parallel()

	t.Run("only resolve issuers get a policy", func(t *testing.T) {
		t.Parallel()
		const namespacedIssuer = "https://namespaced.example.com"
		policies, err := newActorPolicies(map[string]*JWTBearerGrantPolicy{
			idJAGTestIssuer:  {SubjectMode: SubjectModeResolve},
			namespacedIssuer: {SubjectMode: SubjectModeNamespaced},
		})
		require.NoError(t, err)
		assert.Contains(t, policies, idJAGTestIssuer)
		assert.NotContains(t, policies, namespacedIssuer)
	})

	t.Run("an actor issuer containing # is refused", func(t *testing.T) {
		t.Parallel()
		_, err := newActorPolicies(map[string]*JWTBearerGrantPolicy{
			idJAGTestIssuer: {SubjectMode: SubjectModeResolve, ActorIssuers: []string{"https://a.example.com#x"}},
		})
		require.ErrorContains(t, err, `issuer_url "`+idJAGTestIssuer+`": actor policy:`)
	})
}
