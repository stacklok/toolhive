// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

var _ = Describe("MCPServer AuthServerRef Integration Tests", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When creating an MCPServer with authServerRef pointing to embeddedAuthServer", Ordered, func() {
		var (
			namespace      = "authserverref-mcpserver-happy"
			serverName     = "test-authref-happy"
			configMapName  = serverName + "-runconfig"
			authConfigName = "test-embedded-auth"
			oidcConfigName = "test-oidc-config"
		)

		BeforeAll(func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
			_ = k8sClient.Create(ctx, ns)

			By("creating MCPOIDCConfig")
			oidcConfig := &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: namespace},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer: "http://localhost:9090",
					},
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).To(Succeed())

			By("creating MCPExternalAuthConfig with embeddedAuthServer type")
			authConfig := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: namespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
						Issuer: "http://localhost:9090",
						UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
							{
								Name: "test-provider",
								Type: mcpv1beta1.UpstreamProviderTypeOIDC,
								OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
									IssuerURL: "https://accounts.google.com",
									ClientID:  "test-client-id",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, authConfig)).To(Succeed())

			By("creating MCPServer with authServerRef")
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:v1.0.0",
					Transport: "streamable-http",
					AuthServerRef: &mcpv1beta1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: authConfigName,
					},
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
						Name:        oidcConfigName,
						Audience:    "https://test-resource.example.com",
						ResourceURL: "https://test-resource.example.com",
					},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: namespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: namespace},
			})
		})

		It("should set AuthServerRefValidated condition to True", func() {
			Eventually(func() metav1.ConditionStatus {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: serverName, Namespace: namespace,
				}, server); err != nil {
					return metav1.ConditionUnknown
				}
				cond := meta.FindStatusCondition(server.Status.Conditions,
					mcpv1beta1.ConditionTypeAuthServerRefValidated)
				if cond == nil {
					return metav1.ConditionUnknown
				}
				return cond.Status
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))
		})

		It("should have embedded_auth_server_config in the runconfig ConfigMap", func() {
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: configMapName, Namespace: namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			Expect(configMap.Data).To(HaveKey("runconfig.json"))

			var runConfig map[string]interface{}
			Expect(json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)).To(Succeed())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))
		})
	})

	Context("When creating an MCPServer with conflicting authServerRef and externalAuthConfigRef", Ordered, func() {
		var (
			namespace          = "authserverref-mcpserver-conflict"
			serverName         = "test-authref-conflict"
			authConfigName     = "conflict-embedded-auth"
			authConfigConflict = "conflict-embedded-auth-2"
			oidcConfigName     = "conflict-oidc-config"
		)

		BeforeAll(func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
			_ = k8sClient.Create(ctx, ns)

			By("creating MCPOIDCConfig")
			oidcConfig := &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: namespace},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer: "http://localhost:9090",
					},
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).To(Succeed())

			By("creating two MCPExternalAuthConfig resources with embeddedAuthServer type")
			for _, name := range []string{authConfigName, authConfigConflict} {
				authConfig := &mcpv1beta1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
						Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
							Issuer: "http://localhost:9090",
							UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
								{
									Name: "test-provider",
									Type: mcpv1beta1.UpstreamProviderTypeOIDC,
									OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
										IssuerURL: "https://accounts.google.com",
										ClientID:  "test-client-id",
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, authConfig)).To(Succeed())
			}

			By("creating MCPServer with both authServerRef and externalAuthConfigRef pointing to embeddedAuthServer")
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:v1.0.0",
					Transport: "streamable-http",
					AuthServerRef: &mcpv1beta1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: authConfigName,
					},
					ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
						Name: authConfigConflict,
					},
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
						Name:        oidcConfigName,
						Audience:    "https://test-resource.example.com",
						ResourceURL: "https://test-resource.example.com",
					},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
			})
			for _, name := range []string{authConfigName, authConfigConflict} {
				_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				})
			}
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: namespace},
			})
		})

		It("should reach Failed phase", func() {
			Eventually(func() mcpv1beta1.MCPServerPhase {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: serverName, Namespace: namespace,
				}, server); err != nil {
					return ""
				}
				return server.Status.Phase
			}, timeout, interval).Should(Equal(mcpv1beta1.MCPServerPhaseFailed))
		})

		It("should report conflict error in Status.Message", func() {
			Eventually(func() string {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: serverName, Namespace: namespace,
				}, server); err != nil {
					return ""
				}
				return server.Status.Message
			}, timeout, interval).Should(ContainSubstring(
				"both authServerRef and externalAuthConfigRef reference an embedded auth server"))
		})
	})

	Context("When creating an MCPServer with authServerRef pointing to non-embeddedAuthServer type", Ordered, func() {
		var (
			namespace      = "authserverref-mcpserver-typemismatch"
			serverName     = "test-authref-typemismatch"
			authConfigName = "test-unauth-config"
		)

		BeforeAll(func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
			_ = k8sClient.Create(ctx, ns)

			By("creating MCPExternalAuthConfig with unauthenticated type")
			authConfig := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: namespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
				},
			}
			Expect(k8sClient.Create(ctx, authConfig)).To(Succeed())

			By("creating MCPServer with authServerRef to unauthenticated config")
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:v1.0.0",
					Transport: "streamable-http",
					AuthServerRef: &mcpv1beta1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: authConfigName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: namespace},
			})
		})

		It("should reach Failed phase", func() {
			Eventually(func() mcpv1beta1.MCPServerPhase {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: serverName, Namespace: namespace,
				}, server); err != nil {
					return ""
				}
				return server.Status.Phase
			}, timeout, interval).Should(Equal(mcpv1beta1.MCPServerPhaseFailed))
		})

		It("should set AuthServerRefValidated condition to False with type mismatch message", func() {
			Eventually(func() string {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: serverName, Namespace: namespace,
				}, server); err != nil {
					return ""
				}
				cond := meta.FindStatusCondition(server.Status.Conditions,
					mcpv1beta1.ConditionTypeAuthServerRefValidated)
				if cond == nil || cond.Status != metav1.ConditionFalse {
					return ""
				}
				return cond.Message
			}, timeout, interval).Should(ContainSubstring("only embeddedAuthServer is supported"))
		})
	})

	Context("When creating an MCPServer with legacy externalAuthConfigRef only (backward compatibility)", Ordered, func() {
		var (
			namespace      = "authserverref-mcpserver-legacy"
			serverName     = "test-legacy-extauth"
			configMapName  = serverName + "-runconfig"
			authConfigName = "legacy-embedded-auth"
			oidcConfigName = "legacy-oidc-config"
		)

		BeforeAll(func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
			_ = k8sClient.Create(ctx, ns)

			By("creating MCPOIDCConfig")
			oidcConfig := &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: namespace},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer: "http://localhost:9090",
					},
				},
			}
			Expect(k8sClient.Create(ctx, oidcConfig)).To(Succeed())

			By("creating MCPExternalAuthConfig with embeddedAuthServer type")
			authConfig := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: namespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
						Issuer: "http://localhost:9090",
						UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
							{
								Name: "test-provider",
								Type: mcpv1beta1.UpstreamProviderTypeOIDC,
								OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
									IssuerURL: "https://accounts.google.com",
									ClientID:  "test-client-id",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, authConfig)).To(Succeed())

			By("creating MCPServer with only externalAuthConfigRef (no authServerRef)")
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:v1.0.0",
					Transport: "streamable-http",
					ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
						Name: authConfigName,
					},
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
						Name:        oidcConfigName,
						Audience:    "https://test-resource.example.com",
						ResourceURL: "https://test-resource.example.com",
					},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: namespace},
			})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: namespace},
			})
		})

		It("should have embedded_auth_server_config in the runconfig ConfigMap", func() {
			configMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: configMapName, Namespace: namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			Expect(configMap.Data).To(HaveKey("runconfig.json"))

			var runConfig map[string]interface{}
			Expect(json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)).To(Succeed())
			Expect(runConfig).To(HaveKey("embedded_auth_server_config"))
		})

		It("should not be in Failed phase", func() {
			// The prior It already synchronized on ConfigMap creation,
			// so reconciliation has completed. A point-in-time check suffices.
			server := &mcpv1beta1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: serverName, Namespace: namespace,
			}, server)).To(Succeed())
			Expect(server.Status.Phase).NotTo(Equal(mcpv1beta1.MCPServerPhaseFailed))
		})
	})
})
