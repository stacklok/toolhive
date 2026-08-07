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

// These tests exercise the CEL XValidation rule on EmbeddedAuthServerConfig
// through the real apiserver (envtest): allowConfidentialClientRegistration
// combined with insecureAllowHTTP would issue client secrets in cleartext
// over an unauthenticated registration endpoint, so the pair must be
// rejected at admission rather than surfacing only as a pod crash at
// startup. EmbeddedAuthServerConfig is shared by MCPExternalAuthConfig and
// VirtualMCPServer, so exercising the rule through one CRD's generated
// schema covers both.
var _ = Describe("EmbeddedAuthServerConfig confidential-client-transport CEL validation", func() {
	const namespace = "default"

	makeAuthConfig := func(name string, allowConfidential, insecureHTTP bool) *mcpv1beta1.MCPExternalAuthConfig {
		issuer := "https://auth.example.com"
		if insecureHTTP {
			issuer = "http://auth.internal.svc.cluster.local"
		}
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: "embeddedAuthServer",
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer:                              issuer,
					InsecureAllowHTTP:                   insecureHTTP,
					AllowConfidentialClientRegistration: allowConfidential,
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
	}

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	type validationCase struct {
		name              string
		allowConfidential bool
		insecureHTTP      bool
		shouldAdmit       bool
	}

	cases := []validationCase{
		{
			name:              "both allowConfidentialClientRegistration and insecureAllowHTTP set",
			allowConfidential: true,
			insecureHTTP:      true,
			shouldAdmit:       false,
		},
		{
			name:              "allowConfidentialClientRegistration alone",
			allowConfidential: true,
			insecureHTTP:      false,
			shouldAdmit:       true,
		},
		{
			name:              "insecureAllowHTTP alone",
			allowConfidential: false,
			insecureHTTP:      true,
			shouldAdmit:       true,
		},
	}

	for i, c := range cases {
		name := fmt.Sprintf("confidential-client-transport-%d", i)
		It(c.name, func() {
			cfg := makeAuthConfig(name, c.allowConfidential, c.insecureHTTP)
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
			Expect(err.Error()).To(ContainSubstring(
				"allowConfidentialClientRegistration cannot be combined with insecureAllowHTTP"))
		})
	}
})
