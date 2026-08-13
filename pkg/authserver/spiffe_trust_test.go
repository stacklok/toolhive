// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestNormalizeSPIFFEPrincipal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		principal string
		want      string
		wantErr   string
	}{
		{name: "rejects uppercase trust domain", principal: "spiffe://EXAMPLE.ORG/ns/default/sa/agent", wantErr: "must be a SPIFFE ID"},
		{name: "rejects wildcard", principal: "spiffe://example.org/ns/default/*", wantErr: "terminal"},
		{name: "rejects port", principal: "spiffe://example.org:8080/ns/default/agent", wantErr: "must be a SPIFFE ID"},
		{name: "rejects userinfo", principal: "spiffe://user@example.org/ns/default/agent", wantErr: "must be a SPIFFE ID"},
		{name: "rejects unicode authority", principal: "spiffe://exämple.org/ns/default/agent", wantErr: "must be a SPIFFE ID"},
		{name: "allows underscore trust domain", principal: "spiffe://example_org/ns/default/agent", want: "spiffe://example_org/ns/default/agent"},
		{name: "rejects escaped path", principal: "spiffe://example.org/ns%2Fdefault/agent", wantErr: "must be a SPIFFE ID"},
		{name: "rejects query", principal: "spiffe://example.org/ns/default?x=y", wantErr: "must be a SPIFFE ID"},
		{name: "rejects dot segment", principal: "spiffe://example.org/ns/../agent", wantErr: "must be a SPIFFE ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeSPIFFEPrincipal(tt.principal)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMatchSPIFFEPrincipalPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pattern   string
		principal string
		want      bool
	}{
		{name: "exact match", pattern: "spiffe://example.org/ns/default/sa/agent", principal: "spiffe://example.org/ns/default/sa/agent", want: true},
		{name: "terminal wildcard matches descendant", pattern: "spiffe://example.org/ns/default/*", principal: "spiffe://example.org/ns/default/sa/agent", want: true},
		{name: "terminal wildcard requires descendant", pattern: "spiffe://example.org/ns/default/*", principal: "spiffe://example.org/ns/default", want: false},
		{name: "terminal wildcard is segment safe", pattern: "spiffe://example.org/ns/default/*", principal: "spiffe://example.org/ns/defaulted/sa/agent", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, MatchSPIFFEPrincipalPattern(tt.pattern, tt.principal))
		})
	}
}

