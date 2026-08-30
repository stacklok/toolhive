// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

var _ = Describe("MCPExternalAuthConfig inbound grants CEL validation", func() {
	const namespace = "default"

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	baseConfig := func(name string) *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer: "https://auth.example.com",
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
						Name: "github", Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token", ClientID: "test-client-id",
						},
					}},
				},
			},
		}
	}
	delegateClient := func() mcpv1beta1.DelegateClientConfig {
		return mcpv1beta1.DelegateClientConfig{
			ClientID: "delegate", ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "delegate-secret", Key: "secret"},
			Scopes: []string{"openid"}, Audiences: []string{"https://mcp.example.com"},
		}
	}
	legacyRFC8693Issuer := func() mcpv1beta1.TrustedIssuerConfig {
		return mcpv1beta1.TrustedIssuerConfig{
			IssuerURL: "https://issuer.example.com", ExpectedAudience: "https://mcp.example.com",
			AllowedActors: []string{"actor"}, AllowedDelegateClients: []string{"delegate"},
		}
	}
	jwtPolicy := func(ref string) mcpv1beta1.JWTBearerIssuerPolicyConfig {
		return mcpv1beta1.JWTBearerIssuerPolicyConfig{
			IssuerRef: ref,
			JWTBearerGrantConfig: mcpv1beta1.JWTBearerGrantConfig{
				MaxAssertionAge: &metav1.Duration{Duration: time.Minute},
				SubjectBindings: []mcpv1beta1.JWTBearerSubjectBinding{{
					Subject: "workload", AllowedResources: []string{"https://mcp.example.com"},
				}},
			},
		}
	}

	type validationCase struct {
		name        string
		mutate      func(*mcpv1beta1.EmbeddedAuthServerConfig)
		shouldAdmit bool
		errMatch    string
	}
	cases := []validationCase{
		{name: "released legacy delegate clients remain admitted", shouldAdmit: true, mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.DelegateClients = []mcpv1beta1.DelegateClientConfig{delegateClient()}
		}},
		{name: "released legacy RFC 8693 policy remains admitted", shouldAdmit: true, mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{legacyRFC8693Issuer()}
		}},
		{name: "released legacy JWT bearer policy remains admitted", shouldAdmit: true, mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			grant := jwtPolicy("").JWTBearerGrantConfig
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{{IssuerURL: "https://issuer.example.com", JWTBearerGrant: &grant}}
		}},
		{name: "canonical token exchange", shouldAdmit: true, mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{{Name: "issuer", IssuerURL: "https://issuer.example.com"}}
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{
				DelegateClients: []mcpv1beta1.DelegateClientConfig{delegateClient()},
				IssuerPolicies: []mcpv1beta1.TokenExchangeIssuerPolicyConfig{{
					IssuerRef: "issuer", ExpectedAudience: "https://mcp.example.com",
					AllowedActors: []string{"actor"}, AllowedDelegateClients: []string{"delegate"},
				}},
			}}
		}},
		{name: "canonical JWT bearer", shouldAdmit: true, mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{{Name: "issuer", IssuerURL: "https://issuer.example.com"}}
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{JWTBearer: &mcpv1beta1.JWTBearerInboundGrantConfig{
				IssuerPolicies: []mcpv1beta1.JWTBearerIssuerPolicyConfig{jwtPolicy("issuer")},
			}}
		}},
		{name: "canonical token exchange conflicts with legacy delegate clients", errMatch: "canonical tokenExchange conflicts", mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.DelegateClients = []mcpv1beta1.DelegateClientConfig{delegateClient()}
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{}}
		}},
		{name: "canonical token exchange conflicts with legacy issuer policy", errMatch: "canonical tokenExchange conflicts", mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{legacyRFC8693Issuer()}
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{}}
		}},
		{name: "canonical JWT bearer conflicts with legacy JWT bearer", errMatch: "canonical jwtBearer conflicts", mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			grant := jwtPolicy("").JWTBearerGrantConfig
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{{Name: "issuer", IssuerURL: "https://issuer.example.com", JWTBearerGrant: &grant}}
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{JWTBearer: &mcpv1beta1.JWTBearerInboundGrantConfig{}}
		}},
		{name: "canonical token exchange coexists with legacy JWT bearer", shouldAdmit: true, mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			grant := jwtPolicy("").JWTBearerGrantConfig
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{{Name: "issuer", IssuerURL: "https://issuer.example.com", JWTBearerGrant: &grant}}
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{}}
		}},
		{name: "canonical JWT bearer coexists with legacy token exchange", shouldAdmit: true, mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			issuer := legacyRFC8693Issuer()
			issuer.Name = "issuer"
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{issuer}
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{JWTBearer: &mcpv1beta1.JWTBearerInboundGrantConfig{
				IssuerPolicies: []mcpv1beta1.JWTBearerIssuerPolicyConfig{jwtPolicy("issuer")},
			}}
		}},
		{name: "unknown canonical issuer reference", errMatch: "must reference a named trusted issuer", mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{JWTBearer: &mcpv1beta1.JWTBearerInboundGrantConfig{
				IssuerPolicies: []mcpv1beta1.JWTBearerIssuerPolicyConfig{jwtPolicy("missing")},
			}}
		}},
		{name: "duplicate trusted issuer names", errMatch: "trustedIssuers must not contain duplicate names", mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{
				{Name: "issuer", IssuerURL: "https://one.example.com"}, {Name: "issuer", IssuerURL: "https://two.example.com"},
			}
		}},
		{name: "duplicate issuer policy references", errMatch: "must not contain duplicate issuerRef", mutate: func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.TrustedIssuers = []mcpv1beta1.TrustedIssuerConfig{{Name: "issuer", IssuerURL: "https://issuer.example.com"}}
			c.InboundGrants = &mcpv1beta1.InboundGrantsConfig{JWTBearer: &mcpv1beta1.JWTBearerInboundGrantConfig{
				IssuerPolicies: []mcpv1beta1.JWTBearerIssuerPolicyConfig{jwtPolicy("issuer"), jwtPolicy("issuer")},
			}}
		}},
	}

	for i, test := range cases {
		test := test
		It(test.name, func() {
			config := baseConfig(fmt.Sprintf("inbound-grants-%d", i))
			test.mutate(config.Spec.EmbeddedAuthServer)
			err := k8sClient.Create(ctx, config)
			if test.shouldAdmit {
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, config)).To(Succeed()) })
				return
			}
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(test.errMatch))
		})
	}
})
