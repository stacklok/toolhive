// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const actorProfileTestSecret = "0123456789abcdef0123456789abcdef"

func actorProfileTestExchange(profile *DelegateClientActorProfile) *DelegateClientTokenExchange {
	return &DelegateClientTokenExchange{
		SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted,
		AllowedSourceClients:    []string{"tui"},
		SubjectTokenAudiences:   []string{"https://input.example.com"},
		ExchangeRefreshTokens:   &DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: "12h"},
		IDJAG:                   &DelegateClientIDJAG{Targets: []DelegateClientIDJAGTarget{idJAGTestTarget()}},
		ActorProfile:            profile,
	}
}

func validActorProfile() *DelegateClientActorProfile {
	return &DelegateClientActorProfile{Validator: "broker", ActorTokenType: oauthproto.TokenTypeJWT}
}

func actorProfileRunConfigClient(exchange *DelegateClientTokenExchange) DelegateClientRunConfig {
	return DelegateClientRunConfig{
		ClientID: "delegate", ClientSecretEnvVar: "SECRET", TokenExchange: exchange,
		Scopes: []string{"openid"}, Audiences: []string{"https://mcp.example.com"},
	}
}

func validateActorProfileViaRunConfig(t *testing.T, cfg *RunConfig) error {
	t.Helper()
	normalized, err := NormalizeInboundGrants(cfg)
	require.NoError(t, err)
	return validateDelegateClients(normalized.DelegateClients,
		[]string{"openid", "offline_access"},
		[]string{"https://mcp.example.com", "https://input.example.com"},
		idJAGTestIssuer, true)
}

func TestActorProfileValidate(t *testing.T) {
	t.Parallel()

	const prefix = `delegate_clients: client_id "delegate": `
	tests := []struct {
		name    string
		mutate  func(*DelegateClientTokenExchange)
		wantErr string
	}{
		{name: "valid profile", mutate: func(*DelegateClientTokenExchange) {}},
		{name: "no profile", mutate: func(e *DelegateClientTokenExchange) { e.ActorProfile = nil }},
		{
			name:    "no id_jag block",
			mutate:  func(e *DelegateClientTokenExchange) { e.IDJAG = nil },
			wantErr: prefix + "actor_profile requires id_jag.targets",
		},
		{
			name: "any mode is refused by the id_jag check",
			mutate: func(e *DelegateClientTokenExchange) {
				e.SelfIssuedSubjectTokens = SelfIssuedSubjectTokensAny
				e.AllowedSourceClients = nil
				e.SubjectTokenAudiences = nil
				e.ExchangeRefreshTokens = nil
			},
			wantErr: prefix + errIDJAGRequiresRestricted.Error(),
		},
		{
			name:    "no refresh tokens block is refused by the id_jag check",
			mutate:  func(e *DelegateClientTokenExchange) { e.ExchangeRefreshTokens = nil },
			wantErr: prefix + errIDJAGRequiresSubjectSource.Error(),
		},
		{
			name: "id_jag error comes before a profile shape error",
			mutate: func(e *DelegateClientTokenExchange) {
				e.ExchangeRefreshTokens = nil
				e.ActorProfile.Validator = ""
			},
			wantErr: prefix + errIDJAGRequiresSubjectSource.Error(),
		},
		{
			name:    "missing validator",
			mutate:  func(e *DelegateClientTokenExchange) { e.ActorProfile.Validator = "" },
			wantErr: prefix + "actor_profile.validator is required",
		},
		{
			name:    "missing actor token type",
			mutate:  func(e *DelegateClientTokenExchange) { e.ActorProfile.ActorTokenType = "" },
			wantErr: prefix + "actor_profile.actor_token_type is required",
		},
		{
			name:    "relative actor token type",
			mutate:  func(e *DelegateClientTokenExchange) { e.ActorProfile.ActorTokenType = "/relative" },
			wantErr: prefix + `actor_profile.actor_token_type "/relative" is not an absolute URI`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			exchange := actorProfileTestExchange(validActorProfile())
			tt.mutate(exchange)
			runClient := actorProfileRunConfigClient(exchange)
			allowed := []string{"https://mcp.example.com", "https://input.example.com"}
			supported := []string{"openid", "offline_access"}

			errs := map[string]error{
				"delegate_clients": validateDelegateClients([]DelegateClientRunConfig{runClient}, supported, allowed, idJAGTestIssuer, true),
				"inbound_grants": validateActorProfileViaRunConfig(t, &RunConfig{InboundGrants: &InboundGrantsRunConfig{
					TokenExchange: &TokenExchangeInboundGrantRunConfig{DelegateClients: []DelegateClientRunConfig{runClient}},
				}}),
				"resolved": validateResolvedDelegateClients([]DelegateClient{{
					ClientID: "delegate", ClientSecret: actorProfileTestSecret, TokenExchange: exchange,
					Scopes: []string{"openid"}, Audiences: []string{"https://mcp.example.com"},
				}}, supported, allowed, idJAGTestIssuer, true),
			}
			for placement, err := range errs {
				if tt.wantErr == "" {
					assert.NoError(t, err, placement)
					continue
				}
				require.EqualError(t, err, tt.wantErr, placement)
			}
		})
	}
}

