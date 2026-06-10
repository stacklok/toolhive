// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// These tests exercise the kubebuilder validation on OBOConfig (required
// fields, field patterns, and the "at least one of audience or scopes" CEL
// rule) through the real apiserver (envtest). They are the admission-time half
// of the OBOConfig validation contract: the upstream Go Validate() arm
// intentionally defers field-level validation to these markers and to the
// registered enterprise OBO handler, so the apiserver is where a malformed
// obo spec must be rejected.
var _ = Describe("MCPExternalAuthConfig OBOConfig CEL validation", Label("k8s", "cel", "validation"), func() {
	const namespace = "default"

	// makeOBOConfig returns an MCPExternalAuthConfig of type "obo" whose only
	// varying piece is the OBOConfig block, so each test controls exactly the
	// fields under test. The referenced Secret need not exist: admission only
	// validates the structural schema; secret resolution happens at reconcile.
	makeOBOConfig := func(name string, obo *mcpv1beta1.OBOConfig) *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeOBO,
				OBO:  obo,
			},
		}
	}

	secretRef := &mcpv1beta1.SecretKeyRef{Name: "entra-client", Key: "clientSecret"}

	Context("valid configurations", func() {
		It("should accept a minimal config with audience", func() {
			cfg := makeOBOConfig("obo-valid-audience", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})

		It("should accept a config using scopes instead of audience", func() {
			cfg := makeOBOConfig("obo-valid-scopes", &mcpv1beta1.OBOConfig{
				TenantID:        "contoso.onmicrosoft.com",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Scopes:          []string{"api://backend/.default"},
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})

		It("should accept all optional fields set (authority, subjectTokenProviderName, cacheSkew)", func() {
			cfg := makeOBOConfig("obo-valid-full", &mcpv1beta1.OBOConfig{
				TenantID:                 "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Authority:                "https://login.microsoftonline.us",
				ClientID:                 "app-client-id",
				ClientSecretRef:          secretRef,
				Audience:                 "api://backend",
				Scopes:                   []string{"api://backend/.default"},
				SubjectTokenProviderName: "corp-idp",
				CacheSkew:                &metav1.Duration{Duration: 30_000_000_000}, // 30s
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})
	})

	Context("required fields", func() {
		It("should reject an empty OBOConfig (missing required fields)", func() {
			cfg := makeOBOConfig("obo-empty", &mcpv1beta1.OBOConfig{})
			Expect(k8sClient.Create(ctx, cfg)).ShouldNot(Succeed())
		})

		It("should reject a missing tenantId", func() {
			cfg := makeOBOConfig("obo-no-tenant", &mcpv1beta1.OBOConfig{
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			err := k8sClient.Create(ctx, cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("tenantId"))
		})

		It("should accept a config without clientId or clientSecretRef (operator enforces per auth mode)", func() {
			// clientId/clientSecretRef are optional at the CRD level so future
			// client-auth methods (certificate, workload identity) need no
			// breaking schema change; the operator enforces the v1 shared-secret
			// combination. Admission must therefore accept their absence.
			cfg := makeOBOConfig("obo-no-client-auth", &mcpv1beta1.OBOConfig{
				TenantID: "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Audience: "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})
	})

	Context("at least one of audience or scopes", func() {
		It("should reject when neither audience nor scopes is set", func() {
			cfg := makeOBOConfig("obo-no-target", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
			})
			err := k8sClient.Create(ctx, cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one of audience or scopes must be set"))
		})

		It("should reject when scopes is an empty list and audience is unset", func() {
			cfg := makeOBOConfig("obo-empty-scopes", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Scopes:          []string{},
			})
			err := k8sClient.Create(ctx, cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one of audience or scopes must be set"))
		})

		It("should reject a whitespace-only audience (mirrors ExchangeTarget trimming)", func() {
			cfg := makeOBOConfig("obo-blank-audience", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "   ",
			})
			err := k8sClient.Create(ctx, cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("non-blank value"))
		})

		It("should reject scopes containing only blank entries", func() {
			cfg := makeOBOConfig("obo-blank-scopes", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Scopes:          []string{"   "},
			})
			err := k8sClient.Create(ctx, cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("non-blank value"))
		})
	})

	Context("field patterns", func() {
		It("should reject a tenantId containing a path separator", func() {
			cfg := makeOBOConfig("obo-bad-tenant", &mcpv1beta1.OBOConfig{
				TenantID:        "tenant/../evil",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).ShouldNot(Succeed())
		})

		It("should reject a non-HTTPS authority", func() {
			cfg := makeOBOConfig("obo-http-authority", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Authority:       "http://login.microsoftonline.us",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).ShouldNot(Succeed())
		})

		It("should reject an authority with a trailing slash", func() {
			cfg := makeOBOConfig("obo-authority-slash", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Authority:       "https://login.microsoftonline.us/",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).ShouldNot(Succeed())
		})

		It("should reject a tenantId well-known alias like 'common' (OBO needs a specific tenant)", func() {
			cfg := makeOBOConfig("obo-tenant-common", &mcpv1beta1.OBOConfig{
				TenantID:        "common",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).ShouldNot(Succeed())
		})

		It("should accept an authority with a path (B2C/CIAM/sovereign clouds use token paths)", func() {
			cfg := makeOBOConfig("obo-authority-path", &mcpv1beta1.OBOConfig{
				TenantID:        "contoso.onmicrosoft.com",
				Authority:       "https://contoso.ciamlogin.com/contoso.onmicrosoft.com",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})

		It("should reject an uppercase subjectTokenProviderName", func() {
			cfg := makeOBOConfig("obo-bad-subject", &mcpv1beta1.OBOConfig{
				TenantID:                 "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:                 "app-client-id",
				ClientSecretRef:          secretRef,
				Audience:                 "api://backend",
				SubjectTokenProviderName: "Corp-IDP",
			})
			Expect(k8sClient.Create(ctx, cfg)).ShouldNot(Succeed())
		})
	})
})
