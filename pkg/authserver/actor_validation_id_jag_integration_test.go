// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	actorTestValidator = "agent-broker"
	// actorTestActIssuer is the issuer the test validator is registered with.
	actorTestActIssuer = "https://agents.example.com"
)

// setBrokerActorProfile gives the broker an actor profile that selects the
// test validator.
func setBrokerActorProfile(cfg *authserver.RunConfig) {
	for i := range cfg.InboundGrants.TokenExchange.DelegateClients {
		client := &cfg.InboundGrants.TokenExchange.DelegateClients[i]
		if client.ClientID == cfgBrokerID {
			client.TokenExchange.ActorProfile = &authserver.DelegateClientActorProfile{
				Validator: actorTestValidator, ActorTokenType: oauthproto.TokenTypeJWT,
			}
		}
	}
}

// withActorTestValidator registers v as the test validator.
func withActorTestValidator(v tokenexchange.ActorTokenValidator) authserverrunner.EmbeddedAuthServerOption {
	return authserverrunner.WithActorTokenValidators(map[string]tokenexchange.ActorValidatorRegistration{
		actorTestValidator: {Validator: v, ActIssuer: actorTestActIssuer},
	})
}

// actorPairWith sets the actor token to token, with the configured type.
func actorPairWith(token string) func(url.Values) {
	return func(f url.Values) {
		f.Set("actor_token", token)
		f.Set("actor_token_type", oauthproto.TokenTypeJWT)
	}
}

// actorRequestRecorder expects one validator call, records its request and
// accepts the agent.
func actorRequestRecorder(validator *mocks.MockActorTokenValidator) *tokenexchange.ActorRequest {
	got := &tokenexchange.ActorRequest{}
	validator.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
			*got = req
			return tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: time.Now().Add(time.Hour)}, nil
		}).Times(1)
	return got
}

// TestIDJAGActorValidationRealLogin proves the validator receives the user's
// upstream identity as a real login recorded it, for an upstream whose
// subject_claim is not sub: an OIDC login through /oauth/authorize and the
// callback, the source app's access token, a grant exchanged from it, and the
// ID-JAG request that calls the validator.
func TestIDJAGActorValidationRealLogin(t *testing.T) {
	t.Parallel()
	const token = "actor-real-login"
	oidc := startRealLoginUpstream(t)
	validator := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	got := actorRequestRecorder(validator)
	env := startXRTServerWith(t, "memory", func(cfg *authserver.RunConfig, issuer string) {
		setBrokerActorProfile(cfg)
		cfg.Upstreams = []authserver.UpstreamRunConfig{{
			Name: "mockoidc",
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL: oidc.Issuer(), ClientID: oidc.Config().ClientID,
				ClientSecretFile:  writeSecretFile(t, oidc.Config().ClientSecret),
				RedirectURI:       issuer + "/oauth/callback",
				Scopes:            []string{"openid", "profile", "email"},
				SubjectClaim:      "email",
				InsecureAllowHTTP: true, AllowPrivateIPs: true,
			},
		}}
	}, withActorTestValidator(validator))

	grant := env.requestRefreshToken(t, env.broker, realLoginAccessToken(t, env), func(f url.Values) {
		f.Set("authorization_details", "["+xrtEntry(oauthproto.TokenTypeIDJAG, nil)+"]")
	})
	require.Equal(t, http.StatusOK, grant.status, grant.body)
	res := env.requestIDJAGWith(t, env.broker, grant.str(t, "access_token"), xrtTargetPlain, actorPairWith(token))
	require.Equal(t, http.StatusOK, res.status, res.body)

	stored := env.grant(t, grant.str(t, "access_token"))
	assert.Equal(t, tokenexchange.ActorRequest{
		ActorToken: token, ActorTokenType: oauthproto.TokenTypeJWT, ClientID: cfgBrokerID,
		Target: tokenexchange.ActorTarget{Issuer: xrtTargetPlain, Resource: xrtIDJAGResource},
		Grant: tokenexchange.GrantOwner{
			Subject: stored.Subject, SubjectIdentifier: stored.SubjectIdentifier,
			Upstream: tokenexchange.UpstreamIdentity{
				Issuer: oidc.Issuer(), Subject: realLoginUpstreamSubject,
				StableClaim: "email", StableValue: realLoginUpstreamEmail,
			},
		},
	}, *got)
	assertJSONEqual(t, map[string]any{"format": "iss_sub", "iss": oidc.Issuer(), "sub": realLoginUpstreamSubject},
		got.Grant.SubjectIdentifier)
}
