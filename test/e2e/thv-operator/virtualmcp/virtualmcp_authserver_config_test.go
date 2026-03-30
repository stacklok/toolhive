// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer AuthServerConfig Validation", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "auth-server-test-group"
		timeout         = 2 * time.Minute
		pollingInterval = 1 * time.Second
	)

	BeforeAll(func() {
		By("Creating MCPGroup for auth server tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for AuthServerConfig validation", timeout, pollingInterval)
	})

	AfterAll(func() {
		By("Cleaning up MCPGroup")
		_ = k8sClient.Delete(ctx, &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
		})
	})

	Context("when AuthServerConfig is set with valid inline config", func() {
		const vmcpName = "auth-server-valid-vmcp"

		BeforeAll(func() {
			By("Creating VirtualMCPServer with valid inline AuthServerConfig")
			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "oidc",
						OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
							Type: "inline",
							Inline: &mcpv1alpha1.InlineOIDCConfig{
								Issuer: "http://localhost:9090",
							},
						},
					},
					Config: vmcpconfig.Config{Group: mcpGroupName},
					AuthServerConfig: &mcpv1alpha1.EmbeddedAuthServerConfig{
						Issuer: "http://localhost:9090",
						UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
							{
								Name: "test-provider",
								Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
								OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
									IssuerURL: "https://accounts.google.com",
									ClientID:  "test-client-id",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: vmcpName, Namespace: testNamespace},
			})
		})

		It("should set AuthServerConfigValidated condition to True", func() {
			WaitForCondition(ctx, k8sClient, vmcpName, testNamespace,
				mcpv1alpha1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollingInterval)
		})
	})
})
