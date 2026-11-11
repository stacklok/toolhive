// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

const (
	idJAGTestIssuer      = "https://as1.example.com"
	idJAGTestMCPAudience = "https://mcp.example.com"
	idJAGTestAPIAudience = "https://broker.example.com/api"
	idJAGTestSecret      = "0123456789abcdef0123456789abcdef"
)

var idJAGTestAllowedAudiences = []string{idJAGTestMCPAudience, idJAGTestAPIAudience}

// idJAGTestScopesSupported includes offline_access, which exchange_refresh_tokens
// requires in scopes_supported.
var idJAGTestScopesSupported = []string{"openid", "offline_access"}

func idJAGTestTarget() DelegateClientIDJAGTarget {
	return DelegateClientIDJAGTarget{
		Issuer:    "https://as2.example.com",
		ClientID:  "broker-at-as2",
		Resources: []string{"https://gw.example.com/mcp"},
	}
}

// idJAGTestTokenExchange is a restricted policy that can hold ID-JAG grants,
// with the given targets.
func idJAGTestTokenExchange(targets ...DelegateClientIDJAGTarget) *DelegateClientTokenExchange {
	return &DelegateClientTokenExchange{
		SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted,
		AllowedSourceClients:    []string{"mcp-client"},
		SubjectTokenAudiences:   []string{idJAGTestAPIAudience},
		ExchangeRefreshTokens:   &DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: "12h"},
		IDJAG:                   &DelegateClientIDJAG{Targets: targets},
	}
}

func idJAGTestRunConfigClient(clientID string, te *DelegateClientTokenExchange) DelegateClientRunConfig {
	return DelegateClientRunConfig{
		ClientID: clientID, ClientSecretEnvVar: "SECRET", TokenExchange: te,
		Scopes: []string{"openid"}, Audiences: []string{idJAGTestMCPAudience},
	}
}

func idJAGTestResolvedClient(clientID string, te *DelegateClientTokenExchange) DelegateClient {
	return DelegateClient{
		ClientID: clientID, ClientSecret: idJAGTestSecret, TokenExchange: te,
		Scopes: []string{"openid"}, Audiences: []string{idJAGTestMCPAudience},
	}
}

func idJAGTestRunConfig(clients ...DelegateClientRunConfig) RunConfig {
	return RunConfig{
		Issuer:           idJAGTestIssuer,
		ScopesSupported:  idJAGTestScopesSupported,
		AllowedAudiences: idJAGTestAllowedAudiences,
		DelegateClients:  clients,
	}
}

func idJAGTestConfig(clients ...DelegateClient) Config {
	return Config{
		Issuer:      idJAGTestIssuer,
		KeyProvider: keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets: &servercrypto.HMACSecrets{Current: make([]byte, 32)},
		Upstreams: []UpstreamConfig{{
			Type: UpstreamProviderTypeOAuth2,
			OAuth2Config: &upstream.OAuth2Config{
				CommonOAuthConfig:     upstream.CommonOAuthConfig{ClientID: "upstream", RedirectURI: idJAGTestIssuer + "/callback"},
				AuthorizationEndpoint: "https://idp.example.com/authorize",
				TokenEndpoint:         "https://idp.example.com/token",
			},
		}},
		ScopesSupported:  idJAGTestScopesSupported,
		AllowedAudiences: idJAGTestAllowedAudiences,
		DelegateClients:  clients,
	}
}

