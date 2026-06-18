// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// The MCPAuthzConfig controller is not registered in this suite, so we pre-seed
// the config's Valid condition + ConfigHash directly; the MCPRemoteProxy
// controller (which is registered) only reads them.
var _ = Describe("MCPRemoteProxy AuthzConfigRef Integration Tests", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	// seedAuthzConfig creates an MCPAuthzConfig and stamps its status (Valid
	// condition + ConfigHash) as the MCPAuthzConfig controller would.
	seedAuthzConfig := func(name, namespace, typ, rawConfig, hash string, valid bool) *mcpv1beta1.MCPAuthzConfig {
		cfg := &mcpv1beta1.MCPAuthzConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPAuthzConfigSpec{
				Type:   typ,
				Config: runtime.RawExtension{Raw: []byte(rawConfig)},
			},
		}
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())

		status := metav1.ConditionFalse
		if valid {
			status = metav1.ConditionTrue
		}
		cfg.Status.ConfigHash = hash
		meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
			Type:    mcpv1beta1.ConditionTypeAuthzConfigValid,
			Status:  status,
			Reason:  "Test",
			Message: "seeded by integration test",
		})
		Expect(k8sClient.Status().Update(ctx, cfg)).To(Succeed())
		return cfg
	}

	newProxy := func(name, namespace, authzRefName string) *mcpv1beta1.MCPRemoteProxy {
		return &mcpv1beta1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPRemoteProxySpec{
				RemoteURL:      "https://example.com",
				Transport:      "streamable-http",
				AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: authzRefName},
			},
		}
	}

	const (
		cedarConfig = `{"policies":["permit(principal, action, resource);"],"entities_json":"[]"}`
		httpConfig  = `{"http":{"url":"https://pdp.example.com"},"claim_mapping":"standard"}`
	)

	DescribeTable("a valid referenced MCPAuthzConfig is validated and hash-tracked, for any backend",
		func(typ, rawConfig string) {
			namespace := createTestNamespace(ctx)
			DeferCleanup(func() { deleteTestNamespace(ctx, namespace) })

			seedAuthzConfig("authz-cfg", namespace, typ, rawConfig, "hash-1", true)
			Expect(k8sClient.Create(ctx, newProxy("rp-authz", namespace, "authz-cfg"))).To(Succeed())

			By("setting AuthzConfigRefValidated=True and tracking the hash")
			Eventually(func(g Gomega) {
				var got mcpv1beta1.MCPRemoteProxy
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rp-authz", Namespace: namespace}, &got)).To(Succeed())
				cond := meta.FindStatusCondition(got.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(got.Status.AuthzConfigHash).To(Equal("hash-1"))
			}, timeout, interval).Should(Succeed())

			By("materializing the authz ConfigMap the proxy mounts (any backend)")
			Eventually(func(g Gomega) {
				var cm corev1.ConfigMap
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rp-authz-authz-ref", Namespace: namespace}, &cm)).To(Succeed())
				g.Expect(cm.Data).To(HaveKey("authz.json"))
				g.Expect(cm.Data["authz.json"]).To(ContainSubstring(typ))
			}, timeout, interval).Should(Succeed())
		},
		Entry("cedarv1", "cedarv1", cedarConfig),
		Entry("httpv1", "httpv1", httpConfig),
	)

	Context("when the referenced MCPAuthzConfig changes", Ordered, func() {
		var namespace string
		const (
			cfgName = "authz-watch"
			rpName  = "rp-watch"
		)
		BeforeAll(func() {
			namespace = createTestNamespace(ctx)
			seedAuthzConfig(cfgName, namespace, "cedarv1", cedarConfig, "hash-1", true)
			Expect(k8sClient.Create(ctx, newProxy(rpName, namespace, cfgName))).To(Succeed())
		})
		AfterAll(func() {
			deleteTestNamespace(ctx, namespace)
		})

		It("reflects a config hash change on the referencing MCPRemoteProxy", func() {
			Eventually(func(g Gomega) {
				var got mcpv1beta1.MCPRemoteProxy
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rpName, Namespace: namespace}, &got)).To(Succeed())
				g.Expect(got.Status.AuthzConfigHash).To(Equal("hash-1"))
			}, timeout, interval).Should(Succeed())

			By("bumping the config hash")
			var cfg mcpv1beta1.MCPAuthzConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfgName, Namespace: namespace}, &cfg)).To(Succeed())
			cfg.Status.ConfigHash = "hash-2"
			meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
				Type: mcpv1beta1.ConditionTypeAuthzConfigValid, Status: metav1.ConditionTrue, Reason: "Test",
			})
			Expect(k8sClient.Status().Update(ctx, &cfg)).To(Succeed())

			// The MCPRemoteProxy controller watches MCPAuthzConfig, so the change is
			// picked up without an external nudge. (This asserts the observable
			// outcome; it does not attempt to prove the watch is the sole trigger.)
			By("observing the MCPRemoteProxy eventually reflect the new hash")
			Eventually(func(g Gomega) {
				var got mcpv1beta1.MCPRemoteProxy
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rpName, Namespace: namespace}, &got)).To(Succeed())
				g.Expect(got.Status.AuthzConfigHash).To(Equal("hash-2"))
			}, timeout, interval).Should(Succeed())
		})

		It("transitions AuthzConfigRefValidated to False when the config becomes invalid", func() {
			By("flagging the referenced config invalid")
			var cfg mcpv1beta1.MCPAuthzConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfgName, Namespace: namespace}, &cfg)).To(Succeed())
			meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
				Type: mcpv1beta1.ConditionTypeAuthzConfigValid, Status: metav1.ConditionFalse, Reason: "Invalidated",
			})
			Expect(k8sClient.Status().Update(ctx, &cfg)).To(Succeed())

			By("observing the MCPRemoteProxy condition flip to False/NotValid")
			Eventually(func(g Gomega) {
				var got mcpv1beta1.MCPRemoteProxy
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rpName, Namespace: namespace}, &got)).To(Succeed())
				cond := meta.FindStatusCondition(got.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(mcpv1beta1.ConditionReasonAuthzConfigRefNotValid))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("when the referenced MCPAuthzConfig is not valid", Ordered, func() {
		var namespace string
		const (
			cfgName = "authz-invalid"
			rpName  = "rp-invalid"
		)
		BeforeAll(func() {
			namespace = createTestNamespace(ctx)
			seedAuthzConfig(cfgName, namespace, "cedarv1", cedarConfig, "", false)
			Expect(k8sClient.Create(ctx, newProxy(rpName, namespace, cfgName))).To(Succeed())
		})
		AfterAll(func() {
			deleteTestNamespace(ctx, namespace)
		})

		It("sets AuthzConfigRefValidated=False with reason NotValid", func() {
			Eventually(func(g Gomega) {
				var got mcpv1beta1.MCPRemoteProxy
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rpName, Namespace: namespace}, &got)).To(Succeed())
				cond := meta.FindStatusCondition(got.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(mcpv1beta1.ConditionReasonAuthzConfigRefNotValid))
			}, timeout, interval).Should(Succeed())
		})
	})
})
