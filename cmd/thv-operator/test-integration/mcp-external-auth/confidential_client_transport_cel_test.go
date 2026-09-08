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
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
)

// These tests exercise the CEL XValidation rules on EmbeddedAuthServerConfig
// through the real apiserver (envtest). insecureAllowHTTP rejects every
// configuration that issues or uses a client secret: confidential registration
// and legacy or canonical delegate clients. private_key_jwt registration has no
// equivalent rule because it never returns a secret. URL-specific delegate-client
// transport policy is handled by the shared Go validator because CEL cannot
// safely parse URLs. EmbeddedAuthServerConfig is shared by MCPExternalAuthConfig
// and VirtualMCPServer, so exercising the rule through one CRD's generated
// schema covers both.
var _ = Describe("EmbeddedAuthServerConfig confidential-client-transport CEL validation", func() {
	const namespace = "default"

	makeAuthConfig := func(
		name string, allowConfidential, allowPrivateKeyJWT, insecureHTTP, httpsIssuer, delegateClient, canonicalDelegateClient, loopbackHTTP, loopbackOptIn bool,
	) *mcpv1beta1.MCPExternalAuthConfig {
		issuer := "https://auth.example.com"
		if insecureHTTP && !httpsIssuer {
			issuer = "http://auth.internal.svc.cluster.local"
		}
		if loopbackHTTP {
			issuer = "http://127.0.0.1:8080"
		}
		config := &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: "embeddedAuthServer",
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer:                                    issuer,
					InsecureAllowHTTP:                         insecureHTTP,
					AllowConfidentialClientRegistration:       allowConfidential,
					AllowPrivateKeyJWTRegistration:            allowPrivateKeyJWT,
					InsecureAllowConfidentialOverLoopbackHTTP: loopbackOptIn,
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "test-client-id",
						},
					}},
				},
			},
		}
		if delegateClient {
			delegate := mcpv1beta1.DelegateClientConfig{
				ClientID:        "delegate-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "delegate-secret", Key: "credential"},
				Scopes:          []string{"openid"},
				Audiences:       []string{"https://api.example.com"},
			}
			if canonicalDelegateClient {
				config.Spec.EmbeddedAuthServer.InboundGrants = &mcpv1beta1.InboundGrantsConfig{
					TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{DelegateClients: []mcpv1beta1.DelegateClientConfig{delegate}},
				}
			} else {
				config.Spec.EmbeddedAuthServer.DelegateClients = []mcpv1beta1.DelegateClientConfig{delegate}
			}
		}
		return config
	}

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	type validationCase struct {
		name                    string
		allowConfidential       bool
		allowPrivateKeyJWT      bool
		insecureHTTP            bool
		httpsIssuer             bool
		delegateClient          bool
		canonicalDelegateClient bool
		loopbackHTTP            bool
		loopbackOptIn           bool
		shouldAdmit             bool
		expectedMessage         string
	}

	cases := []validationCase{
		{
			name:              "both allowConfidentialClientRegistration and insecureAllowHTTP set",
			allowConfidential: true,
			insecureHTTP:      true,
			shouldAdmit:       false,
			expectedMessage:   "insecureAllowHTTP cannot be combined with confidential client registration or delegateClients",
		},
		{
			name:               "allowPrivateKeyJWTRegistration with insecureAllowHTTP is admitted (no secret to protect)",
			allowPrivateKeyJWT: true,
			insecureHTTP:       true,
			shouldAdmit:        true,
		},
		{
			name:              "allowConfidentialClientRegistration alone",
			allowConfidential: true,
			insecureHTTP:      false,
			shouldAdmit:       true,
		},
		{
			name:            "legacy delegate clients with insecureAllowHTTP set",
			delegateClient:  true,
			insecureHTTP:    true,
			shouldAdmit:     false,
			expectedMessage: "insecureAllowHTTP cannot be combined with confidential client registration or delegateClients",
		},
		{
			name:                    "canonical delegate clients with insecureAllowHTTP set",
			delegateClient:          true,
			canonicalDelegateClient: true,
			insecureHTTP:            true,
			shouldAdmit:             false,
			expectedMessage:         "insecureAllowHTTP cannot be combined with confidential client registration or delegateClients",
		},
		{
			name:            "legacy delegate clients with insecureAllowHTTP and HTTPS issuer",
			delegateClient:  true,
			insecureHTTP:    true,
			httpsIssuer:     true,
			shouldAdmit:     false,
			expectedMessage: "insecureAllowHTTP cannot be combined with confidential client registration or delegateClients",
		},
		{
			name:                    "canonical delegate clients with insecureAllowHTTP and HTTPS issuer",
			delegateClient:          true,
			canonicalDelegateClient: true,
			insecureHTTP:            true,
			httpsIssuer:             true,
			shouldAdmit:             false,
			expectedMessage:         "insecureAllowHTTP cannot be combined with confidential client registration or delegateClients",
		},
		{
			name:            "delegate clients with loopback HTTP issuer without opt-in",
			delegateClient:  true,
			loopbackHTTP:    true,
			shouldAdmit:     false,
			expectedMessage: "confidential client registration or delegateClients with an HTTP issuer require insecureAllowConfidentialOverLoopbackHTTP",
		},
		{
			name:                    "canonical delegate clients with loopback HTTP issuer without opt-in",
			delegateClient:          true,
			canonicalDelegateClient: true,
			loopbackHTTP:            true,
			shouldAdmit:             false,
			expectedMessage:         "confidential client registration or delegateClients with an HTTP issuer require insecureAllowConfidentialOverLoopbackHTTP",
		},
		{
			name:              "confidential registration with loopback HTTP issuer without opt-in",
			allowConfidential: true,
			loopbackHTTP:      true,
			shouldAdmit:       false,
			expectedMessage:   "confidential client registration or delegateClients with an HTTP issuer require insecureAllowConfidentialOverLoopbackHTTP",
		},
		{
			name:              "confidential registration with opted-in loopback HTTP issuer",
			allowConfidential: true,
			loopbackHTTP:      true,
			loopbackOptIn:     true,
			shouldAdmit:       true,
		},
		{
			name:           "delegate clients with opted-in loopback HTTP issuer",
			delegateClient: true,
			loopbackHTTP:   true,
			loopbackOptIn:  true,
			shouldAdmit:    true,
		},
		{
			name:                    "canonical delegate clients with opted-in loopback HTTP issuer",
			delegateClient:          true,
			canonicalDelegateClient: true,
			loopbackHTTP:            true,
			loopbackOptIn:           true,
			shouldAdmit:             true,
		},
	}

	for i, c := range cases {
		name := fmt.Sprintf("confidential-client-transport-%d", i)
		It(c.name, func() {
			cfg := makeAuthConfig(
				name, c.allowConfidential, c.allowPrivateKeyJWT, c.insecureHTTP, c.httpsIssuer, c.delegateClient, c.canonicalDelegateClient, c.loopbackHTTP, c.loopbackOptIn)
			err := k8sClient.Create(ctx, cfg)
			if c.shouldAdmit {
				Expect(err).NotTo(HaveOccurred(),
					"expected apiserver to admit config: %s", c.name)
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
				})
				return
			}
			Expect(err).To(HaveOccurred(),
				"expected apiserver to reject config: %s", c.name)
			Expect(err.Error()).To(ContainSubstring(c.expectedMessage))
		})
	}

	It("admits an opted-in loopback HTTP issuer with delegate clients on VirtualMCPServer", func() {
		vmcp := v1beta1test.NewVirtualMCPServer("delegate-client-http", namespace,
			v1beta1test.WithVMCPGroupRef("test-group"),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "http://127.0.0.1:8080",
				InsecureAllowConfidentialOverLoopbackHTTP: true,
				DelegateClients: []mcpv1beta1.DelegateClientConfig{{
					ClientID:        "delegate-client",
					ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "delegate-secret", Key: "credential"},
					Scopes:          []string{"openid"},
					Audiences:       []string{"https://api.example.com"},
				}},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
					Name: "github", Type: mcpv1beta1.UpstreamProviderTypeOIDC,
					OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "test-client-id"},
				}},
			}),
		)
		err := k8sClient.Create(ctx, vmcp)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, vmcp)).To(Succeed())
		})
	})

	It("rejects a loopback HTTP issuer with delegate clients without opt-in on VirtualMCPServer", func() {
		vmcp := v1beta1test.NewVirtualMCPServer("delegate-client-http-without-opt-in", namespace,
			v1beta1test.WithVMCPGroupRef("test-group"),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "http://127.0.0.1:8080",
				DelegateClients: []mcpv1beta1.DelegateClientConfig{{
					ClientID:        "delegate-client",
					ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "delegate-secret", Key: "credential"},
					Scopes:          []string{"openid"},
					Audiences:       []string{"https://api.example.com"},
				}},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
					Name: "github", Type: mcpv1beta1.UpstreamProviderTypeOIDC,
					OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "test-client-id"},
				}},
			}),
		)
		err := k8sClient.Create(ctx, vmcp)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(
			"confidential client registration or delegateClients with an HTTP issuer require insecureAllowConfidentialOverLoopbackHTTP"))
	})
})