// TestValidateIDJAGTargets runs every per-target rule through the delegate
// client validation of both the RunConfig and the resolved Config path.
func TestValidateIDJAGTargets(t *testing.T) {
	t.Parallel()

	withTarget := func(mutate func(*DelegateClientIDJAGTarget)) []DelegateClientIDJAGTarget {
		target := idJAGTestTarget()
		mutate(&target)
		return []DelegateClientIDJAGTarget{target}
	}
	withIssuer := func(issuer string) []DelegateClientIDJAGTarget {
		return withTarget(func(target *DelegateClientIDJAGTarget) { target.Issuer = issuer })
	}
	withLifetime := func(lifetime string) []DelegateClientIDJAGTarget {
		return withTarget(func(target *DelegateClientIDJAGTarget) { target.Lifetime = lifetime })
	}
	withResources := func(resources ...string) []DelegateClientIDJAGTarget {
		return withTarget(func(target *DelegateClientIDJAGTarget) { target.Resources = resources })
	}
	withClaims := func(claims ...string) []DelegateClientIDJAGTarget {
		return withTarget(func(target *DelegateClientIDJAGTarget) { target.RequiredSubjectClaims = claims })
	}
	twoIssuers := func(first, second string) []DelegateClientIDJAGTarget {
		a, b := idJAGTestTarget(), idJAGTestTarget()
		a.Issuer, b.Issuer = first, second
		return []DelegateClientIDJAGTarget{a, b}
	}

	tests := []struct {
		name    string
		targets []DelegateClientIDJAGTarget
		wantErr error
		wantMsg string
	}{
		{name: "valid target", targets: []DelegateClientIDJAGTarget{idJAGTestTarget()}},
		{name: "two targets", targets: twoIssuers("https://as2.example.com", "https://as3.example.com")},
		{name: "same host as an allowed audience but another path", targets: withIssuer("https://mcp.example.com/as")},
		{name: "issuer with trailing slash", targets: withIssuer("https://as2.example.com/")},
		{name: "http loopback issuer with non-default port", targets: withIssuer("http://localhost:8080")},
		{name: "http loopback issuer", targets: withIssuer("http://localhost:9000")},
		{name: "http loopback IP issuer", targets: withIssuer("http://127.0.0.1:9000/as")},
		{name: "no targets", targets: nil, wantErr: errIDJAGNoTargets},

		{name: "empty issuer", targets: withIssuer(""), wantErr: errIDJAGIssuerForm},
		{name: "relative issuer", targets: withIssuer("as2.example.com/path"), wantErr: errIDJAGIssuerForm},
		{name: "issuer with fragment", targets: withIssuer("https://as2.example.com#frag"), wantErr: errIDJAGIssuerForm},
		{name: "issuer with userinfo", targets: withIssuer("https://user@as2.example.com"), wantErr: errIDJAGIssuerForm},
		{name: "issuer with query", targets: withIssuer("https://as2.example.com?tenant=a"), wantErr: errIDJAGIssuerForm},
		{name: "issuer with tenant path", targets: withIssuer("https://as2.example.com/tenant/a")},
		{name: "issuer with tenant path and trailing slash", targets: withIssuer("https://as2.example.com/tenant/")},
		{name: "issuer with explicit non-default port", targets: withIssuer("https://as2.example.com:8443")},
		{name: "issuer with dot segment", targets: withIssuer("https://as2.example.com/a/./b"), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with dot-dot segment", targets: withIssuer("https://as2.example.com/a/../b"), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer that is a dot segment", targets: withIssuer("https://as2.example.com/."), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with empty path segment", targets: withIssuer("https://as2.example.com/a//b"), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with leading double slash", targets: withIssuer("https://as2.example.com//"), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with trailing double slash", targets: withIssuer("https://as2.example.com/a//"), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with needless percent-encoding", targets: withIssuer("https://as2.example.com/%7Etenant"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with leading zero in port", targets: withIssuer("https://as2.example.com:0443"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with empty port", targets: withIssuer("https://as2.example.com:"), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with upper-case host", targets: withIssuer("https://AS2.example.com"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with mixed-case host", targets: withIssuer("https://as2.Example.com/tenant"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "issuer with upper-case scheme", targets: withIssuer("HTTPS://as2.example.com"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "https issuer with default port", targets: withIssuer("https://as2.example.com:443"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "https issuer with default port and trailing slash", targets: withIssuer("https://as2.example.com:443/"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "http loopback issuer with default port", targets: withIssuer("http://localhost:80"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "http non-loopback issuer", targets: withIssuer("http://as2.example.com"), wantErr: errIDJAGIssuerScheme},
		{name: "non-http issuer", targets: withIssuer("ftp://as2.example.com"), wantErr: errIDJAGIssuerScheme},

		{name: "duplicate issuer", targets: twoIssuers("https://as2.example.com", "https://as2.example.com"),
			wantErr: errIDJAGIssuerDuplicate},
		{name: "duplicate issuer differing in case", targets: twoIssuers("https://as2.example.com", "HTTPS://AS2.Example.com"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "duplicate issuer differing in trailing slash", targets: twoIssuers("https://as2.example.com", "https://as2.example.com/"),
			wantErr: errIDJAGIssuerDuplicate},
		{name: "duplicate issuer differing in trailing slash, slash first",
			targets: twoIssuers("https://as2.example.com/", "https://as2.example.com"), wantErr: errIDJAGIssuerDuplicate},
		{name: "duplicate issuer differing in default port", targets: twoIssuers("https://as2.example.com", "https://as2.example.com:443"),
			wantErr: errIDJAGIssuerNotWellFormed},

		{name: "server issuer", targets: withIssuer(idJAGTestIssuer), wantErr: errIDJAGIssuerIsServerIssuer},
		{name: "server issuer differing in case", targets: withIssuer("HTTPS://AS1.EXAMPLE.COM"), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "server issuer with trailing slash", targets: withIssuer(idJAGTestIssuer + "/"), wantErr: errIDJAGIssuerIsServerIssuer},
		{name: "server issuer with default port", targets: withIssuer("https://as1.example.com:443"), wantErr: errIDJAGIssuerNotWellFormed},

		{name: "token endpoint", targets: withIssuer(idJAGTestIssuer + oauthserver.TokenEndpointPath),
			wantErr: errIDJAGIssuerIsTokenEndpoint},
		{name: "token endpoint differing in case", targets: withIssuer("https://AS1.example.com/oauth/token"),
			wantErr: errIDJAGIssuerNotWellFormed},
		{name: "token endpoint with trailing slash", targets: withIssuer("https://as1.example.com/oauth/token/"),
			wantErr: errIDJAGIssuerIsTokenEndpoint},
		{name: "token endpoint with default port", targets: withIssuer("https://as1.example.com:443/oauth/token"),
			wantErr: errIDJAGIssuerNotWellFormed},

		{name: "allowed audience", targets: withIssuer(idJAGTestMCPAudience), wantErr: errIDJAGIssuerIsAudience},
		{name: "allowed audience differing in case", targets: withIssuer("https://MCP.example.com"), wantErr: errIDJAGIssuerNotWellFormed},
		{name: "allowed audience with trailing slash", targets: withIssuer(idJAGTestAPIAudience + "/"), wantErr: errIDJAGIssuerIsAudience},
		{name: "allowed audience with default port", targets: withIssuer("https://broker.example.com:443/api"),
			wantErr: errIDJAGIssuerNotWellFormed},

		{name: "missing client_id", targets: withTarget(func(target *DelegateClientIDJAGTarget) { target.ClientID = "" }),
			wantErr: errIDJAGClientIDRequired},

		{name: "no resources", targets: withResources(), wantErr: errIDJAGResourcesRequired},
		{name: "empty resource", targets: withResources(""), wantErr: errIDJAGResourceInvalid},
		{name: "relative resource", targets: withResources("/mcp"), wantErr: errIDJAGResourceInvalid},
		{name: "resource with fragment", targets: withResources("https://gw.example.com/mcp#x"), wantErr: errIDJAGResourceInvalid,
			wantMsg: "must not contain a fragment"},
		{name: "non-http resource", targets: withResources("ftp://gw.example.com/mcp"), wantErr: errIDJAGResourceInvalid,
			wantMsg: "must use http or https scheme"},
		{name: "duplicate resource", targets: withResources("https://gw.example.com/mcp", "https://gw.example.com/mcp"),
			wantErr: errIDJAGResourceDuplicate},
		{name: "two resources", targets: withResources("https://gw.example.com/mcp", "https://gw.example.com/other")},

		{name: "default lifetime", targets: withLifetime("")},
		{name: "lifetime at the ceiling", targets: withLifetime("300s")},
		{name: "lifetime at the ceiling in minutes", targets: withLifetime("5m")},
		{name: "lifetime of one second", targets: withLifetime("1s")},
		{name: "lifetime above the ceiling", targets: withLifetime("301s"), wantErr: errIDJAGLifetime},
		{name: "zero lifetime", targets: withLifetime("0s"), wantErr: errIDJAGLifetime},
		{name: "negative lifetime", targets: withLifetime("-60s"), wantErr: errIDJAGLifetime},
		{name: "sub-second lifetime", targets: withLifetime("500ms"), wantErr: errIDJAGLifetime},
		{name: "fractional-second lifetime", targets: withLifetime("1.5s"), wantErr: errIDJAGLifetime},
		{name: "unparsable lifetime", targets: withLifetime("soon"), wantErr: errIDJAGLifetime},

		{name: "both subject claims", targets: withClaims("upstream_subject", "sub_id")},
		{name: "unknown subject claim", targets: withClaims("email"), wantErr: errIDJAGSubjectClaimUnknown},
		{name: "wire key instead of token", targets: withClaims("https://toolhive.dev/upstream_subject"),
			wantErr: errIDJAGSubjectClaimUnknown},
		{name: "duplicate subject claim", targets: withClaims("sub_id", "sub_id"), wantErr: errIDJAGSubjectClaimDuplicate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			te := idJAGTestTokenExchange(tt.targets...)

			runConfigErr := validateDelegateClients(
				[]DelegateClientRunConfig{idJAGTestRunConfigClient("broker", te)},
				idJAGTestScopesSupported, idJAGTestAllowedAudiences, idJAGTestIssuer, true)
			resolvedErr := validateResolvedDelegateClients(
				[]DelegateClient{idJAGTestResolvedClient("broker", te)},
				idJAGTestScopesSupported, idJAGTestAllowedAudiences, idJAGTestIssuer, true)

			for path, err := range map[string]error{"RunConfig": runConfigErr, "Config": resolvedErr} {
				if tt.wantErr == nil {
					assert.NoError(t, err, path)
					continue
				}
				assert.ErrorIs(t, err, tt.wantErr, path)
				if err != nil {
					assert.Contains(t, err.Error(), `client_id "broker"`, path)
					assert.Contains(t, err.Error(), tt.wantMsg, path)
				}
			}
		})
	}
}

// TestValidateIDJAGReservedOwnSpellings pins that the reserved check folds
// this server's own issuer, token endpoint and allowed_audiences, which are not
// held to the target issuer rules, so a well-formed target still matches them.
func TestValidateIDJAGReservedOwnSpellings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		issuer   string
		audience string
		target   string
		wantErr  error
	}{
		{name: "own issuer with upper-case host", issuer: "https://AS1.Example.com", target: "https://as1.example.com",
			wantErr: errIDJAGIssuerIsServerIssuer},
		{name: "own issuer with default port", issuer: "https://as1.example.com:443", target: "https://as1.example.com",
			wantErr: errIDJAGIssuerIsServerIssuer},
		{name: "own issuer with upper-case scheme, port and slash", issuer: "HTTPS://AS1.example.com:443/",
			target: "https://as1.example.com/", wantErr: errIDJAGIssuerIsServerIssuer},
		{name: "own token endpoint with default port", issuer: "https://AS1.example.com:443", target: "https://as1.example.com/oauth/token",
			wantErr: errIDJAGIssuerIsTokenEndpoint},
		{name: "allowed audience with upper-case host and default port", issuer: idJAGTestIssuer,
			audience: "HTTPS://MCP.example.com:443/", target: "https://mcp.example.com", wantErr: errIDJAGIssuerIsAudience},
		{name: "unrelated target", issuer: "https://AS1.Example.com:443", audience: "HTTPS://MCP.example.com:443/",
			target: "https://as2.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			target := idJAGTestTarget()
			target.Issuer = tt.target
			audiences := []string{idJAGTestAPIAudience}
			if tt.audience != "" {
				audiences = append(audiences, tt.audience)
			}
			err := validateIDJAG("broker", idJAGTestTokenExchange(target),
				tt.issuer, strings.TrimSuffix(tt.issuer, "/")+oauthserver.TokenEndpointPath, audiences, true)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// TestValidateIDJAGPreconditions pins the rules on the token_exchange block
// that an id_jag block needs. Token exchange disabled can only be configured
// on the Config path: a RunConfig that yields a delegate client always enables
// token exchange (see TestRunConfigDelegateClientsImplyTokenExchange).
func TestValidateIDJAGPreconditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		te              *DelegateClientTokenExchange
		exchangeEnabled bool
		wantErr         error
	}{
		{
			name:            "token exchange disabled",
			te:              idJAGTestTokenExchange(idJAGTestTarget()),
			exchangeEnabled: false,
			wantErr:         errIDJAGRequiresTokenExchange,
		},
		{
			name: "any mode",
			te: &DelegateClientTokenExchange{
				SelfIssuedSubjectTokens: SelfIssuedSubjectTokensAny,
				IDJAG:                   &DelegateClientIDJAG{Targets: []DelegateClientIDJAGTarget{idJAGTestTarget()}},
			},
			exchangeEnabled: true,
			wantErr:         errIDJAGRequiresRestricted,
		},
		{
			name: "no exchange_refresh_tokens",
			te: &DelegateClientTokenExchange{
				SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted,
				SubjectTokenAudiences:   []string{idJAGTestAPIAudience},
				IDJAG:                   &DelegateClientIDJAG{Targets: []DelegateClientIDJAGTarget{idJAGTestTarget()}},
			},
			exchangeEnabled: true,
			wantErr:         errIDJAGRequiresSubjectSource,
		},
		{
			name:            "all preconditions met",
			te:              idJAGTestTokenExchange(idJAGTestTarget()),
			exchangeEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := idJAGTestConfig(idJAGTestResolvedClient("broker", tt.te))
			cfg.DisableTokenExchange = !tt.exchangeEnabled
			err := cfg.Validate()
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), `client_id "broker"`)
		})
	}
}

