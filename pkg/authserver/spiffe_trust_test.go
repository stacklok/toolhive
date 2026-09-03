// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestNormalizeSPIFFEPrincipal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		principal string
		want      string
		wantErr   string
	}{
		{name: "rejects bare trust domain", principal: "spiffe://example.org", wantErr: "must include a path"},
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
			got, err := normalizeSPIFFEPrincipal(tt.principal, false)
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
		{name: "domain wildcard matches descendant", pattern: "spiffe://example.org/*", principal: "spiffe://example.org/ns/default/sa/agent", want: true},
		{name: "exact match", pattern: "spiffe://example.org/ns/default/sa/agent", principal: "spiffe://example.org/ns/default/sa/agent", want: true},
		{name: "terminal wildcard matches descendant", pattern: "spiffe://example.org/ns/default/*", principal: "spiffe://example.org/ns/default/sa/agent", want: true},
		{name: "terminal wildcard requires descendant", pattern: "spiffe://example.org/ns/default/*", principal: "spiffe://example.org/ns/default", want: false},
		{name: "terminal wildcard is segment safe", pattern: "spiffe://example.org/ns/default/*", principal: "spiffe://example.org/ns/defaulted/sa/agent", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, matchSPIFFEPrincipalPattern(tt.pattern, tt.principal))
		})
	}
}

// validWorkloadAPIBundleSource is a minimal valid BundleSource declaration
// used by fixtures that don't exercise bundle-source validation directly.
func validWorkloadAPIBundleSource() SPIFFEBundleSourceRunConfig {
	return SPIFFEBundleSourceRunConfig{
		Type:        SPIFFEBundleSourceTypeWorkloadAPI,
		WorkloadAPI: &SPIFFEWorkloadAPIBundleSourceRunConfig{},
	}
}

