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
			name: "endpoint X509 and workload API JWT configurations are valid",
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
			name: "disabled token exchange is rejected",
			cfg: func() *EmbeddedAuthServerConfig {
				cfg := validSPIFFEEmbeddedAuthServerConfig()
				cfg.InboundGrants.SPIFFEClientAuth[0].TokenExchange.Enabled = false
				return cfg
			}(),
			want: "one grantType, and tokenExchange are required",
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
}

func validSPIFFEEmbeddedAuthServerConfig() *EmbeddedAuthServerConfig {
	return &EmbeddedAuthServerConfig{
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
					Type:        SPIFFEBundleSourceTypeWorkloadAPI,
					WorkloadAPI: &SPIFFEWorkloadAPIBundleSourceConfig{},
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
