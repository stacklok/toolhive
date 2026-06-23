// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// The MCPAuthzConfig controller is NOT registered in this suite, so we pre-seed
// the config's Valid condition + ConfigHash directly; the VirtualMCPServer
// controller (which is registered) only reads them.
var _ = Describe("VirtualMCPServer AuthzConfigRef Integration", Label("k8s", "authz"), func() {
	const (
		timeout   = time.Second * 30
		interval  = time.Millisecond * 250
		namespace = "default"

		cedarConfig = `{"policies":["permit(principal, action, resource);"],"entities_json":"[]"}`
		httpConfig  = `{"http":{"url":"https://pdp.example.com"},"claim_mapping":"standard"}`
	)

	// seedAuthzConfig creates an MCPAuthzConfig and stamps its status (Valid
	// condition + ConfigHash) as the MCPAuthzConfig controller would.
	seedAuthzConfig := func(name, typ, rawConfig, hash string, valid bool) *mcpv1beta1.MCPAuthzConfig {
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

	newGroup := func(name string) *mcpv1beta1.MCPGroup {
		return &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       mcpv1beta1.MCPGroupSpec{Description: "authz ref integration test"},
		}
	}

	newVmcp := func(name, groupName, authzRefName string) *mcpv1beta1.VirtualMCPServer {
		return &mcpv1beta1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.VirtualMCPServerSpec{
				GroupRef: &mcpv1beta1.MCPGroupRef{Name: groupName},
				Config:   vmcpconfig.Config{Group: groupName},
				IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
					Type:           "anonymous",
					AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: authzRefName},
				},
			},
		}
	}

	getVmcp := func(name string) *mcpv1beta1.VirtualMCPServer {
		got := &mcpv1beta1.VirtualMCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, got)).To(Succeed())
		return got
	}

	Context("with a valid cedarv1 MCPAuthzConfig", Ordered, func() {
		const (
			grpName   = "authz-grp-cedar"
			cfgName   = "authz-cedar"
			vmcpName  = "vmcp-authz-cedar"
			cmKeyYAML = "config.yaml"
		)
		BeforeAll(func() {
			Expect(k8sClient.Create(ctx, newGroup(grpName))).To(Succeed())
			seedAuthzConfig(cfgName, "cedarv1", cedarConfig, "hash-1", true)
			Expect(k8sClient.Create(ctx, newVmcp(vmcpName, grpName, cfgName))).To(Succeed())
		})
		AfterAll(func() {
			_ = k8sClient.Delete(ctx, newVmcp(vmcpName, grpName, cfgName))
			_ = k8sClient.Delete(ctx, newGroup(grpName))
			Eventually(func() bool {
				err := k8sClient.Delete(ctx, &mcpv1beta1.MCPAuthzConfig{
					ObjectMeta: metav1.ObjectMeta{Name: cfgName, Namespace: namespace},
				})
				return err == nil || apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})

		It("sets AuthzConfigRefValidated=True and tracks the hash", func() {
			Eventually(func(g Gomega) {
				got := getVmcp(vmcpName)
				cond := meta.FindStatusCondition(got.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal(mcpv1beta1.ConditionReasonAuthzConfigRefValid))
				g.Expect(got.Status.AuthzConfigHash).To(Equal("hash-1"))
			}, timeout, interval).Should(Succeed())
		})

		It("resolves the referenced Cedar policy into the generated vmcp config", func() {
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: fmt.Sprintf("%s-vmcp-config", vmcpName), Namespace: namespace,
				}, cm)).To(Succeed())

				var config vmcpconfig.Config
				g.Expect(yaml.Unmarshal([]byte(cm.Data[cmKeyYAML]), &config)).To(Succeed())
				g.Expect(config.IncomingAuth).NotTo(BeNil())
				g.Expect(config.IncomingAuth.Authz).NotTo(BeNil())
				g.Expect(config.IncomingAuth.Authz.Type).To(Equal("cedar"))
				g.Expect(config.IncomingAuth.Authz.Policies).To(ContainElement("permit(principal, action, resource);"))
			}, timeout, interval).Should(Succeed())
		})

		It("reflects a config hash change on the referencing VirtualMCPServer", func() {
			var cfg mcpv1beta1.MCPAuthzConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfgName, Namespace: namespace}, &cfg)).To(Succeed())
			cfg.Status.ConfigHash = "hash-2"
			meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
				Type: mcpv1beta1.ConditionTypeAuthzConfigValid, Status: metav1.ConditionTrue, Reason: "Test",
			})
			Expect(k8sClient.Status().Update(ctx, &cfg)).To(Succeed())

			// The VirtualMCPServer controller watches MCPAuthzConfig, so the change
			// is picked up without an external nudge.
			Eventually(func(g Gomega) {
				g.Expect(getVmcp(vmcpName).Status.AuthzConfigHash).To(Equal("hash-2"))
			}, timeout, interval).Should(Succeed())
		})

		It("transitions AuthzConfigRefValidated to False when the config becomes invalid", func() {
			var cfg mcpv1beta1.MCPAuthzConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cfgName, Namespace: namespace}, &cfg)).To(Succeed())
			meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
				Type: mcpv1beta1.ConditionTypeAuthzConfigValid, Status: metav1.ConditionFalse, Reason: "Invalidated",
			})
			Expect(k8sClient.Status().Update(ctx, &cfg)).To(Succeed())

			Eventually(func(g Gomega) {
				cond := meta.FindStatusCondition(getVmcp(vmcpName).Status.Conditions,
					mcpv1beta1.ConditionAuthzConfigRefValidated)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(mcpv1beta1.ConditionReasonAuthzConfigRefNotValid))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("with a non-Cedar (httpv1) MCPAuthzConfig", Ordered, func() {
		const (
			grpName  = "authz-grp-http"
			cfgName  = "authz-http"
			vmcpName = "vmcp-authz-http"
		)
		BeforeAll(func() {
			Expect(k8sClient.Create(ctx, newGroup(grpName))).To(Succeed())
			seedAuthzConfig(cfgName, "httpv1", httpConfig, "hash-1", true)
			Expect(k8sClient.Create(ctx, newVmcp(vmcpName, grpName, cfgName))).To(Succeed())
		})
		AfterAll(func() {
			_ = k8sClient.Delete(ctx, newVmcp(vmcpName, grpName, cfgName))
			_ = k8sClient.Delete(ctx, newGroup(grpName))
			Eventually(func() bool {
				err := k8sClient.Delete(ctx, &mcpv1beta1.MCPAuthzConfig{
					ObjectMeta: metav1.ObjectMeta{Name: cfgName, Namespace: namespace},
				})
				return err == nil || apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})

		It("sets AuthzConfigRefValidated=False/NotValid naming the unsupported type", func() {
			Eventually(func(g Gomega) {
				cond := meta.FindStatusCondition(getVmcp(vmcpName).Status.Conditions,
					mcpv1beta1.ConditionAuthzConfigRefValidated)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(mcpv1beta1.ConditionReasonAuthzConfigRefNotValid))
				g.Expect(cond.Message).To(ContainSubstring("httpv1"))
			}, timeout, interval).Should(Succeed())
		})

		It("fails fast: the vmcp config is never generated for a non-Cedar reference", func() {
			// vMCP's runtime authz middleware is Cedar-only, so the controller rejects
			// httpv1 before materializing the config ConfigMap.
			Consistently(func() bool {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name: fmt.Sprintf("%s-vmcp-config", vmcpName), Namespace: namespace,
				}, cm)
				return apierrors.IsNotFound(err)
			}, time.Second*3, interval).Should(BeTrue(),
				"non-Cedar authzConfigRef must not produce a vmcp config ConfigMap")
		})
	})
})