// TestValidateIDJAGSubjectSourceBlock calls validateIDJAG directly. The absent
// block case is also reached through Config.Validate in TestValidateIDJAGPreconditions;
// the block present with an empty id_jag_max_lifetime is reached only here.
func TestValidateIDJAGSubjectSourceBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		refresh *DelegateClientExchangeRefreshTokens
		wantErr error
	}{
		{name: "no exchange_refresh_tokens block", refresh: nil, wantErr: errIDJAGRequiresSubjectSource},
		{name: "block without id_jag_max_lifetime", refresh: &DelegateClientExchangeRefreshTokens{}, wantErr: errIDJAGRequiresSubjectSource},
		{name: "block with id_jag_max_lifetime", refresh: &DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: "12h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			te := idJAGTestTokenExchange(idJAGTestTarget())
			te.ExchangeRefreshTokens = tt.refresh
			err := validateIDJAG("broker", te, idJAGTestIssuer, idJAGTestIssuer+"/token",
				idJAGTestAllowedAudiences, true)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// TestRunConfigDelegateClientsImplyTokenExchange pins why the RunConfig path
// cannot reach the "requires token exchange" refusal: every RunConfig shape
// that yields a delegate client also enables token exchange.
func TestRunConfigDelegateClientsImplyTokenExchange(t *testing.T) {
	t.Parallel()

	client := idJAGTestRunConfigClient("broker", idJAGTestTokenExchange(idJAGTestTarget()))
	tests := []struct {
		name string
		cfg  RunConfig
	}{
		{name: "legacy delegate_clients", cfg: RunConfig{DelegateClients: []DelegateClientRunConfig{client}}},
		{name: "legacy delegate_clients with other inbound grants", cfg: RunConfig{
			DelegateClients: []DelegateClientRunConfig{client},
			InboundGrants:   &InboundGrantsRunConfig{JWTBearer: &JWTBearerInboundGrantRunConfig{}},
		}},
		{name: "canonical inbound_grants.token_exchange", cfg: RunConfig{
			InboundGrants: &InboundGrantsRunConfig{
				TokenExchange: &TokenExchangeInboundGrantRunConfig{DelegateClients: []DelegateClientRunConfig{client}},
			},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			normalized, err := NormalizeInboundGrants(&tt.cfg)
			require.NoError(t, err)
			require.NotEmpty(t, normalized.DelegateClients)
			assert.True(t, normalized.Capabilities.TokenExchange)
		})
	}
}

// TestValidateIDJAGTargetReusedAcrossClients refuses one (issuer, client_id)
// pair on two delegate clients, comparing issuers with one trailing slash
// ignored.
func TestValidateIDJAGTargetReusedAcrossClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		secondIssuer string
		secondClient string
		wantErr      error
	}{
		{name: "same pair", secondIssuer: "https://as2.example.com", secondClient: "broker-at-as2",
			wantErr: errIDJAGTargetReused},
		{name: "same pair, issuer differing in trailing slash", secondIssuer: "https://as2.example.com/",
			secondClient: "broker-at-as2", wantErr: errIDJAGTargetReused},
		{name: "same pair, issuer with upper-case host", secondIssuer: "https://AS2.example.com",
			secondClient: "broker-at-as2", wantErr: errIDJAGIssuerNotWellFormed},
		{name: "same issuer, another client_id", secondIssuer: "https://as2.example.com", secondClient: "other-at-as2"},
		{name: "same client_id, another issuer", secondIssuer: "https://as3.example.com", secondClient: "broker-at-as2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			second := idJAGTestTarget()
			second.Issuer, second.ClientID = tt.secondIssuer, tt.secondClient
			first := idJAGTestTokenExchange(idJAGTestTarget())
			other := idJAGTestTokenExchange(second)

			runConfigErr := validateDelegateClients(
				[]DelegateClientRunConfig{idJAGTestRunConfigClient("broker", first), idJAGTestRunConfigClient("other", other)},
				idJAGTestScopesSupported, idJAGTestAllowedAudiences, idJAGTestIssuer, true)
			resolvedErr := validateResolvedDelegateClients(
				[]DelegateClient{idJAGTestResolvedClient("broker", first), idJAGTestResolvedClient("other", other)},
				idJAGTestScopesSupported, idJAGTestAllowedAudiences, idJAGTestIssuer, true)

			for path, err := range map[string]error{"RunConfig": runConfigErr, "Config": resolvedErr} {
				if tt.wantErr == nil {
					assert.NoError(t, err, path)
					continue
				}
				assert.ErrorIs(t, err, tt.wantErr, path)
				if err != nil && tt.wantErr == errIDJAGTargetReused {
					assert.Contains(t, err.Error(), `used by "broker"`, path)
				}
			}
		})
	}
}

