// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// An MCPServer that starts with an invalid PodTemplateSpec is blocked
// (PodTemplateValid=False, no Deployment). Once the spec is corrected the
// controller must re-process it: the PodTemplateValid condition flips to True
// and the Deployment is created. Those two signals are the recovery proof that
// is observable under envtest — phase/Ready stay stale because no kubelet runs
// pods (the reconciler preserves Failed while zero backend pods exist), so this
// test deliberately does not assert on Status.Phase.
var _ = Describe("MCPServer recovery from an invalid configuration", Ordered, func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		namespace     string
		mcpServerName string
		mcpServer     *mcpv1beta1.MCPServer
		ns            *corev1.Namespace
	)

	BeforeAll(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-mcpserver-recovery-"},
		}
		Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
		namespace = ns.Name
		mcpServerName = "test-mcpserver-recovery"

		mcpServer = &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerName,
				Namespace: namespace,
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:     "example/mcp-server:latest",
				Transport: "stdio",
				ProxyPort: 8080,
				// containers must be an array - this is rejected during validation.
				PodTemplateSpec: &runtime.RawExtension{
					Raw: []byte(`{"spec": {"containers": "invalid-not-an-array"}}`),
				},
			},
		}
		Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
	})

	AfterAll(func() {
		_ = k8sClient.Delete(ctx, mcpServer)
		Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
	})

	It("blocks the Deployment while the PodTemplateSpec is invalid", func() {
		Eventually(func() bool {
			updated := &mcpv1beta1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, updated); err != nil {
				return false
			}
			cond := apimeta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionPodTemplateValid)
			return cond != nil && cond.Status == metav1.ConditionFalse
		}, timeout, interval).Should(BeTrue())

		// No Deployment should exist while validation is failing.
		Consistently(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, &appsv1.Deployment{})
			return err != nil
		}, 3*time.Second, interval).Should(BeTrue())
	})

	It("recovers once the PodTemplateSpec is corrected", func() {
		// Correct the spec with a valid PodTemplateSpec.
		Eventually(func() error {
			updated := &mcpv1beta1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, updated); err != nil {
				return err
			}
			updated.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec": {"containers": [{"name": "mcp"}]}}`),
			}
			return k8sClient.Update(ctx, updated)
		}, timeout, interval).Should(Succeed())

		// The validation condition flips to True.
		Eventually(func() bool {
			updated := &mcpv1beta1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, updated); err != nil {
				return false
			}
			cond := apimeta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionPodTemplateValid)
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, timeout, interval).Should(BeTrue())

		// The Deployment that was blocked is now created.
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, &appsv1.Deployment{})
		}, timeout, interval).Should(Succeed())
	})
})
