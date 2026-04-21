// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package mcptoolconfig_test contains integration tests for the MCPToolConfig controller
package mcptoolconfig_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

const (
	timeout             = 30 * time.Second
	interval            = 1 * time.Second
	testConfigName      = "test-config"
	testServerName      = "test-server"
	testImage           = "test-image:latest"
	toolConfigFinalizer = "toolhive.stacklok.dev/toolconfig-finalizer"
)

var _ = Describe("MCPToolConfig Controller Integration Tests", func() {
	Context("When creating a basic MCPToolConfig", Ordered, func() {
		var (
			namespace  string
			configName string
			toolConfig *mcpv1beta1.MCPToolConfig
			ns         *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-toolconfig-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testConfigName

			// Create MCPToolConfig
			toolConfig = &mcpv1beta1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
			}
			Expect(k8sClient.Create(ctx, toolConfig)).Should(Succeed())
		})

		AfterAll(func() {
			Expect(k8sClient.Delete(ctx, toolConfig)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should add finalizer", func() {
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				for _, f := range updated.Finalizers {
					if f == toolConfigFinalizer {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("should set config hash in status", func() {
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.ConfigHash != ""
			}, timeout, interval).Should(BeTrue())
		})

		It("should set ObservedGeneration", func() {
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.ObservedGeneration == updated.Generation
			}, timeout, interval).Should(BeTrue())
		})

		It("should set Valid=True condition", func() {
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				condition := meta.FindStatusCondition(updated.Status.Conditions, "Valid")
				if condition == nil {
					return false
				}
				return condition.Status == metav1.ConditionTrue &&
					condition.Reason == "ValidationSucceeded"
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When updating MCPToolConfig spec", Ordered, func() {
		var (
			namespace   string
			configName  string
			toolConfig  *mcpv1beta1.MCPToolConfig
			ns          *corev1.Namespace
			initialHash string
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-toolconfig-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testConfigName

			// Create MCPToolConfig
			toolConfig = &mcpv1beta1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
			}
			Expect(k8sClient.Create(ctx, toolConfig)).Should(Succeed())

			// Wait for initial hash to be set
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				initialHash = updated.Status.ConfigHash
				return initialHash != ""
			}, timeout, interval).Should(BeTrue())

			// Update the spec to add a third tool
			Eventually(func() error {
				updated := &mcpv1beta1.MCPToolConfig{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated); err != nil {
					return err
				}
				updated.Spec.ToolsFilter = []string{"tool1", "tool2", "tool3"}
				return k8sClient.Update(ctx, updated)
			}, timeout, interval).Should(Succeed())
		})

		AfterAll(func() {
			Expect(k8sClient.Delete(ctx, toolConfig)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should update config hash after spec change", func() {
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.ConfigHash != "" && updated.Status.ConfigHash != initialHash
			}, timeout, interval).Should(BeTrue())
		})

		It("should maintain Valid=True condition after update", func() {
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				condition := meta.FindStatusCondition(updated.Status.Conditions, "Valid")
				if condition == nil {
					return false
				}
				return condition.Status == metav1.ConditionTrue
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When MCPServers reference the MCPToolConfig", Ordered, func() {
		var (
			namespace     string
			configName    string
			toolConfig    *mcpv1beta1.MCPToolConfig
			mcpServerName string
			mcpServer     *mcpv1beta1.MCPServer
			ns            *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-toolconfig-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testConfigName
			mcpServerName = testServerName

			// Create MCPToolConfig
			toolConfig = &mcpv1beta1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
			}
			Expect(k8sClient.Create(ctx, toolConfig)).Should(Succeed())

			// Wait for hash to be set before creating the MCPServer
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.ConfigHash != ""
			}, timeout, interval).Should(BeTrue())

			// Create MCPServer with ToolConfigRef
			mcpServer = &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image: testImage,
					ToolConfigRef: &mcpv1beta1.ToolConfigRef{
						Name: configName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Ignore errors on cleanup since some tests may have already deleted these
			_ = k8sClient.Delete(ctx, mcpServer)
			Expect(k8sClient.Delete(ctx, toolConfig)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("should track referencing workloads in status", func() {
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref.Kind == "MCPServer" && ref.Name == mcpServerName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("should remove server from status when MCPServer is deleted", func() {
			// Delete the MCPServer
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())

			// Eventually the referencing workloads list should be empty
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return len(updated.Status.ReferencingWorkloads) == 0
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When deleting MCPToolConfig with active references", Ordered, func() {
		var (
			namespace     string
			configName    string
			toolConfig    *mcpv1beta1.MCPToolConfig
			mcpServerName string
			mcpServer     *mcpv1beta1.MCPServer
			ns            *corev1.Namespace
		)

		BeforeAll(func() {
			// Create a unique namespace for this test context
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-toolconfig-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
			namespace = ns.Name

			configName = testConfigName
			mcpServerName = testServerName

			// Create MCPToolConfig
			toolConfig = &mcpv1beta1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
			}
			Expect(k8sClient.Create(ctx, toolConfig)).Should(Succeed())

			// Wait for hash to be set
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				return updated.Status.ConfigHash != ""
			}, timeout, interval).Should(BeTrue())

			// Create MCPServer with ToolConfigRef
			mcpServer = &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image: testImage,
					ToolConfigRef: &mcpv1beta1.ToolConfigRef{
						Name: configName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

			// Wait for ReferencingWorkloads to be populated
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				if err != nil {
					return false
				}
				for _, ref := range updated.Status.ReferencingWorkloads {
					if ref.Kind == "MCPServer" && ref.Name == mcpServerName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Attempt to delete the MCPToolConfig (should be blocked by finalizer)
			Expect(k8sClient.Delete(ctx, toolConfig)).Should(Succeed())
		})

		AfterAll(func() {
			// Cleanup: delete the MCPServer first to unblock the finalizer,
			// then wait for the MCPToolConfig to be fully deleted, then delete the namespace.
			_ = k8sClient.Delete(ctx, mcpServer)

			// Wait for MCPToolConfig to be fully removed
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
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
				updated := &mcpv1beta1.MCPToolConfig{}
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

			// The MCPToolConfig should eventually be fully deleted
			Eventually(func() bool {
				updated := &mcpv1beta1.MCPToolConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configName,
					Namespace: namespace,
				}, updated)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})
	})
})
