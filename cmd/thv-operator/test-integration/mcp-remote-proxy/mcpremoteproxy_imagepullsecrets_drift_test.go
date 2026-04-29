// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

var _ = Describe("MCPRemoteProxy Deployment ImagePullSecrets Drift",
	Label("k8s", "remoteproxy", "deployment-update"), func() {
		var (
			testCtx       context.Context
			proxyHelper   *MCPRemoteProxyTestHelper
			testNamespace string
		)

		BeforeEach(func() {
			testCtx = context.Background()
			testNamespace = createTestNamespace(testCtx)
			proxyHelper = NewMCPRemoteProxyTestHelper(testCtx, k8sClient, testNamespace)
		})

		AfterEach(func() {
			Expect(proxyHelper.CleanupRemoteProxies()).To(Succeed())
			deleteTestNamespace(testCtx, testNamespace)
		})

		Context("when imagePullSecrets is added after initial creation", func() {
			It("rolls the Deployment to include the new pull secrets", func() {
				By("creating an MCPRemoteProxy without resourceOverrides")
				proxy := proxyHelper.NewRemoteProxyBuilder("ips-add-test").Create(proxyHelper)

				By("waiting for the Deployment to be created")
				deployment := proxyHelper.WaitForDeployment(proxy.Name, MediumTimeout)
				Expect(deployment.Spec.Template.Spec.ImagePullSecrets).To(BeEmpty())

				By("patching the proxy to add imagePullSecrets")
				Eventually(func() error {
					current, err := proxyHelper.GetRemoteProxy(proxy.Name)
					if err != nil {
						return err
					}
					current.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							ImagePullSecrets: []corev1.LocalObjectReference{
								{Name: "registry-creds"},
							},
						},
					}
					return k8sClient.Update(testCtx, current)
				}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

				By("waiting for the Deployment to be updated with the new pull secret")
				Eventually(func() []corev1.LocalObjectReference {
					d := &appsv1.Deployment{}
					if err := k8sClient.Get(testCtx, types.NamespacedName{
						Name:      proxy.Name,
						Namespace: testNamespace,
					}, d); err != nil {
						return nil
					}
					return d.Spec.Template.Spec.ImagePullSecrets
				}, MediumTimeout, DefaultPollingInterval).Should(
					ContainElement(corev1.LocalObjectReference{Name: "registry-creds"}),
				)
			})
		})

		Context("when imagePullSecrets value is changed", func() {
			It("rolls the Deployment with the updated pull secret name", func() {
				By("creating an MCPRemoteProxy with initial imagePullSecrets")
				proxy := proxyHelper.NewRemoteProxyBuilder("ips-change-test").Build()
				proxy.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						ImagePullSecrets: []corev1.LocalObjectReference{{Name: "old-creds"}},
					},
				}
				Expect(k8sClient.Create(testCtx, proxy)).To(Succeed())

				By("waiting for the Deployment with the initial pull secret")
				Eventually(func() []corev1.LocalObjectReference {
					d := &appsv1.Deployment{}
					if err := k8sClient.Get(testCtx, types.NamespacedName{
						Name:      proxy.Name,
						Namespace: testNamespace,
					}, d); err != nil {
						return nil
					}
					return d.Spec.Template.Spec.ImagePullSecrets
				}, MediumTimeout, DefaultPollingInterval).Should(
					ContainElement(corev1.LocalObjectReference{Name: "old-creds"}),
				)

				By("patching the proxy to change the pull secret name")
				Eventually(func() error {
					current, err := proxyHelper.GetRemoteProxy(proxy.Name)
					if err != nil {
						return err
					}
					current.Spec.ResourceOverrides.ProxyDeployment.ImagePullSecrets = []corev1.LocalObjectReference{
						{Name: "new-creds"},
					}
					return k8sClient.Update(testCtx, current)
				}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

				By("waiting for the Deployment to roll with the new pull secret")
				Eventually(func() []corev1.LocalObjectReference {
					d := &appsv1.Deployment{}
					if err := k8sClient.Get(testCtx, types.NamespacedName{
						Name:      proxy.Name,
						Namespace: testNamespace,
					}, d); err != nil {
						return nil
					}
					return d.Spec.Template.Spec.ImagePullSecrets
				}, MediumTimeout, DefaultPollingInterval).Should(
					And(
						ContainElement(corev1.LocalObjectReference{Name: "new-creds"}),
						Not(ContainElement(corev1.LocalObjectReference{Name: "old-creds"})),
					),
				)
			})
		})
	})
