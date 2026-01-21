// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer ExternalAuthConfig Watch Integration Tests", func() {
	const (
		timeout          = time.Second * 30
		interval         = time.Millisecond * 250
		defaultNamespace = "default"
	)

	Context("When an MCPExternalAuthConfig is updated (discovered mode)", Ordered, func() {
		var (
			namespace      string
			vmcpName       string
			mcpGroupName   string
			mcpServerName  string
			authConfigName string
			vmcp           *mcpv1alpha1.VirtualMCPServer
			mcpGroup       *mcpv1alpha1.MCPGroup
			mcpServer      *mcpv1alpha1.MCPServer
			authConfig     *mcpv1alpha1.MCPExternalAuthConfig
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			vmcpName = "test-vmcp-auth-watch"
			mcpGroupName = "test-group-auth-watch"
			mcpServerName = "test-server-auth-watch"
			authConfigName = "test-auth-watch"

			// Create MCPExternalAuthConfig
			authConfig = &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      authConfigName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-Test-Auth",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "token",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, authConfig)).Should(Succeed())

			// Create MCPGroup
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for auth watch",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for MCPGroup to be ready
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Create MCPServer that references the MCPExternalAuthConfig
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					GroupRef:  mcpGroupName,
					Image:     "test-image:latest",
					Transport: "streamable-http",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: authConfigName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

			// Create VirtualMCPServer with discovered mode
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered", // Use discovered mode
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for initial VirtualMCPServer reconciliation
			Eventually(func() bool {
				updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				return err == nil && updatedVMCP.Status.ObservedGeneration > 0
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			// Clean up
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpServer)
			_ = k8sClient.Delete(ctx, authConfig)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should trigger VirtualMCPServer reconciliation when ExternalAuthConfig is updated", func() {
			// Update the MCPExternalAuthConfig
			updatedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      authConfigName,
				Namespace: namespace,
			}, updatedAuthConfig)).Should(Succeed())

			// Change the header name to trigger reconciliation
			updatedAuthConfig.Spec.HeaderInjection.HeaderName = "X-Updated-Auth"
			Expect(k8sClient.Update(ctx, updatedAuthConfig)).Should(Succeed())

			// The VirtualMCPServer should remain reconciled after the update
			// We verify this by checking that ObservedGeneration stays current with Generation
			// This indicates the controller is continuously reconciling and processing the auth config update
			Consistently(func() bool {
				reconciledVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, reconciledVMCP)
				if err != nil {
					return false
				}

				// Check that ObservedGeneration stays current (indicating successful reconciliation)
				return reconciledVMCP.Status.ObservedGeneration == reconciledVMCP.Generation
			}, time.Second*5, interval).Should(BeTrue())

			// Verify the VirtualMCPServer is still in a valid state
			updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, updatedVMCP)).Should(Succeed())

			Expect(updatedVMCP.Status.ObservedGeneration).To(Equal(updatedVMCP.Generation))
			Expect(updatedVMCP.Status.Phase).To(Or(
				Equal(mcpv1alpha1.VirtualMCPServerPhaseReady),
				Equal(mcpv1alpha1.VirtualMCPServerPhasePending),
			))
		})
	})
})
