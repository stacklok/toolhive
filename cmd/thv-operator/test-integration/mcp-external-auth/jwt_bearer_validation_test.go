// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

var _ = Describe("MCPExternalAuthConfig JWT-bearer grant schema validation", Label("k8s", "validation"), func() {
	const namespace = "default"

	makeConfig := func(name string, maxAssertionAge time.Duration) *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer: "https://auth.example.com",
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
						Name:       "upstream",
						Type:       mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://upstream.example.com", ClientID: "client-id"},
					}},
					TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{
						IssuerURL: "https://issuer.example.com",
						JWTBearerGrant: &mcpv1beta1.JWTBearerGrantConfig{
							MaxAssertionAge: &metav1.Duration{Duration: maxAssertionAge},
							SubjectBindings: []mcpv1beta1.JWTBearerSubjectBinding{{
								Subject:          "external-subject",
								AllowedResources: []string{"https://mcp.example.com"},
							}},
						},
					}},
				},
			},
		}
	}

	It("accepts a grant-only issuer with a positive maxAssertionAge", func() {
		Expect(k8sClient.Create(ctx, makeConfig("jwt-bearer-positive-age", time.Minute))).To(Succeed())
	})

	It("reconciles a grant-only issuer to Valid=True", func() {
		name := "jwt-bearer-reconciles-valid"
		Expect(k8sClient.Create(ctx, makeConfig(name, time.Minute))).To(Succeed())

		Eventually(func() bool {
			updated := &mcpv1beta1.MCPExternalAuthConfig{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, updated); err != nil {
				return false
			}
			return meta.IsStatusConditionTrue(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
		}, time.Second*30, time.Millisecond*250).Should(BeTrue())
	})

	It("accepts configured JWT-bearer assertion audiences", func() {
		config := makeConfig("jwt-bearer-accepted-audiences", time.Minute)
		config.Spec.EmbeddedAuthServer.TrustedIssuers[0].JWTBearerGrant.AcceptedAudiences = []string{
			"https://auth.example.com/legacy-token",
		}
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
	})

	It("rejects an invalid configured JWT-bearer assertion audience", func() {
		config := makeConfig("jwt-bearer-invalid-accepted-audience", time.Minute)
		config.Spec.EmbeddedAuthServer.TrustedIssuers[0].JWTBearerGrant.AcceptedAudiences = []string{"not a URI"}
		err := k8sClient.Create(ctx, config)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("acceptedAudiences"))
	})

	It("rejects a zero maxAssertionAge", func() {
		err := k8sClient.Create(ctx, makeConfig("jwt-bearer-zero-age", 0))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("maxAssertionAge"))
	})

	It("rejects jwtBearerGrant combined with actorClaim and no expectedAudience", func() {
		config := makeConfig("jwt-bearer-mixed-no-audience", time.Minute)
		config.Spec.EmbeddedAuthServer.TrustedIssuers[0].ActorClaim = "client_id"
		config.Spec.EmbeddedAuthServer.TrustedIssuers[0].AllowedDelegateClients = []string{"*"}
		err := k8sClient.Create(ctx, config)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("expectedAudience is required"))
	})

	It("rejects jwtBearerGrant combined with allowedActors and no expectedAudience", func() {
		config := makeConfig("jwt-bearer-mixed-allowed-actors-no-audience", time.Minute)
		config.Spec.EmbeddedAuthServer.TrustedIssuers[0].AllowedActors = []string{"ext-agent"}
		config.Spec.EmbeddedAuthServer.TrustedIssuers[0].AllowedDelegateClients = []string{"*"}
		err := k8sClient.Create(ctx, config)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("expectedAudience is required"))
	})
})
