// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func newMCPServerWithSessionStorage(name string, ss *mcpv1alpha1.SessionStorageConfig) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:          "example/mcp-server:latest",
			SessionStorage: ss,
		},
	}
}

var _ = Describe("CEL Validation for SessionStorageConfig on MCPServer",
	Label("k8s", "cel", "validation"), func() {
		Context("provider=redis", func() {
			It("should reject when address is missing", func() {
				server := newMCPServerWithSessionStorage("mcp-redis-no-addr", &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("address is required"))
			})

			It("should reject when address is empty string", func() {
				server := newMCPServerWithSessionStorage("mcp-redis-empty-addr", &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  "",
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
			})

			It("should accept when address is set", func() {
				server := newMCPServerWithSessionStorage("mcp-redis-with-addr", &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  "redis:6379",
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept with all fields set", func() {
				server := newMCPServerWithSessionStorage("mcp-redis-full", &mcpv1alpha1.SessionStorageConfig{
					Provider:  "redis",
					Address:   "redis:6379",
					DB:        1,
					KeyPrefix: "thv:",
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject negative DB number", func() {
				server := newMCPServerWithSessionStorage("mcp-redis-neg-db", &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  "redis:6379",
					DB:       -1,
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("provider=memory", func() {
			It("should accept without address", func() {
				server := newMCPServerWithSessionStorage("mcp-memory-no-addr", &mcpv1alpha1.SessionStorageConfig{
					Provider: "memory",
				})
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("replicas fields", func() {
			It("should accept nil replicas (HPA-compatible)", func() {
				server := newMinimalMCPServer("mcp-nil-replicas", nil, nil)
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept explicit replicas value", func() {
				replicas := int32(3)
				server := newMinimalMCPServer("mcp-explicit-replicas", nil, nil)
				server.Spec.Replicas = &replicas
				err := k8sClient.Create(ctx, server)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject negative replicas", func() {
				replicas := int32(-1)
				server := newMinimalMCPServer("mcp-neg-replicas", nil, nil)
				server.Spec.Replicas = &replicas
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
			})

			It("should reject negative backendReplicas", func() {
				backendReplicas := int32(-1)
				server := newMinimalMCPServer("mcp-neg-backend-replicas", nil, nil)
				server.Spec.BackendReplicas = &backendReplicas
				err := k8sClient.Create(ctx, server)
				Expect(err).To(HaveOccurred())
			})
		})
	})
