// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	testRemoteProxyName = "test-remote-proxy"
	testRemoteURL       = "https://remote.example.com/mcp"
)

// newTestMCPRemoteProxy creates an MCPRemoteProxy with the required OIDCConfig inline field
// and an optional OIDCConfigRef pointing to a shared MCPOIDCConfig.
func newTestMCPRemoteProxy(name, namespace string, oidcConfigRefName string) *mcpv1alpha1.MCPRemoteProxy {
	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: testRemoteURL,
			ProxyPort: 8080,
			Transport: "streamable-http",
			OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
				Type: "inline",
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer:   "https://auth.example.com",
					Audience: "test-audience",
					ClientID: "test-client",
				},
			},
		},
	}

	if oidcConfigRefName != "" {
		proxy.Spec.OIDCConfigRef = &mcpv1alpha1.MCPOIDCConfigReference{
			Name:     oidcConfigRefName,
			Audience: "test-proxy-audience",
			Scopes:   []string{"openid"},
		}
	}

	return proxy
}

var _ = Describe("MCPOIDCConfig and MCPRemoteProxy Cross-Resource Integration Tests", func() {
	Context("When MCPRemoteProxy references an MCPOIDCConfig (happy path)", Ordered, func() {
		var (
			namespace  string
			configName string
			proxyName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			proxy      *mcpv1alpha1.MCPRemoteProxy
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-proxy-oidcref-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			proxyName = testRemoteProxyName

			// Create MCPOIDCConfig
			oidcConfig = &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "test-client",
					},
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

			// Wait for Ready condition and ConfigHash to be set
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				if updated.Status.ConfigHash == "" {
					return false
				}
				for _, cond := range updated.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionTypeOIDCConfigReady && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Create MCPRemoteProxy with OIDCConfigRef
			proxy = newTestMCPRemoteProxy(proxyName, namespace, configName)
			Expect(k8sClient.Create(ctx, proxy)).Should(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, proxy)
			_ = k8sClient.Delete(ctx, oidcConfig)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should set OIDCConfigRefValidated condition to True", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionOIDCConfigRefValidated)
				if condition == nil {
					return false
				}
				return condition.Status == metav1.ConditionTrue &&
					condition.Reason == mcpv1alpha1.ConditionReasonOIDCConfigRefValid
			}, timeout, interval).Should(BeTrue())
		})

		It("should set OIDCConfigHash in MCPRemoteProxy status", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.OIDCConfigHash != ""
			}, timeout, interval).Should(BeTrue())
		})

		It("should track MCPRemoteProxy in MCPOIDCConfig ReferencingWorkloads", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: proxyName}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref == expectedRef {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When MCPRemoteProxy references non-existent MCPOIDCConfig (fail-closed on missing)", Ordered, func() {
		var (
			namespace string
			proxyName string
			proxy     *mcpv1alpha1.MCPRemoteProxy
			ns        *corev1.Namespace
		)

		BeforeAll(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-proxy-oidcref-missing-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			proxyName = testRemoteProxyName

			// Create MCPRemoteProxy with OIDCConfigRef pointing to a non-existent config
			proxy = newTestMCPRemoteProxy(proxyName, namespace, "does-not-exist")
			Expect(k8sClient.Create(ctx, proxy)).Should(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, proxy)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should enter Failed phase", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.Phase == mcpv1alpha1.MCPRemoteProxyPhaseFailed
			}, timeout, interval).Should(BeTrue())
		})

		It("should set OIDCConfigRefValidated condition to False with NotFound reason", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionOIDCConfigRefValidated)
				if condition == nil {
					return false
				}
				return condition.Status == metav1.ConditionFalse &&
					condition.Reason == mcpv1alpha1.ConditionReasonOIDCConfigRefNotFound
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When MCPRemoteProxy references a not-ready MCPOIDCConfig (fail-closed on not-ready)", Ordered, func() {
		var (
			namespace  string
			configName string
			proxyName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			proxy      *mcpv1alpha1.MCPRemoteProxy
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-proxy-oidcref-notready-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			proxyName = testRemoteProxyName

			// Create MCPOIDCConfig with invalid spec (missing inline config for inline type)
			// so it will have Ready=False
			oidcConfig = &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: nil, // Missing inline config should cause Ready=False
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

			// Wait for the MCPOIDCConfig to be reconciled with Ready=False
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				for _, cond := range updated.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionTypeOIDCConfigReady && cond.Status == metav1.ConditionFalse {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Create MCPRemoteProxy with OIDCConfigRef pointing to the not-ready config
			proxy = newTestMCPRemoteProxy(proxyName, namespace, configName)
			Expect(k8sClient.Create(ctx, proxy)).Should(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, proxy)
			_ = k8sClient.Delete(ctx, oidcConfig)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should set OIDCConfigRefValidated condition to False with NotReady reason", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionOIDCConfigRefValidated)
				if condition == nil {
					return false
				}
				return condition.Status == metav1.ConditionFalse &&
					condition.Reason == mcpv1alpha1.ConditionReasonOIDCConfigRefNotValid
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When MCPOIDCConfig spec is updated (hash change cascade)", Ordered, func() {
		var (
			namespace       string
			configName      string
			proxyName       string
			oidcConfig      *mcpv1alpha1.MCPOIDCConfig
			proxy           *mcpv1alpha1.MCPRemoteProxy
			ns              *corev1.Namespace
			originalHash    string
			originalCfgHash string
		)

		BeforeAll(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-proxy-oidcref-hash-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			proxyName = testRemoteProxyName

			// Create MCPOIDCConfig
			oidcConfig = &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "test-client",
					},
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

			// Wait for Ready condition and ConfigHash to be set
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				if updated.Status.ConfigHash == "" {
					return false
				}
				originalCfgHash = updated.Status.ConfigHash
				for _, cond := range updated.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionTypeOIDCConfigReady && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Create MCPRemoteProxy with OIDCConfigRef
			proxy = newTestMCPRemoteProxy(proxyName, namespace, configName)
			Expect(k8sClient.Create(ctx, proxy)).Should(Succeed())

			// Wait for the proxy to pick up the original hash
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				if updated.Status.OIDCConfigHash != "" {
					originalHash = updated.Status.OIDCConfigHash
					return true
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, proxy)
			_ = k8sClient.Delete(ctx, oidcConfig)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should update MCPRemoteProxy OIDCConfigHash when MCPOIDCConfig spec changes", func() {
			// Update the MCPOIDCConfig spec to trigger a hash change
			updated := &mcpv1alpha1.MCPOIDCConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configName,
				Namespace: namespace,
			}, updated)).Should(Succeed())

			updated.Spec.Inline.ClientID = "updated-client"
			Expect(k8sClient.Update(ctx, updated)).Should(Succeed())

			// Wait for MCPOIDCConfig ConfigHash to change
			Eventually(func() bool {
				cfg := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, cfg)
				if err != nil {
					return false
				}
				return cfg.Status.ConfigHash != "" && cfg.Status.ConfigHash != originalCfgHash
			}, timeout, interval).Should(BeTrue())

			// Eventually the MCPRemoteProxy should pick up the new hash
			Eventually(func() bool {
				proxyUpdated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, proxyUpdated)
				if err != nil {
					return false
				}
				return proxyUpdated.Status.OIDCConfigHash != "" &&
					proxyUpdated.Status.OIDCConfigHash != originalHash
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When deleting MCPOIDCConfig with active MCPRemoteProxy references (deletion protection)", Ordered, func() {
		var (
			namespace  string
			configName string
			proxyName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			proxy      *mcpv1alpha1.MCPRemoteProxy
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-proxy-oidcref-delete-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			proxyName = testRemoteProxyName

			// Create MCPOIDCConfig
			oidcConfig = &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "test-client",
					},
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

			// Wait for ready
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.ConfigHash != ""
			}, timeout, interval).Should(BeTrue())

			// Create MCPRemoteProxy with OIDCConfigRef
			proxy = newTestMCPRemoteProxy(proxyName, namespace, configName)
			Expect(k8sClient.Create(ctx, proxy)).Should(Succeed())

			// Wait for ReferencingWorkloads to be populated
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: proxyName}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref == expectedRef {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Attempt to delete the MCPOIDCConfig (should be blocked by finalizer)
			Expect(k8sClient.Delete(ctx, oidcConfig)).Should(Succeed())
		})

		AfterAll(func() {
			// Cleanup: delete the MCPRemoteProxy first to unblock the finalizer,
			// then wait for the MCPOIDCConfig to be fully deleted, then delete the namespace.
			_ = k8sClient.Delete(ctx, proxy)

			// Wait for MCPOIDCConfig to be fully removed
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())

			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should not be deleted while referenced by MCPRemoteProxy", func() {
			// The object should still exist because the finalizer blocks deletion
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return !updated.DeletionTimestamp.IsZero()
			}, timeout, interval).Should(BeTrue())
		})

		It("should be deleted after MCPRemoteProxy reference is removed", func() {
			// Delete the MCPRemoteProxy to remove the reference
			Expect(k8sClient.Delete(ctx, proxy)).Should(Succeed())

			// The MCPOIDCConfig should eventually be fully deleted
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When MCPRemoteProxy removes its OIDCConfigRef (reference removal cleanup)", Ordered, func() {
		var (
			namespace  string
			configName string
			proxyName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			proxy      *mcpv1alpha1.MCPRemoteProxy
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-proxy-oidcref-remove-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			proxyName = testRemoteProxyName

			// Create MCPOIDCConfig
			oidcConfig = &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "test-client",
					},
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

			// Wait for ready
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				if updated.Status.ConfigHash == "" {
					return false
				}
				for _, cond := range updated.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionTypeOIDCConfigReady && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Create MCPRemoteProxy with OIDCConfigRef
			proxy = newTestMCPRemoteProxy(proxyName, namespace, configName)
			Expect(k8sClient.Create(ctx, proxy)).Should(Succeed())

			// Wait for ReferencingWorkloads to contain the proxy
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: proxyName}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref == expectedRef {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Wait for the proxy OIDCConfigHash to be populated
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.OIDCConfigHash != ""
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, proxy)
			_ = k8sClient.Delete(ctx, oidcConfig)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should clean up ReferencingWorkloads and clear OIDCConfigHash after ref removal", func() {
			// Remove the OIDCConfigRef from the MCPRemoteProxy
			updated := &mcpv1alpha1.MCPRemoteProxy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      proxyName,
				Namespace: namespace,
			}, updated)).Should(Succeed())

			updated.Spec.OIDCConfigRef = nil
			Expect(k8sClient.Update(ctx, updated)).Should(Succeed())

			// MCPOIDCConfig should no longer list MCPRemoteProxy in ReferencingWorkloads
			Eventually(func() bool {
				cfg := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, cfg)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: proxyName}
				for _, ref := range cfg.Status.ReferencingWorkloads {
					if ref == expectedRef {
						return false
					}
				}
				return true
			}, timeout, interval).Should(BeTrue())

			// MCPRemoteProxy OIDCConfigHash should be cleared
			Eventually(func() bool {
				proxyUpdated := &mcpv1alpha1.MCPRemoteProxy{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      proxyName,
					Namespace: namespace,
				}, proxyUpdated)
				if err != nil {
					return false
				}
				return proxyUpdated.Status.OIDCConfigHash == ""
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When MCPRemoteProxy is deleted, should clean up ReferencingWorkloads", Ordered, func() {
		var (
			namespace  string
			configName string
			proxyName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			proxy      *mcpv1alpha1.MCPRemoteProxy
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-proxy-oidcref-cleanup-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			proxyName = testRemoteProxyName

			// Create MCPOIDCConfig
			oidcConfig = &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "test-client",
					},
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

			// Wait for ready
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.ConfigHash != ""
			}, timeout, interval).Should(BeTrue())

			// Create MCPRemoteProxy with OIDCConfigRef
			proxy = newTestMCPRemoteProxy(proxyName, namespace, configName)
			Expect(k8sClient.Create(ctx, proxy)).Should(Succeed())

			// Wait for ReferencingWorkloads to contain the proxy
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: proxyName}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref == expectedRef {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, oidcConfig)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should remove MCPRemoteProxy from ReferencingWorkloads after deletion", func() {
			// Delete the MCPRemoteProxy
			Expect(k8sClient.Delete(ctx, proxy)).Should(Succeed())

			// Eventually the referencing workloads list should not contain the proxy
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: proxyName}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref == expectedRef {
						return false
					}
				}
				return true
			}, timeout, interval).Should(BeTrue())
		})
	})
})
