// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const blanketTrustWarning = "delegate client has blanket self-issued token exchange rights"

// TestSelfIssuedSubjectTokensModesMatchRegistration keeps the mode names here
// in step with the copies registration validates against, which it cannot
// import from this package.
func TestSelfIssuedSubjectTokensModesMatchRegistration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    SelfIssuedSubjectTokens
		wantErr bool
	}{
		{name: "any", mode: SelfIssuedSubjectTokensAny},
		{name: "restricted", mode: SelfIssuedSubjectTokensRestricted},
		{name: "unknown", mode: "everything", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := registration.ValidateConfiguredClient(registration.ConfiguredClientSpec{
				HasSecret:               true,
				TokenExchange:           true,
				SelfIssuedSubjectTokens: string(tt.mode),
			})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func trustModeTestClient(id string, grants []string, policy *DelegateClientTokenExchange) DelegateClient {
	return DelegateClient{
		ClientID:      id,
		ClientSecret:  "delegate-secret-well-above-the-minimum-length",
		GrantTypes:    grants,
		Scopes:        []string{"openid"},
		Audiences:     []string{"https://mcp.example.com"},
		TokenExchange: policy,
	}
}

func TestRestrictedClientPolicies(t *testing.T) {
	t.Parallel()

	exchangeOnly := []string{oauthproto.GrantTypeTokenExchange}
	tests := []struct {
		name    string
		clients []DelegateClient
		want    map[string]tokenexchange.ConfiguredClientPolicy
	}{
		{name: "no block", clients: []DelegateClient{trustModeTestClient("legacy", nil, nil)}},
		{name: "explicit any", clients: []DelegateClient{trustModeTestClient("broker", exchangeOnly,
			&DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensAny})}},
		{name: "restricted without source clients", clients: []DelegateClient{trustModeTestClient("broker", exchangeOnly,
			&DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted})},
			want: map[string]tokenexchange.ConfiguredClientPolicy{"broker": {}}},
		{name: "only restricted clients get an entry", clients: []DelegateClient{
			trustModeTestClient("legacy", nil, nil),
			trustModeTestClient("any-broker", exchangeOnly,
				&DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensAny}),
			trustModeTestClient("broker", exchangeOnly, &DelegateClientTokenExchange{
				SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted,
				AllowedSourceClients:    []string{"tui"},
				APIAudience:             "https://mcp.example.com",
			}),
		}, want: map[string]tokenexchange.ConfiguredClientPolicy{"broker": {
			AllowedSourceClients: []string{"tui"}, APIAudience: "https://mcp.example.com",
		}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policies, err := configuredClientPolicies(tt.clients)
			require.NoError(t, err)
			assert.Equal(t, tt.want, policies)
		})
	}
}

// TestWarnBlanketSelfIssuedExchange checks that every delegate
// left with blanket trust logs one WARN, with explicit_any for a client that
// declared any alongside grant_types and redeems_id_jag for a client without
// grant_types while an issuer accepts id_jag.
//
//nolint:paralleltest // captures the package-global slog.Default()
func TestWarnBlanketSelfIssuedExchange(t *testing.T) {
	exchangeOnly := []string{oauthproto.GrantTypeTokenExchange}
	anyMode := &DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensAny}
	restricted := &DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted}
	clients := []DelegateClient{
		trustModeTestClient("legacy", nil, nil),
		trustModeTestClient("no-grants-any", nil, anyMode),
		trustModeTestClient("explicit-any", exchangeOnly, anyMode),
		trustModeTestClient("restricted", exchangeOnly, restricted),
	}

	tests := []struct {
		name            string
		idJAGRedeemable bool
		want            map[string]map[string]bool
	}{
		{name: "id_jag not redeemable", want: map[string]map[string]bool{
			"legacy":        {},
			"no-grants-any": {},
			"explicit-any":  {"explicit_any": true},
		}},
		{name: "id_jag redeemable", idJAGRedeemable: true, want: map[string]map[string]bool{
			"legacy":        {"redeems_id_jag": true},
			"no-grants-any": {"redeems_id_jag": true},
			"explicit-any":  {"explicit_any": true},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			warnBlanketSelfIssuedExchange(clients, tt.idJAGRedeemable)

			got := map[string]map[string]bool{}
			scanner := bufio.NewScanner(&buf)
			for scanner.Scan() {
				var record map[string]any
				require.NoError(t, json.Unmarshal(scanner.Bytes(), &record))
				msg, _ := record["msg"].(string)
				if !strings.Contains(msg, blanketTrustWarning) {
					continue
				}
				clientID, _ := record["client_id"].(string)
				require.NotContains(t, got, clientID, "one WARN per client")
				flags := map[string]bool{}
				for _, key := range []string{"explicit_any", "redeems_id_jag"} {
					if v, ok := record[key].(bool); ok {
						flags[key] = v
					}
				}
				got[clientID] = flags
			}
			require.NoError(t, scanner.Err())
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIDJAGRedeemable(t *testing.T) {
	t.Parallel()

	issuer := func(types ...tokenexchange.JWTBearerAssertionType) tokenexchange.TrustedIssuer {
		return tokenexchange.TrustedIssuer{JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{AcceptedAssertionTypes: types}}
	}
	jwtBearer := tokenexchange.JWTBearerAssertionTypeJWTBearer
	idJAG := tokenexchange.JWTBearerAssertionTypeIDJAG

	tests := []struct {
		name    string
		issuers []tokenexchange.TrustedIssuer
		want    bool
	}{
		{name: "no issuers", want: false},
		{name: "issuer without jwt-bearer grant", issuers: []tokenexchange.TrustedIssuer{{}}, want: false},
		{name: "unset types accept both", issuers: []tokenexchange.TrustedIssuer{issuer()}, want: true},
		{name: "jwt_bearer only", issuers: []tokenexchange.TrustedIssuer{issuer(jwtBearer)}, want: false},
		{name: "id_jag", issuers: []tokenexchange.TrustedIssuer{issuer(idJAG)}, want: true},
		{name: "mixed issuers", issuers: []tokenexchange.TrustedIssuer{issuer(jwtBearer), issuer(idJAG)}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, idJAGRedeemable(tt.issuers))
		})
	}
}
