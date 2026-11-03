// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

var _ = Describe("MCPExternalAuthConfig delegate client CEL validation", func() {
	const (
		namespace     = "default"
		tokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	)

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	secretClient := func() mcpv1beta1.DelegateClientConfig {
		return mcpv1beta1.DelegateClientConfig{
			ClientID:        "delegate",
			ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "delegate-secret", Key: "secret"},
			Scopes:          []string{"openid"}, Audiences: []string{"https://mcp.example.com"},
		}
	}
	jwksClient := func() mcpv1beta1.DelegateClientConfig {
		c := secretClient()
		c.ClientSecretRef = nil
		c.TokenEndpointAuthMethod = "private_key_jwt"
		c.JWKSRef = &mcpv1beta1.ConfigMapKeyRef{Name: "delegate-jwks", Key: "jwks.json"}
		c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
		return c
	}

	cases := []struct {
		name     string
		mutate   func(*mcpv1beta1.DelegateClientConfig)
		base     func() mcpv1beta1.DelegateClientConfig
		errMatch string
	}{
		{name: "secret client without a method", base: secretClient, mutate: func(*mcpv1beta1.DelegateClientConfig) {}},
		{name: "secret client with an explicit method", base: secretClient, mutate: func(c *mcpv1beta1.DelegateClientConfig) {
			c.TokenEndpointAuthMethod = "client_secret_post"
		}},
		{name: "secret client without clientSecretRef", base: secretClient, errMatch: "requires a non-empty clientSecretRef",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.ClientSecretRef = nil }},
		{name: "secret client with jwksRef", base: secretClient, errMatch: "forbids jwksRef",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.JWKSRef = jwksClient().JWKSRef }},
		{name: "private_key_jwt client", base: jwksClient, mutate: func(*mcpv1beta1.DelegateClientConfig) {}},
		{name: "private_key_jwt without a tokenExchange block", base: jwksClient,
			errMatch: "tokenExchange.selfIssuedSubjectTokens is required for private_key_jwt clients",
			mutate:   func(c *mcpv1beta1.DelegateClientConfig) { c.TokenExchange = nil }},
		{name: "private_key_jwt with restricted tokenExchange", base: jwksClient,
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "restricted"}
			}},
		{name: "jwksRef with an empty key", base: jwksClient, errMatch: "jwksRef.key",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.JWKSRef.Key = "" }},
		{name: "jwksRef with an empty name", base: jwksClient, errMatch: "jwksRef.name",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.JWKSRef.Name = "" }},
		{name: "redirect URI with a query string", base: secretClient,
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.GrantTypes = []string{"authorization_code", tokenExchange}
				c.RedirectURIs = []string{"https://app.example.com/cb?tenant=a"}
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
			}},
		{name: "redirect URI with a fragment", base: secretClient, errMatch: "should match",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.GrantTypes = []string{"authorization_code", tokenExchange}
				c.RedirectURIs = []string{"https://app.example.com/cb#frag"}
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
			}},
		{name: "allowedSourceClients containing the client's own id", base: secretClient,
			errMatch: "must not contain the client's own clientId",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{
					SelfIssuedSubjectTokens: "restricted", AllowedSourceClients: []string{"delegate"},
				}
			}},
		{name: "allowedSourceClients containing the wildcard", base: secretClient,
			errMatch: "must not contain the client's own clientId or the \"*\" wildcard",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{
					SelfIssuedSubjectTokens: "restricted", AllowedSourceClients: []string{"*"},
				}
			}},
		{name: "http redirect URI", base: secretClient, errMatch: "should match",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.GrantTypes = []string{"authorization_code", tokenExchange}
				c.RedirectURIs = []string{"http://broker.example.com/callback"}
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
			}},
		{name: "private_key_jwt without jwksRef", base: jwksClient, errMatch: "private_key_jwt requires a non-empty jwksRef",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.JWKSRef = nil }},
		{name: "private_key_jwt with clientSecretRef", base: jwksClient, errMatch: "forbids clientSecretRef",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.ClientSecretRef = secretClient().ClientSecretRef }},
		{name: "grantTypes with token exchange and login", base: secretClient, mutate: func(c *mcpv1beta1.DelegateClientConfig) {
			c.GrantTypes = []string{"authorization_code", "refresh_token", tokenExchange}
			c.RedirectURIs = []string{"https://broker.example.com/callback"}
			c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
		}},
		{name: "grantTypes without a tokenExchange block", base: secretClient,
			errMatch: "tokenExchange.selfIssuedSubjectTokens is required when grantTypes is set",
			mutate:   func(c *mcpv1beta1.DelegateClientConfig) { c.GrantTypes = []string{tokenExchange} }},
		{name: "unsupported selfIssuedSubjectTokens", base: secretClient, errMatch: "Unsupported value",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "all"}
			}},
		{name: "restricted with source clients and subject token audiences", base: secretClient,
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.GrantTypes = []string{tokenExchange}
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{
					SelfIssuedSubjectTokens: "restricted", AllowedSourceClients: []string{"tui"},
					SubjectTokenAudiences: []string{"https://mcp.example.com"},
				}
			}},
		{name: "any with source clients", base: secretClient, errMatch: "valid only when selfIssuedSubjectTokens is restricted",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{
					SelfIssuedSubjectTokens: "any", AllowedSourceClients: []string{"tui"},
				}
			}},
		{name: "any with subject token audiences", base: secretClient, errMatch: "valid only when selfIssuedSubjectTokens is restricted",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{
					SelfIssuedSubjectTokens: "any", SubjectTokenAudiences: []string{"https://mcp.example.com"},
				}
			}},
		{name: "grantTypes without token exchange", base: secretClient, errMatch: "grantTypes must include",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.GrantTypes = []string{"refresh_token"} }},
		{name: "grantTypes outside the allowed set", base: secretClient, errMatch: "Unsupported value",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.GrantTypes = []string{tokenExchange, "password"} }},
		{name: "authorization_code without redirectUris", base: secretClient, errMatch: "redirectUris is required",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.GrantTypes = []string{"authorization_code", tokenExchange} }},
		{name: "private_key_jwt client with an ES256 signing alg", base: jwksClient,
			mutate: func(c *mcpv1beta1.DelegateClientConfig) { c.TokenEndpointAuthSigningAlg = "ES256" }},
		{name: "signing alg on a secret client", base: secretClient,
			errMatch: "tokenEndpointAuthSigningAlg is only allowed with private_key_jwt",
			mutate:   func(c *mcpv1beta1.DelegateClientConfig) { c.TokenEndpointAuthSigningAlg = "RS256" }},
		{name: "private_key_jwt client with an unsupported signing alg", base: jwksClient,
			errMatch: `"HS256"`,
			mutate:   func(c *mcpv1beta1.DelegateClientConfig) { c.TokenEndpointAuthSigningAlg = "HS256" }},
		{name: "redirectUris without the authorization_code grant", base: secretClient,
			errMatch: "redirectUris is only allowed with the authorization_code grant",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.GrantTypes = []string{tokenExchange}
				c.RedirectURIs = []string{"https://broker.example.com/callback"}
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
			}},
		{name: "refresh_token without authorization_code", base: secretClient,
			errMatch: "grantTypes must include authorization_code with refresh_token",
			mutate: func(c *mcpv1beta1.DelegateClientConfig) {
				c.GrantTypes = []string{"refresh_token", tokenExchange}
				c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
			}},
	}

	for i, test := range cases {
		It(test.name, func() {
			client := test.base()
			test.mutate(&client)
			config := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("delegate-cel-%d", i), Namespace: namespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						InboundGrants: &mcpv1beta1.InboundGrantsConfig{
							TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{
								DelegateClients: []mcpv1beta1.DelegateClientConfig{client},
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, config)
			if test.errMatch == "" {
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, config)).To(Succeed()) })
				return
			}
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(test.errMatch))
		})
	}

	// The deprecated top-level list accepts only the original fields. Each row is
	// valid per client, so the deprecated-list rule is the only one that fires.
	// A row that sets one field also sets the fields the per-client rules require
	// alongside it (grantTypes needs tokenExchange, redirectUris needs
	// authorization_code, private_key_jwt needs jwksRef and tokenExchange).
	deprecatedCases := []struct {
		name string
		base func() mcpv1beta1.DelegateClientConfig
		set  func(*mcpv1beta1.DelegateClientConfig)
	}{
		{name: "jwksRef", base: jwksClient, set: func(*mcpv1beta1.DelegateClientConfig) {}},
		{name: "tokenEndpointAuthMethod", base: secretClient, set: func(c *mcpv1beta1.DelegateClientConfig) {
			c.TokenEndpointAuthMethod = "client_secret_post"
		}},
		{name: "grantTypes", base: secretClient, set: func(c *mcpv1beta1.DelegateClientConfig) {
			c.GrantTypes = []string{tokenExchange}
			c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
		}},
		{name: "redirectUris", base: secretClient, set: func(c *mcpv1beta1.DelegateClientConfig) {
			c.GrantTypes = []string{"authorization_code", tokenExchange}
			c.RedirectURIs = []string{"https://broker.example.com/callback"}
			c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
		}},
		{name: "tokenExchange", base: secretClient, set: func(c *mcpv1beta1.DelegateClientConfig) {
			c.TokenExchange = &mcpv1beta1.DelegateClientTokenExchangeConfig{SelfIssuedSubjectTokens: "any"}
		}},
		{name: "tokenEndpointAuthSigningAlg", base: jwksClient, set: func(c *mcpv1beta1.DelegateClientConfig) {
			c.TokenEndpointAuthSigningAlg = "ES256"
		}},
	}

	for _, test := range deprecatedCases {
		It("rejects "+test.name+" on the deprecated delegateClients list", func() {
			client := test.base()
			test.set(&client)
			config := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "delegate-cel-deprecated-" + strings.ToLower(test.name), Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
						Issuer:          "https://auth.example.com",
						DelegateClients: []mcpv1beta1.DelegateClientConfig{client},
					},
				},
			}
			err := k8sClient.Create(ctx, config)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("use inboundGrants.tokenExchange.delegateClients"))
		})
	}

	It("accepts the original fields on the deprecated delegateClients list", func() {
		config := &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "delegate-cel-deprecated-ok", Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer:          "https://auth.example.com",
					DelegateClients: []mcpv1beta1.DelegateClientConfig{secretClient()},
				},
			},
		}
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, config)).To(Succeed()) })
	})

	// These cases need fields the typed client cannot send: an empty object, or
	// an explicit empty list that omitempty would drop.
	rawCases := []struct {
		name          string
		tokenExchange map[string]interface{}
		errMatch      string
	}{
		{name: "empty tokenExchange block", tokenExchange: map[string]interface{}{},
			errMatch: "selfIssuedSubjectTokens: Required value"},
		{name: "restricted with an explicit empty source list", tokenExchange: map[string]interface{}{
			"selfIssuedSubjectTokens": "restricted", "allowedSourceClients": []interface{}{},
		}},
		{name: "restricted with an explicit empty subject token audiences", tokenExchange: map[string]interface{}{
			"selfIssuedSubjectTokens": "restricted", "subjectTokenAudiences": []interface{}{},
		}, errMatch: "subjectTokenAudiences"},
		{name: "any with an explicit empty source list and subject token audiences", tokenExchange: map[string]interface{}{
			"selfIssuedSubjectTokens": "any", "allowedSourceClients": []interface{}{}, "subjectTokenAudiences": []interface{}{},
		}, errMatch: "subjectTokenAudiences"},
	}

	for i, test := range rawCases {
		It(test.name, func() {
			config := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "toolhive.stacklok.dev/v1beta1",
				"kind":       "MCPExternalAuthConfig",
				"metadata": map[string]interface{}{
					"name": fmt.Sprintf("delegate-cel-raw-%d", i), "namespace": namespace,
				},
				"spec": map[string]interface{}{
					"type": "embeddedAuthServer",
					"embeddedAuthServer": map[string]interface{}{
						"issuer": "https://auth.example.com",
						"inboundGrants": map[string]interface{}{
							"tokenExchange": map[string]interface{}{
								"delegateClients": []interface{}{map[string]interface{}{
									"clientId":        "delegate",
									"clientSecretRef": map[string]interface{}{"name": "delegate-secret", "key": "secret"},
									"scopes":          []interface{}{"openid"},
									"audiences":       []interface{}{"https://mcp.example.com"},
									"grantTypes":      []interface{}{tokenExchange},
									"tokenExchange":   test.tokenExchange,
								}},
							},
						},
					},
				},
			}}
			err := k8sClient.Create(ctx, config)
			if test.errMatch == "" {
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, config)).To(Succeed()) })
				return
			}
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(test.errMatch))
		})
	}
})
