// Package controllers contains integration tests for the MCPExternalAuthConfig controller
package controllers

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPExternalAuthConfig Controller Integration Tests", func() {
	const (
		timeout          = time.Second * 30
		interval         = time.Millisecond * 250
		defaultNamespace = "default"
	)

	Context("When creating an MCPExternalAuthConfig with token exchange", Ordered, func() {
		var (
			namespace       string
			authConfigName  string
			authConfig      *mcpv1alpha1.MCPExternalAuthConfig
			oauthSecret     *corev1.Secret
			oauthSecretName string
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			authConfigName = "test-external-auth"
			oauthSecretName = "oauth-test-secret"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create OAuth secret first
			oauthSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      oauthSecretName,
					Namespace: namespace,
				},
				StringData: map[string]string{
					"client-secret": "test-secret-value",
				},
			}
			Expect(k8sClient.Create(ctx, oauthSecret)).Should(Succeed())

			// Define the MCPExternalAuthConfig resource
			authConfig = &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      authConfigName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "tokenExchange",
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client-id",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: oauthSecretName,
							Key:  "client-secret",
						},
						Audience:                "mcp-backend",
						Scopes:                  []string{"read", "write"},
						ExternalTokenHeaderName: "X-Upstream-Token",
					},
				},
			}

			// Create the MCPExternalAuthConfig
			Expect(k8sClient.Create(ctx, authConfig)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up resources
			Expect(k8sClient.Delete(ctx, authConfig)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, oauthSecret)).Should(Succeed())
		})

		It("Should calculate and set config hash in status", func() {
			// Wait for the status to be updated with the config hash
			Eventually(func() bool {
				updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      authConfigName,
					Namespace: namespace,
				}, updatedAuthConfig)
				if err != nil {
					return false
				}
				// Check if the config hash is set
				return updatedAuthConfig.Status.ConfigHash != ""
			}, timeout, interval).Should(BeTrue())

			// Verify the config hash is not empty
			updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfigName,
				Namespace: namespace,
			}, updatedAuthConfig)).Should(Succeed())

			Expect(updatedAuthConfig.Status.ConfigHash).NotTo(BeEmpty())
			Expect(updatedAuthConfig.Status.ObservedGeneration).To(Equal(updatedAuthConfig.Generation))
		})

		It("Should have a finalizer added", func() {
			updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfigName,
				Namespace: namespace,
			}, updatedAuthConfig)).Should(Succeed())

			Expect(updatedAuthConfig.Finalizers).To(ContainElement("mcpexternalauthconfig.toolhive.stacklok.dev/finalizer"))
		})
	})

	Context("When creating an MCPServer with external auth reference", Ordered, func() {
		var (
			namespace       string
			authConfigName  string
			authConfig      *mcpv1alpha1.MCPExternalAuthConfig
			mcpServerName   string
			mcpServer       *mcpv1alpha1.MCPServer
			oauthSecret     *corev1.Secret
			oauthSecretName string
			configHash      string
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			authConfigName = "test-external-auth-with-server"
			mcpServerName = "external-auth-test-server"
			oauthSecretName = "oauth-test-secret-2"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create OAuth secret
			oauthSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      oauthSecretName,
					Namespace: namespace,
				},
				StringData: map[string]string{
					"client-secret": "test-secret-value-2",
				},
			}
			Expect(k8sClient.Create(ctx, oauthSecret)).Should(Succeed())

			// Create MCPExternalAuthConfig
			authConfig = &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      authConfigName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "tokenExchange",
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client-id-2",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: oauthSecretName,
							Key:  "client-secret",
						},
						Audience: "mcp-backend-2",
						Scopes:   []string{"admin", "user"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, authConfig)).Should(Succeed())

			// Wait for the auth config to have a hash
			Eventually(func() bool {
				updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      authConfigName,
					Namespace: namespace,
				}, updatedAuthConfig)
				if err != nil {
					return false
				}
				configHash = updatedAuthConfig.Status.ConfigHash
				return configHash != ""
			}, timeout, interval).Should(BeTrue())

			// Create MCPServer with external auth reference
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: authConfigName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up resources
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, authConfig)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, oauthSecret)).Should(Succeed())
		})

		It("Should propagate external auth config hash to MCPServer status", func() {
			// Wait for the MCPServer status to be updated with the external auth config hash
			Eventually(func() bool {
				updatedMCPServer := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}
				// Check if the external auth config hash matches
				return updatedMCPServer.Status.ExternalAuthConfigHash == configHash
			}, timeout, interval).Should(BeTrue())
		})

		// It("Should update MCPExternalAuthConfig status with referencing server", func() {
		// 	// Wait for the auth config status to be updated with the referencing server
		// 	Eventually(func() bool {
		// 		updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
		// 		err := k8sClient.Get(ctx, types.NamespacedName{
		// 			Name:      authConfigName,
		// 			Namespace: namespace,
		// 		}, updatedAuthConfig)
		// 		if err != nil {
		// 			return false
		// 		}
		// 		// Check if the server is in the referencing servers list
		// 		for _, server := range updatedAuthConfig.Status.ReferencingServers {
		// 			if server == mcpServerName {
		// 				return true
		// 			}
		// 		}
		// 		return false
		// 	}, timeout, interval).Should(BeTrue())
		// })

		It("Should create ConfigMap with token exchange configuration", func() {
			// Wait for ConfigMap to be created
			configMapName := mcpServerName + "-runconfig"
			Eventually(func() bool {
				configMap := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, configMap)
				return err == nil && configMap.Data["runconfig.json"] != ""
			}, timeout, interval).Should(BeTrue())

			// Get the ConfigMap and verify runconfig content
			configMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: namespace,
			}, configMap)).Should(Succeed())

			// Parse and verify the runconfig.json
			runconfigJSON := configMap.Data["runconfig.json"]
			Expect(runconfigJSON).NotTo(BeEmpty())

			var runconfig map[string]interface{}
			Expect(json.Unmarshal([]byte(runconfigJSON), &runconfig)).Should(Succeed())

			// Verify middleware_configs exists
			middlewareConfigs, ok := runconfig["middleware_configs"].([]interface{})
			Expect(ok).To(BeTrue(), "middleware_configs should be present in runconfig")
			Expect(middlewareConfigs).NotTo(BeEmpty())

			// Find the tokenexchange middleware
			var tokenExchangeConfig map[string]interface{}
			for _, middleware := range middlewareConfigs {
				m := middleware.(map[string]interface{})
				if m["type"] == "tokenexchange" {
					params := m["parameters"].(map[string]interface{})
					tokenExchangeConfig = params["token_exchange_config"].(map[string]interface{})
					break
				}
			}

			Expect(tokenExchangeConfig).NotTo(BeNil(), "tokenexchange middleware should be present")

			// Verify token exchange configuration fields
			Expect(tokenExchangeConfig["token_url"]).To(Equal("https://oauth.example.com/token"))
			Expect(tokenExchangeConfig["client_id"]).To(Equal("test-client-id-2"))
			Expect(tokenExchangeConfig["audience"]).To(Equal("mcp-backend-2"))

			// Verify scopes array
			scopes := tokenExchangeConfig["scopes"].([]interface{})
			Expect(scopes).To(ConsistOf("admin", "user"))

			// Client secret should be empty or not present in the ConfigMap (for security)
			if secret, ok := tokenExchangeConfig["client_secret"]; ok {
				Expect(secret).To(BeEmpty(), "client_secret should be empty in ConfigMap for security")
			}
		})
	})

	Context("When updating an MCPExternalAuthConfig", Ordered, func() {
		var (
			namespace       string
			authConfigName  string
			authConfig      *mcpv1alpha1.MCPExternalAuthConfig
			mcpServerName   string
			mcpServer       *mcpv1alpha1.MCPServer
			oauthSecret     *corev1.Secret
			oauthSecretName string
			originalHash    string
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			authConfigName = "test-external-auth-update"
			mcpServerName = "external-auth-update-server"
			oauthSecretName = "oauth-test-secret-update"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create OAuth secret
			oauthSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      oauthSecretName,
					Namespace: namespace,
				},
				StringData: map[string]string{
					"client-secret": "original-secret",
				},
			}
			Expect(k8sClient.Create(ctx, oauthSecret)).Should(Succeed())

			// Create MCPExternalAuthConfig
			authConfig = &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      authConfigName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "tokenExchange",
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "original-client-id",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: oauthSecretName,
							Key:  "client-secret",
						},
						Audience: "original-audience",
						Scopes:   []string{"read"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, authConfig)).Should(Succeed())

			// Wait for the auth config to have a hash
			Eventually(func() bool {
				updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      authConfigName,
					Namespace: namespace,
				}, updatedAuthConfig)
				if err != nil {
					return false
				}
				originalHash = updatedAuthConfig.Status.ConfigHash
				return originalHash != ""
			}, timeout, interval).Should(BeTrue())

			// Create MCPServer with external auth reference
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: authConfigName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

			// Wait for the MCPServer to have the original hash
			Eventually(func() bool {
				updatedMCPServer := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}
				return updatedMCPServer.Status.ExternalAuthConfigHash == originalHash
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			// Clean up resources
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, authConfig)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, oauthSecret)).Should(Succeed())
		})

		It("Should update config hash when auth config is modified", func() {
			// Update the auth config
			Eventually(func() error {
				updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      authConfigName,
					Namespace: namespace,
				}, updatedAuthConfig); err != nil {
					return err
				}

				// Modify the audience
				updatedAuthConfig.Spec.TokenExchange.Audience = "updated-audience"
				return k8sClient.Update(ctx, updatedAuthConfig)
			}, timeout, interval).Should(Succeed())

			// Wait for the config hash to change
			var newHash string
			Eventually(func() bool {
				updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      authConfigName,
					Namespace: namespace,
				}, updatedAuthConfig)
				if err != nil {
					return false
				}
				newHash = updatedAuthConfig.Status.ConfigHash
				return newHash != "" && newHash != originalHash
			}, timeout, interval).Should(BeTrue())

			// Verify the new hash is different
			Expect(newHash).NotTo(Equal(originalHash))
		})

		It("Should trigger MCPServer reconciliation with updated hash", func() {
			// Wait for the MCPServer to get the updated hash
			Eventually(func() bool {
				updatedMCPServer := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}
				// Check if the hash has been updated
				return updatedMCPServer.Status.ExternalAuthConfigHash != originalHash
			}, timeout, interval).Should(BeTrue())
		})
	})
})