func TestValidateSPIFFETrust(t *testing.T) {
	t.Parallel()

	valid := func() ([]SPIFFETrustDomainRunConfig, *InboundGrantsRunConfig) {
		return []SPIFFETrustDomainRunConfig{{
				Name:         "production",
				TrustDomain:  "example.org",
				Methods:      []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
				BundleSource: SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeWorkloadAPI, WorkloadAPI: &SPIFFEWorkloadAPIBundleSourceRunConfig{}},
			}}, &InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
				TrustDomainRef: "production",
				Principal:      "spiffe://example.org/ns/default/*",
				ClientID:       "agent-client",
				Methods:        []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
				Resources:      []string{"https://mcp.example.org/resource"},
				Audiences:      []string{"https://mcp.example.org/resource"},
				Scopes:         []string{"openid"},
				GrantTypes:     []string{SPIFFEGrantTypeTokenExchange},
				TokenExchange:  &SPIFFETokenExchangeRunConfig{Enabled: true},
			}}}
	}

	tests := []struct {
		name    string
		mutate  func([]SPIFFETrustDomainRunConfig, *InboundGrantsRunConfig)
		wantErr string
	}{
		{name: "valid", mutate: func([]SPIFFETrustDomainRunConfig, *InboundGrantsRunConfig) {}},
		{name: "missing association", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) { grants.SPIFFEClientAuth = nil }, wantErr: "spiffe_client_auth is required"},
		{name: "unknown trust domain", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].TrustDomainRef = "missing"
		}, wantErr: "unknown trust_domain_ref"},
		{name: "wrong trust domain", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Principal = "spiffe://other.org/ns/default/agent"
		}, wantErr: "does not match"},
		{name: "duplicate client", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth = append(grants.SPIFFEClientAuth, SPIFFEClientAuthRunConfig{TrustDomainRef: "production", Principal: "spiffe://example.org/ns/other/agent", ClientID: "agent-client", Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, Resources: []string{"https://mcp.example.org/other"}, Audiences: []string{"https://mcp.example.org/other"}, Scopes: []string{"openid"}, GrantTypes: []string{SPIFFEGrantTypeTokenExchange}, TokenExchange: &SPIFFETokenExchangeRunConfig{Enabled: true}})
		}, wantErr: "duplicate client_id"},
		{name: "exact parent does not overlap descendant wildcard", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Principal = "spiffe://example.org/ns/default"
			grants.SPIFFEClientAuth = append(grants.SPIFFEClientAuth, SPIFFEClientAuthRunConfig{TrustDomainRef: "production", Principal: "spiffe://example.org/ns/default/*", ClientID: "other-client", Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, Resources: []string{"https://mcp.example.org/other"}, Audiences: []string{"https://mcp.example.org/other"}, Scopes: []string{"openid"}, GrantTypes: []string{SPIFFEGrantTypeTokenExchange}, TokenExchange: &SPIFFETokenExchangeRunConfig{Enabled: true}})
		}},
		{name: "nested wildcards overlap", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth = append(grants.SPIFFEClientAuth, SPIFFEClientAuthRunConfig{TrustDomainRef: "production", Principal: "spiffe://example.org/ns/default/sa/*", ClientID: "other-client", Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, Resources: []string{"https://mcp.example.org/other"}, Audiences: []string{"https://mcp.example.org/other"}, Scopes: []string{"openid"}, GrantTypes: []string{SPIFFEGrantTypeTokenExchange}, TokenExchange: &SPIFFETokenExchangeRunConfig{Enabled: true}})
		}, wantErr: "overlaps"},
		{name: "resource must be absolute HTTP URI", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Resources = []string{"relative"}
		}, wantErr: "absolute HTTP"},
		{name: "resource rejects fragment", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Resources = []string{"https://mcp.example.org/#fragment"}
		}, wantErr: "without a fragment"},
		{name: "resource not in global resource allowlist", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Resources = []string{"https://unlisted.example.org/resource"}
		}, wantErr: "resource \"https://unlisted.example.org/resource\" is not allowed"},
		{name: "audience not in global resource allowlist", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Audiences = []string{"unlisted-audience"}
		}, wantErr: "audience \"unlisted-audience\" is not allowed"},
		{name: "client_id must not be an absolute URL", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].ClientID = "https://cimd.example.org/client"
		}, wantErr: "client_id must not be an absolute URL"},
		{name: "scope must be supported", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Scopes = []string{"admin"}
		}, wantErr: "not in scopes_supported"},
		{name: "unknown method", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Methods = []SPIFFEAuthenticationMethod{"magic"}
		}, wantErr: "unknown method"},
		{name: "contradictory token exchange", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].TokenExchange.Enabled = false
		}, wantErr: "token_exchange must be enabled"},
		{name: "bundle source requires one source", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{}
		}, wantErr: "unknown source type"},
		{name: "bundle source rejects both source payloads", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{
				Type:        SPIFFEBundleSourceTypeWorkloadAPI,
				Endpoint:    &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundle.example.org"},
				WorkloadAPI: &SPIFFEWorkloadAPIBundleSourceRunConfig{},
			}
		}, wantErr: "requires workload_api only"},
		{name: "method not enabled by trust domain", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].Methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}
		}, wantErr: "not enabled by trust domain"},
		{name: "bundle endpoint rejects userinfo", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://token@bundle.example.org/bundle"}}
		}, wantErr: "must not contain credentials"},
		{name: "bundle endpoint rejects query", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundle.example.org/bundle?version=1"}}
		}, wantErr: "must not contain credentials"},
		{name: "bundle endpoint rejects fragment", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundle.example.org/bundle#latest"}}
		}, wantErr: "must not contain credentials"},
		{name: "bundle endpoint rejects IPv4 literal", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://127.0.0.1:8443/bundle"}}
		}, wantErr: "IP-literal"},
		{name: "bundle endpoint rejects IPv6 literal", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://[::1]/bundle"}}
		}, wantErr: "IP-literal"},
		{name: "bundle endpoint accepts localhost", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://localhost/bundle"}}
		}},
		{name: "bundle endpoint rejects malformed authority", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundle.example.org:invalid/bundle"}}
		}, wantErr: "valid authority"},
		{name: "bundle endpoint rejects missing authority", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeEndpoint, Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https:///bundle"}}
		}, wantErr: "valid authority"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			domains, grants := valid()
			tt.mutate(domains, grants)
			err := ValidateSPIFFETrust(domains, grants, []string{"openid"}, []string{"https://mcp.example.org/resource", "https://mcp.example.org/other"})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSPIFFETrustConfigPreservesBundleSource(t *testing.T) {
	t.Parallel()

	config, err := NewSPIFFETrustConfig(
		[]SPIFFETrustDomainRunConfig{{
			Name:        "production",
			TrustDomain: "example.org",
			Methods:     []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			BundleSource: SPIFFEBundleSourceRunConfig{
				Type:     SPIFFEBundleSourceTypeEndpoint,
				Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundle.example.org:8443/bundle"},
			},
		}},
		&InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
			TrustDomainRef: "production",
			Principal:      "spiffe://example.org/ns/default/agent",
			ClientID:       "agent-client",
			Methods:        []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			Resources:      []string{"https://resource.example.org"},
			Audiences:      []string{"https://resource.example.org"},
			Scopes:         []string{"openid"},
			GrantTypes:     []string{SPIFFEGrantTypeTokenExchange},
			TokenExchange:  &SPIFFETokenExchangeRunConfig{Enabled: true},
		}}},
		nil,
		[]string{"https://resource.example.org"},
	)
	require.NoError(t, err)

	domain, ok := config.TrustDomain("production")
	require.True(t, ok)
	assert.Equal(t, SPIFFEBundleSourceTypeEndpoint, domain.BundleSource().Type())
	assert.Equal(t, "https://bundle.example.org:8443/bundle", domain.BundleSource().Endpoint())
}

