// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func actorProfileConfig(t *testing.T, profile *DelegateClientActorProfile) Config {
	t.Helper()
	cfg := minimalValidConfig(t)
	cfg.AllowedAudiences = []string{"https://mcp.example.com", "https://input.example.com"}
	cfg.ScopesSupported = []string{"openid", "offline_access"}
	cfg.DelegateClients = []DelegateClient{{
		ClientID: "delegate", ClientSecret: actorProfileTestSecret,
		Scopes: []string{"openid"}, Audiences: []string{"https://mcp.example.com"},
		TokenExchange: actorProfileTestExchange(profile),
	}}
	return cfg
}

func TestConfigValidateActorProfileRegistered(t *testing.T) {
	t.Parallel()

	registered := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	validators := map[string]tokenexchange.ActorValidatorRegistration{
		"broker": {Validator: registered, ActIssuer: "spiffe://example.org"},
	}

	t.Run("registered name passes", func(t *testing.T) {
		t.Parallel()
		cfg := actorProfileConfig(t, validActorProfile())
		cfg.ActorTokenValidators = validators
		require.NoError(t, cfg.Validate())
	})

	t.Run("unknown name fails Validate and New", func(t *testing.T) {
		t.Parallel()
		const want = `delegate_clients: client_id "delegate": ` +
			`actor_profile.validator "broker" is not registered in ActorTokenValidators`

		cfg := actorProfileConfig(t, validActorProfile())
		require.EqualError(t, cfg.Validate(), want)

		// New calls cfg.Validate before touching storage, so a storage mock
		// with no expectations proves the failure comes from validation.
		_, err := New(t.Context(), cfg, storagemocks.NewMockStorage(gomock.NewController(t)))
		require.EqualError(t, err, "invalid config: "+want)
	})

	t.Run("first unregistered client in configured order is reported", func(t *testing.T) {
		t.Parallel()
		cfg := actorProfileConfig(t, &DelegateClientActorProfile{Validator: "zeta", ActorTokenType: oauthproto.TokenTypeJWT})
		second := cfg.DelegateClients[0]
		second.ClientID = "second"
		second.TokenExchange = actorProfileTestExchange(
			&DelegateClientActorProfile{Validator: "alpha", ActorTokenType: oauthproto.TokenTypeJWT})
		secondTarget := idJAGTestTarget()
		secondTarget.ClientID = "second-broker-at-as2"
		second.TokenExchange.IDJAG.Targets = []DelegateClientIDJAGTarget{secondTarget}
		cfg.DelegateClients = append(cfg.DelegateClients, second)
		require.EqualError(t, cfg.Validate(), `delegate_clients: client_id "delegate": `+
			`actor_profile.validator "zeta" is not registered in ActorTokenValidators`)
	})

	t.Run("profile shape errors win over not registered", func(t *testing.T) {
		t.Parallel()
		cfg := actorProfileConfig(t, validActorProfile())
		cfg.DelegateClients[0].TokenExchange.IDJAG = nil
		require.EqualError(t, cfg.Validate(),
			`delegate_clients: client_id "delegate": actor_profile requires id_jag.targets`)
	})

	t.Run("empty name keeps the required message", func(t *testing.T) {
		t.Parallel()
		cfg := actorProfileConfig(t, &DelegateClientActorProfile{ActorTokenType: oauthproto.TokenTypeJWT})
		cfg.ActorTokenValidators = validators
		require.EqualError(t, cfg.Validate(),
			`delegate_clients: client_id "delegate": actor_profile.validator is required`)
	})
}

func TestConfiguredClientPoliciesActorProfile(t *testing.T) {
	t.Parallel()

	registered := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	other := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	validators := map[string]tokenexchange.ActorValidatorRegistration{
		"broker": {Validator: registered, ActIssuer: "spiffe://example.org"},
		"other":  {Validator: other, ActIssuer: "urn:example:other"},
	}

	withProfile := actorProfileTestExchange(validActorProfile())
	withoutProfile := actorProfileTestExchange(nil)

	t.Run("policy holds the registered instance and its ActIssuer", func(t *testing.T) {
		t.Parallel()
		policies, err := configuredClientPolicies([]DelegateClient{
			{ClientID: "with", TokenExchange: withProfile},
			{ClientID: "without", TokenExchange: withoutProfile},
			{ClientID: "any", TokenExchange: &DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensAny}},
		}, validators)
		require.NoError(t, err)

		profile := policies["with"].ActorProfile
		require.NotNil(t, profile)
		assert.Equal(t, "broker", profile.Name)
		assert.Equal(t, oauthproto.TokenTypeJWT, profile.ActorTokenType)
		assert.Same(t, registered, profile.Validator)
		assert.Equal(t, "spiffe://example.org", profile.ActIssuer)

		require.Contains(t, policies, "without")
		assert.Nil(t, policies["without"].ActorProfile)
		assert.NotContains(t, policies, "any")
	})
}
