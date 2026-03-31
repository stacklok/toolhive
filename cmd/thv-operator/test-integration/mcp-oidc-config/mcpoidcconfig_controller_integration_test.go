// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the MCPOIDCConfig controller
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
		oidcConfigName := "test-oidc-config-ready"

		oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcConfigName,
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
		Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

		// Verify configHash is set
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			return fetched.Status.ConfigHash != ""
		}, timeout, interval).Should(BeTrue())

		// Verify a "Valid" condition exists with Status=True
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			cond := meta.FindStatusCondition(fetched.Status.Conditions, "Valid")
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, timeout, interval).Should(BeTrue())
	})

	It("should track referencing servers", func() {
		oidcConfigName := "test-oidc-config-ref"
		mcpServerName := "test-server-ref"

		// Create the MCPOIDCConfig
		oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcConfigName,
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
					Issuer:   "https://accounts.google.com",
					ClientID: "ref-client",
				},
			},
		}
		Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

		// Wait for the OIDC config to be reconciled (hash set)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			return fetched.Status.ConfigHash != ""
		}, timeout, interval).Should(BeTrue())

		// Create an MCPServer that references the OIDC config
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerName,
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "ghcr.io/example/test:latest",
				OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
					Name:     oidcConfigName,
					Audience: "test-audience",
				},
			},
		}
		Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

		// Verify the OIDC config status tracks the referencing server
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			for _, name := range fetched.Status.ReferencingServers {
				if name == mcpServerName {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	})

	It("should cascade config changes via annotation", func() {
		oidcConfigName := "test-oidc-config-cascade"
		mcpServerName := "test-server-cascade"

		// Create the MCPOIDCConfig
		oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcConfigName,
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
					Issuer:   "https://accounts.google.com",
					ClientID: "cascade-client",
				},
			},
		}
		Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

		// Wait for configHash to be set
		var originalHash string
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			originalHash = fetched.Status.ConfigHash
			return originalHash != ""
		}, timeout, interval).Should(BeTrue())

		// Create an MCPServer that references the OIDC config
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerName,
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "ghcr.io/example/test:latest",
				OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
					Name:     oidcConfigName,
					Audience: "cascade-audience",
				},
			},
		}
		Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

		// Wait for the MCPServer to appear in referencing servers
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			for _, name := range fetched.Status.ReferencingServers {
				if name == mcpServerName {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())

		// Update the MCPOIDCConfig spec (change issuer to trigger hash change)
		Eventually(func() error {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched); err != nil {
				return err
			}
			fetched.Spec.Inline.Issuer = "https://login.microsoftonline.com/common/v2.0"
			return k8sClient.Update(ctx, fetched)
		}, timeout, interval).Should(Succeed())

		// Verify the MCPServer gets the oidcconfig-hash annotation
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			hashAnnotation, ok := fetched.Annotations["toolhive.stacklok.dev/oidcconfig-hash"]
			return ok && hashAnnotation != "" && hashAnnotation != originalHash
		}, timeout, interval).Should(BeTrue())
	})

	It("should block deletion while referenced", func() {
		oidcConfigName := "test-oidc-config-block-del"
		mcpServerName := "test-server-block-del"

		// Create the MCPOIDCConfig
		oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcConfigName,
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
					Issuer:   "https://accounts.google.com",
					ClientID: "block-del-client",
				},
			},
		}
		Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

		// Wait for reconciliation (hash set)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			return fetched.Status.ConfigHash != ""
		}, timeout, interval).Should(BeTrue())

		// Create an MCPServer that references the OIDC config
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerName,
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "ghcr.io/example/test:latest",
				OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
					Name:     oidcConfigName,
					Audience: "block-del-audience",
				},
			},
		}
		Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

		// Wait for referencing servers to be populated
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			for _, name := range fetched.Status.ReferencingServers {
				if name == mcpServerName {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())

		// Attempt to delete the MCPOIDCConfig
		toDelete := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcConfigName,
				Namespace: "default",
			},
		}
		Expect(k8sClient.Delete(ctx, toDelete)).Should(Succeed())

		// Verify the MCPOIDCConfig still exists (finalizer blocks actual deletion)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			return !fetched.DeletionTimestamp.IsZero()
		}, timeout, interval).Should(BeTrue())

		// Verify "DeletionBlocked" condition is set
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			cond := meta.FindStatusCondition(fetched.Status.Conditions, "DeletionBlocked")
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, timeout, interval).Should(BeTrue())

		// Clean up: delete the MCPServer so the OIDC config can be garbage collected
		Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())

		// Wait for the MCPOIDCConfig to be fully deleted
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue())
	})

	It("should allow deletion after references removed", func() {
		oidcConfigName := "test-oidc-config-allow-del"
		mcpServerName := "test-server-allow-del"

		// Create the MCPOIDCConfig
		oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcConfigName,
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
					Issuer:   "https://accounts.google.com",
					ClientID: "allow-del-client",
				},
			},
		}
		Expect(k8sClient.Create(ctx, oidcConfig)).Should(Succeed())

		// Wait for reconciliation (hash set)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			return fetched.Status.ConfigHash != ""
		}, timeout, interval).Should(BeTrue())

		// Create an MCPServer that references the OIDC config
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerName,
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "ghcr.io/example/test:latest",
				OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
					Name:     oidcConfigName,
					Audience: "allow-del-audience",
				},
			},
		}
		Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

		// Wait for referencing servers to be populated
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			for _, name := range fetched.Status.ReferencingServers {
				if name == mcpServerName {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())

		// Delete the MCPServer first to remove the reference
		Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())

		// Wait for referencing servers to become empty
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			if err != nil {
				return false
			}
			return len(fetched.Status.ReferencingServers) == 0
		}, timeout, interval).Should(BeTrue())

		// Now delete the MCPOIDCConfig
		toDelete := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oidcConfigName,
				Namespace: "default",
			},
		}
		Expect(k8sClient.Delete(ctx, toDelete)).Should(Succeed())

		// Verify the MCPOIDCConfig is actually deleted (NotFound)
		Eventually(func() bool {
			fetched := &mcpv1alpha1.MCPOIDCConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      oidcConfigName,
				Namespace: "default",
			}, fetched)
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue())
	})
})
