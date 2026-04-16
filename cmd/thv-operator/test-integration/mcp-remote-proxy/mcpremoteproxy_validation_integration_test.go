// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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

	Context("Remote URL Format Validation", func() {
		It("should reject creation when remote URL has invalid scheme via CRD validation", func() {
			By("attempting to create an MCPRemoteProxy with ftp:// remote URL")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-bad-url").
				WithRemoteURL("ftp://bad-scheme.example.com").
				Build()

			By("verifying the API server rejects the resource")
			err := k8sClient.Create(testCtx, proxy)
			Expect(err).To(HaveOccurred(), "expected CRD validation to reject ftp:// URL")
			Expect(err.Error()).To(ContainSubstring("remoteUrl"))
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

	Context("Kubernetes Events", func() {
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

	})

})
