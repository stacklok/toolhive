// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestValidateSPIFFEConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *EmbeddedAuthServerConfig
		want string
	}{
		{
			name: "endpoint X509 and file JWT configurations are valid",
			cfg:  validSPIFFEEmbeddedAuthServerConfig(),
		},
		{
			name: "empty methods are rejected",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				cfg.SPIFFETrustDomains[0].Methods = nil
				return cfg
			}(),
			want: "spiffeTrustDomains[0].methods is required",
		},
		{
			name: "invalid bundle source union is rejected",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				cfg.SPIFFETrustDomains[0].BundleSource.WorkloadAPI = &SPIFFEWorkloadAPIBundleSourceConfig{}
				return cfg
			}(),
			want: "bundleSource must select exactly its matching source",
		},
		{
			name: "duplicate parsed trust domain is rejected despite distinct names",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				duplicate := cfg.SPIFFETrustDomains[0]
				duplicate.Name = "production-copy"
				cfg.SPIFFETrustDomains = append(cfg.SPIFFETrustDomains, duplicate)
				return cfg
			}(),
			want: "duplicate trust domain",
		},
		{
			name: "loopback bundle endpoint is rejected",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				cfg.SPIFFETrustDomains[0].BundleSource.Endpoint.URL = "https://localhost/bundle"
				return cfg
			}(),
			want: "loopback host",
		},
		{
			name: "disabled token exchange is rejected",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				cfg.InboundGrants.SPIFFEClientAuth[0].TokenExchange.Enabled = false
				return cfg
			}(),
			want: "tokenExchange must be enabled for token-exchange grant",
		},
		{
			name: "client credentials only is valid",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				association := &cfg.InboundGrants.SPIFFEClientAuth[0]
				association.GrantTypes = []string{"client_credentials"}
				association.TokenExchange = nil
				return cfg
			}(),
		},
		{
			name: "both grants are valid when token exchange enabled",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				cfg.InboundGrants.SPIFFEClientAuth[0].GrantTypes = []string{"client_credentials", "urn:ietf:params:oauth:grant-type:token-exchange"}
				return cfg
			}(),
		},
		{
			name: "empty grants are rejected",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				cfg.InboundGrants.SPIFFEClientAuth[0].GrantTypes = nil
				return cfg
			}(),
			want: "grantTypes is required",
		},
		{
			name: "duplicate grants are rejected",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				association := &cfg.InboundGrants.SPIFFEClientAuth[0]
				association.GrantTypes = []string{"client_credentials", "client_credentials"}
				association.TokenExchange = nil
				return cfg
			}(),
			want: "duplicate grant",
		},
		{
			name: "unknown grant is rejected",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				association := &cfg.InboundGrants.SPIFFEClientAuth[0]
				association.GrantTypes = []string{"authorization_code"}
				association.TokenExchange = nil
				return cfg
			}(),
			want: "unsupported grant",
		},
		{
			name: "token exchange requires grant",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				association := &cfg.InboundGrants.SPIFFEClientAuth[0]
				association.GrantTypes = []string{"client_credentials"}
				return cfg
			}(),
			want: "tokenExchange requires token-exchange grant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSPIFFEConfig(tt.cfg)
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestSPIFFEPrincipalSchemaPattern(t *testing.T) {
	t.Parallel()

	crdRelativePath := filepath.Join("deploy", "charts", "operator-crds", "files", "crds", "toolhive.stacklok.dev_mcpexternalauthconfigs.yaml")
	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "..", "..", "..", "..", crdRelativePath))
	require.NoError(t, err)

	jsonContents, err := k8syaml.ToJSON(contents)
	require.NoError(t, err)

	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, json.Unmarshal(jsonContents, &crd))

	var schema *apiextensionsv1.JSONSchemaProps
	for _, version := range crd.Spec.Versions {
		if version.Name == "v1beta1" {
			schema = version.Schema.OpenAPIV3Schema
			break
		}
	}
	require.NotNil(t, schema)

	principalSchema := schema.Properties["spec"].Properties["embeddedAuthServer"].Properties["inboundGrants"].
		Properties["spiffeClientAuth"].Items.Schema.Properties["principal"]
	pattern, err := regexp.Compile(principalSchema.Pattern)
	require.NoError(t, err)

	assert.True(t, pattern.MatchString("spiffe://example.org/ns/default/*"))
	assert.False(t, pattern.MatchString("spiffe://example.org/ns/*/agent"))

	grantTypesSchema := schema.Properties["spec"].Properties["embeddedAuthServer"].Properties["inboundGrants"].
		Properties["spiffeClientAuth"].Items.Schema.Properties["grantTypes"]
	require.NotNil(t, grantTypesSchema.MinItems)
	assert.Equal(t, int64(1), *grantTypesSchema.MinItems)
	require.NotNil(t, grantTypesSchema.MaxItems)
	assert.Equal(t, int64(2), *grantTypesSchema.MaxItems)
	require.NotNil(t, grantTypesSchema.XListType)
	assert.Equal(t, "set", *grantTypesSchema.XListType)
	require.NotNil(t, grantTypesSchema.Items)
	enumGrants := make([]string, 0, len(grantTypesSchema.Items.Schema.Enum))
	for _, enum := range grantTypesSchema.Items.Schema.Enum {
		var grant string
		require.NoError(t, json.Unmarshal(enum.Raw, &grant))
		enumGrants = append(enumGrants, grant)
	}
	assert.ElementsMatch(t, []string{"client_credentials", "urn:ietf:params:oauth:grant-type:token-exchange"}, enumGrants)
}

