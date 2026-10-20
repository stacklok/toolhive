// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestBuildAuthServerRunConfigConvertsCanonicalInboundGrants(t *testing.T) {
	t.Parallel()

	authConfig := &mcpv1beta1.EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.com",
		TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{
			Name: "external", IssuerURL: "https://issuer.example.com",
		}},
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{
			TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{
				DelegateClients: []mcpv1beta1.DelegateClientConfig{{
					ClientID: "delegate", ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "delegate", Key: "secret"},
					Scopes: []string{"openid"}, Audiences: []string{"https://resource.example.com"},
				}},
				IssuerPolicies: []mcpv1beta1.TokenExchangeIssuerPolicyConfig{{
					IssuerRef: "external", ExpectedAudience: "https://subject-api.example.com",
					ActorClaim: "azp", AllowedActors: []string{"external-client"},
					AllowedDelegateClients: []string{"delegate"},
				}},
			},
			JWTBearer: &mcpv1beta1.JWTBearerInboundGrantConfig{
				IssuerPolicies: []mcpv1beta1.JWTBearerIssuerPolicyConfig{{
					IssuerRef: "external",
					JWTBearerGrantConfig: mcpv1beta1.JWTBearerGrantConfig{
						MaxAssertionAge: &metav1.Duration{Duration: 5 * time.Minute},
						SubjectBindings: []mcpv1beta1.JWTBearerSubjectBinding{{
							Subject: "workload", AllowedResources: []string{"https://resource.example.com"},
						}},
					},
				}},
			},
		},
	}

	config, err := BuildAuthServerRunConfig(
		"default", "server", authConfig, []string{"https://resource.example.com"}, []string{"openid"}, "https://resource.example.com")
	require.NoError(t, err)
	require.NotNil(t, config.InboundGrants)
	require.NotNil(t, config.InboundGrants.TokenExchange)
	require.NotNil(t, config.InboundGrants.JWTBearer)
	require.Len(t, config.TrustedIssuers, 1)
	assert.Equal(t, "external", config.TrustedIssuers[0].Name)
	require.Len(t, config.InboundGrants.TokenExchange.DelegateClients, 1)
	assert.Equal(t, "TOOLHIVE_DELEGATE_CLIENT_SECRET_0",
		config.InboundGrants.TokenExchange.DelegateClients[0].ClientSecretEnvVar)
	require.Len(t, config.InboundGrants.TokenExchange.IssuerPolicies, 1)
	assert.Equal(t, "external", config.InboundGrants.TokenExchange.IssuerPolicies[0].IssuerRef)
	assert.Equal(t, []string{"external-client"}, config.InboundGrants.TokenExchange.IssuerPolicies[0].AllowedActors)
	require.Len(t, config.InboundGrants.JWTBearer.IssuerPolicies, 1)
	assert.Equal(t, "5m0s", config.InboundGrants.JWTBearer.IssuerPolicies[0].MaxAssertionAge)
	assert.Equal(t, "workload", config.InboundGrants.JWTBearer.IssuerPolicies[0].SubjectBindings[0].Subject)
}

func TestGenerateAuthServerEnvVarsUsesCanonicalDelegateClients(t *testing.T) {
	t.Parallel()

	authConfig := &mcpv1beta1.EmbeddedAuthServerConfig{
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{
			TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{
				DelegateClients: []mcpv1beta1.DelegateClientConfig{{
					ClientID: "delegate", ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "delegate", Key: "secret"},
					Scopes: []string{"openid"}, Audiences: []string{"https://resource.example.com"},
				}},
			},
		},
	}

	envVars := GenerateAuthServerEnvVars(authConfig)
	require.Len(t, envVars, 1)
	assert.Equal(t, "TOOLHIVE_DELEGATE_CLIENT_SECRET_0", envVars[0].Name)
	require.NotNil(t, envVars[0].ValueFrom)
	require.NotNil(t, envVars[0].ValueFrom.SecretKeyRef)
	assert.Equal(t, "delegate", envVars[0].ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "secret", envVars[0].ValueFrom.SecretKeyRef.Key)
}
