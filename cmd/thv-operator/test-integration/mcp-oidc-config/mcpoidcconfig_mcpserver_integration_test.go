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
	testOIDCConfigName = "test-oidc-config"
	testServerName     = "test-server"
	testServerImage    = "test-image:latest"
)

var _ = Describe("MCPOIDCConfig and MCPServer Cross-Resource Integration Tests", func() {
	Context("When MCPServer references an MCPOIDCConfig", Ordered, func() {
		var (
			namespace  string
			configName string
			serverName string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			mcpServer  *mcpv1alpha1.MCPServer
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-oidcref-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			serverName = testServerName

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

			// Wait for Valid condition and ConfigHash to be set
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
					if cond.Type == conditionTypeValid && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Create MCPServer with OIDCConfigRef
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: testServerImage,
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     configName,
						Audience: "test-audience",
						Scopes:   []string{"openid"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Ignore errors on cleanup since some tests may have already deleted these
			_ = k8sClient.Delete(ctx, mcpServer)
			_ = k8sClient.Delete(ctx, oidcConfig)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should set OIDCConfigRefValidated condition to True", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      serverName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionOIDCConfigRefValidated)
				if condition == nil {
					return false
				}
				return condition.Status == metav1.ConditionTrue
			}, timeout, interval).Should(BeTrue())
		})

		It("should set OIDCConfigHash in MCPServer status", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      serverName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.OIDCConfigHash != ""
			}, timeout, interval).Should(BeTrue())
		})

		It("should track MCPServer in MCPOIDCConfig ReferencingServers", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				for _, server := range updated.Status.ReferencingServers {
					if server == serverName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When MCPServer is deleted, should clean up ReferencingServers", Ordered, func() {
		var (
			namespace  string
			configName string
			serverName string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			mcpServer  *mcpv1alpha1.MCPServer
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-oidcref-cleanup-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			serverName = testServerName

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

			// Create MCPServer with OIDCConfigRef
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: testServerImage,
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     configName,
						Audience: "test-audience",
						Scopes:   []string{"openid"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

			// Wait for ReferencingServers to contain the server
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				for _, server := range updated.Status.ReferencingServers {
					if server == serverName {
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

		It("should remove server from ReferencingServers after MCPServer deletion", func() {
			// Delete the MCPServer
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())

			// Eventually the referencing servers list should be empty
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return len(updated.Status.ReferencingServers) == 0
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When deleting MCPOIDCConfig with active references", Ordered, func() {
		var (
			namespace  string
			configName string
			serverName string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			mcpServer  *mcpv1alpha1.MCPServer
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-oidcref-delete-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			serverName = testServerName

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

			// Create MCPServer with OIDCConfigRef
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: testServerImage,
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     configName,
						Audience: "test-audience",
						Scopes:   []string{"openid"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

			// Wait for ReferencingServers to be populated
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				for _, server := range updated.Status.ReferencingServers {
					if server == serverName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Attempt to delete the MCPOIDCConfig (should be blocked by finalizer)
			Expect(k8sClient.Delete(ctx, oidcConfig)).Should(Succeed())
		})

		AfterAll(func() {
			// Cleanup: delete the MCPServer first to unblock the finalizer,
			// then wait for the MCPOIDCConfig to be fully deleted, then delete the namespace.
			_ = k8sClient.Delete(ctx, mcpServer)

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

		It("should not be deleted while referenced", func() {
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

		It("should be deleted after references are removed", func() {
			// Delete the MCPServer to remove the reference
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())

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

	Context("When MCPServer references non-existent MCPOIDCConfig", Ordered, func() {
		var (
			namespace  string
			serverName string
			mcpServer  *mcpv1alpha1.MCPServer
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-oidcref-missing-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			serverName = testServerName

			// Create MCPServer with OIDCConfigRef pointing to a non-existent config
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: testServerImage,
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     "does-not-exist",
						Audience: "test-audience",
						Scopes:   []string{"openid"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, mcpServer)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should set OIDCConfigRefValidated condition to False with NotFound reason", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      serverName,
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
})
