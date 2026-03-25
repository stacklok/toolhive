// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func newVirtualMCPServerWithSessionStorage(name string, ss *mcpv1alpha1.SessionStorageConfig) *mcpv1alpha1.VirtualMCPServer {
	return &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			Config: vmcpconfig.Config{
				Group: "test-group",
			},
			SessionStorage: ss,
		},
	}
}

var _ = Describe("CEL Validation for SessionStorageConfig on VirtualMCPServer",
	Label("k8s", "cel", "validation"), func() {
		Context("provider=redis", func() {
			It("should reject when address is missing", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-redis-no-addr", &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("address is required"))
			})

			It("should reject when address is empty string", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-redis-empty-addr", &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  "",
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
			})

			It("should accept when address is set", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-redis-with-addr", &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  "redis:6379",
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject negative DB number", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-redis-neg-db", &mcpv1alpha1.SessionStorageConfig{
					Provider: "redis",
					Address:  "redis:6379",
					DB:       -1,
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("provider=memory", func() {
			It("should accept without address", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-memory-no-addr", &mcpv1alpha1.SessionStorageConfig{
					Provider: "memory",
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("replicas field", func() {
			It("should accept nil replicas (HPA-compatible)", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-nil-replicas", nil)
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept explicit replicas value", func() {
				replicas := int32(2)
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-explicit-replicas", nil)
				vmcp.Spec.Replicas = &replicas
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject negative replicas", func() {
				replicas := int32(-1)
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-neg-replicas", nil)
				vmcp.Spec.Replicas = &replicas
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
			})
		})
	})
