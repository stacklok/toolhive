// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func newVirtualMCPServerWithOptimizer(name string, optimizer *vmcpconfig.OptimizerConfig,
	opts ...v1beta1test.VirtualMCPServerOption) *mcpv1beta1.VirtualMCPServer {
	base := []v1beta1test.VirtualMCPServerOption{
		v1beta1test.WithVMCPGroupRef("test-group"),
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
		v1beta1test.WithVMCPConfig(vmcpconfig.Config{Group: "test-group", Optimizer: optimizer}),
	}
	return v1beta1test.NewVirtualMCPServer(name, "default", append(base, opts...)...)
}

var _ = Describe("CEL Validation for embedding provider on VirtualMCPServer",
	Label("k8s", "cel", "validation"), func() {
		It("should reject embeddingServerRef combined with embeddingProvider openai", func() {
			vmcp := newVirtualMCPServerWithOptimizer("vmcp-ref-openai",
				&vmcpconfig.OptimizerConfig{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small"},
				v1beta1test.WithVMCPEmbeddingServerRef("managed-tei"))
			err := k8sClient.Create(ctx, vmcp)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(
				"embeddingServerRef provisions a managed TEI server and cannot be combined with optimizer.embeddingProvider 'openai'"))
		})

		It("should accept embeddingServerRef with the default (tei) provider", func() {
			vmcp := newVirtualMCPServerWithOptimizer("vmcp-ref-tei",
				&vmcpconfig.OptimizerConfig{EmbeddingProvider: "tei"},
				v1beta1test.WithVMCPEmbeddingServerRef("managed-tei"))
			err := k8sClient.Create(ctx, vmcp)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should accept embeddingProvider openai without an embeddingServerRef", func() {
			vmcp := newVirtualMCPServerWithOptimizer("vmcp-openai-no-ref",
				&vmcpconfig.OptimizerConfig{
					EmbeddingProvider: "openai",
					EmbeddingService:  "http://gateway.example:8080",
					EmbeddingModel:    "text-embedding-3-small",
				})
			err := k8sClient.Create(ctx, vmcp)
			Expect(err).NotTo(HaveOccurred())
		})
	})
