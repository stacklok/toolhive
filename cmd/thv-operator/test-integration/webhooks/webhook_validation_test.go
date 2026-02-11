// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhooks

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("Webhook Validation Integration Tests", func() {
	Context("VirtualMCPServer Webhook", func() {
		It("should reject VirtualMCPServer without required groupRef", func() {
			invalidVMCP := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: config.Config{
						Group: "", // Missing required field
					},
				},
			}

			err := k8sClient.Create(ctx, invalidVMCP)
			Expect(err).To(HaveOccurred(), "webhook should reject resource without groupRef")
			Expect(err.Error()).To(ContainSubstring("spec.config.groupRef is required"))
		})

		It("should accept valid VirtualMCPServer", func() {
			validVMCP := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
					},
				},
			}

			err := k8sClient.Create(ctx, validVMCP)
			Expect(err).NotTo(HaveOccurred(), "webhook should accept valid resource")

			// Clean up
			err = k8sClient.Delete(ctx, validVMCP)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject VirtualMCPServer with invalid backend auth config", func() {
			invalidVMCP := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-backend-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
					},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							"test-backend": {
								Type: "invalid-type",
							},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, invalidVMCP)
			Expect(err).To(HaveOccurred(), "webhook should reject invalid backend auth type")
			Expect(err.Error()).To(ContainSubstring("spec.outgoingAuth.backends[test-backend].type must be one of"))
		})

		It("should reject VirtualMCPServer with invalid aggregation strategy", func() {
			invalidVMCP := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-aggregation",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
						Aggregation: &config.AggregationConfig{
							ConflictResolution: "invalid-strategy",
						},
					},
				},
			}

			err := k8sClient.Create(ctx, invalidVMCP)
			Expect(err).To(HaveOccurred(), "webhook should reject invalid conflict resolution strategy")
			Expect(err.Error()).To(ContainSubstring("config.aggregation.conflictResolution must be one of"))
		})
	})

	Context("MCPExternalAuthConfig Webhook", func() {
		It("should reject MCPExternalAuthConfig with mismatched type and config", func() {
			invalidAuth := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					// Missing TokenExchange config
				},
			}

			err := k8sClient.Create(ctx, invalidAuth)
			Expect(err).To(HaveOccurred(), "webhook should reject mismatched type and config")
			Expect(err.Error()).To(ContainSubstring("tokenExchange configuration must be set"))
		})

		It("should accept valid MCPExternalAuthConfig with tokenExchange", func() {
			validAuth := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-tokenexchange",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
				},
			}

			err := k8sClient.Create(ctx, validAuth)
			Expect(err).NotTo(HaveOccurred(), "webhook should accept valid tokenExchange config")

			// Clean up
			err = k8sClient.Delete(ctx, validAuth)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should accept valid MCPExternalAuthConfig with bearerToken", func() {
			validAuth := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-bearertoken",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeBearerToken,
					BearerToken: &mcpv1alpha1.BearerTokenConfig{
						TokenSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "bearer-secret",
							Key:  "token",
						},
					},
				},
			}

			err := k8sClient.Create(ctx, validAuth)
			Expect(err).NotTo(HaveOccurred(), "webhook should accept valid bearerToken config")

			// Clean up
			err = k8sClient.Delete(ctx, validAuth)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject MCPExternalAuthConfig with multiple configs", func() {
			invalidAuth := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-multiple-configs",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
					},
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "Authorization",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
				},
			}

			err := k8sClient.Create(ctx, invalidAuth)
			Expect(err).To(HaveOccurred(), "webhook should reject multiple auth configs")
			Expect(err.Error()).To(ContainSubstring("headerInjection configuration must be set if and only if"))
		})

		It("should reject embeddedAuthServer with invalid upstream provider", func() {
			invalidAuth := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-upstream",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "signing-key", Key: "private-key"},
						},
						HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
							{
								Name: "missing-config",
								Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
								// Missing OIDCConfig
							},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, invalidAuth)
			Expect(err).To(HaveOccurred(), "webhook should reject invalid upstream provider")
			Expect(err.Error()).To(ContainSubstring("oidcConfig must be set when type is 'oidc'"))
		})
	})

	Context("VirtualMCPCompositeToolDefinition Webhook", func() {
		It("should reject composite tool without required fields", func() {
			invalidTool := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-tool",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						// Missing required name
						Description: "Test tool",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Tool: "backend.tool1",
							},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, invalidTool)
			Expect(err).To(HaveOccurred(), "webhook should reject tool without name")
			Expect(err.Error()).To(ContainSubstring("name is required"))
		})

		It("should accept valid composite tool definition", func() {
			validTool := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-tool",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "test-tool",
						Description: "Test composite tool",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool: "backend.tool1",
							},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, validTool)
			Expect(err).NotTo(HaveOccurred(), "webhook should accept valid composite tool")

			// Clean up
			err = k8sClient.Delete(ctx, validTool)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject composite tool with invalid step configuration", func() {
			invalidTool := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-step",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "test-tool",
						Description: "Test tool",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: mcpv1alpha1.WorkflowStepTypeToolCall,
								// Missing Tool field for tool type
							},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, invalidTool)
			Expect(err).To(HaveOccurred(), "webhook should reject invalid step configuration")
			Expect(err.Error()).To(ContainSubstring("tool is required when type is tool"))
		})

		It("should reject composite tool with duplicate step IDs", func() {
			invalidTool := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-steps",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "test-tool",
						Description: "Test tool",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool: "backend.tool1",
							},
							{
								ID:   "step1", // Duplicate ID
								Type: mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool: "backend.tool2",
							},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, invalidTool)
			Expect(err).To(HaveOccurred(), "webhook should reject duplicate step IDs")
			Expect(err.Error()).To(ContainSubstring("step1"))
			Expect(err.Error()).To(ContainSubstring("duplicated"))
		})

		It("should reject composite tool with invalid error handling", func() {
			invalidTool := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-error-handling",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: config.CompositeToolConfig{
						Name:        "test-tool",
						Description: "Test tool",
						Steps: []config.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool: "backend.tool1",
								OnError: &config.StepErrorHandling{
									Action: "invalid-action",
								},
							},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, invalidTool)
			Expect(err).To(HaveOccurred(), "webhook should reject invalid error handling")
			Expect(err.Error()).To(ContainSubstring("onError.action must be one of"))
		})
	})

	Context("Webhook Update Operations", func() {
		It("should reject invalid updates to VirtualMCPServer", func() {
			// First create a valid resource
			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "update-test",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: config.Config{
						Group: "test-group",
					},
				},
			}

			err := k8sClient.Create(ctx, vmcp)
			Expect(err).NotTo(HaveOccurred())

			// Try to update with invalid configuration
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "update-test", Namespace: "default"}, vmcp)
			Expect(err).NotTo(HaveOccurred())

			vmcp.Spec.Config.Group = "" // Make it invalid
			err = k8sClient.Update(ctx, vmcp)
			Expect(err).To(HaveOccurred(), "webhook should reject invalid update")
			Expect(err.Error()).To(ContainSubstring("spec.config.groupRef is required"))

			// Clean up
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "update-test", Namespace: "default"}, vmcp)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Delete(ctx, vmcp)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should allow valid updates to MCPExternalAuthConfig", func() {
			// First create a valid resource
			auth := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "update-auth-test",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "secret1",
							Key:  "key1",
						},
					},
				},
			}

			err := k8sClient.Create(ctx, auth)
			Expect(err).NotTo(HaveOccurred())

			// Update with valid changes
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "update-auth-test", Namespace: "default"}, auth)
			Expect(err).NotTo(HaveOccurred())

			auth.Spec.HeaderInjection.ValueSecretRef.Name = "secret2"
			err = k8sClient.Update(ctx, auth)
			Expect(err).NotTo(HaveOccurred(), "webhook should accept valid update")

			// Clean up
			err = k8sClient.Delete(ctx, auth)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
