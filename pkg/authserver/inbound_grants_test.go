// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
)

func TestNormalizeInboundGrants(t *testing.T) {
	t.Parallel()

	delegate := DelegateClientRunConfig{
		ClientID: "delegate", ClientSecretEnvVar: "DELEGATE_SECRET",
		Scopes: []string{"openid"}, Audiences: []string{"https://mcp.example.com"},
	}
	legacyJWT := &tokenexchange.JWTBearerGrantPolicy{
		MaxAssertionAge: "5m",
		SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
			Subject: "workload", AllowedResources: []string{"https://mcp.example.com"},
		}},
		AcceptedAudiences: []string{"https://auth.example.com/token"},
	}
	issuer := tokenexchange.TrustedIssuer{Name: "idp", IssuerURL: "https://idp.example.com"}

	tests := []struct {
		name string
		cfg  *RunConfig
		want *NormalizedInboundGrants
	}{
		{
			name: "inbound grants absent preserves legacy default capability",
			cfg:  &RunConfig{},
			want: &NormalizedInboundGrants{Capabilities: InboundGrantCapabilities{TokenExchange: true}},
		},
		{
			name: "inbound grants present with no families disables capabilities",
			cfg:  &RunConfig{InboundGrants: &InboundGrantsRunConfig{}},
			want: &NormalizedInboundGrants{},
		},
		{
			// Regression test: SPIFFE client-auth associations always
			// require the token-exchange grant (see validateSPIFFEGrants),
			// so a SPIFFE-only configuration must not leave
			// Capabilities.TokenExchange false -- that would disable the
			// RFC 8693 grant handler server-wide. This exact fix was
			// previously lost during a rebase because its only regression
			// coverage lived in a different package (pkg/authserver/runner);
			// this case guards it directly where the logic lives.
			name: "SPIFFE client auth alone sets the token exchange capability",
			cfg: &RunConfig{
				InboundGrants: &InboundGrantsRunConfig{
					SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
						TrustDomainRef:   "prod",
						PrincipalPattern: "spiffe://example.org/ns/default/agent",
						ClientID:         "spiffe-agent",
						Methods:          []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
						Scopes:           []string{"openid"},
						Audiences:        []string{"https://mcp.example.com"},
						GrantTypes:       []string{SPIFFEGrantTypeTokenExchange},
					}},
				},
			},
			want: &NormalizedInboundGrants{Capabilities: InboundGrantCapabilities{TokenExchange: true}},
		},
		{
			name: "legacy delegate RFC8693 and JWT policies are preserved and deprecated",
			cfg: &RunConfig{
				DelegateClients: []DelegateClientRunConfig{delegate},
				TrustedIssuers: []tokenexchange.TrustedIssuer{{
					Name: "idp", IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com",
					ActorClaim: "azp", AllowedActors: []string{"agent"}, ActorMatcher: `claims.team == "platform"`,
					AllowedDelegateClients: []string{"delegate"}, AllowMayAct: true, JWTBearerGrant: legacyJWT,
				}},
			},
			want: &NormalizedInboundGrants{
				DelegateClients: []DelegateClientRunConfig{delegate},
				TrustedIssuers: []tokenexchange.TrustedIssuer{{
					Name: "idp", IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com",
					ActorClaim: "azp", AllowedActors: []string{"agent"}, ActorMatcher: `claims.team == "platform"`,
					AllowedDelegateClients: []string{"delegate"}, AllowMayAct: true, JWTBearerGrant: legacyJWT,
				}},
				Capabilities: InboundGrantCapabilities{TokenExchange: true, JWTBearer: true},
				DeprecatedFields: []DeprecatedFieldPath{
					{Path: "trusted_issuers[0]", Replacement: "inbound_grants.token_exchange.issuer_policies"},
					{Path: "trusted_issuers[0].jwt_bearer_grant", Replacement: "inbound_grants.jwt_bearer.issuer_policies"},
					{Path: "delegate_clients", Replacement: "inbound_grants.token_exchange.delegate_clients"},
				},
			},
		},
		{
			name: "canonical token exchange and JWT bearer policies are applied",
			cfg: &RunConfig{
				TrustedIssuers: []tokenexchange.TrustedIssuer{issuer},
				InboundGrants: &InboundGrantsRunConfig{
					TokenExchange: &TokenExchangeInboundGrantRunConfig{
						DelegateClients: []DelegateClientRunConfig{delegate},
						IssuerPolicies: []TokenExchangeIssuerPolicyRunConfig{{
							IssuerRef: "idp", ExpectedAudience: "https://mcp.example.com", ActorClaim: "azp",
							AllowedActors: []string{"agent"}, ActorMatcher: `claims.team == "platform"`,
							AllowedDelegateClients: []string{"delegate"}, AllowMayAct: true,
						}},
					},
					JWTBearer: &JWTBearerInboundGrantRunConfig{IssuerPolicies: []JWTBearerIssuerPolicyRunConfig{{
						IssuerRef: "idp", MaxAssertionAge: "5m",
						SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
							Subject: "workload", AllowedResources: []string{"https://mcp.example.com"},
						}},
						AcceptedAudiences: []string{"https://auth.example.com/token"},
					}}},
				},
			},
			want: &NormalizedInboundGrants{
				DelegateClients: []DelegateClientRunConfig{delegate},
				TrustedIssuers: []tokenexchange.TrustedIssuer{{
					Name: "idp", IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com",
					ActorClaim: "azp", AllowedActors: []string{"agent"}, ActorMatcher: `claims.team == "platform"`,
					AllowedDelegateClients: []string{"delegate"}, AllowMayAct: true, JWTBearerGrant: legacyJWT,
				}},
				Capabilities: InboundGrantCapabilities{TokenExchange: true, JWTBearer: true},
			},
		},
		{
			name: "legacy token exchange coexists with canonical JWT bearer",
			cfg: &RunConfig{
				DelegateClients: []DelegateClientRunConfig{delegate}, TrustedIssuers: []tokenexchange.TrustedIssuer{issuer},
				InboundGrants: &InboundGrantsRunConfig{JWTBearer: &JWTBearerInboundGrantRunConfig{
					IssuerPolicies: []JWTBearerIssuerPolicyRunConfig{{IssuerRef: "idp", MaxAssertionAge: "5m"}},
				}},
			},
			want: &NormalizedInboundGrants{
				DelegateClients: []DelegateClientRunConfig{delegate},
				TrustedIssuers: []tokenexchange.TrustedIssuer{{
					Name: "idp", IssuerURL: "https://idp.example.com",
					JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{MaxAssertionAge: "5m"},
				}},
				Capabilities: InboundGrantCapabilities{TokenExchange: true, JWTBearer: true},
				DeprecatedFields: []DeprecatedFieldPath{{
					Path: "delegate_clients", Replacement: "inbound_grants.token_exchange.delegate_clients",
				}},
			},
		},
		{
			name: "legacy JWT bearer coexists with canonical token exchange",
			cfg: &RunConfig{
				TrustedIssuers: []tokenexchange.TrustedIssuer{{Name: "idp", IssuerURL: "https://idp.example.com", JWTBearerGrant: legacyJWT}},
				InboundGrants: &InboundGrantsRunConfig{TokenExchange: &TokenExchangeInboundGrantRunConfig{
					IssuerPolicies: []TokenExchangeIssuerPolicyRunConfig{{
						IssuerRef: "idp", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"delegate"},
					}},
				}},
			},
			want: &NormalizedInboundGrants{
				TrustedIssuers: []tokenexchange.TrustedIssuer{{
					Name: "idp", IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com",
					AllowedDelegateClients: []string{"delegate"}, JWTBearerGrant: legacyJWT,
				}},
				Capabilities: InboundGrantCapabilities{TokenExchange: true, JWTBearer: true},
				DeprecatedFields: []DeprecatedFieldPath{{
					Path: "trusted_issuers[0].jwt_bearer_grant", Replacement: "inbound_grants.jwt_bearer.issuer_policies",
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeInboundGrants(tt.cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeInboundGrantsRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	issuer := tokenexchange.TrustedIssuer{Name: "idp", IssuerURL: "https://idp.example.com"}
	canonicalTokenExchange := &InboundGrantsRunConfig{TokenExchange: &TokenExchangeInboundGrantRunConfig{}}
	canonicalJWT := &InboundGrantsRunConfig{JWTBearer: &JWTBearerInboundGrantRunConfig{}}

	tests := []struct {
		name    string
		cfg     *RunConfig
		errText string
	}{
		{name: "nil config", errText: "config is required"},
		{
			name:    "legacy delegate conflicts with canonical token exchange",
			cfg:     &RunConfig{DelegateClients: []DelegateClientRunConfig{{ClientID: "delegate"}}, InboundGrants: canonicalTokenExchange},
			errText: "inbound_grants.token_exchange conflicts with legacy delegate_clients",
		},
		{
			name:    "legacy RFC8693 policy conflicts with canonical token exchange",
			cfg:     &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{{IssuerURL: "https://idp.example.com", ExpectedAudience: "aud"}}, InboundGrants: canonicalTokenExchange},
			errText: "inbound_grants.token_exchange conflicts with legacy delegate_clients or RFC 8693 policy",
		},
		{
			name:    "legacy JWT policy conflicts with canonical JWT bearer",
			cfg:     &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{{IssuerURL: "https://idp.example.com", JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{}}}, InboundGrants: canonicalJWT},
			errText: "inbound_grants.jwt_bearer conflicts with legacy",
		},
		{
			name:    "duplicate issuer names",
			cfg:     &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{issuer, {Name: "idp", IssuerURL: "https://other.example.com"}}},
			errText: `trusted_issuers[1].name duplicates trusted_issuers[0].name "idp"`,
		},
		{
			name:    "duplicate issuer URLs",
			cfg:     &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{issuer, {Name: "other", IssuerURL: "https://idp.example.com"}}},
			errText: `trusted_issuers[1].issuer_url duplicates trusted_issuers[0].issuer_url "https://idp.example.com"`,
		},
		{
			name: "empty token exchange issuer ref",
			cfg: &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{issuer}, InboundGrants: &InboundGrantsRunConfig{
				TokenExchange: &TokenExchangeInboundGrantRunConfig{IssuerPolicies: []TokenExchangeIssuerPolicyRunConfig{{}}},
			}},
			errText: "inbound_grants.token_exchange.issuer_policies[0].issuer_ref is required",
		},
		{
			name: "unknown JWT bearer issuer ref",
			cfg: &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{issuer}, InboundGrants: &InboundGrantsRunConfig{
				JWTBearer: &JWTBearerInboundGrantRunConfig{IssuerPolicies: []JWTBearerIssuerPolicyRunConfig{{IssuerRef: "other"}}},
			}},
			errText: `issuer_ref references unknown or unnamed trusted issuer "other"`,
		},
		{
			name: "duplicate token exchange issuer ref",
			cfg: &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{issuer}, InboundGrants: &InboundGrantsRunConfig{
				TokenExchange: &TokenExchangeInboundGrantRunConfig{IssuerPolicies: []TokenExchangeIssuerPolicyRunConfig{{IssuerRef: "idp"}, {IssuerRef: "idp"}}},
			}},
			errText: `issuer_ref duplicates issuer policy [0] for "idp"`,
		},
		{
			name: "duplicate JWT bearer issuer ref",
			cfg: &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{issuer}, InboundGrants: &InboundGrantsRunConfig{
				JWTBearer: &JWTBearerInboundGrantRunConfig{IssuerPolicies: []JWTBearerIssuerPolicyRunConfig{{IssuerRef: "idp"}, {IssuerRef: "idp"}}},
			}},
			errText: `issuer_ref duplicates issuer policy [0] for "idp"`,
		},
		{
			name: "unnamed legacy issuer cannot be referenced",
			cfg: &RunConfig{TrustedIssuers: []tokenexchange.TrustedIssuer{{IssuerURL: "https://idp.example.com"}}, InboundGrants: &InboundGrantsRunConfig{
				TokenExchange: &TokenExchangeInboundGrantRunConfig{IssuerPolicies: []TokenExchangeIssuerPolicyRunConfig{{IssuerRef: "idp"}}},
			}},
			errText: `issuer_ref references unknown or unnamed trusted issuer "idp"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeInboundGrants(tt.cfg)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), tt.errText)
		})
	}
}

func TestNormalizeInboundGrantsDeepCopiesCallerInput(t *testing.T) {
	t.Parallel()

	cfg := &RunConfig{
		TrustedIssuers: []tokenexchange.TrustedIssuer{{Name: "idp", IssuerURL: "https://idp.example.com"}},
		InboundGrants: &InboundGrantsRunConfig{
			TokenExchange: &TokenExchangeInboundGrantRunConfig{
				DelegateClients: []DelegateClientRunConfig{{ClientID: "delegate", Scopes: []string{"openid"}, Audiences: []string{"resource"}}},
				IssuerPolicies: []TokenExchangeIssuerPolicyRunConfig{{
					IssuerRef: "idp", AllowedActors: []string{"actor"}, AllowedDelegateClients: []string{"delegate"},
				}},
			},
			JWTBearer: &JWTBearerInboundGrantRunConfig{IssuerPolicies: []JWTBearerIssuerPolicyRunConfig{{
				IssuerRef: "idp", SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
					Subject: "workload", AllowedResources: []string{"resource"},
				}}, AcceptedAudiences: []string{"token-endpoint"},
			}}},
		},
	}
	before := cloneRunConfigForTest(t, cfg)

	got, err := NormalizeInboundGrants(cfg)
	require.NoError(t, err)
	assert.Equal(t, before, cfg, "normalization must not mutate its input")

	got.DelegateClients[0].Scopes[0] = "changed"
	got.DelegateClients[0].Audiences[0] = "changed"
	got.TrustedIssuers[0].AllowedActors[0] = "changed"
	got.TrustedIssuers[0].AllowedDelegateClients[0] = "changed"
	got.TrustedIssuers[0].JWTBearerGrant.SubjectBindings[0].AllowedResources[0] = "changed"
	got.TrustedIssuers[0].JWTBearerGrant.AcceptedAudiences[0] = "changed"
	assert.Equal(t, before, cfg, "mutating normalized nested values must not mutate the input")

	cfg.InboundGrants.TokenExchange.DelegateClients[0].Scopes[0] = "source-change"
	cfg.InboundGrants.TokenExchange.IssuerPolicies[0].AllowedActors[0] = "source-change"
	cfg.InboundGrants.JWTBearer.IssuerPolicies[0].SubjectBindings[0].AllowedResources[0] = "source-change"
	assert.Equal(t, "changed", got.DelegateClients[0].Scopes[0])
	assert.Equal(t, "changed", got.TrustedIssuers[0].AllowedActors[0])
	assert.Equal(t, "changed", got.TrustedIssuers[0].JWTBearerGrant.SubjectBindings[0].AllowedResources[0])
}

func TestInboundGrantsRunConfigSerializationLayouts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		input             string
		unmarshal         func([]byte, *RunConfig) error
		marshal           func(*RunConfig) ([]byte, error)
		serializedFields  []string
		wantCapabilities  InboundGrantCapabilities
		wantSPIFFEClients int
	}{
		{
			name:             "canonical JSON",
			input:            `{"trusted_issuers":[{"name":"idp","issuer_url":"https://idp.example.com","expected_audience":""}],"inbound_grants":{"spiffe_client_auth":[{"trust_domain_ref":"prod","principal_pattern":"spiffe://example.org/agent","client_id":"agent","methods":["spiffe_x509"],"audiences":["resource"],"scopes":["openid"],"grant_types":["urn:ietf:params:oauth:grant-type:token-exchange"]}],"token_exchange":{"delegate_clients":[{"client_id":"delegate","scopes":["openid"],"audiences":["resource"]}],"issuer_policies":[{"issuer_ref":"idp","expected_audience":"resource","allowed_delegate_clients":["delegate"]}]},"jwt_bearer":{"issuer_policies":[{"issuer_ref":"idp","max_assertion_age":"5m","subject_bindings":[{"subject":"workload","allowed_resources":["resource"]}],"accepted_audiences":["token-endpoint"]}]}}}`,
			unmarshal:        func(data []byte, cfg *RunConfig) error { return json.Unmarshal(data, cfg) },
			marshal:          func(cfg *RunConfig) ([]byte, error) { return json.Marshal(cfg) },
			serializedFields: []string{`"inbound_grants"`, `"token_exchange"`, `"jwt_bearer"`, `"spiffe_client_auth"`, `"issuer_ref"`},
			wantCapabilities: InboundGrantCapabilities{TokenExchange: true, JWTBearer: true}, wantSPIFFEClients: 1,
		},
		{
			name:             "canonical YAML",
			input:            "trusted_issuers:\n  - name: idp\n    issuer_url: https://idp.example.com\n    expected_audience: \"\"\ninbound_grants:\n  spiffe_client_auth:\n    - trust_domain_ref: prod\n      principal_pattern: spiffe://example.org/agent\n      client_id: agent\n      methods: [spiffe_x509]\n      audiences: [resource]\n      scopes: [openid]\n      grant_types: [\"urn:ietf:params:oauth:grant-type:token-exchange\"]\n  token_exchange:\n    issuer_policies:\n      - issuer_ref: idp\n        expected_audience: resource\n        allowed_delegate_clients: [delegate]\n  jwt_bearer:\n    issuer_policies:\n      - issuer_ref: idp\n        max_assertion_age: 5m\n        subject_bindings:\n          - subject: workload\n            allowed_resources: [resource]\n",
			unmarshal:        func(data []byte, cfg *RunConfig) error { return yaml.Unmarshal(data, cfg) },
			marshal:          func(cfg *RunConfig) ([]byte, error) { return yaml.Marshal(cfg) },
			serializedFields: []string{"inbound_grants:", "token_exchange:", "jwt_bearer:", "spiffe_client_auth:", "issuer_ref:"},
			wantCapabilities: InboundGrantCapabilities{TokenExchange: true, JWTBearer: true}, wantSPIFFEClients: 1,
		},
		{
			name:             "released legacy JSON",
			input:            `{"delegate_clients":[{"client_id":"delegate","scopes":["openid"],"audiences":["resource"]}],"trusted_issuers":[{"issuer_url":"https://idp.example.com","expected_audience":"resource","allowed_actors":["agent"],"allowed_delegate_clients":["delegate"],"jwt_bearer_grant":{"max_assertion_age":"5m","subject_bindings":[{"subject":"workload","allowed_resources":["resource"]}]}}]}`,
			unmarshal:        func(data []byte, cfg *RunConfig) error { return json.Unmarshal(data, cfg) },
			marshal:          func(cfg *RunConfig) ([]byte, error) { return json.Marshal(cfg) },
			serializedFields: []string{`"delegate_clients"`, `"trusted_issuers"`, `"expected_audience"`, `"jwt_bearer_grant"`},
			wantCapabilities: InboundGrantCapabilities{TokenExchange: true, JWTBearer: true},
		},
		{
			name:             "released legacy YAML",
			input:            "delegate_clients:\n  - client_id: delegate\n    scopes: [openid]\n    audiences: [resource]\ntrusted_issuers:\n  - issuer_url: https://idp.example.com\n    expected_audience: resource\n    allowed_actors: [agent]\n    allowed_delegate_clients: [delegate]\n    jwt_bearer_grant:\n      max_assertion_age: 5m\n      subject_bindings:\n        - subject: workload\n          allowed_resources: [resource]\n",
			unmarshal:        func(data []byte, cfg *RunConfig) error { return yaml.Unmarshal(data, cfg) },
			marshal:          func(cfg *RunConfig) ([]byte, error) { return yaml.Marshal(cfg) },
			serializedFields: []string{"delegate_clients:", "trusted_issuers:", "expected_audience:", "jwt_bearer_grant:"},
			wantCapabilities: InboundGrantCapabilities{TokenExchange: true, JWTBearer: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cfg RunConfig
			require.NoError(t, tt.unmarshal([]byte(tt.input), &cfg))

			normalized, err := NormalizeInboundGrants(&cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCapabilities, normalized.Capabilities)
			gotSPIFFEClients := 0
			if cfg.InboundGrants != nil {
				gotSPIFFEClients = len(cfg.InboundGrants.SPIFFEClientAuth)
			}
			assert.Equal(t, tt.wantSPIFFEClients, gotSPIFFEClients)

			encoded, err := tt.marshal(&cfg)
			require.NoError(t, err)
			for _, field := range tt.serializedFields {
				assert.Contains(t, string(encoded), field)
			}

			var roundTripped RunConfig
			require.NoError(t, tt.unmarshal(encoded, &roundTripped))
			if len(cfg.Upstreams) == 0 && len(roundTripped.Upstreams) == 0 {
				cfg.Upstreams = nil
				roundTripped.Upstreams = nil
			}
			if len(cfg.AllowedAudiences) == 0 && len(roundTripped.AllowedAudiences) == 0 {
				cfg.AllowedAudiences = nil
				roundTripped.AllowedAudiences = nil
			}
			assert.Equal(t, cfg, roundTripped)
		})
	}
}

func cloneRunConfigForTest(t *testing.T, cfg *RunConfig) *RunConfig {
	t.Helper()
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	var cloned RunConfig
	require.NoError(t, json.Unmarshal(data, &cloned))
	return &cloned
}
