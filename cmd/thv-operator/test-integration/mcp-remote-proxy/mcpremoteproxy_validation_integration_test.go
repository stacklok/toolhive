// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

})