// TestIDJAGValidationReachedFromBothPaths shows that RunConfig.Validate and
// Config.Validate both run the id_jag rules. The one exception is the
// required_subject_claims audience rule, which runs on the Config path only
// because RunConfig has no subject identifier audiences.
func TestIDJAGValidationReachedFromBothPaths(t *testing.T) {
	t.Parallel()

	serverIssuerTarget := idJAGTestTarget()
	serverIssuerTarget.Issuer = idJAGTestIssuer + "/"
	claimTarget := idJAGTestTarget()
	claimTarget.RequiredSubjectClaims = []string{idJAGSubjectClaimUpstreamSubject}

	t.Run("RunConfig refuses the server issuer as a target", func(t *testing.T) {
		t.Parallel()
		cfg := idJAGTestRunConfig(idJAGTestRunConfigClient("broker", idJAGTestTokenExchange(serverIssuerTarget)))
		require.ErrorIs(t, cfg.Validate(), errIDJAGIssuerIsServerIssuer)
	})
	t.Run("Config refuses the server issuer as a target", func(t *testing.T) {
		t.Parallel()
		cfg := idJAGTestConfig(idJAGTestResolvedClient("broker", idJAGTestTokenExchange(serverIssuerTarget)))
		require.ErrorIs(t, cfg.Validate(), errIDJAGIssuerIsServerIssuer)
	})
	t.Run("RunConfig accepts a valid target", func(t *testing.T) {
		t.Parallel()
		cfg := idJAGTestRunConfig(idJAGTestRunConfigClient("broker", idJAGTestTokenExchange(idJAGTestTarget())))
		require.NoError(t, cfg.Validate())
	})
	t.Run("RunConfig does not run the subject claims audience rule", func(t *testing.T) {
		t.Parallel()
		cfg := idJAGTestRunConfig(idJAGTestRunConfigClient("broker", idJAGTestTokenExchange(claimTarget)))
		require.NoError(t, cfg.Validate())
	})
}

