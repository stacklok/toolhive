// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"fmt"
	"strings"

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

		It("should reject embeddingHeaders with the tei provider", func() {
			vmcp := newVirtualMCPServerWithOptimizer("vmcp-headers-tei",
				&vmcpconfig.OptimizerConfig{
					EmbeddingProvider: "tei",
					EmbeddingService:  "http://embeddings.example:8080",
					EmbeddingHeaders:  map[string]vmcpconfig.EmbeddingHeaderValue{"x-cache-key": "toolhive-optimizer"},
				})
			err := k8sClient.Create(ctx, vmcp)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(
				"embeddingHeaders is only supported when embeddingProvider is 'openai'"))
		})

		It("should reject embeddingHeaders when the provider is defaulted to tei", func() {
			vmcp := newVirtualMCPServerWithOptimizer("vmcp-headers-default",
				&vmcpconfig.OptimizerConfig{
					EmbeddingService: "http://embeddings.example:8080",
					EmbeddingHeaders: map[string]vmcpconfig.EmbeddingHeaderValue{"x-cache-key": "toolhive-optimizer"},
				})
			err := k8sClient.Create(ctx, vmcp)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(
				"embeddingHeaders is only supported when embeddingProvider is 'openai'"))
		})

		It("should accept embeddingHeaders with the openai provider", func() {
			vmcp := newVirtualMCPServerWithOptimizer("vmcp-headers-openai",
				&vmcpconfig.OptimizerConfig{
					EmbeddingProvider: "openai",
					EmbeddingService:  "http://gateway.example:8080",
					EmbeddingModel:    "text-embedding-3-small",
					EmbeddingHeaders:  map[string]vmcpconfig.EmbeddingHeaderValue{"x-cache-key": "toolhive-optimizer"},
				})
			err := k8sClient.Create(ctx, vmcp)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject reserved names, invalid names, and unsafe values in embeddingHeaders", func() {
			for i, tc := range []struct {
				headers map[string]vmcpconfig.EmbeddingHeaderValue
				want    string
			}{
				{map[string]vmcpconfig.EmbeddingHeaderValue{"Authorization": "Bearer x"}, "must not include Authorization or Content-Type"},
				{map[string]vmcpconfig.EmbeddingHeaderValue{"authorization": "Bearer x"}, "must not include Authorization or Content-Type"},
				{map[string]vmcpconfig.EmbeddingHeaderValue{"Content-Type": "application/json"}, "must not include Authorization or Content-Type"},
				{map[string]vmcpconfig.EmbeddingHeaderValue{"content-type": "application/json"}, "must not include Authorization or Content-Type"},
				{map[string]vmcpconfig.EmbeddingHeaderValue{"": "value"}, "names must be valid HTTP header names"},
				{map[string]vmcpconfig.EmbeddingHeaderValue{"x bad": "value"}, "names must be valid HTTP header names"},
				{map[string]vmcpconfig.EmbeddingHeaderValue{"x-cache-key": ""}, "should be at least 1 chars long"},
				{map[string]vmcpconfig.EmbeddingHeaderValue{"x-cache-key": "a\r\nb"}, "should match"},
				{map[string]vmcpconfig.EmbeddingHeaderValue{
					"x-cache-key": vmcpconfig.EmbeddingHeaderValue(strings.Repeat("a", 8193)),
				}, "8192"},
			} {
				vmcp := newVirtualMCPServerWithOptimizer(fmt.Sprintf("vmcp-headers-reject-%d", i),
					&vmcpconfig.OptimizerConfig{
						EmbeddingProvider: "openai",
						EmbeddingService:  "http://gateway.example:8080",
						EmbeddingModel:    "text-embedding-3-small",
						EmbeddingHeaders:  tc.headers,
					})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred(), "headers %v should be rejected", tc.headers)
				Expect(err.Error()).To(ContainSubstring(tc.want))
			}
		})

		It("should accept uncommon but valid RFC token characters in header names", func() {
			vmcp := newVirtualMCPServerWithOptimizer("vmcp-headers-token-chars",
				&vmcpconfig.OptimizerConfig{
					EmbeddingProvider: "openai",
					EmbeddingService:  "http://gateway.example:8080",
					EmbeddingModel:    "text-embedding-3-small",
					EmbeddingHeaders:  map[string]vmcpconfig.EmbeddingHeaderValue{"x-key'name`x": "value"},
				})
			err := k8sClient.Create(ctx, vmcp)
			Expect(err).NotTo(HaveOccurred())
		})
	})
