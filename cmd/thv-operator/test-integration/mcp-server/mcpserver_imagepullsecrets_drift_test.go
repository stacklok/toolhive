// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

var _ = Describe("MCPServer Deployment ImagePullSecrets Drift", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("when imagePullSecrets is added after initial creation", Ordered, func() {
		var (
			namespace     = "default"
			mcpServerName = "ips-add-test-server"
			mcpServer     *mcpv1beta1.MCPServer
		)

		BeforeAll(func() {
			mcpServer = &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: namespace},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:latest",
					Transport: "stdio",
					ProxyPort: 8080,
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).To(Succeed())
		})

		AfterAll(func() {
			Expect(k8sClient.Delete(ctx, mcpServer)).To(Succeed())
		})

		It("rolls the Deployment to include the new pull secrets", func() {
			By("waiting for the initial Deployment to be created with no pull secrets")
			Eventually(func() []corev1.LocalObjectReference {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: mcpServerName, Namespace: namespace,
				}, d); err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, timeout, interval).Should(BeEmpty())

			By("patching the MCPServer to add imagePullSecrets")
			Eventually(func() error {
				current := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: mcpServerName, Namespace: namespace,
				}, current); err != nil {
					return err
				}
				current.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-creds"}},
					},
				}
				return k8sClient.Update(ctx, current)
			}, timeout, interval).Should(Succeed())

			By("waiting for the Deployment to roll with the new pull secret")
			Eventually(func() []corev1.LocalObjectReference {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: mcpServerName, Namespace: namespace,
				}, d); err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, timeout, interval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "registry-creds"}),
			)
		})
	})

	Context("when imagePullSecrets value is changed", Ordered, func() {
		var (
			namespace     = "default"
			mcpServerName = "ips-change-test-server"
			mcpServer     *mcpv1beta1.MCPServer
		)

		BeforeAll(func() {
			mcpServer = &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: namespace},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:latest",
					Transport: "stdio",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							ImagePullSecrets: []corev1.LocalObjectReference{{Name: "old-creds"}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).To(Succeed())
		})

		AfterAll(func() {
			Expect(k8sClient.Delete(ctx, mcpServer)).To(Succeed())
		})

		It("rolls the Deployment with the updated pull secret name", func() {
			By("waiting for the Deployment with the initial pull secret")
			Eventually(func() []corev1.LocalObjectReference {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: mcpServerName, Namespace: namespace,
				}, d); err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, timeout, interval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "old-creds"}),
			)

			By("patching the MCPServer to change the pull secret name")
			Eventually(func() error {
				current := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: mcpServerName, Namespace: namespace,
				}, current); err != nil {
					return err
				}
				current.Spec.ResourceOverrides.ProxyDeployment.ImagePullSecrets = []corev1.LocalObjectReference{
					{Name: "new-creds"},
				}
				return k8sClient.Update(ctx, current)
			}, timeout, interval).Should(Succeed())

			By("waiting for the Deployment to roll with the new pull secret")
			Eventually(func() []corev1.LocalObjectReference {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: mcpServerName, Namespace: namespace,
				}, d); err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, timeout, interval).Should(
				And(
					ContainElement(corev1.LocalObjectReference{Name: "new-creds"}),
					Not(ContainElement(corev1.LocalObjectReference{Name: "old-creds"})),
				),
			)
		})
	})
})