// TestValidateIDJAGSubjectClaimAudiences pins the Config-path rule that a target
// listing required_subject_claims needs every one of the client's
// subject_token_audiences in the subject identifier audiences.
func TestValidateIDJAGSubjectClaimAudiences(t *testing.T) {
	t.Parallel()

	const secondInput = "https://broker.example.com/api2"
	tests := []struct {
		name                string
		claims              []string
		inputAudiences      []string
		subjectIDAudiences  []string
		wantErr             bool
		wantClaimInErrorMsg string
		wantMissingAudience string
	}{
		{name: "no claims, no audiences"},
		{name: "upstream_subject without the audience", claims: []string{"upstream_subject"},
			subjectIDAudiences: []string{idJAGTestMCPAudience}, wantErr: true, wantClaimInErrorMsg: "upstream_subject",
			wantMissingAudience: idJAGTestAPIAudience},
		{name: "sub_id without any audience", claims: []string{"sub_id"},
			wantErr: true, wantClaimInErrorMsg: "sub_id", wantMissingAudience: idJAGTestAPIAudience},
		{name: "upstream_subject with the audience", claims: []string{"upstream_subject"},
			subjectIDAudiences: []string{idJAGTestAPIAudience}},
		{name: "both claims with the audience", claims: []string{"upstream_subject", "sub_id"},
			subjectIDAudiences: []string{idJAGTestMCPAudience, idJAGTestAPIAudience}},
		{name: "sub_id with only one of two subject token audiences names the missing one", claims: []string{"sub_id"},
			inputAudiences: []string{idJAGTestAPIAudience, secondInput}, subjectIDAudiences: []string{idJAGTestAPIAudience},
			wantErr: true, wantClaimInErrorMsg: "sub_id", wantMissingAudience: secondInput},
		{name: "sub_id with both subject token audiences", claims: []string{"sub_id"},
			inputAudiences:     []string{idJAGTestAPIAudience, secondInput},
			subjectIDAudiences: []string{idJAGTestAPIAudience, secondInput}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			target := idJAGTestTarget()
			target.RequiredSubjectClaims = tt.claims
			te := idJAGTestTokenExchange(target)
			cfg := idJAGTestConfig(idJAGTestResolvedClient("broker", te))
			if tt.inputAudiences != nil {
				te.SubjectTokenAudiences = tt.inputAudiences
				cfg.AllowedAudiences = append(slices.Clone(idJAGTestAllowedAudiences), secondInput)
			}
			cfg.SubjectIdentifierAudiences = tt.subjectIDAudiences

			err := cfg.Validate()
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errIDJAGSubjectClaimsNeedAudience)
			msg := err.Error()
			assert.Contains(t, msg, `client_id "broker"`)
			assert.Contains(t, msg, `targets[0] (issuer "https://as2.example.com")`)
			assert.Contains(t, msg, `subject claim "`+tt.wantClaimInErrorMsg+`"`)
			assert.Contains(t, msg, `add "`+tt.wantMissingAudience+`" to SubjectIdentifierAudiences`)
		})
	}
}

