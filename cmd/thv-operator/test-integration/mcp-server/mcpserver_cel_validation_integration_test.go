// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// newMinimalMCPServer creates a minimal MCPServer with the given name and optional
// OIDCConfigRef and AuthzConfigRef for CEL validation testing.
func newMinimalMCPServer(name string, oidc *mcpv1alpha1.OIDCConfigRef, authz *mcpv1alpha1.AuthzConfigRef) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "example/mcp-server:latest",
			OIDCConfig:  oidc,
			AuthzConfig: authz,
		},
	}
}

var _ = Describe("CEL Validation for OIDCConfigRef and AuthzConfigRef", Label("k8s", "cel", "validation"), func() {
	Context("OIDCConfigRef CEL validation", func() {
		Context("type=configMap", func() {
			It("should reject when configMap field is missing", func() {
				server := newMinimalMCPServer("oidc-cm-missing", &mcpv1alpha1.OIDCConfigRef{
					Type: "configMap",
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("configMap must be set when type is 'configMap'"))
			})

			It("should reject when inline field is also set", func() {
				server := newMinimalMCPServer("oidc-cm-with-inline", &mcpv1alpha1.OIDCConfigRef{
					Type: "configMap",
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "test-cm",
					},
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer: "https://example.com",
					},
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("inline must be set when type is 'inline'"))
			})

			It("should reject when kubernetes field is also set", func() {
				server := newMinimalMCPServer("oidc-cm-with-k8s", &mcpv1alpha1.OIDCConfigRef{
					Type: "configMap",
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "test-cm",
					},
					Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
						ServiceAccount: "test-sa",
					},
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("kubernetes must not be set when type is not 'kubernetes'"))
			})

			It("should accept when only configMap field is set", func() {
				server := newMinimalMCPServer("oidc-cm-valid", &mcpv1alpha1.OIDCConfigRef{
					Type: "configMap",
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "test-cm",
					},
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("type=inline", func() {
			It("should reject when inline field is missing", func() {
				server := newMinimalMCPServer("oidc-inline-missing", &mcpv1alpha1.OIDCConfigRef{
					Type: "inline",
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("inline must be set when type is 'inline'"))
			})

			It("should reject when configMap field is also set", func() {
				server := newMinimalMCPServer("oidc-inline-with-cm", &mcpv1alpha1.OIDCConfigRef{
					Type: "inline",
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer: "https://example.com",
					},
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "test-cm",
					},
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("configMap must be set when type is 'configMap'"))
			})

			It("should accept when only inline field is set", func() {
				server := newMinimalMCPServer("oidc-inline-valid", &mcpv1alpha1.OIDCConfigRef{
					Type: "inline",
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer: "https://example.com",
					},
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("type=kubernetes", func() {
			It("should reject when configMap field is set", func() {
				server := newMinimalMCPServer("oidc-k8s-with-cm", &mcpv1alpha1.OIDCConfigRef{
					Type: "kubernetes",
					ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
						Name: "test-cm",
					},
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("configMap must be set when type is 'configMap'"))
			})

			It("should reject when inline field is set", func() {
				server := newMinimalMCPServer("oidc-k8s-with-inline", &mcpv1alpha1.OIDCConfigRef{
					Type: "kubernetes",
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer: "https://example.com",
					},
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("inline must be set when type is 'inline'"))
			})

			It("should accept when kubernetes field is set", func() {
				server := newMinimalMCPServer("oidc-k8s-valid", &mcpv1alpha1.OIDCConfigRef{
					Type: "kubernetes",
					Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
						ServiceAccount: "test-sa",
					},
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept when kubernetes field is omitted (defaults apply)", func() {
				server := newMinimalMCPServer("oidc-k8s-defaults", &mcpv1alpha1.OIDCConfigRef{
					Type: "kubernetes",
				}, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Context("OIDCConfigRef multi-violation CEL validation", func() {
		It("should report both missing-configMap and extra-inline when type=configMap but only inline is set", func() {
			server := newMinimalMCPServer("oidc-cm-only-inline", &mcpv1alpha1.OIDCConfigRef{
				Type: "configMap",
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer: "https://example.com",
				},
			}, nil)
			err := k8sClient.Create(ctx, server)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(And(
				ContainSubstring("configMap must be set when type is 'configMap'"),
				ContainSubstring("inline must be set when type is 'inline'"),
			))
		})

		It("should report kubernetes-not-allowed violation when type=inline with inline and kubernetes both set", func() {
			server := newMinimalMCPServer("oidc-inline-with-k8s", &mcpv1alpha1.OIDCConfigRef{
				Type: "inline",
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer: "https://example.com",
				},
				Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
					ServiceAccount: "test-sa",
				},
			}, nil)
			err := k8sClient.Create(ctx, server)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("kubernetes must not be set when type is not 'kubernetes'"))
		})
	})

	Context("AuthzConfigRef CEL validation", func() {
		Context("type=configMap", func() {
			It("should reject when configMap field is missing", func() {
				server := newMinimalMCPServer("authz-cm-missing", nil, &mcpv1alpha1.AuthzConfigRef{
					Type: "configMap",
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("configMap must be set when type is 'configMap'"))
			})

			It("should reject when inline field is also set", func() {
				server := newMinimalMCPServer("authz-cm-with-inline", nil, &mcpv1alpha1.AuthzConfigRef{
					Type: "configMap",
					ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
						Name: "test-cm",
					},
					Inline: &mcpv1alpha1.InlineAuthzConfig{
						Policies: []string{"permit(principal, action, resource);"},
					},
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("inline must be set when type is 'inline'"))
			})

			It("should accept when only configMap field is set", func() {
				server := newMinimalMCPServer("authz-cm-valid", nil, &mcpv1alpha1.AuthzConfigRef{
					Type: "configMap",
					ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
						Name: "test-cm",
					},
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("type=inline", func() {
			It("should reject when inline field is missing", func() {
				server := newMinimalMCPServer("authz-inline-missing", nil, &mcpv1alpha1.AuthzConfigRef{
					Type: "inline",
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("inline must be set when type is 'inline'"))
			})

			It("should reject when configMap field is also set", func() {
				server := newMinimalMCPServer("authz-inline-with-cm", nil, &mcpv1alpha1.AuthzConfigRef{
					Type: "inline",
					Inline: &mcpv1alpha1.InlineAuthzConfig{
						Policies: []string{"permit(principal, action, resource);"},
					},
					ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
						Name: "test-cm",
					},
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("configMap must be set when type is 'configMap'"))
			})

			It("should accept when only inline field is set", func() {
				server := newMinimalMCPServer("authz-inline-valid", nil, &mcpv1alpha1.AuthzConfigRef{
					Type: "inline",
					Inline: &mcpv1alpha1.InlineAuthzConfig{
						Policies: []string{"permit(principal, action, resource);"},
					},
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Context("AuthzConfigRef multi-violation CEL validation", func() {
		It("should report both missing-configMap and extra-inline when type=configMap but only inline is set", func() {
			server := newMinimalMCPServer("authz-cm-only-inline", nil, &mcpv1alpha1.AuthzConfigRef{
				Type: "configMap",
				Inline: &mcpv1alpha1.InlineAuthzConfig{
					Policies: []string{"permit(principal, action, resource);"},
				},
			})
			err := k8sClient.Create(ctx, server)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(And(
				ContainSubstring("configMap must be set when type is 'configMap'"),
				ContainSubstring("inline must be set when type is 'inline'"),
			))
		})
	})

	Context("OIDCConfig and OIDCConfigRef mutual exclusion", func() {
		It("should reject when both oidcConfig and oidcConfigRef are set", func() {
			server := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oidc-mutual-exclusion",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "example/mcp-server:latest",
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: "kubernetes",
					},
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     "some-config",
						Audience: "test",
					},
				},
			}
			err := k8sClient.Create(ctx, server)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("oidcConfig and oidcConfigRef are mutually exclusive"))
		})

		It("should accept when only oidcConfigRef is set", func() {
			server := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oidc-ref-only",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "example/mcp-server:latest",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     "some-config",
						Audience: "test",
					},
				},
			}
			err := k8sClient.Create(ctx, server)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