func TestSPIFFETrustRunConfigSerialization(t *testing.T) {
	t.Parallel()

	input := RunConfig{
		SPIFFETrustDomains: []SPIFFETrustDomainRunConfig{{
			Name:         "production",
			TrustDomain:  "example.org",
			Methods:      []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			BundleSource: SPIFFEBundleSourceRunConfig{Type: SPIFFEBundleSourceTypeWorkloadAPI, WorkloadAPI: &SPIFFEWorkloadAPIBundleSourceRunConfig{}},
		}},
		InboundGrants: &InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
			TrustDomainRef: "production",
			Principal:      "spiffe://example.org/ns/default/agent",
			ClientID:       "agent-client",
			Resources:      []string{"https://mcp.example.org/resource"},
			Audiences:      []string{"mcp-api"},
			Scopes:         []string{"openid"},
		}}},
	}

	for _, tt := range []struct {
		name      string
		marshal   func(RunConfig) ([]byte, error)
		unmarshal func([]byte, *RunConfig) error
	}{
		{
			name:      "JSON",
			marshal:   func(config RunConfig) ([]byte, error) { return json.Marshal(config) },
			unmarshal: func(data []byte, config *RunConfig) error { return json.Unmarshal(data, config) },
		},
		{
			name:      "YAML",
			marshal:   func(config RunConfig) ([]byte, error) { return yaml.Marshal(config) },
			unmarshal: func(data []byte, config *RunConfig) error { return yaml.Unmarshal(data, config) },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := tt.marshal(input)
			require.NoError(t, err)

			var output RunConfig
			require.NoError(t, tt.unmarshal(encoded, &output))
			require.Len(t, output.InboundGrants.SPIFFEClientAuth, 1)
			association := output.InboundGrants.SPIFFEClientAuth[0]
			assert.Equal(t, []string{"https://mcp.example.org/resource"}, association.Resources)
			assert.Equal(t, []string{"mcp-api"}, association.Audiences)
			assert.NotEqual(t, association.Resources, association.Audiences)
		})
	}
}
