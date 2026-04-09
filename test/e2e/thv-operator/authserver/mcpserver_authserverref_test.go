// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("MCPServer AuthServerRef", Ordered, func() {
	var (
		testNamespace   = "default"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second

		// Auth config names shared across MCPServer tests
		embeddedAuthName     = "mcpsrv-embedded-auth"
		embeddedAuthConflict = "mcpsrv-embedded-auth-conflict"
		unauthenticatedName  = "mcpsrv-unauth-config"
		legacyEmbeddedName   = "mcpsrv-legacy-embedded"

		// Shared MCPOIDCConfig name for tests that require OIDC
		oidcConfigName = "mcpsrv-oidc-config"
	)

	BeforeAll(func() {
		By("Creating MCPOIDCConfig for embedded auth server tests")
		Expect(k8sClient.Create(ctx, newMCPOIDCConfig(oidcConfigName, testNamespace))).To(Succeed())

		By("Creating MCPExternalAuthConfig resources for MCPServer tests")
		Expect(k8sClient.Create(ctx, newEmbeddedAuthConfig(embeddedAuthName, testNamespace))).To(Succeed())
		Expect(k8sClient.Create(ctx, newEmbeddedAuthConfig(embeddedAuthConflict, testNamespace))).To(Succeed())
		Expect(k8sClient.Create(ctx, newUnauthenticatedConfig(unauthenticatedName, testNamespace))).To(Succeed())
		Expect(k8sClient.Create(ctx, newEmbeddedAuthConfig(legacyEmbeddedName, testNamespace))).To(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up MCPExternalAuthConfig resources")
		deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: embeddedAuthName, Namespace: testNamespace},
		})
		deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: embeddedAuthConflict, Namespace: testNamespace},
		})
		deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: unauthenticatedName, Namespace: testNamespace},
		})
		deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: legacyEmbeddedName, Namespace: testNamespace},
		})

		By("Cleaning up MCPOIDCConfig")
		deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: testNamespace},
		})
	})

	Context("happy path: authServerRef pointing to embeddedAuthServer", func() {
		const serverName = "mcpsrv-authref-happy"

		BeforeAll(func() {
			By("Creating MCPServer with authServerRef to embedded auth config")
			server := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     images.GofetchServerImage,
					Transport: "streamable-http",
					AuthServerRef: &mcpv1alpha1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: embeddedAuthName,
					},
					OIDCConfigRef: newOIDCConfigRef(oidcConfigName),
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: testNamespace},
			})
		})

		It("should reach Ready phase", func() {
			WaitForMCPServerPhase(ctx, k8sClient, serverName, testNamespace,
				mcpv1alpha1.MCPServerPhaseReady, timeout, pollingInterval)
		})

		It("should have embedded_auth_server_config in the runconfig ConfigMap", func() {
			runConfig, err := GetRunConfigFromConfigMap(ctx, k8sClient, serverName, testNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))
		})

		It("should set AuthServerRefValidated condition to True", func() {
			ExpectMCPServerConditionMessage(ctx, k8sClient, serverName, testNamespace,
				mcpv1alpha1.ConditionTypeAuthServerRefValidated,
				metav1.ConditionTrue, "is valid",
				timeout, pollingInterval)
		})
	})

	Context("conflict: authServerRef + externalAuthConfigRef both pointing to embeddedAuthServer", func() {
		const serverName = "mcpsrv-authref-conflict"

		BeforeAll(func() {
			By("Creating MCPServer with conflicting auth references")
			server := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     images.GofetchServerImage,
					Transport: "streamable-http",
					AuthServerRef: &mcpv1alpha1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: embeddedAuthName,
					},
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: embeddedAuthConflict,
					},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: testNamespace},
			})
		})

		It("should reach Failed phase", func() {
			WaitForMCPServerPhase(ctx, k8sClient, serverName, testNamespace,
				mcpv1alpha1.MCPServerPhaseFailed, timeout, pollingInterval)
		})

		It("should report conflict error in Status.Message", func() {
			ExpectMCPServerStatusMessage(ctx, k8sClient, serverName, testNamespace,
				"both authServerRef and externalAuthConfigRef reference an embedded auth server",
				timeout, pollingInterval)
		})
	})

	Context("type mismatch: authServerRef pointing to non-embeddedAuthServer type", func() {
		const serverName = "mcpsrv-authref-typemismatch"

		BeforeAll(func() {
			By("Creating MCPServer with authServerRef to unauthenticated config")
			server := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     images.GofetchServerImage,
					Transport: "streamable-http",
					AuthServerRef: &mcpv1alpha1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: unauthenticatedName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: testNamespace},
			})
		})

		It("should reach Failed phase", func() {
			WaitForMCPServerPhase(ctx, k8sClient, serverName, testNamespace,
				mcpv1alpha1.MCPServerPhaseFailed, timeout, pollingInterval)
		})

		It("should report type mismatch in AuthServerRefValidated condition", func() {
			ExpectMCPServerConditionMessage(ctx, k8sClient, serverName, testNamespace,
				mcpv1alpha1.ConditionTypeAuthServerRefValidated,
				metav1.ConditionFalse,
				"only embeddedAuthServer is supported",
				timeout, pollingInterval)
		})
	})

	Context("backward compatibility: externalAuthConfigRef only (no authServerRef)", func() {
		const serverName = "mcpsrv-legacy-extauth"

		BeforeAll(func() {
			By("Creating MCPServer with legacy externalAuthConfigRef pointing to embedded auth")
			server := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     images.GofetchServerImage,
					Transport: "streamable-http",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: legacyEmbeddedName,
					},
					OIDCConfigRef: newOIDCConfigRef(oidcConfigName),
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: testNamespace},
			})
		})

		It("should reach Ready phase", func() {
			WaitForMCPServerPhase(ctx, k8sClient, serverName, testNamespace,
				mcpv1alpha1.MCPServerPhaseReady, timeout, pollingInterval)
		})

		It("should have embedded_auth_server_config in the runconfig ConfigMap", func() {
			runConfig, err := GetRunConfigFromConfigMap(ctx, k8sClient, serverName, testNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))
		})
	})
})
