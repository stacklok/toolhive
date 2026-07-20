// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

var _ = Describe("MCPRemoteProxy AuthServerRef Integration", Label("k8s", "remoteproxy", "authserverref"), func() {
	var (
		testCtx       context.Context
		proxyHelper   *MCPRemoteProxyTestHelper
		statusHelper  *RemoteProxyStatusTestHelper
		testNamespace string
	)

	BeforeEach(func() {
		testCtx = context.Background()
		testNamespace = createTestNamespace(testCtx)
		proxyHelper = NewMCPRemoteProxyTestHelper(testCtx, k8sClient, testNamespace)
		statusHelper = NewRemoteProxyStatusTestHelper(proxyHelper)
	})

	AfterEach(func() {
		Expect(proxyHelper.CleanupRemoteProxies()).To(Succeed())
		deleteTestNamespace(testCtx, testNamespace)
	})

	Context("Happy path: authServerRef pointing to embeddedAuthServer", func() {
		It("should set AuthServerRefValidated condition to True and generate correct runconfig", func() {
			By("creating MCPOIDCConfig")
			oidcConfig := newMCPOIDCConfig("test-oidc", testNamespace)
			Expect(k8sClient.Create(testCtx, oidcConfig)).To(Succeed())

			By("creating MCPExternalAuthConfig with embeddedAuthServer type")
			authConfig := newEmbeddedAuthConfig("test-embedded-auth", testNamespace)
			Expect(k8sClient.Create(testCtx, authConfig)).To(Succeed())

			By("creating MCPRemoteProxy with authServerRef")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-authref-happy").
				WithAuthServerRef("test-embedded-auth").
				WithOIDCConfigRef("test-oidc", "https://test-resource.example.com").
				Create(proxyHelper)

			By("waiting for AuthServerRefValidated condition to be True")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
				metav1.ConditionTrue,
				MediumTimeout,
			)

			By("verifying the condition message")
			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Message).To(ContainSubstring("is valid"))

			By("verifying embedded_auth_server_config in the runconfig ConfigMap")
			cm := proxyHelper.WaitForConfigMap(ConfigMapName(proxy.Name), MediumTimeout)
			Expect(cm.Data).To(HaveKey("runconfig.json"))

			var runConfig map[string]interface{}
			Expect(json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)).To(Succeed())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))

			By("cleaning up auth resources")
			Expect(k8sClient.Delete(testCtx, authConfig)).To(Succeed())
			Expect(k8sClient.Delete(testCtx, oidcConfig)).To(Succeed())
		})
	})

	Context("Combined auth: authServerRef (embeddedAuthServer) + externalAuthConfigRef (awsSts)", func() {
		It("should generate runconfig with both embedded_auth_server_config and aws_sts_config", func() {
			By("creating MCPOIDCConfig")
			oidcConfig := newMCPOIDCConfig("combined-oidc", testNamespace)
			Expect(k8sClient.Create(testCtx, oidcConfig)).To(Succeed())

			By("creating embedded auth config")
			embeddedAuth := newEmbeddedAuthConfig("combined-embedded", testNamespace)
			Expect(k8sClient.Create(testCtx, embeddedAuth)).To(Succeed())

			By("creating AWS STS auth config")
			awsStsAuth := newAWSStsConfig("combined-aws-sts", testNamespace)
			Expect(k8sClient.Create(testCtx, awsStsAuth)).To(Succeed())

			By("creating MCPRemoteProxy with authServerRef + externalAuthConfigRef (different types)")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-authref-combined").
				WithAuthServerRef("combined-embedded").
				WithExternalAuthConfigRef("combined-aws-sts").
				WithOIDCConfigRef("combined-oidc", "https://test-resource.example.com").
				Create(proxyHelper)

			By("waiting for AuthServerRefValidated condition to be True")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
				metav1.ConditionTrue,
				MediumTimeout,
			)

			By("verifying the runconfig ConfigMap contains both auth configs")
			cm := proxyHelper.WaitForConfigMap(ConfigMapName(proxy.Name), MediumTimeout)
			Expect(cm.Data).To(HaveKey("runconfig.json"))

			var runConfig map[string]interface{}
			Expect(json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)).To(Succeed())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))
			Expect(runConfig).To(HaveKey("aws_sts_config"))

			By("cleaning up auth resources")
			Expect(k8sClient.Delete(testCtx, embeddedAuth)).To(Succeed())
			Expect(k8sClient.Delete(testCtx, awsStsAuth)).To(Succeed())
			Expect(k8sClient.Delete(testCtx, oidcConfig)).To(Succeed())
		})
	})

	Context("Conflict: authServerRef + externalAuthConfigRef both pointing to embeddedAuthServer", func() {
		It("should not reach Ready phase due to conflict error", func() {
			By("creating MCPOIDCConfig")
			oidcConfig := newMCPOIDCConfig("conflict-oidc", testNamespace)
			Expect(k8sClient.Create(testCtx, oidcConfig)).To(Succeed())

			By("creating two embedded auth configs")
			auth1 := newEmbeddedAuthConfig("conflict-auth-1", testNamespace)
			Expect(k8sClient.Create(testCtx, auth1)).To(Succeed())
			auth2 := newEmbeddedAuthConfig("conflict-auth-2", testNamespace)
			Expect(k8sClient.Create(testCtx, auth2)).To(Succeed())

			By("creating MCPRemoteProxy with both refs pointing to embeddedAuthServer")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-authref-conflict").
				WithAuthServerRef("conflict-auth-1").
				WithExternalAuthConfigRef("conflict-auth-2").
				WithOIDCConfigRef("conflict-oidc", "https://test-resource.example.com").
				Create(proxyHelper)

			By("verifying the proxy never reaches Ready phase")
			// The MCPRemoteProxy controller does not set Phase=Failed for
			// ensureAllResources errors — it requeues indefinitely.
			Consistently(func() mcpv1beta1.MCPRemoteProxyPhase {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return ""
				}
				return p.Status.Phase
			}, time.Second*10, DefaultPollingInterval).ShouldNot(Equal(mcpv1beta1.MCPRemoteProxyPhaseReady))

			By("verifying AuthServerRefValidated is True (individual ref is valid)")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
				metav1.ConditionTrue,
				MediumTimeout,
			)

			By("cleaning up auth resources")
			Expect(k8sClient.Delete(testCtx, auth1)).To(Succeed())
			Expect(k8sClient.Delete(testCtx, auth2)).To(Succeed())
			Expect(k8sClient.Delete(testCtx, oidcConfig)).To(Succeed())
		})
	})

	Context("Type mismatch: authServerRef pointing to non-embeddedAuthServer type", func() {
		It("should reach Failed phase with type mismatch condition", func() {
			By("creating MCPExternalAuthConfig with unauthenticated type")
			authConfig := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "typemismatch-auth", Namespace: testNamespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
				},
			}
			Expect(k8sClient.Create(testCtx, authConfig)).To(Succeed())

			By("creating MCPRemoteProxy with authServerRef to unauthenticated config")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-authref-typemismatch").
				WithAuthServerRef("typemismatch-auth").
				Create(proxyHelper)

			By("waiting for Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1beta1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying AuthServerRefValidated condition is False with type mismatch message")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
				metav1.ConditionFalse,
				MediumTimeout,
			)

			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Message).To(ContainSubstring("only embeddedAuthServer is supported"))

			By("cleaning up auth config")
			Expect(k8sClient.Delete(testCtx, authConfig)).To(Succeed())
		})
	})

	Context("Backward compatibility: externalAuthConfigRef only (no authServerRef)", func() {
		It("should generate runconfig with embedded_auth_server_config without Failed phase", func() {
			By("creating MCPOIDCConfig")
			oidcConfig := newMCPOIDCConfig("legacy-oidc", testNamespace)
			Expect(k8sClient.Create(testCtx, oidcConfig)).To(Succeed())

			By("creating MCPExternalAuthConfig with embeddedAuthServer type")
			authConfig := newEmbeddedAuthConfig("legacy-embedded", testNamespace)
			Expect(k8sClient.Create(testCtx, authConfig)).To(Succeed())

			By("creating MCPRemoteProxy with only externalAuthConfigRef")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-legacy-extauth").
				WithExternalAuthConfigRef("legacy-embedded").
				WithOIDCConfigRef("legacy-oidc", "https://test-resource.example.com").
				Create(proxyHelper)

			By("verifying embedded_auth_server_config in the runconfig ConfigMap")
			cm := proxyHelper.WaitForConfigMap(ConfigMapName(proxy.Name), MediumTimeout)
			Expect(cm.Data).To(HaveKey("runconfig.json"))

			var runConfig map[string]interface{}
			Expect(json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)).To(Succeed())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))

			By("verifying the proxy is not in Failed phase")
			phase, err := proxyHelper.GetRemoteProxyPhase(proxy.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(phase).NotTo(Equal(mcpv1beta1.MCPRemoteProxyPhaseFailed))

			By("cleaning up auth resources")
			Expect(k8sClient.Delete(testCtx, authConfig)).To(Succeed())
			Expect(k8sClient.Delete(testCtx, oidcConfig)).To(Succeed())
		})
	})
})

// newEmbeddedAuthConfig creates an MCPExternalAuthConfig with type embeddedAuthServer.
func newEmbeddedAuthConfig(name, namespace string) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "http://localhost:9090",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "test-provider",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL: "https://accounts.google.com",
							ClientID:  "test-client-id",
						},
					},
				},
			},
		},
	}
}

// newAWSStsConfig creates an MCPExternalAuthConfig with type awsSts.
func newAWSStsConfig(name, namespace string) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeAWSSts,
			AWSSts: &mcpv1beta1.AWSStsConfig{
				Region:          "us-east-1",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/test-role",
			},
		},
	}
}

// newMCPOIDCConfig creates an MCPOIDCConfig with inline OIDC configuration.
func newMCPOIDCConfig(name, namespace string) *mcpv1beta1.MCPOIDCConfig {
	return &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer: "http://localhost:9090",
			},
		},
	}
}
