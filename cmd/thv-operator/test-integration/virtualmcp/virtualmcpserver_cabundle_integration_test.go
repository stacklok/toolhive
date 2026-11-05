// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer OIDC CA bundle conditions", Label("k8s", "oidc", "cabundle"), func() {
	It("refreshes advisory validation as the CA ConfigMap is created, repaired, and deleted", func() {
		const (
			timeout  = 30 * time.Second
			interval = 100 * time.Millisecond
			caName   = "incoming-oidc-ca"
		)
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "vmcp-ca-condition-"}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, namespace)).To(Succeed()) })
		group := &mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: "group", Namespace: namespace.Name}}
		Expect(k8sClient.Create(ctx, group)).To(Succeed())
		oidc := &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "oidc", Namespace: namespace.Name},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{
					Issuer: "https://issuer.example.com",
					CABundleRef: &mcpv1beta1.CABundleSource{ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: caName},
						Key:                  "ca.crt",
					}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, oidc)).To(Succeed())
		vmcp := v1beta1test.NewVirtualMCPServer("vmcp", namespace.Name,
			v1beta1test.WithVMCPGroupRef(group.Name),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{Group: group.Name}),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidc.Name, Audience: "audience"},
			}),
		)
		Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
		vmcpKey := types.NamespacedName{Name: vmcp.Name, Namespace: namespace.Name}
		assertCondition := func(status metav1.ConditionStatus, reason string) {
			GinkgoHelper()
			Eventually(func(g Gomega) {
				current := &mcpv1beta1.VirtualMCPServer{}
				g.Expect(k8sClient.Get(ctx, vmcpKey, current)).To(Succeed())
				condition := meta.FindStatusCondition(current.Status.Conditions,
					mcpv1beta1.ConditionTypeVirtualMCPServerCABundleRefValidated)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(status))
				g.Expect(condition.Reason).To(Equal(reason))
				g.Expect(condition.ObservedGeneration).To(Equal(current.Generation))
			}, timeout, interval).Should(Succeed())
		}

		By("reporting the missing CA while still generating the Deployment")
		assertCondition(metav1.ConditionFalse, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefNotFound)
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, vmcpKey, deployment)).To(Succeed())
			var caVolume *corev1.ConfigMapVolumeSource
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.ConfigMap != nil && volume.ConfigMap.Name == caName {
					caVolume = volume.ConfigMap
				}
			}
			g.Expect(caVolume).NotTo(BeNil())
			g.Expect(caVolume.Items).To(ContainElement(corev1.KeyToPath{Key: "ca.crt", Path: "ca.crt"}))
		}, timeout, interval).Should(Succeed())

		By("observing ConfigMap creation without editing the workload")
		bundle := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: caName, Namespace: namespace.Name},
			// Reference validation checks projection availability, not PEM contents.
			Data: map[string]string{"ca.crt": "certificate"},
		}
		Expect(k8sClient.Create(ctx, bundle)).To(Succeed())
		assertCondition(metav1.ConditionTrue, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefValid)

		updateBundle := func(data map[string]string) {
			GinkgoHelper()
			Eventually(func() error {
				current := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: caName, Namespace: namespace.Name}, current); err != nil {
					return err
				}
				current.Data = data
				return k8sClient.Update(ctx, current)
			}, timeout, interval).Should(Succeed())
		}
		By("detecting a removed key and recovering when that key is restored")
		updateBundle(map[string]string{"unrelated": "value"})
		assertCondition(metav1.ConditionFalse, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefInvalid)
		updateBundle(map[string]string{"ca.crt": "replacement certificate"})
		assertCondition(metav1.ConditionTrue, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefValid)

		By("detecting deletion without editing the workload")
		Expect(k8sClient.Delete(ctx, bundle)).To(Succeed())
		assertCondition(metav1.ConditionFalse, mcpv1beta1.ConditionReasonVirtualMCPServerCABundleRefNotFound)

		By("clearing the condition when the CA reference is removed")
		Eventually(func() error {
			current := &mcpv1beta1.MCPOIDCConfig{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: oidc.Name, Namespace: namespace.Name}, current); err != nil {
				return err
			}
			current.Spec.Inline.CABundleRef = nil
			return k8sClient.Update(ctx, current)
		}, timeout, interval).Should(Succeed())
		Eventually(func(g Gomega) {
			current := &mcpv1beta1.VirtualMCPServer{}
			g.Expect(k8sClient.Get(ctx, vmcpKey, current)).To(Succeed())
			g.Expect(meta.FindStatusCondition(current.Status.Conditions,
				mcpv1beta1.ConditionTypeVirtualMCPServerCABundleRefValidated)).To(BeNil())
		}, timeout, interval).Should(Succeed())
	})
})
