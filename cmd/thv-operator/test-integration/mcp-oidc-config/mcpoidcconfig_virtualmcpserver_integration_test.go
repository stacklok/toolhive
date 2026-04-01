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
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

const (
	testVMCPServerName = "test-vmcp-server"
	testVMCPGroupName  = "test-vmcp-group"
)

var _ = Describe("MCPOIDCConfig and VirtualMCPServer Cross-Resource Integration Tests", func() {
	Context("When VirtualMCPServer references an MCPOIDCConfig", Ordered, func() {
		var (
			namespace  string
			configName string
			vmcpName   string
			groupName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			vmcpServer *mcpv1alpha1.VirtualMCPServer
			mcpGroup   *mcpv1alpha1.MCPGroup
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-vmcp-oidcref-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			vmcpName = testVMCPServerName
			groupName = testVMCPGroupName

			// Create MCPGroup (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      groupName,
					Namespace: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

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
					if cond.Type == mcpv1alpha1.ConditionTypeOIDCConfigReady && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Create VirtualMCPServer with OIDCConfigRef
			vmcpServer = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: groupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
							Name:     configName,
							Audience: "test-vmcp-audience",
							Scopes:   []string{"openid"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Ignore errors on cleanup since some tests may have already deleted these
			_ = k8sClient.Delete(ctx, vmcpServer)
			_ = k8sClient.Delete(ctx, oidcConfig)
			_ = k8sClient.Delete(ctx, mcpGroup)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should set OIDCConfigRefValidated condition to True", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
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

		It("should set OIDCConfigHash in VirtualMCPServer status", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.OIDCConfigHash != ""
			}, timeout, interval).Should(BeTrue())
		})

		It("should track VirtualMCPServer in MCPOIDCConfig ReferencingWorkloads", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "VirtualMCPServer", Name: vmcpName}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref == expectedRef {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When VirtualMCPServer is deleted, should clean up ReferencingWorkloads", Ordered, func() {
		var (
			namespace  string
			configName string
			vmcpName   string
			groupName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			vmcpServer *mcpv1alpha1.VirtualMCPServer
			mcpGroup   *mcpv1alpha1.MCPGroup
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-vmcp-oidcref-cleanup-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			vmcpName = testVMCPServerName
			groupName = testVMCPGroupName

			// Create MCPGroup (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      groupName,
					Namespace: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

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

			// Create VirtualMCPServer with OIDCConfigRef
			vmcpServer = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: groupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
							Name:     configName,
							Audience: "test-vmcp-audience",
							Scopes:   []string{"openid"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcpServer)).Should(Succeed())

			// Wait for ReferencingWorkloads to contain the VirtualMCPServer
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "VirtualMCPServer", Name: vmcpName}
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
			_ = k8sClient.Delete(ctx, mcpGroup)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should remove VirtualMCPServer from ReferencingWorkloads after deletion", func() {
			// Delete the VirtualMCPServer
			Expect(k8sClient.Delete(ctx, vmcpServer)).Should(Succeed())

			// Eventually the referencing workloads list should not contain the vmcp entry
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "VirtualMCPServer", Name: vmcpName}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref == expectedRef {
						return false
					}
				}
				return true
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When deleting MCPOIDCConfig with active VirtualMCPServer references", Ordered, func() {
		var (
			namespace  string
			configName string
			vmcpName   string
			groupName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			vmcpServer *mcpv1alpha1.VirtualMCPServer
			mcpGroup   *mcpv1alpha1.MCPGroup
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-vmcp-oidcref-delete-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			vmcpName = testVMCPServerName
			groupName = testVMCPGroupName

			// Create MCPGroup (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      groupName,
					Namespace: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

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

			// Create VirtualMCPServer with OIDCConfigRef
			vmcpServer = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: groupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
							Name:     configName,
							Audience: "test-vmcp-audience",
							Scopes:   []string{"openid"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcpServer)).Should(Succeed())

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
				expectedRef := mcpv1alpha1.WorkloadReference{Kind: "VirtualMCPServer", Name: vmcpName}
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
			// Cleanup: delete the VirtualMCPServer first to unblock the finalizer,
			// then wait for the MCPOIDCConfig to be fully deleted, then delete the namespace.
			_ = k8sClient.Delete(ctx, vmcpServer)

			// Wait for MCPOIDCConfig to be fully removed
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())

			_ = k8sClient.Delete(ctx, mcpGroup)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should not be deleted while referenced by VirtualMCPServer", func() {
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

		It("should be deleted after VirtualMCPServer reference is removed", func() {
			// Delete the VirtualMCPServer to remove the reference
			Expect(k8sClient.Delete(ctx, vmcpServer)).Should(Succeed())

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

	Context("When VirtualMCPServer references non-existent MCPOIDCConfig", Ordered, func() {
		var (
			namespace  string
			vmcpName   string
			groupName  string
			vmcpServer *mcpv1alpha1.VirtualMCPServer
			mcpGroup   *mcpv1alpha1.MCPGroup
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-vmcp-oidcref-missing-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			vmcpName = testVMCPServerName
			groupName = testVMCPGroupName

			// Create MCPGroup (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      groupName,
					Namespace: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Create VirtualMCPServer with OIDCConfigRef pointing to a non-existent config
			vmcpServer = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: groupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
							Name:     "does-not-exist",
							Audience: "test-vmcp-audience",
							Scopes:   []string{"openid"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, vmcpServer)
			_ = k8sClient.Delete(ctx, mcpGroup)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should set OIDCConfigRefValidated condition to False with NotFound reason", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
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

	Context("When both MCPServer and VirtualMCPServer reference same MCPOIDCConfig", Ordered, func() {
		var (
			namespace  string
			configName string
			serverName string
			vmcpName   string
			groupName  string
			oidcConfig *mcpv1alpha1.MCPOIDCConfig
			mcpServer  *mcpv1alpha1.MCPServer
			vmcpServer *mcpv1alpha1.VirtualMCPServer
			mcpGroup   *mcpv1alpha1.MCPGroup
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-vmcp-oidcref-both-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testOIDCConfigName
			serverName = testServerName
			vmcpName = testVMCPServerName
			groupName = testVMCPGroupName

			// Create MCPGroup (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      groupName,
					Namespace: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

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
					if cond.Type == mcpv1alpha1.ConditionTypeOIDCConfigReady && cond.Status == metav1.ConditionTrue {
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

			// Create VirtualMCPServer with OIDCConfigRef
			vmcpServer = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: groupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
							Name:     configName,
							Audience: "test-vmcp-audience",
							Scopes:   []string{"openid"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, vmcpServer)
			_ = k8sClient.Delete(ctx, mcpServer)
			_ = k8sClient.Delete(ctx, oidcConfig)
			_ = k8sClient.Delete(ctx, mcpGroup)
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should track both workloads in ReferencingWorkloads", func() {
			Eventually(func() bool {
				updated := &mcpv1alpha1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				mcpServerRef := mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: serverName}
				vmcpServerRef := mcpv1alpha1.WorkloadReference{Kind: "VirtualMCPServer", Name: vmcpName}
				hasMCPServer := false
				hasVMCPServer := false
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref == mcpServerRef {
						hasMCPServer = true
					}
					if ref == vmcpServerRef {
						hasVMCPServer = true
					}
				}
				return hasMCPServer && hasVMCPServer
			}, timeout, interval).Should(BeTrue())
		})
	})
})
