// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// These tests exercise the kubebuilder schema validation on OBOConfig through
// the real apiserver (envtest). OBOConfig has no required field and no
// cross-field rule: spec.obo shipped as an empty placeholder ({}) in an earlier
// release, so the schema must keep admitting {} (and any subset of fields), and
// presence/combination requirements are enforced by the registered OBO handler
// at reconcile, not at admission. Admission only validates per-field shape
// (patterns, length/item bounds) for values that are present — that is what
// these tests pin down.
var _ = Describe("MCPExternalAuthConfig OBOConfig schema validation", Label("k8s", "validation"), func() {
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

	// rejectsWithField asserts admission fails AND the error names the field
	// under test, so a future schema change that rejected for an unrelated
	// reason cannot silently keep these specs green while dropping the coverage
	// they claim.
	rejectsWithField := func(name, field string, obo *mcpv1beta1.OBOConfig) {
		err := k8sClient.Create(ctx, makeOBOConfig(name, obo))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(field))
	}

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

	Context("permissive schema (no required fields, no cross-field rule)", func() {
		// spec.obo shipped as an empty placeholder in v0.29.3, so the schema must
		// keep admitting {} and any subset of fields; the operator enforces
		// presence/combination requirements at reconcile.
		It("should accept an empty OBOConfig (the v0.29.3 {} placeholder must still round-trip)", func() {
			cfg := makeOBOConfig("obo-empty", &mcpv1beta1.OBOConfig{})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})

		It("should accept a config without tenantId (operator enforces presence at reconcile)", func() {
			cfg := makeOBOConfig("obo-no-tenant", &mcpv1beta1.OBOConfig{
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})

		It("should accept a config without clientId or clientSecretRef (operator enforces per auth mode)", func() {
			// All presence/combination requirements (a tenant, a client-auth
			// credential, an exchange target) are enforced by the operator handler
			// at reconcile, not at admission, so future client-auth methods
			// (certificate, workload identity) need no breaking schema change.
			cfg := makeOBOConfig("obo-no-client-auth", &mcpv1beta1.OBOConfig{
				TenantID: "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Audience: "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})

		It("should accept a config with neither audience nor scopes (operator enforces at reconcile)", func() {
			cfg := makeOBOConfig("obo-no-target", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})
	})

	Context("field patterns", func() {
		It("rejects a tenantId containing a path separator", func() {
			rejectsWithField("obo-bad-tenant", "tenantId", &mcpv1beta1.OBOConfig{
				TenantID:        "tenant/../evil",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
		})

		It("rejects a tenantId well-known alias like 'common' (OBO needs a specific tenant)", func() {
			rejectsWithField("obo-tenant-common", "tenantId", &mcpv1beta1.OBOConfig{
				TenantID:        "common",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
		})

		It("rejects a non-HTTPS authority", func() {
			rejectsWithField("obo-http-authority", "authority", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Authority:       "http://login.microsoftonline.us",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
		})

		It("rejects an authority with a trailing slash", func() {
			rejectsWithField("obo-authority-slash", "authority", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Authority:       "https://login.microsoftonline.us/",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
		})

		It("rejects an authority with a query string", func() {
			rejectsWithField("obo-authority-query", "authority", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Authority:       "https://login.microsoftonline.us?foo=bar",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
		})

		It("rejects an authority with a fragment", func() {
			rejectsWithField("obo-authority-frag", "authority", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Authority:       "https://login.microsoftonline.us#frag",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
		})

		It("rejects an authority with embedded userinfo (RFC 3986 host confusion)", func() {
			rejectsWithField("obo-authority-userinfo", "authority", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				Authority:       "https://login.microsoftonline.com@attacker.example",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
		})

		It("rejects an uppercase subjectTokenProviderName", func() {
			rejectsWithField("obo-bad-subject", "subjectTokenProviderName", &mcpv1beta1.OBOConfig{
				TenantID:                 "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:                 "app-client-id",
				ClientSecretRef:          secretRef,
				Audience:                 "api://backend",
				SubjectTokenProviderName: "Corp-IDP",
			})
		})

		It("accepts an authority with a path (B2C/CIAM/sovereign clouds use token paths)", func() {
			cfg := makeOBOConfig("obo-authority-path", &mcpv1beta1.OBOConfig{
				TenantID:        "contoso.onmicrosoft.com",
				Authority:       "https://contoso.ciamlogin.com/contoso.onmicrosoft.com",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})

		It("accepts a valid lowercase subjectTokenProviderName on its own", func() {
			cfg := makeOBOConfig("obo-valid-subject", &mcpv1beta1.OBOConfig{
				TenantID:                 "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:                 "app-client-id",
				ClientSecretRef:          secretRef,
				Audience:                 "api://backend",
				SubjectTokenProviderName: "corp-idp",
			})
			Expect(k8sClient.Create(ctx, cfg)).Should(Succeed())
		})
	})

	Context("schema bounds", func() {
		It("rejects a tenantId longer than 253 characters", func() {
			// A pattern-valid domain ("a." labels + a TLD) that exceeds the
			// 253-char cap (which mirrors the exchanger's maxTenantLen), so the
			// MaxLength bound fires rather than the pattern.
			longTenant := strings.Repeat("a.", 126) + "co" // 254 chars
			rejectsWithField("obo-tenant-toolong", "tenantId", &mcpv1beta1.OBOConfig{
				TenantID:        longTenant,
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Audience:        "api://backend",
			})
		})

		It("rejects more than 20 scopes", func() {
			scopes := make([]string, 21)
			for i := range scopes {
				scopes[i] = "api://backend/.default"
			}
			rejectsWithField("obo-too-many-scopes", "scopes", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Scopes:          scopes,
			})
		})

		It("rejects a scope item longer than 256 characters", func() {
			rejectsWithField("obo-scope-toolong", "scopes", &mcpv1beta1.OBOConfig{
				TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
				ClientID:        "app-client-id",
				ClientSecretRef: secretRef,
				Scopes:          []string{strings.Repeat("a", 257)},
			})
		})
	})
})