func TestValidateSPIFFETrust(t *testing.T) {
	t.Parallel()

	valid := func() ([]SPIFFETrustDomainRunConfig, *InboundGrantsRunConfig) {
		return []SPIFFETrustDomainRunConfig{{
			Name:         "production",
			TrustDomain:  "example.org",
			Methods:      []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
			BundleSource: validWorkloadAPIBundleSource(),
		}}, &InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
			TrustDomainRef:   "production",
			PrincipalPattern: "spiffe://example.org/ns/default/*",
			ClientID:         "agent-client",
			Methods:          []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			Audiences:        []string{"https://mcp.example.org/resource"},
			Scopes:           []string{"openid"},
			GrantTypes:       []string{SPIFFEGrantTypeTokenExchange},
		}}}
	}

	tests := []struct {
		name    string
		mutate  func([]SPIFFETrustDomainRunConfig, *InboundGrantsRunConfig)
		wantErr string
	}{
		{name: "ordinary client_id", mutate: func([]SPIFFETrustDomainRunConfig, *InboundGrantsRunConfig) {}},
		{name: "client_id must not use synthetic prefix", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].ClientID = storage.SyntheticClientIDPrefix + "agent-client"
		}, wantErr: "reserved synthetic prefix"},
		{name: "missing association", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth = nil
		}, wantErr: "spiffe_client_auth is required"},
		{name: "unknown trust domain", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].TrustDomainRef = "missing"
		}, wantErr: "unknown trust_domain_ref"},
		{name: "wrong trust domain", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].PrincipalPattern = "spiffe://other.org/ns/default/agent"
		}, wantErr: "does not match"},
		{name: "duplicate client", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth = append(grants.SPIFFEClientAuth, SPIFFEClientAuthRunConfig{
				TrustDomainRef: "production", PrincipalPattern: "spiffe://example.org/ns/other/agent", ClientID: "agent-client",
				Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, Audiences: []string{"https://mcp.example.org/other"},
				Scopes: []string{"openid"}, GrantTypes: []string{SPIFFEGrantTypeTokenExchange},
			})
		}, wantErr: "duplicate client_id"},
		{name: "exact parent does not overlap descendant wildcard", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].PrincipalPattern = "spiffe://example.org/ns/default"
			grants.SPIFFEClientAuth = append(grants.SPIFFEClientAuth, SPIFFEClientAuthRunConfig{
				TrustDomainRef: "production", PrincipalPattern: "spiffe://example.org/ns/default/*", ClientID: "other-client",
				Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, Audiences: []string{"https://mcp.example.org/other"},
				Scopes: []string{"openid"}, GrantTypes: []string{SPIFFEGrantTypeTokenExchange},
			})
		}},
		{name: "nested wildcards overlap", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth = append(grants.SPIFFEClientAuth, SPIFFEClientAuthRunConfig{
				TrustDomainRef: "production", PrincipalPattern: "spiffe://example.org/ns/default/sa/*", ClientID: "other-client",
				Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, Audiences: []string{"https://mcp.example.org/other"},
				Scopes: []string{"openid"}, GrantTypes: []string{SPIFFEGrantTypeTokenExchange},
			})
		}, wantErr: "overlaps"},
		{name: "audiences are required", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Audiences = nil
		}, wantErr: "audiences is required"},
		{name: "audiences need not be in the resource allowlist", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Audiences = []string{"urn:example:logical-audience"}
		}},
		{name: "resource indicator must be an absolute HTTP(S) URI", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Resources = []string{"not-a-uri"}
		}, wantErr: "must be an absolute HTTP(S) URI"},
		{name: "resource in global allowlist is valid", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Resources = []string{"https://mcp.example.org/resource"}
		}},
		{name: "resource must be in global allowlist", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Resources = []string{"https://unlisted.example.org/resource"}
		}, wantErr: "resource \"https://unlisted.example.org/resource\" is not allowed by allowed_audiences"},
		{name: "client_id must not be a client metadata document URL", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].ClientID = "https://cimd.example.org/client"
		}, wantErr: "client_id must not be a client metadata document URL"},
		{name: "client_id must not be a malformed client metadata document URL", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].ClientID = "https://example.org/%zz"
		}, wantErr: "client_id must not be a client metadata document URL"},
		{name: "scope must be supported", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Scopes = []string{"admin"}
		}, wantErr: "not in scopes_supported"},
		{name: "unknown method", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].Methods = []SPIFFEAuthenticationMethod{"magic"}
		}, wantErr: "unknown method"},
		{name: "method not enabled by trust domain", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].Methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}
		}, wantErr: "not enabled by trust domain"},
		{name: "grant_types must be exactly token exchange", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].GrantTypes = []string{"authorization_code"}
		}, wantErr: "grant_types must be exactly"},
		{name: "grant_types is required", mutate: func(_ []SPIFFETrustDomainRunConfig, grants *InboundGrantsRunConfig) {
			grants.SPIFFEClientAuth[0].GrantTypes = nil
		}, wantErr: "grant_types must be exactly"},
		{name: "bundle_source type is required", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{}
		}, wantErr: "unknown source type"},
		{name: "bundle_source endpoint requires https", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{
				Type:     SPIFFEBundleSourceTypeEndpoint,
				Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "http://bundle.example.org"},
			}
		}, wantErr: "must be an absolute HTTPS URL"},
		{name: "bundle_source endpoint is valid", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{
				Type: SPIFFEBundleSourceTypeEndpoint,
				Endpoint: &SPIFFEBundleEndpointSourceRunConfig{
					URL: "https://bundle.example.org/bundle", Profile: SPIFFEBundleEndpointProfileHTTPSWeb,
				},
			}
		}},
		{name: "bundle_source endpoint requires a known profile", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{
				Type:     SPIFFEBundleSourceTypeEndpoint,
				Endpoint: &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundle.example.org/bundle"},
			}
		}, wantErr: "profile must be"},
		{name: "bundle_source endpoint must not mix payloads", mutate: func(domains []SPIFFETrustDomainRunConfig, _ *InboundGrantsRunConfig) {
			domains[0].BundleSource = SPIFFEBundleSourceRunConfig{
				Type:        SPIFFEBundleSourceTypeEndpoint,
				Endpoint:    &SPIFFEBundleEndpointSourceRunConfig{URL: "https://bundle.example.org/bundle"},
				WorkloadAPI: &SPIFFEWorkloadAPIBundleSourceRunConfig{},
			}
		}, wantErr: "requires endpoint only"},
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

