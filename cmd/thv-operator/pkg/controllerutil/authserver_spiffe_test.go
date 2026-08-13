// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/authserver"
)

func TestBuildAuthServerRunConfigConvertsSPIFFETrust(t *testing.T) {
	t.Parallel()

	authConfig := &mcpv1beta1.EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.org",
		UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
			Name:       "oidc",
			Type:       mcpv1beta1.UpstreamProviderTypeOIDC,
			OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://issuer.example.org", ClientID: "client-id"},
		}},
		SPIFFETrustDomains: []mcpv1beta1.SPIFFETrustDomainConfig{
			{
				Name:        "production",
				TrustDomain: "example.org",
				Methods:     []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
				BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
					Type:     mcpv1beta1.SPIFFEBundleSourceTypeEndpoint,
					Endpoint: &mcpv1beta1.SPIFFEBundleEndpointSourceConfig{URL: "https://bundles.example.org/"},
				},
			},
			{
				Name:        "development",
				TrustDomain: "dev.example.org",
				Methods:     []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodJWT},
				BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
					Type:        mcpv1beta1.SPIFFEBundleSourceTypeWorkloadAPI,
					WorkloadAPI: &mcpv1beta1.SPIFFEWorkloadAPIBundleSourceConfig{},
				},
			},
		},
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientAuthConfig{
			{
				TrustDomainRef: "production",
				Principal:      "spiffe://example.org/ns/default/agent",
				ClientID:       "production-agent",
				Methods:        []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
				Resources:      []string{"https://mcp.example.org"},
				Audiences:      []string{"mcp"},
				Scopes:         []string{"openid"},
				GrantTypes:     []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
				TokenExchange:  &mcpv1beta1.SPIFFETokenExchangeConfig{Enabled: true},
			},
			{
				TrustDomainRef: "development",
				Principal:      "spiffe://dev.example.org/ns/default/agent",
				ClientID:       "development-agent",
				Methods:        []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodJWT},
				Resources:      []string{"https://mcp.example.org"},
				Audiences:      []string{"mcp"},
				Scopes:         []string{"openid"},
				GrantTypes:     []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
				TokenExchange:  &mcpv1beta1.SPIFFETokenExchangeConfig{Enabled: true},
			},
		}},
	}

	config, err := BuildAuthServerRunConfig(
		"default", "server", authConfig, []string{"https://mcp.example.org", "mcp"}, []string{"openid"}, "https://mcp.example.org")
	require.NoError(t, err)
	require.NoError(t, config.Validate())
	require.Len(t, config.SPIFFETrustDomains, 2)
	require.Equal(t, authserver.SPIFFEBundleSourceTypeEndpoint, config.SPIFFETrustDomains[0].BundleSource.Type)
	require.Equal(t, "https://bundles.example.org/", config.SPIFFETrustDomains[0].BundleSource.Endpoint.URL)
	require.Equal(t, authserver.SPIFFEBundleSourceTypeWorkloadAPI, config.SPIFFETrustDomains[1].BundleSource.Type)
	require.NotNil(t, config.SPIFFETrustDomains[1].BundleSource.WorkloadAPI)
	require.Equal(t, []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509}, config.SPIFFETrustDomains[0].Methods)
	require.Len(t, config.InboundGrants.SPIFFEClientAuth, 2)
	require.Equal(t, "production", config.InboundGrants.SPIFFEClientAuth[0].TrustDomainRef)
	require.Equal(t, "spiffe://example.org/ns/default/agent", config.InboundGrants.SPIFFEClientAuth[0].Principal)
	require.True(t, config.InboundGrants.SPIFFEClientAuth[0].TokenExchange.Enabled)
}

func TestBuildAuthServerRunConfigRejectsUnknownTrustDomainRef(t *testing.T) {
	t.Parallel()

	authConfig := &mcpv1beta1.EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.org",
		SPIFFETrustDomains: []mcpv1beta1.SPIFFETrustDomainConfig{
			{
				Name:        "production",
				TrustDomain: "example.org",
				Methods:     []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
				BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
					Type:     mcpv1beta1.SPIFFEBundleSourceTypeEndpoint,
					Endpoint: &mcpv1beta1.SPIFFEBundleEndpointSourceConfig{URL: "https://bundles.example.org/"},
				},
			},
		},
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientAuthConfig{
			{
				// TrustDomainRef typo'd — no such declared trust domain.
				TrustDomainRef: "productio",
				Principal:      "spiffe://example.org/ns/default/agent",
				ClientID:       "production-agent",
				Methods:        []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
				Resources:      []string{"https://mcp.example.org"},
				Audiences:      []string{"mcp"},
				Scopes:         []string{"openid"},
				GrantTypes:     []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
			},
		}},
	}

	config, err := BuildAuthServerRunConfig(
		"default", "server", authConfig, []string{"https://mcp.example.org"}, []string{"openid"}, "https://mcp.example.org")
	require.Error(t, err)
	require.Nil(t, config)
}
