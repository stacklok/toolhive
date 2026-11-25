// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/pkg/mcp/instructions"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// newVirtualMCPServerWithInstructions builds a minimal VirtualMCPServer with
// the given spec.instructions and spec.config.instructions.
func newVirtualMCPServerWithInstructions(
	name string, topLevel, configLevel *instructions.Config,
) *mcpv1beta1.VirtualMCPServer {
	return v1beta1test.NewVirtualMCPServer(name, "default",
		v1beta1test.WithVMCPGroupRef("test-group"),
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
		v1beta1test.WithVMCPConfig(vmcpconfig.Config{Group: "test-group", Instructions: configLevel}),
		v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
			v.Spec.Instructions = topLevel
		}),
	)
}

var _ = Describe("CEL and schema validation for VirtualMCPServer instructions",
	Label("k8s", "cel", "validation"), func() {
		policy := &instructions.Config{Value: "policy"}

		It("accepts spec.instructions alone", func() {
			Expect(k8sClient.Create(ctx, newVirtualMCPServerWithInstructions(
				"vmcp-instructions-top", policy, nil))).To(Succeed())
		})

		It("accepts spec.config.instructions alone", func() {
			Expect(k8sClient.Create(ctx, newVirtualMCPServerWithInstructions(
				"vmcp-instructions-config", nil, policy))).To(Succeed())
		})

		It("rejects spec.instructions together with spec.config.instructions", func() {
			err := k8sClient.Create(ctx, newVirtualMCPServerWithInstructions(
				"vmcp-instructions-both", policy, policy))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("set instructions or config.instructions, not both"))
		})

		It("rejects a value that is only white space", func() {
			err := k8sClient.Create(ctx, newVirtualMCPServerWithInstructions(
				"vmcp-instructions-blank", &instructions.Config{Value: " \n"}, nil))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.instructions.value"))
		})

		It("rejects a value longer than the bound", func() {
			err := k8sClient.Create(ctx, newVirtualMCPServerWithInstructions(
				"vmcp-instructions-long",
				&instructions.Config{Value: strings.Repeat("a", instructions.MaxValueLength+1)}, nil))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.instructions.value"))
		})
	})