func validSPIFFEEmbeddedAuthServerConfig() *EmbeddedAuthServerConfig {
	return &EmbeddedAuthServerConfig{
		// The base fixture uses SPIFFE X.509 client authentication, which
		// requires a listener TLS certificate to terminate mTLS.
		ListenerTLS: &ListenerTLSConfig{
			CertificateSecretRef: &SecretKeyRef{Name: "authserver-tls", Key: "tls.crt"},
			PrivateKeySecretRef:  &SecretKeyRef{Name: "authserver-tls", Key: "tls.key"},
		},
		SPIFFETrustDomains: []SPIFFETrustDomainConfig{
			{
				Name:        "production",
				TrustDomain: "example.org",
				Methods:     []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
				BundleSource: SPIFFEBundleSourceConfig{
					Type:     SPIFFEBundleSourceTypeEndpoint,
					Endpoint: &SPIFFEBundleEndpointSourceConfig{URL: "https://bundles.example.org/"},
				},
			},
			{
				Name:        "development",
				TrustDomain: "dev.example.org",
				Methods:     []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT},
				BundleSource: SPIFFEBundleSourceConfig{
					Type: SPIFFEBundleSourceTypeFile,
					File: &SPIFFEFileBundleSourceConfig{
						ConfigMapName: "spire-bundle",
						ConfigMapKey:  "bundle.json",
					},
				},
			},
		},
		InboundGrants: &InboundGrantsConfig{SPIFFEClientAuth: []SPIFFEClientAuthConfig{
			validSPIFFEClientAuth("production", "spiffe://example.org/ns/default/agent", "production-agent", SPIFFEAuthenticationMethodX509),
			validSPIFFEClientAuth("development", "spiffe://dev.example.org/ns/default/agent", "development-agent", SPIFFEAuthenticationMethodJWT),
		}},
	}
}

func validSPIFFEClientAuth(
	trustDomainRef string,
	principal string,
	clientID string,
	method SPIFFEAuthenticationMethod,
) SPIFFEClientAuthConfig {
	return SPIFFEClientAuthConfig{
		TrustDomainRef: trustDomainRef,
		Principal:      principal,
		ClientID:       clientID,
		Methods:        []SPIFFEAuthenticationMethod{method},
		Resources:      []string{"https://mcp.example.org"},
		Audiences:      []string{"mcp"},
		Scopes:         []string{"openid"},
		GrantTypes:     []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
		TokenExchange:  &SPIFFETokenExchangeConfig{Enabled: true},
	}
}