func TestActorProfileValidateTopLevelPlacement(t *testing.T) {
	t.Parallel()

	exchange := actorProfileTestExchange(&DelegateClientActorProfile{ActorTokenType: oauthproto.TokenTypeJWT})
	err := validateActorProfileViaRunConfig(t, &RunConfig{
		DelegateClients: []DelegateClientRunConfig{actorProfileRunConfigClient(exchange)},
	})
	require.EqualError(t, err, `delegate_clients: client_id "delegate": actor_profile.validator is required`)
}

func TestActorProfileTokenTypeIsAbsoluteURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		actorTokenType string
		valid          bool
	}{
		{actorTokenType: "urn:ietf:params:oauth:token-type:jwt", valid: true},
		{actorTokenType: "https://example.com/type", valid: true},
		{actorTokenType: "jwt"},
		{actorTokenType: "://bad"},
	}
	for _, tt := range tests {
		t.Run(tt.actorTokenType, func(t *testing.T) {
			t.Parallel()

			err := validateActorProfile(delegateClientView{
				ClientID: "delegate",
				TokenExchange: actorProfileTestExchange(&DelegateClientActorProfile{
					Validator: "broker", ActorTokenType: tt.actorTokenType,
				}),
			})
			if tt.valid {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, `delegate_clients: client_id "delegate": actor_profile.actor_token_type `+
				`"`+tt.actorTokenType+`" is not an absolute URI`)
		})
	}
}

func TestActorProfileIsGoOnly(t *testing.T) {
	t.Parallel()

	// JSON is the wire form: the workload export and the generated SDK.
	const jsonDoc = `{"self_issued_subject_tokens": "restricted",
"actor_profile": {"validator": "mecatl-broker", "actor_token_type": "urn:ietf:params:oauth:token-type:jwt"}}`
	var fromJSON DelegateClientTokenExchange
	require.NoError(t, json.Unmarshal([]byte(jsonDoc), &fromJSON))
	assert.Nil(t, fromJSON.ActorProfile)

	encodedJSON, err := json.Marshal(actorProfileTestExchange(validActorProfile()))
	require.NoError(t, err)
	assert.NotContains(t, string(encodedJSON), "actor_profile")

	// YAML is the config-file form.
	const yamlDoc = `
self_issued_subject_tokens: restricted
actor_profile:
  validator: mecatl-broker
  actor_token_type: urn:ietf:params:oauth:token-type:jwt
`
	var fromYAML DelegateClientTokenExchange
	require.NoError(t, yaml.Unmarshal([]byte(yamlDoc), &fromYAML))
	assert.Nil(t, fromYAML.ActorProfile)

	encodedYAML, err := yaml.Marshal(actorProfileTestExchange(validActorProfile()))
	require.NoError(t, err)
	assert.NotContains(t, string(encodedYAML), "actor_profile")
}

func TestDelegateClientTokenExchangeCloneActorProfile(t *testing.T) {
	t.Parallel()

	assert.Nil(t, actorProfileTestExchange(nil).Clone().ActorProfile)

	original := actorProfileTestExchange(validActorProfile())
	clone := original.Clone()
	require.Equal(t, original, clone)

	clone.ActorProfile.Validator = "other"
	clone.ActorProfile.ActorTokenType = "https://example.com/type"
	assert.Equal(t, validActorProfile(), original.ActorProfile)

	clone.IDJAG.Targets[0].Issuer = "https://other.example.com"
	clone.IDJAG.Targets[0].Resources[0] = "https://other.example.com/mcp"
	assert.Equal(t, idJAGTestTarget(), original.IDJAG.Targets[0])
}