func TestDelegateClientTokenExchangeCloneIDJAG(t *testing.T) {
	t.Parallel()

	original := idJAGTestTokenExchange(DelegateClientIDJAGTarget{
		Issuer:                "https://as2.example.com",
		ClientID:              "broker-at-as2",
		Resources:             []string{"https://gw.example.com/mcp"},
		Lifetime:              "60s",
		RequiredSubjectClaims: []string{"upstream_subject"},
	})
	want := idJAGTestTokenExchange(original.IDJAG.Targets[0])
	want.IDJAG.Targets[0].Resources = []string{"https://gw.example.com/mcp"}
	want.IDJAG.Targets[0].RequiredSubjectClaims = []string{"upstream_subject"}

	clone := original.Clone()
	require.Equal(t, original, clone)
	require.NotSame(t, original.IDJAG, clone.IDJAG)

	clone.IDJAG.Targets[0].Issuer = "https://changed.example.com"
	clone.IDJAG.Targets[0].Resources[0] = "https://changed.example.com/mcp"
	clone.IDJAG.Targets[0].RequiredSubjectClaims[0] = "sub_id"
	clone.IDJAG.Targets = append(clone.IDJAG.Targets, idJAGTestTarget())

	assert.Equal(t, want, original)
	assert.Nil(t, idJAGTestTokenExchange().Clone().IDJAG.Targets)
}

