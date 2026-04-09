// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPRemoteProxy AuthServerRef", Ordered, func() {
	var (
		testNamespace   = "default"
		timeout         = 3 * time.Minute
		pollingInterval = 1 * time.Second

		// Auth config names shared across MCPRemoteProxy tests
		embeddedAuthName     = "proxy-embedded-auth"
		embeddedAuthConflict = "proxy-embedded-auth-conflict"
		awsStsName           = "proxy-aws-sts"
		unauthenticatedName  = "proxy-unauth-config"
		legacyEmbeddedName   = "proxy-legacy-embedded"

		// Dummy remote URL for proxy tests
		remoteURL = "https://example.com/mcp"
	)

	BeforeAll(func() {
		By("Creating MCPExternalAuthConfig resources for MCPRemoteProxy tests")

		Expect(k8sClient.Create(ctx, newEmbeddedAuthConfig(embeddedAuthName, testNamespace))).To(Succeed())
		Expect(k8sClient.Create(ctx, newEmbeddedAuthConfig(embeddedAuthConflict, testNamespace))).To(Succeed())
		Expect(k8sClient.Create(ctx, newAWSStsConfig(awsStsName, testNamespace))).To(Succeed())
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
			ObjectMeta: metav1.ObjectMeta{Name: awsStsName, Namespace: testNamespace},
		})
		deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: unauthenticatedName, Namespace: testNamespace},
		})
		deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: legacyEmbeddedName, Namespace: testNamespace},
		})
	})

	Context("happy path: authServerRef pointing to embeddedAuthServer", func() {
		const proxyName = "proxy-authref-happy"

		BeforeAll(func() {
			By("Creating MCPRemoteProxy with authServerRef to embedded auth config")
			proxy := &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      proxyName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: remoteURL,
					Transport: "streamable-http",
					AuthServerRef: &mcpv1alpha1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: embeddedAuthName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, proxy)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: proxyName, Namespace: testNamespace},
			})
		})

		It("should reach Ready phase", func() {
			WaitForMCPRemoteProxyPhase(ctx, k8sClient, proxyName, testNamespace,
				mcpv1alpha1.MCPRemoteProxyPhaseReady, timeout, pollingInterval)
		})

		It("should have embedded_auth_server_config in the runconfig ConfigMap", func() {
			runConfig, err := GetRunConfigFromConfigMap(ctx, k8sClient, proxyName, testNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))
		})
	})

	Context("combined auth: authServerRef (embeddedAuthServer) + externalAuthConfigRef (awsSts)", func() {
		const proxyName = "proxy-authref-combined"

		BeforeAll(func() {
			By("Creating MCPRemoteProxy with both authServerRef and externalAuthConfigRef (different types)")
			proxy := &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      proxyName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: remoteURL,
					Transport: "streamable-http",
					AuthServerRef: &mcpv1alpha1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: embeddedAuthName,
					},
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: awsStsName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, proxy)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: proxyName, Namespace: testNamespace},
			})
		})

		It("should reach Ready phase", func() {
			WaitForMCPRemoteProxyPhase(ctx, k8sClient, proxyName, testNamespace,
				mcpv1alpha1.MCPRemoteProxyPhaseReady, timeout, pollingInterval)
		})

		It("should have embedded_auth_server_config in the runconfig ConfigMap", func() {
			runConfig, err := GetRunConfigFromConfigMap(ctx, k8sClient, proxyName, testNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))
		})

		It("should have aws_sts_config in the runconfig ConfigMap", func() {
			runConfig, err := GetRunConfigFromConfigMap(ctx, k8sClient, proxyName, testNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(runConfig).To(HaveKey("aws_sts_config"))
		})
	})

	Context("conflict: authServerRef + externalAuthConfigRef both pointing to embeddedAuthServer", func() {
		const proxyName = "proxy-authref-conflict"

		BeforeAll(func() {
			By("Creating MCPRemoteProxy with conflicting auth references")
			proxy := &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      proxyName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: remoteURL,
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
			Expect(k8sClient.Create(ctx, proxy)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: proxyName, Namespace: testNamespace},
			})
		})

		It("should reach Failed phase", func() {
			WaitForMCPRemoteProxyPhase(ctx, k8sClient, proxyName, testNamespace,
				mcpv1alpha1.MCPRemoteProxyPhaseFailed, timeout, pollingInterval)
		})
	})

	Context("type mismatch: authServerRef pointing to non-embeddedAuthServer type", func() {
		const proxyName = "proxy-authref-typemismatch"

		BeforeAll(func() {
			By("Creating MCPRemoteProxy with authServerRef to unauthenticated config")
			proxy := &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      proxyName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: remoteURL,
					Transport: "streamable-http",
					AuthServerRef: &mcpv1alpha1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: unauthenticatedName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, proxy)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: proxyName, Namespace: testNamespace},
			})
		})

		It("should reach Failed phase", func() {
			WaitForMCPRemoteProxyPhase(ctx, k8sClient, proxyName, testNamespace,
				mcpv1alpha1.MCPRemoteProxyPhaseFailed, timeout, pollingInterval)
		})

		It("should report type mismatch in AuthServerRefValidated condition", func() {
			ExpectMCPRemoteProxyConditionMessage(ctx, k8sClient, proxyName, testNamespace,
				mcpv1alpha1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
				"only embeddedAuthServer is supported",
				timeout, pollingInterval)
		})
	})

	Context("backward compatibility: externalAuthConfigRef only (no authServerRef)", func() {
		const proxyName = "proxy-legacy-extauth"

		BeforeAll(func() {
			By("Creating MCPRemoteProxy with legacy externalAuthConfigRef pointing to embedded auth")
			proxy := &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      proxyName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: remoteURL,
					Transport: "streamable-http",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: legacyEmbeddedName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, proxy)).To(Succeed())
		})

		AfterAll(func() {
			deleteIgnoreNotFound(ctx, k8sClient, &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: proxyName, Namespace: testNamespace},
			})
		})

		It("should reach Ready phase", func() {
			WaitForMCPRemoteProxyPhase(ctx, k8sClient, proxyName, testNamespace,
				mcpv1alpha1.MCPRemoteProxyPhaseReady, timeout, pollingInterval)
		})
	})
})
