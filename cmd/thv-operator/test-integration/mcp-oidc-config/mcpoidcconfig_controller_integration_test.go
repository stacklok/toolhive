// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	timeout  = time.Second * 30
	interval = time.Millisecond * 250
)

var _ = Describe("MCPOIDCConfig Controller", func() {
	It("should set Ready condition and config hash on creation", func() {
		oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-oidc-creation",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
					Issuer:   "https://accounts.google.com",
					ClientID: "test-client",
				},
			},
		}

		Expect(k8sClient.Create(ctx, oidcConfig)).To(Succeed())

		// Verify config hash is set
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfig.Name,
				Namespace: oidcConfig.Namespace,
			}, fetched)
			if err != nil {
				return false
			}
			return fetched.Status.ConfigHash != ""
		}, timeout, interval).Should(BeTrue())

		// Verify Ready condition is set to True
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfig.Name,
				Namespace: oidcConfig.Namespace,
			}, fetched)
			if err != nil {
				return false
			}
			for _, cond := range fetched.Status.Conditions {
				if cond.Type == mcpv1alpha1.ConditionTypeOIDCConfigValid && cond.Status == metav1.ConditionTrue {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	})

	It("should update config hash when spec changes", func() {
		oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-oidc-hash-change",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
					Issuer:   "https://accounts.google.com",
					ClientID: "original-client",
				},
			},
		}

		Expect(k8sClient.Create(ctx, oidcConfig)).To(Succeed())

		// Wait for initial hash
		var firstHash string
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfig.Name,
				Namespace: oidcConfig.Namespace,
			}, fetched)
			if err != nil || fetched.Status.ConfigHash == "" {
				return false
			}
			firstHash = fetched.Status.ConfigHash
			return true
		}, timeout, interval).Should(BeTrue())

		// Update the spec
		fetched := &mcpv1alpha1.MCPOIDCConfig{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
		}, fetched)).To(Succeed())

		fetched.Spec.Inline.ClientID = "updated-client"
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())

		// Verify hash changed
		Eventually(func() bool {
			updated := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfig.Name,
				Namespace: oidcConfig.Namespace,
			}, updated)
			if err != nil {
				return false
			}
			return updated.Status.ConfigHash != "" && updated.Status.ConfigHash != firstHash
		}, timeout, interval).Should(BeTrue())
	})

	It("should allow deletion by removing finalizer", func() {
		oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-oidc-deletion",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
				KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
					Issuer: "https://kubernetes.default.svc",
				},
			},
		}

		Expect(k8sClient.Create(ctx, oidcConfig)).To(Succeed())

		// Wait for finalizer to be added
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfig.Name,
				Namespace: oidcConfig.Namespace,
			}, fetched)
			if err != nil {
				return false
			}
			for _, f := range fetched.Finalizers {
				if f == "mcpoidcconfig.toolhive.stacklok.dev/finalizer" {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())

		// Delete the config
		Expect(k8sClient.Delete(ctx, oidcConfig)).To(Succeed())

		// Verify it's actually deleted (finalizer removed, object gone)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfig.Name,
				Namespace: oidcConfig.Namespace,
			}, fetched)
			return err != nil // Should be NotFound
		}, timeout, interval).Should(BeTrue())
	})
})
