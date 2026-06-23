// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// newVirtualMCPServerWithIncomingAuth builds a minimal VirtualMCPServer whose
// IncomingAuth carries the supplied authzConfig / authzConfigRef. The pair is
// the subject of the CEL XValidation rule under test.
func newVirtualMCPServerWithIncomingAuth(
	name string,
	authzConfig *mcpv1beta1.AuthzConfigRef,
	authzConfigRef *mcpv1beta1.MCPAuthzConfigReference,
) *mcpv1beta1.VirtualMCPServer {
	return v1beta1test.NewVirtualMCPServer(name, "default",
		v1beta1test.WithVMCPGroupRef("test-group"),
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type:           "anonymous",
			AuthzConfig:    authzConfig,
			AuthzConfigRef: authzConfigRef,
		}),
		v1beta1test.WithVMCPConfig(vmcpconfig.Config{
			Group: "test-group",
		}),
	)
}

var _ = Describe("CEL Validation for authzConfig vs authzConfigRef on VirtualMCPServer",
	Label("k8s", "cel", "validation"), func() {
		Context("IncomingAuth.authzConfig vs IncomingAuth.authzConfigRef", func() {
			It("should accept only inline authzConfig", func() {
				vmcp := newVirtualMCPServerWithIncomingAuth(
					"vmcp-authzmutex-inline-only",
					&mcpv1beta1.AuthzConfigRef{
						Type:   "inline",
						Inline: &mcpv1beta1.InlineAuthzConfig{Policies: []string{"permit(principal, action, resource);"}},
					},
					nil,
				)
				Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
			})

			It("should accept only authzConfigRef", func() {
				vmcp := newVirtualMCPServerWithIncomingAuth(
					"vmcp-authzmutex-ref-only",
					nil,
					&mcpv1beta1.MCPAuthzConfigReference{Name: "shared-authz"},
				)
				Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
			})

			It("should reject when both authzConfig and authzConfigRef are set", func() {
				vmcp := newVirtualMCPServerWithIncomingAuth(
					"vmcp-authzmutex-both",
					&mcpv1beta1.AuthzConfigRef{
						Type:   "inline",
						Inline: &mcpv1beta1.InlineAuthzConfig{Policies: []string{"permit(principal, action, resource);"}},
					},
					&mcpv1beta1.MCPAuthzConfigReference{Name: "shared-authz"},
				)
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("authzConfig and authzConfigRef are mutually exclusive"))
			})
		})
	})