func TestDelegateClientIDJAGRoundTrip(t *testing.T) {
	t.Parallel()

	const (
		jsonDoc = `{"targets":[{"issuer":"https://as2.example.com","client_id":"broker-at-as2",` +
			`"resources":["https://gw.example.com/mcp"],"lifetime":"60s"}]}`
		yamlDoc = "targets:\n- issuer: https://as2.example.com\n  client_id: broker-at-as2\n" +
			"  resources: [https://gw.example.com/mcp]\n  lifetime: 60s\n"
	)
	want := DelegateClientIDJAG{Targets: []DelegateClientIDJAGTarget{{
		Issuer:    "https://as2.example.com",
		ClientID:  "broker-at-as2",
		Resources: []string{"https://gw.example.com/mcp"},
		Lifetime:  "60s",
	}}}

	var fromJSON DelegateClientIDJAG
	require.NoError(t, json.Unmarshal([]byte(jsonDoc), &fromJSON))
	assert.Equal(t, want, fromJSON)
	var fromYAML DelegateClientIDJAG
	require.NoError(t, yaml.Unmarshal([]byte(yamlDoc), &fromYAML))
	assert.Equal(t, want, fromYAML)

	var te DelegateClientTokenExchange
	require.NoError(t, json.Unmarshal([]byte(`{"self_issued_subject_tokens":"restricted","id_jag":`+jsonDoc+`}`), &te))
	assert.Equal(t, &want, te.IDJAG)

	// required_subject_claims is Go-only: config files cannot set it and it is
	// never marshaled.
	withClaims := `{"targets":[{"issuer":"https://as2.example.com","client_id":"c","resources":["https://gw.example.com/mcp"],` +
		`"required_subject_claims":["sub_id"]}]}`
	var ignoredJSON DelegateClientIDJAG
	require.NoError(t, json.Unmarshal([]byte(withClaims), &ignoredJSON))
	assert.Empty(t, ignoredJSON.Targets[0].RequiredSubjectClaims)
	var ignoredYAML DelegateClientIDJAG
	require.NoError(t, yaml.Unmarshal([]byte(
		"targets:\n- issuer: https://as2.example.com\n  client_id: c\n  required_subject_claims: [sub_id]\n"), &ignoredYAML))
	assert.Empty(t, ignoredYAML.Targets[0].RequiredSubjectClaims)
	goOnly := DelegateClientIDJAG{Targets: []DelegateClientIDJAGTarget{{Issuer: "https://as2.example.com",
		RequiredSubjectClaims: []string{"sub_id"}}}}
	marshaledJSON, err := json.Marshal(goOnly)
	require.NoError(t, err)
	assert.NotContains(t, string(marshaledJSON), "required_subject_claims")
	marshaledYAML, err := yaml.Marshal(goOnly)
	require.NoError(t, err)
	assert.NotContains(t, string(marshaledYAML), "required_subject_claims")

	encoded, err := json.Marshal(DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "id_jag")
}

