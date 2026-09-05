// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// These tests exercise the SPIFFE CEL and pattern validation through the real
// apiserver. Runtime parsing remains authoritative for complete URI,
// trust-domain, and principal validation.
var _ = Describe("MCPExternalAuthConfig SPIFFE CEL validation", func() {
	const namespace = "default"

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	workloadAPIBundleSource := func() mcpv1beta1.SPIFFEBundleSourceConfig {
		return mcpv1beta1.SPIFFEBundleSourceConfig{
			Type:        mcpv1beta1.SPIFFEBundleSourceTypeWorkloadAPI,
			WorkloadAPI: &mcpv1beta1.SPIFFEWorkloadAPIBundleSourceConfig{},
		}
	}

	makeAuthConfig := func(name string, trustDomains, clients bool, principalPattern string) *mcpv1beta1.MCPExternalAuthConfig {
		config := &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer: "https://auth.example.com",
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
						Name: "github", Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "test-client-id",
						},
					}},
				},
			},
		}
		if trustDomains {
			config.Spec.EmbeddedAuthServer.SPIFFETrustDomains = []mcpv1beta1.SPIFFETrustDomainConfig{{
				Name: "example", TrustDomain: "example.org",
				Methods:      []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
				BundleSource: workloadAPIBundleSource(),
			}}
		}
		if clients {
			config.Spec.EmbeddedAuthServer.InboundGrants = &mcpv1beta1.InboundGrantsConfig{
				SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientConfig{{
					TrustDomainRef: "example", PrincipalPattern: principalPattern, ClientID: "spiffe-client",
					Methods:   []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
					Audiences: []string{"https://mcp.example.com"}, Scopes: []string{"openid"},
				}},
			}
		}
		return config
	}

	type validationCase struct {
		name         string
		trustDomains bool
		clients      bool
		principal    string
		mutate       func(*mcpv1beta1.EmbeddedAuthServerConfig)
		shouldAdmit  bool
		errMatch     string
	}

	configured := func(name, trustDomain, principal, clientID string) func(*mcpv1beta1.EmbeddedAuthServerConfig) {
		return func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
			config.SPIFFETrustDomains = append(config.SPIFFETrustDomains, mcpv1beta1.SPIFFETrustDomainConfig{
				Name: name, TrustDomain: trustDomain,
				Methods:      []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
				BundleSource: workloadAPIBundleSource(),
			})
			client := config.InboundGrants.SPIFFEClientAuth[0]
			client.TrustDomainRef = name
			client.PrincipalPattern = principal
			client.ClientID = clientID
			config.InboundGrants.SPIFFEClientAuth = append(config.InboundGrants.SPIFFEClientAuth, client)
		}
	}

	cases := []validationCase{
		{name: "trust domains without clients", trustDomains: true, errMatch: "must be configured together"},
		{
			name: "clients without trust domains", clients: true, principal: "spiffe://example.org/*",
			errMatch: "must be configured together",
		},
		{
			name: "domain-wide principal wildcard", trustDomains: true, clients: true,
			principal: "spiffe://example.org/*", shouldAdmit: true,
		},
		{
			name: "path principal wildcard", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/*", shouldAdmit: true,
		},
		{
			name: "workload principal path", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", shouldAdmit: true,
		},
		{
			name: "distinct trust-domain names and client IDs", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", shouldAdmit: true,
			mutate: configured("development", "dev.example.org", "spiffe://dev.example.org/ns/default/agent", "dev-client"),
		},
		{
			name: "duplicate trust-domain names", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "spiffeTrustDomains must not contain duplicate names",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains = append(config.SPIFFETrustDomains, config.SPIFFETrustDomains[0])
			},
		},
		{
			name: "duplicate trust-domain values", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "must not contain duplicate trust domains",
			mutate: configured("secondary", "example.org", "spiffe://example.org/ns/other/agent", "other-client"),
		},
		{
			name: "unreferenced trust domain", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "every SPIFFE trust domain must be referenced",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains = append(config.SPIFFETrustDomains, mcpv1beta1.SPIFFETrustDomainConfig{
					Name: "unused", TrustDomain: "unused.example.org",
					Methods:      []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
					BundleSource: workloadAPIBundleSource(),
				})
			},
		},
		{
			name: "duplicate client IDs", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "spiffeClientAuth must not contain duplicate client IDs",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				client := config.InboundGrants.SPIFFEClientAuth[0]
				client.PrincipalPattern = "spiffe://example.org/ns/other/agent"
				config.InboundGrants.SPIFFEClientAuth = append(config.InboundGrants.SPIFFEClientAuth, client)
			},
		},
		{
			name: "duplicate principal patterns", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent",
			errMatch:  "must not contain duplicate principal patterns",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				client := config.InboundGrants.SPIFFEClientAuth[0]
				client.ClientID = "other-client"
				config.InboundGrants.SPIFFEClientAuth = append(config.InboundGrants.SPIFFEClientAuth, client)
			},
		},
		{
			name: "reserved synthetic client ID", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "reserved synthetic: prefix",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.InboundGrants.SPIFFEClientAuth[0].ClientID = "synthetic:spiffe-client"
			},
		},
		{
			name: "absolute URL client ID", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "must not be an absolute URL",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.InboundGrants.SPIFFEClientAuth[0].ClientID = "https://client.example.com/metadata.json"
			},
		},
		{
			name: "unknown trust domain reference", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "trustDomainRef must reference a declared trust domain",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.InboundGrants.SPIFFEClientAuth[0].TrustDomainRef = "unknown"
			},
		},
		{
			name: "client method is a subset", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", shouldAdmit: true,
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].Methods = []mcpv1beta1.SPIFFEAuthenticationMethod{
					mcpv1beta1.SPIFFEAuthenticationMethodX509, mcpv1beta1.SPIFFEAuthenticationMethodJWT,
				}
			},
		},
		{
			name: "client method is not declared", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "methods must be enabled by the referenced trust domain",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.InboundGrants.SPIFFEClientAuth[0].Methods = []mcpv1beta1.SPIFFEAuthenticationMethod{
					mcpv1beta1.SPIFFEAuthenticationMethodJWT,
				}
			},
		},
		{
			name: "mismatched principal trust domain", trustDomains: true, clients: true,
			principal: "spiffe://other.example.org/ns/default/agent", errMatch: "principalPattern trust domain must match",
		},
		{
			name: "audiences are required", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "audiences",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.InboundGrants.SPIFFEClientAuth[0].Audiences = nil
			},
		},
		{
			name: "scopes are required", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "scopes",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.InboundGrants.SPIFFEClientAuth[0].Scopes = nil
			},
		},
		{
			name: "resources are optional", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", shouldAdmit: true,
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.InboundGrants.SPIFFEClientAuth[0].Resources = []string{"https://backend.example.com"}
			},
		},
		{
			name: "valid underscore trust domain", trustDomains: true, clients: true,
			principal: "spiffe://example_org/ns/default/agent", shouldAdmit: true,
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].TrustDomain = "example_org"
			},
		},
		{
			name: "leading trust-domain dot", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "trustDomain",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].TrustDomain = ".example.org"
			},
		},
		{
			name: "trailing trust-domain dot", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "trustDomain",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].TrustDomain = "example.org."
			},
		},
		{
			name: "leading trust-domain hyphen", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "trustDomain",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].TrustDomain = "-example.org"
			},
		},
		{
			name: "trailing trust-domain hyphen", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "trustDomain",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].TrustDomain = "example.org-"
			},
		},
		{
			name: "consecutive trust-domain dots", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", errMatch: "trustDomain",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].TrustDomain = "example..org"
			},
		},
		{
			name: "three-dot path segment", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/.../agent", shouldAdmit: true,
		},
		{
			name: "dot path segment", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/./agent", errMatch: "principalPattern path must not contain",
		},
		{
			name: "dot-dot path segment", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/../agent", errMatch: "principalPattern path must not contain",
		},
		{
			name: "bare trust-domain principal", trustDomains: true, clients: true,
			principal: "spiffe://example.org", errMatch: "principalPattern",
		},
		{
			// ~ is not a valid SPIFFE path-segment character (verified
			// against go-spiffe's spiffeid.FromString), so a concrete
			// principal containing it must be rejected at admission time,
			// not just by the runtime parser at reconcile time.
			name: "tilde in concrete principal path segment", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent~1", errMatch: "principalPattern",
		},
		{
			name: "tilde in wildcard principal pattern", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent~1/*", errMatch: "principalPattern",
		},
		{
			name: "uppercase trust-domain principal", trustDomains: true, clients: true,
			principal: "spiffe://Example.org/ns/default/agent", errMatch: "principalPattern",
		},
		{
			name: "bundle-endpoint source requires endpoint payload", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent",
			errMatch:  "endpoint configuration must be set if and only if type is 'bundle_endpoint'",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].BundleSource = mcpv1beta1.SPIFFEBundleSourceConfig{
					Type: mcpv1beta1.SPIFFEBundleSourceTypeEndpoint,
				}
			},
		},
		{
			name: "workload-api source rejects endpoint payload", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent",
			errMatch:  "workloadAPI configuration must be set if and only if type is 'workload_api'",
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].BundleSource = mcpv1beta1.SPIFFEBundleSourceConfig{
					Type: mcpv1beta1.SPIFFEBundleSourceTypeWorkloadAPI,
					Endpoint: &mcpv1beta1.SPIFFEBundleEndpointSourceConfig{
						URL: "https://bundle.example.com", Profile: mcpv1beta1.SPIFFEBundleEndpointProfileHTTPSWeb,
					},
				}
			},
		},
		{
			// Covers the deviation from the old commit's shape: the
			// top-level "at least one upstream provider or inbound grant
			// family" rule must also accept a SPIFFE-only config (no
			// upstream providers, no tokenExchange/jwtBearer) — every other
			// case in this file always carries an upstream provider, so
			// without this case the rule change is untested.
			name: "spiffe-only config with no upstream providers is admitted", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", shouldAdmit: true,
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.UpstreamProviders = nil
			},
		},
		{
			name: "bundle-endpoint source is admitted", trustDomains: true, clients: true,
			principal: "spiffe://example.org/ns/default/agent", shouldAdmit: true,
			mutate: func(config *mcpv1beta1.EmbeddedAuthServerConfig) {
				config.SPIFFETrustDomains[0].BundleSource = mcpv1beta1.SPIFFEBundleSourceConfig{
					Type: mcpv1beta1.SPIFFEBundleSourceTypeEndpoint,
					Endpoint: &mcpv1beta1.SPIFFEBundleEndpointSourceConfig{
						URL: "https://bundle.example.com", Profile: mcpv1beta1.SPIFFEBundleEndpointProfileHTTPSWeb,
					},
				}
			},
		},
	}

	for i, test := range cases {
		test := test
		It(test.name, func() {
			config := makeAuthConfig(fmt.Sprintf("spiffe-validation-%d", i), test.trustDomains, test.clients, test.principal)
			if test.mutate != nil {
				test.mutate(config.Spec.EmbeddedAuthServer)
			}
			err := k8sClient.Create(ctx, config)
			if test.shouldAdmit {
				Expect(err).NotTo(HaveOccurred(), "expected apiserver to admit config: %s", test.name)
				DeferCleanup(func() { Expect(k8sClient.Delete(ctx, config)).To(Succeed()) })
				return
			}
			Expect(err).To(HaveOccurred(), "expected apiserver to reject config: %s", test.name)
			Expect(err.Error()).To(ContainSubstring(test.errMatch))
		})
	}
})
