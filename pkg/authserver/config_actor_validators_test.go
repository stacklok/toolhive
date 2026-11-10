// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
)

// actorValidatorsCase is a validator map and the error startup validation must
// report for it, or "" when the map is accepted.
type actorValidatorsCase struct {
	name       string
	validators map[string]tokenexchange.ActorValidatorRegistration
	wantErr    string
}

func TestConfigValidateActorTokenValidators(t *testing.T) {
	t.Parallel()

	valid := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	reg := func(actIssuer string) tokenexchange.ActorValidatorRegistration {
		return tokenexchange.ActorValidatorRegistration{Validator: valid, ActIssuer: actIssuer}
	}
	const issuer = "https://registry.example.com"

	tests := []actorValidatorsCase{
		{name: "nil map passes"},
		{name: "empty map passes", validators: map[string]tokenexchange.ActorValidatorRegistration{}},
		{
			name:       "non-nil validator passes",
			validators: map[string]tokenexchange.ActorValidatorRegistration{"agent-registry": reg(issuer)},
		},
		{
			name:       "empty name rejected",
			validators: map[string]tokenexchange.ActorValidatorRegistration{"": reg(issuer)},
			wantErr:    "actor token validator name must not be empty",
		},
		{
			name: "nil interface value rejected",
			validators: map[string]tokenexchange.ActorValidatorRegistration{
				"agent-registry": {ActIssuer: issuer},
			},
			wantErr: `actor token validator "agent-registry" is nil`,
		},
		{
			name: "typed nil pointer rejected",
			validators: map[string]tokenexchange.ActorValidatorRegistration{
				"agent-registry": {Validator: (*mocks.MockActorTokenValidator)(nil), ActIssuer: issuer},
			},
			wantErr: `actor token validator "agent-registry" is nil`,
		},
		{
			name:       "toolhive prefix rejected",
			validators: map[string]tokenexchange.ActorValidatorRegistration{"toolhive.jwt-svid": reg(issuer)},
			wantErr:    `actor token validator name "toolhive.jwt-svid" is reserved for built-in validators`,
		},
		{
			name: "first bad name in sorted order is reported",
			validators: map[string]tokenexchange.ActorValidatorRegistration{
				"toolhive.z": reg(issuer),
				"b":          {ActIssuer: issuer},
				"a":          {ActIssuer: issuer},
			},
			wantErr: `actor token validator "a" is nil`,
		},
		{
			name: "nil validator is reported before a bad ActIssuer",
			validators: map[string]tokenexchange.ActorValidatorRegistration{
				"a": {},
			},
			wantErr: `actor token validator "a" is nil`,
		},
	}
	tests = append(tests, actIssuerTestCases(reg)...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := minimalValidConfig(t)
			cfg.ActorTokenValidators = tt.validators
			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

// actIssuerTestCases holds the ActIssuer rules for a server whose issuer is
// https://issuer.example.com (minimalValidConfig).
func actIssuerTestCases(
	reg func(string) tokenexchange.ActorValidatorRegistration,
) []actorValidatorsCase {
	one := func(actIssuer string) map[string]tokenexchange.ActorValidatorRegistration {
		return map[string]tokenexchange.ActorValidatorRegistration{"broker": reg(actIssuer)}
	}
	const prefix = `actor token validator "broker": `
	const canonicalFormTail = `no default port, dot segments, empty path segments, unneeded percent-encoding, ` +
		`or an empty or zero-padded port`
	return []actorValidatorsCase{
		{name: "ActIssuer https passes", validators: one("https://registry.example.com")},
		{name: "ActIssuer https with path passes", validators: one("https://registry.example.com/agents/v1")},
		{name: "ActIssuer loopback http passes", validators: one("http://127.0.0.1:8080")},
		{name: "ActIssuer localhost http passes", validators: one("http://localhost")},
		{name: "ActIssuer spiffe trust domain passes", validators: one("spiffe://example.org")},
		{name: "ActIssuer urn passes", validators: one("urn:example:agents")},
		{
			name: "ActIssuer shared by two registrations passes",
			validators: map[string]tokenexchange.ActorValidatorRegistration{
				"a": reg("spiffe://example.org"), "b": reg("spiffe://example.org"),
			},
		},
		{name: "ActIssuer required", validators: one(""), wantErr: prefix + "ActIssuer is required"},
		{
			name:       "ActIssuer without scheme",
			validators: one("registry.example.com"),
			wantErr:    prefix + `ActIssuer "registry.example.com" is not an absolute URI: has no scheme`,
		},
		{
			name:       "ActIssuer with userinfo",
			validators: one("https://user@registry.example.com"),
			wantErr:    prefix + `ActIssuer "https://user@registry.example.com" is not an absolute URI: URI contains userinfo`,
		},
		{
			name:       "ActIssuer with fragment",
			validators: one("https://registry.example.com/#x"),
			wantErr: prefix + `ActIssuer "https://registry.example.com/#x" is not an absolute URI: ` +
				`URI contains '#' at offset 29`,
		},
		{
			name:       "ActIssuer with query",
			validators: one("https://registry.example.com/?a=b"),
			wantErr:    prefix + `ActIssuer "https://registry.example.com/?a=b" is not an absolute URI: URI contains a query`,
		},
		{
			name:       "ActIssuer with empty query marker",
			validators: one("https://registry.example.com/?"),
			wantErr:    prefix + `ActIssuer "https://registry.example.com/?" is not an absolute URI: URI contains a query`,
		},
		{
			name:       "ActIssuer with nothing after the scheme",
			validators: one("spiffe:"),
			wantErr:    prefix + `ActIssuer "spiffe:" is not an absolute URI: URI has nothing after the scheme`,
		},
		{
			name:       "ActIssuer with a space",
			validators: one("urn:a b"),
			wantErr: prefix + `ActIssuer "urn:a b" is not an absolute URI: ` +
				`byte 0x20 at offset 5 is not allowed in a URI`,
		},
		{
			name:       "ActIssuer with a bad percent-escape",
			validators: one("urn:a%zz"),
			wantErr:    prefix + `ActIssuer "urn:a%zz" is not an absolute URI: bad percent-escape at offset 5`,
		},
		{
			name:       "ActIssuer too long",
			validators: one("urn:" + strings.Repeat("a", 2045)),
			wantErr: prefix + `ActIssuer "urn:` + strings.Repeat("a", 2045) +
				`" is not an absolute URI: length 2049 is outside 1 to 2048 bytes`,
		},
		{
			name:       "ActIssuer with upper-case scheme",
			validators: one("SPIFFE://example.org"),
			wantErr: prefix + `ActIssuer "SPIFFE://example.org" is not in canonical form: lower-case scheme and host, ` +
				canonicalFormTail,
		},
		{
			name:       "ActIssuer with upper-case host",
			validators: one("https://Registry.example.com"),
			wantErr: prefix + `ActIssuer "https://Registry.example.com" is not in canonical form: lower-case scheme and host, ` +
				canonicalFormTail,
		},
		{
			name:       "ActIssuer with default port",
			validators: one("https://registry.example.com:443"),
			wantErr: prefix + `ActIssuer "https://registry.example.com:443" is not in canonical form: lower-case scheme and host, ` +
				canonicalFormTail,
		},
		{
			name:       "ActIssuer with dot segment",
			validators: one("https://registry.example.com/a/../b"),
			wantErr: prefix + `ActIssuer "https://registry.example.com/a/../b" is not in canonical form: lower-case scheme and host, ` +
				canonicalFormTail,
		},
		{
			name:       "ActIssuer with a zero-padded port",
			validators: one("https://registry.example.com:0443"),
			wantErr: prefix + `ActIssuer "https://registry.example.com:0443" is not in canonical form: lower-case scheme and host, ` +
				canonicalFormTail,
		},
		{
			name:       "ActIssuer with an empty port",
			validators: one("https://registry.example.com:"),
			wantErr: prefix + `ActIssuer "https://registry.example.com:" is not in canonical form: lower-case scheme and host, ` +
				canonicalFormTail,
		},
		{
			name:       "ActIssuer https without slashes has no host",
			validators: one("https:registry.example.com"),
			wantErr:    prefix + `ActIssuer "https:registry.example.com" must name a host`,
		},
		{
			name:       "ActIssuer https with an empty host",
			validators: one("https:///agents"),
			wantErr:    prefix + `ActIssuer "https:///agents" must name a host`,
		},
		{
			name:       "ActIssuer http on a public host",
			validators: one("http://registry.example.com"),
			wantErr:    prefix + `ActIssuer "http://registry.example.com" must use https, or http on a loopback host`,
		},
		{
			name:       "ActIssuer is the server issuer",
			validators: one("https://issuer.example.com"),
			wantErr:    prefix + "ActIssuer must not be this server's issuer",
		},
		{
			name:       "ActIssuer is the server issuer with a trailing slash",
			validators: one("https://issuer.example.com/"),
			wantErr:    prefix + "ActIssuer must not be this server's issuer",
		},
		{
			name:       "ActIssuer is the server issuer in another case",
			validators: one("HTTPS://Issuer.Example.com"),
			wantErr: prefix + `ActIssuer "HTTPS://Issuer.Example.com" is not in canonical form: lower-case scheme and host, ` +
				canonicalFormTail,
		},
	}
}

// runConfigActorValidatorsCase is a validator map checked against a RunConfig.
type runConfigActorValidatorsCase struct {
	name       string
	cfg        *RunConfig
	validators map[string]tokenexchange.ActorValidatorRegistration
	wantErr    string
}

func TestValidateActorProfilesForRunConfig(t *testing.T) {
	t.Parallel()

	const serverIssuer = "https://issuer.example.com"
	withIssuer := &RunConfig{Issuer: serverIssuer}
	reg := func(actIssuer string) tokenexchange.ActorValidatorRegistration {
		return tokenexchange.ActorValidatorRegistration{Validator: &mocks.MockActorTokenValidator{}, ActIssuer: actIssuer}
	}
	profileConfig := &RunConfig{InboundGrants: &InboundGrantsRunConfig{
		TokenExchange: &TokenExchangeInboundGrantRunConfig{DelegateClients: []DelegateClientRunConfig{
			actorProfileRunConfigClient(actorProfileTestExchange(validActorProfile())),
		}},
	}}
	topLevelConfig := &RunConfig{DelegateClients: []DelegateClientRunConfig{
		actorProfileRunConfigClient(actorProfileTestExchange(validActorProfile())),
	}}
	const unregistered = `delegate_clients: client_id "delegate": ` +
		`actor_profile.validator "broker" is not registered in ActorTokenValidators`

	tests := []runConfigActorValidatorsCase{
		{name: "nil map and no profiles passes", cfg: &RunConfig{}},
		{
			name:       "empty name",
			cfg:        withIssuer,
			validators: map[string]tokenexchange.ActorValidatorRegistration{"": reg("urn:x")},
			wantErr:    "actor token validator name must not be empty",
		},
		{
			name:       "reserved name",
			cfg:        withIssuer,
			validators: map[string]tokenexchange.ActorValidatorRegistration{"toolhive.x": reg("urn:x")},
			wantErr:    `actor token validator name "toolhive.x" is reserved for built-in validators`,
		},
		{
			name:       "nil value",
			cfg:        withIssuer,
			validators: map[string]tokenexchange.ActorValidatorRegistration{"broker": {ActIssuer: "urn:x"}},
			wantErr:    `actor token validator "broker" is nil`,
		},
		{
			name: "typed nil value",
			cfg:  withIssuer,
			validators: map[string]tokenexchange.ActorValidatorRegistration{
				"broker": {Validator: (*mocks.MockActorTokenValidator)(nil), ActIssuer: "urn:x"},
			},
			wantErr: `actor token validator "broker" is nil`,
		},
		{name: "unregistered name in inbound_grants", cfg: profileConfig, wantErr: unregistered},
		{name: "unregistered name in top-level delegate_clients", cfg: topLevelConfig, wantErr: unregistered},
		{name: "nil config", cfg: nil, wantErr: "normalize inbound grants: config is required"},
		{
			name:       "other name registered only",
			cfg:        profileConfig,
			validators: map[string]tokenexchange.ActorValidatorRegistration{"other": reg("urn:x")},
			wantErr:    unregistered,
		},
		{
			name:       "registered name passes",
			cfg:        profileConfig,
			validators: map[string]tokenexchange.ActorValidatorRegistration{"broker": reg("spiffe://example.org")},
		},
	}
	for _, c := range actIssuerTestCases(reg) {
		tests = append(tests, runConfigActorValidatorsCase{
			name: c.name, cfg: withIssuer, validators: c.validators, wantErr: c.wantErr,
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateActorProfilesForRunConfig(tt.cfg, tt.validators)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantErr)
		})
	}
}