func TestValidateSPIFFETrustRejectsDuplicateCanonicalTrustDomains(t *testing.T) {
	t.Parallel()

	err := ValidateSPIFFETrust(
		[]SPIFFETrustDomainRunConfig{
			{
				Name: "production", TrustDomain: "example.org",
				Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509}, BundleSource: validWorkloadAPIBundleSource(),
			},
			{
				Name: "secondary", TrustDomain: "example.org",
				Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, BundleSource: validWorkloadAPIBundleSource(),
			},
		},
		nil,
		nil,
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate trust domain \"example.org\" already declared as \"production\"")
}

func TestNewSPIFFETrustConfigDefensivelyCopiesAudiences(t *testing.T) {
	t.Parallel()

	audiences := []string{"https://mcp.example.org/resource"}
	resources := []string{"https://mcp.example.org/api"}
	trust, err := NewSPIFFETrustConfig(
		[]SPIFFETrustDomainRunConfig{{
			Name: "production", TrustDomain: "example.org", Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT},
			BundleSource: validWorkloadAPIBundleSource(),
		}},
		&InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
			TrustDomainRef: "production", PrincipalPattern: "spiffe://example.org/ns/default/agent", ClientID: "agent-client",
			Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, Audiences: audiences, Resources: resources,
			Scopes: []string{"openid"}, GrantTypes: []string{SPIFFEGrantTypeTokenExchange},
		}}},
		[]string{"openid"}, []string{"https://mcp.example.org/resource", "https://mcp.example.org/api"},
	)
	require.NoError(t, err)
	require.NotNil(t, trust)

	audiences[0] = "https://mutated.example.org"
	resources[0] = "https://mutated.example.org"
	policy := trust.Associations()[0].AuthorizationPolicy()
	assert.Equal(t, []string{"https://mcp.example.org/resource"}, policy.Audiences())
	assert.Equal(t, []string{"https://mcp.example.org/api"}, policy.Resources())

	policyAudiences := policy.Audiences()
	policyAudiences[0] = "https://mutated.example.org"
	assert.Equal(t, []string{"https://mcp.example.org/resource"}, trust.Associations()[0].AuthorizationPolicy().Audiences())
}

func TestSPIFFETrustConfigTrustDomainLookup(t *testing.T) {
	t.Parallel()

	trust, err := NewSPIFFETrustConfig(
		[]SPIFFETrustDomainRunConfig{{
			Name: "production", TrustDomain: "example.org",
			Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT, SPIFFEAuthenticationMethodX509},
			BundleSource: SPIFFEBundleSourceRunConfig{
				Type: SPIFFEBundleSourceTypeEndpoint,
				Endpoint: &SPIFFEBundleEndpointSourceRunConfig{
					URL: "https://bundle.example.org/bundle", Profile: SPIFFEBundleEndpointProfileHTTPSSPIFFE,
				},
			},
		}},
		&InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{{
			TrustDomainRef: "production", PrincipalPattern: "spiffe://example.org/ns/default/agent", ClientID: "agent-client",
			Methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, Audiences: []string{"https://mcp.example.org/resource"},
			Scopes: []string{"openid"}, GrantTypes: []string{SPIFFEGrantTypeTokenExchange},
		}}},
		[]string{"openid"}, []string{"https://mcp.example.org/resource"},
	)
	require.NoError(t, err)
	require.NotNil(t, trust)

	domain, ok := trust.TrustDomain("production")
	require.True(t, ok)
	assert.Equal(t, "example.org", domain.TrustDomain())
	assert.Equal(t, []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT}, domain.Methods())
	assert.Equal(t, SPIFFEBundleSourceTypeEndpoint, domain.BundleSource().Type())
	assert.Equal(t, "https://bundle.example.org/bundle", domain.BundleSource().Endpoint())
	assert.Equal(t, SPIFFEBundleEndpointProfileHTTPSSPIFFE, domain.BundleSource().Profile())

	_, ok = trust.TrustDomain("unknown")
	assert.False(t, ok)

	var nilTrust *SPIFFETrustConfig
	_, ok = nilTrust.TrustDomain("production")
	assert.False(t, ok)
}