// TestConfiguredClientPoliciesIDJAGTargets pins the conversion of an id_jag
// block into the runtime policy.
func TestConfiguredClientPoliciesIDJAGTargets(t *testing.T) {
	t.Parallel()

	te := idJAGTestTokenExchange(
		DelegateClientIDJAGTarget{
			Issuer:                "https://as2.example.com/",
			ClientID:              "broker-at-as2",
			Resources:             []string{"https://gw.example.com/mcp", "https://gw.example.com/other"},
			RequiredSubjectClaims: []string{"upstream_subject", "sub_id"},
		},
		DelegateClientIDJAGTarget{
			Issuer:    "https://as3.example.com",
			ClientID:  "broker-at-as3",
			Resources: []string{"https://as3.example.com/mcp"},
			Lifetime:  "300s",
		},
	)
	clients := []DelegateClient{
		{ClientID: "with-targets", TokenExchange: te},
		{ClientID: "without-targets", TokenExchange: idJAGTestTokenExchange()},
	}
	clients[1].TokenExchange.IDJAG = nil

	policies, err := configuredClientPolicies(clients)
	require.NoError(t, err)

	assert.Equal(t, map[string]tokenexchange.IDJAGTarget{
		"https://as2.example.com/": {
			Issuer:                "https://as2.example.com/",
			ClientID:              "broker-at-as2",
			Resources:             []string{"https://gw.example.com/mcp", "https://gw.example.com/other"},
			Lifetime:              defaultIDJAGLifetime,
			RequiredSubjectClaims: []string{"upstream_subject", "sub_id"},
		},
		"https://as3.example.com": {
			Issuer:    "https://as3.example.com",
			ClientID:  "broker-at-as3",
			Resources: []string{"https://as3.example.com/mcp"},
			Lifetime:  maxIDJAGLifetime,
		},
	}, policies["with-targets"].IDJAGTargets)
	assert.Nil(t, policies["without-targets"].IDJAGTargets)

	assert.Equal(t, 60*time.Second, defaultIDJAGLifetime)
	assert.Equal(t, 300*time.Second, maxIDJAGLifetime)

	te.IDJAG.Targets[0].Resources[0] = "https://changed.example.com"
	te.IDJAG.Targets[0].RequiredSubjectClaims[0] = "changed"
	got := policies["with-targets"].IDJAGTargets["https://as2.example.com/"]
	assert.Equal(t, "https://gw.example.com/mcp", got.Resources[0])
	assert.Equal(t, "upstream_subject", got.RequiredSubjectClaims[0])
}

type idJAGLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *idJAGLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *idJAGLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestIDJAGGrantsWithoutTargetsLogOnce shows that a client that can hold
// ID-JAG grants but has no targets starts, and that the log line about it is
// logged once, by configuredClientPolicies, although validation runs on both
// paths. The default logger is process-wide, so the test asserts only on
// client_id values no other test uses.
//
//nolint:paralleltest // captures the process-global slog default
func TestIDJAGGrantsWithoutTargetsLogOnce(t *testing.T) {
	logs := &idJAGLogBuffer{}
	prev := slog.Default()
	prevWriter, prevFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		// slog.SetDefault also redirects the log package's output and flags,
		// and restoring the previous slog default does not undo that.
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	const (
		warnClient    = "idjag-warn-grants-no-targets"
		targetsClient = "idjag-warn-with-targets"
	)
	noTargets := idJAGTestTokenExchange()
	noTargets.IDJAG = nil
	withTargets := idJAGTestTokenExchange(idJAGTestTarget())

	runConfig := idJAGTestRunConfig(
		idJAGTestRunConfigClient(warnClient, noTargets),
		idJAGTestRunConfigClient(targetsClient, withTargets),
	)
	require.NoError(t, runConfig.Validate())
	cfg := idJAGTestConfig(
		idJAGTestResolvedClient(warnClient, noTargets),
		idJAGTestResolvedClient(targetsClient, withTargets),
	)
	require.NoError(t, cfg.Validate())
	_, err := configuredClientPolicies(cfg.DelegateClients)
	require.NoError(t, err)

	countLines := func(clientID string) int {
		count := 0
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "level=DEBUG") &&
				strings.Contains(line, "no id_jag.targets; it cannot mint ID-JAGs") &&
				strings.Contains(line, "client_id="+clientID) {
				count++
			}
		}
		return count
	}
	assert.Equal(t, 1, countLines(warnClient))
	assert.Zero(t, countLines(targetsClient))
}

func TestFoldIssuerForComparison(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "https://as2.example.com", want: "https://as2.example.com"},
		{in: "HTTPS://AS2.Example.COM", want: "https://as2.example.com"},
		{in: "https://as2.example.com/", want: "https://as2.example.com"},
		{in: "https://as2.example.com:443", want: "https://as2.example.com"},
		{in: "https://as2.example.com:8443", want: "https://as2.example.com:8443"},
		{in: "http://localhost:80/", want: "http://localhost"},
		{in: "http://localhost:443", want: "http://localhost:443"},
		{in: "https://as2.example.com/Tenant/", want: "https://as2.example.com/Tenant"},
		{in: "https://as2.example.com/tenant//", want: "https://as2.example.com/tenant/"},
		{in: "https://[::1]:443/", want: "https://[::1]"},
		{in: "https://[::1]:8443", want: "https://[::1]:8443"},
		{in: "://not a url", want: "://not a url"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, foldIssuerForComparison(tt.in))
		})
	}
}
