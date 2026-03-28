// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPRemoteProxy Configuration Validation", Label("k8s", "remoteproxy", "validation"), func() {
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

	Context("OIDC Issuer URL Validation", func() {
		It("should set ConfigurationValid=False and Failed phase when OIDC issuer uses HTTP", func() {
			By("creating an MCPRemoteProxy with HTTP OIDC issuer")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-http-oidc").
				WithInlineOIDCConfig("http://insecure-idp.example.com", "test-audience", false).
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying the ConfigurationValid condition is False with OIDCIssuerInsecure reason")
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1alpha1.ConditionTypeConfigurationValid,
				mcpv1alpha1.ConditionReasonOIDCIssuerInsecure,
				MediumTimeout,
			)

			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeConfigurationValid,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Message).To(ContainSubstring("HTTP scheme"))
		})

		It("should set ConfigurationValid=True when OIDC issuer uses HTTPS", func() {
			By("creating an MCPRemoteProxy with HTTPS OIDC issuer")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-https-oidc").
				WithInlineOIDCConfig("https://secure-idp.example.com", "test-audience", false).
				Create(proxyHelper)

			By("waiting for the ConfigurationValid condition to be True")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1alpha1.ConditionTypeConfigurationValid,
				metav1.ConditionTrue,
				MediumTimeout,
			)

			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeConfigurationValid,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonConfigurationValid))
		})
	})

	Context("Remote URL Format Validation", func() {
		It("should reject creation when remote URL has invalid scheme via CRD validation", func() {
			By("attempting to create an MCPRemoteProxy with ftp:// remote URL")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-bad-url").
				WithRemoteURL("ftp://bad-scheme.example.com").
				Build()

			By("verifying the API server rejects the resource")
			err := k8sClient.Create(testCtx, proxy)
			Expect(err).To(HaveOccurred(), "expected CRD validation to reject ftp:// URL")
			Expect(err.Error()).To(ContainSubstring("remoteURL"))
		})
	})

	Context("JWKS URL Validation", func() {
		It("should set ConfigurationValid=False when JWKS URL uses HTTP", func() {
			By("creating an MCPRemoteProxy with HTTP JWKS URL")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-http-jwks").
				WithInlineOIDCConfigAndJWKS(
					"https://auth.example.com", "test-audience",
					"http://jwks.example.com/.well-known/jwks.json",
				).
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying the ConfigurationValid condition")
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1alpha1.ConditionTypeConfigurationValid,
				mcpv1alpha1.ConditionReasonJWKSURLInvalid,
				MediumTimeout,
			)
		})
	})

	Context("Cedar Policy Syntax Validation", func() {
		It("should set ConfigurationValid=False when Cedar policy has invalid syntax", func() {
			By("creating an MCPRemoteProxy with invalid Cedar policy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-bad-cedar").
				WithInlineAuthzConfig([]string{"not valid cedar policy syntax"}).
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying the ConfigurationValid condition")
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1alpha1.ConditionTypeConfigurationValid,
				mcpv1alpha1.ConditionReasonAuthzPolicySyntaxInvalid,
				MediumTimeout,
			)
		})
	})

	Context("ConfigMap and Secret Reference Validation", func() {
		It("should set ConfigurationValid=False when authz ConfigMap does not exist", func() {
			By("creating an MCPRemoteProxy with missing authz ConfigMap reference")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-missing-cm").
				WithAuthzConfigMapRef("does-not-exist", "").
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying the ConfigurationValid condition")
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1alpha1.ConditionTypeConfigurationValid,
				mcpv1alpha1.ConditionReasonAuthzConfigMapNotFound,
				MediumTimeout,
			)
		})

		It("should set ConfigurationValid=False when header Secret does not exist", func() {
			By("creating an MCPRemoteProxy with missing header Secret reference")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-missing-secret").
				WithHeaderFromSecret("X-API-Key", "missing-secret", "api-key").
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying the ConfigurationValid condition")
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1alpha1.ConditionTypeConfigurationValid,
				mcpv1alpha1.ConditionReasonHeaderSecretNotFound,
				MediumTimeout,
			)
		})
	})

	Context("CA Bundle ConfigMap Reference Validation", func() {
		It("should set CABundleRefValidated=True when CA bundle ConfigMap exists with correct key", func() {
			By("creating a CA bundle ConfigMap")
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ca-bundle-valid",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"ca.crt": "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
				},
			}
			Expect(k8sClient.Create(testCtx, configMap)).To(Succeed())

			By("creating an MCPRemoteProxy with valid CA bundle reference")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-valid-ca").
				WithCABundleRef("ca-bundle-valid", "ca.crt").
				Create(proxyHelper)

			By("waiting for the CABundleRefValidated condition to be True")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1alpha1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
				metav1.ConditionTrue,
				MediumTimeout,
			)

			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonCABundleRefValid))
		})

		It("should set CABundleRefValidated=False when CA bundle ConfigMap does not exist", func() {
			By("creating an MCPRemoteProxy with non-existent CA bundle ConfigMap")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-missing-ca-cm").
				WithCABundleRef("non-existent-ca-bundle", "ca.crt").
				Create(proxyHelper)

			By("waiting for the CABundleRefValidated condition to be False")
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1alpha1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
				mcpv1alpha1.ConditionReasonCABundleRefNotFound,
				MediumTimeout,
			)

			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Message).To(ContainSubstring("not found"))
		})

		It("should set CABundleRefValidated=False when CA bundle ConfigMap exists but key is missing", func() {
			By("creating a CA bundle ConfigMap with a different key")
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ca-bundle-wrong-key",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"other-key": "some-data",
				},
			}
			Expect(k8sClient.Create(testCtx, configMap)).To(Succeed())

			By("creating an MCPRemoteProxy referencing a non-existent key")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-wrong-key-ca").
				WithCABundleRef("ca-bundle-wrong-key", "ca.crt").
				Create(proxyHelper)

			By("waiting for the CABundleRefValidated condition to be False")
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1alpha1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
				mcpv1alpha1.ConditionReasonCABundleRefInvalid,
				MediumTimeout,
			)

			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Message).To(ContainSubstring("ca.crt"))
		})

		It("should not set CABundleRefValidated condition when no CA bundle is configured", func() {
			By("creating an MCPRemoteProxy without CA bundle reference")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-no-ca").
				Create(proxyHelper)

			By("waiting for the ConfigurationValid condition to be True")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1alpha1.ConditionTypeConfigurationValid,
				metav1.ConditionTrue,
				MediumTimeout,
			)

			By("verifying the CABundleRefValidated condition is not set")
			_, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	Context("Kubernetes Events", func() {
		It("should emit a Warning event when OIDC issuer uses HTTP", func() {
			By("creating an MCPRemoteProxy with HTTP OIDC issuer")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-event-http-oidc").
				WithInlineOIDCConfig("http://insecure-idp.example.com", "test-audience", false).
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying a Warning event was emitted with OIDCIssuerInsecure reason")
			Eventually(func() bool {
				eventList := &corev1.EventList{}
				err := k8sClient.List(testCtx, eventList,
					client.InNamespace(testNamespace),
				)
				if err != nil {
					return false
				}
				for _, event := range eventList.Items {
					if event.InvolvedObject.Name == proxy.Name &&
						event.Type == corev1.EventTypeWarning &&
						event.Reason == mcpv1alpha1.ConditionReasonOIDCIssuerInsecure {
						Expect(event.Message).To(ContainSubstring("HTTP scheme"))
						return true
					}
				}
				return false
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue(),
				"expected a Warning event with reason OIDCIssuerInsecure")
		})

		It("should emit a Warning event when Cedar policy has invalid syntax", func() {
			By("creating an MCPRemoteProxy with invalid Cedar policy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-event-bad-cedar").
				WithInlineAuthzConfig([]string{"not valid cedar"}).
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying a Warning event was emitted with AuthzPolicySyntaxInvalid reason")
			Eventually(func() bool {
				eventList := &corev1.EventList{}
				err := k8sClient.List(testCtx, eventList, client.InNamespace(testNamespace))
				if err != nil {
					return false
				}
				for _, event := range eventList.Items {
					if event.InvolvedObject.Name == proxy.Name &&
						event.Type == corev1.EventTypeWarning &&
						event.Reason == mcpv1alpha1.ConditionReasonAuthzPolicySyntaxInvalid {
						return true
					}
				}
				return false
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue(),
				"expected a Warning event with reason AuthzPolicySyntaxInvalid")
		})

		It("should emit a Warning event when authz ConfigMap is not found", func() {
			By("creating an MCPRemoteProxy with missing authz ConfigMap")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-event-missing-cm").
				WithAuthzConfigMapRef("nonexistent-cm", "").
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying a Warning event was emitted")
			Eventually(func() bool {
				eventList := &corev1.EventList{}
				err := k8sClient.List(testCtx, eventList, client.InNamespace(testNamespace))
				if err != nil {
					return false
				}
				for _, event := range eventList.Items {
					if event.InvolvedObject.Name == proxy.Name &&
						event.Type == corev1.EventTypeWarning &&
						event.Reason == mcpv1alpha1.ConditionReasonAuthzConfigMapNotFound {
						return true
					}
				}
				return false
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue(),
				"expected a Warning event with reason AuthzConfigMapNotFound")
		})

		It("should emit a Warning event when header Secret is not found", func() {
			By("creating an MCPRemoteProxy with missing header Secret")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-event-missing-secret").
				WithHeaderFromSecret("X-API-Key", "nonexistent-secret", "key").
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying a Warning event was emitted")
			Eventually(func() bool {
				eventList := &corev1.EventList{}
				err := k8sClient.List(testCtx, eventList, client.InNamespace(testNamespace))
				if err != nil {
					return false
				}
				for _, event := range eventList.Items {
					if event.InvolvedObject.Name == proxy.Name &&
						event.Type == corev1.EventTypeWarning &&
						event.Reason == mcpv1alpha1.ConditionReasonHeaderSecretNotFound {
						return true
					}
				}
				return false
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue(),
				"expected a Warning event with reason HeaderSecretNotFound")
		})

		It("should not emit a Warning event when OIDC issuer uses HTTPS", func() {
			By("creating an MCPRemoteProxy with HTTPS OIDC issuer")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-event-https-oidc").
				WithInlineOIDCConfig("https://secure-idp.example.com", "test-audience", false).
				Create(proxyHelper)

			By("waiting for the ConfigurationValid condition to be True")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1alpha1.ConditionTypeConfigurationValid,
				metav1.ConditionTrue,
				MediumTimeout,
			)

			By("verifying no Warning event with OIDCIssuerInsecure reason was emitted")
			eventList := &corev1.EventList{}
			Expect(k8sClient.List(testCtx, eventList,
				client.InNamespace(testNamespace),
			)).To(Succeed())

			for _, event := range eventList.Items {
				if event.InvolvedObject.Name != proxy.Name {
					continue
				}
				Expect(event.Reason).NotTo(Equal(mcpv1alpha1.ConditionReasonOIDCIssuerInsecure),
					"should not have emitted an OIDCIssuerInsecure warning event")
			}
		})
	})

})
