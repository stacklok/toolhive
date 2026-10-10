// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/test-integration/testutil"
	"github.com/stacklok/toolhive/pkg/redisconfig"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func newVirtualMCPServerWithSessionStorage(name string, ss *mcpv1beta1.SessionStorageConfig) *mcpv1beta1.VirtualMCPServer {
	return v1beta1test.NewVirtualMCPServer(name, "default",
		v1beta1test.WithVMCPGroupRef("test-group"),
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type: "anonymous",
		}),
		v1beta1test.WithVMCPConfig(vmcpconfig.Config{
			Group: "test-group",
		}),
		v1beta1test.WithVMCPSessionStorage(ss),
	)
}

// virtualMCPServerAtVersion returns vmcp as an object of the given served API
// version. v1alpha1 reuses the v1beta1 spec type, so only the wrapper differs.
func virtualMCPServerAtVersion(vmcp *mcpv1beta1.VirtualMCPServer, version string) client.Object {
	if version == mcpv1beta1.GroupVersion.Version {
		return vmcp
	}
	return &mcpv1alpha1.VirtualMCPServer{ObjectMeta: vmcp.ObjectMeta, Spec: vmcp.Spec}
}

var _ = Describe("CEL Validation for SessionStorageConfig on VirtualMCPServer",
	Label("k8s", "cel", "validation"), func() {
		Context("provider=redis", func() {
			It("should reject when address is missing", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-redis-no-addr", &mcpv1beta1.SessionStorageConfig{
					Provider: "redis",
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("address is required"))
			})

			It("should reject when address is empty string", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-redis-empty-addr", &mcpv1beta1.SessionStorageConfig{
					Provider: "redis",
					Address:  "",
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
			})

			It("should accept when address is set", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-redis-with-addr", &mcpv1beta1.SessionStorageConfig{
					Provider: "redis",
					Address:  "redis:6379",
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject negative DB number", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-redis-neg-db", &mcpv1beta1.SessionStorageConfig{
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
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-memory-no-addr", &mcpv1beta1.SessionStorageConfig{
					Provider: "memory",
				})
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("tls", func() {
			for _, version := range testutil.ServedAPIVersions {
				for _, tc := range testutil.SessionStorageTLSCases() {
					It(fmt.Sprintf("%s %s", version, tc.Name), func() {
						vmcp := newVirtualMCPServerWithSessionStorage(
							fmt.Sprintf("vmcp-tls-%s-%s", version, tc.Name), tc.SessionStorage)
						err := k8sClient.Create(ctx, virtualMCPServerAtVersion(vmcp, version))
						if tc.WantErr == "" {
							Expect(err).NotTo(HaveOccurred())
							return
						}
						Expect(err).To(HaveOccurred())
						Expect(err.Error()).To(ContainSubstring(tc.WantErr))
					})
				}

				It(fmt.Sprintf("%s rejects tls under config.sessionStorage, which the operator ignores", version), func() {
					vmcp := newVirtualMCPServerWithSessionStorage(
						fmt.Sprintf("vmcp-config-tls-%s", version), &mcpv1beta1.SessionStorageConfig{
							Provider: "redis",
							Address:  "redis:6380",
						})
					vmcp.Spec.Config.SessionStorage = &vmcpconfig.SessionStorageConfig{
						Provider: "redis",
						Address:  "redis:6380",
						TLS:      &redisconfig.TLSConfig{},
					}
					err := k8sClient.Create(ctx, virtualMCPServerAtVersion(vmcp, version))
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("config.sessionStorage.tls is ignored by the operator"))
				})
			}
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

		Context("rateLimiting", func() {
			It("should reject rate limiting without redis session storage", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-rl-no-redis", nil)
				vmcp.Spec.Config.RateLimiting = &mcpv1beta1.RateLimitConfig{
					Shared: &mcpv1beta1.RateLimitBucket{
						MaxTokens:    1,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				}

				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("config.rateLimiting requires sessionStorage with provider 'redis'"))
			})

			It("should reject perUser rate limiting with anonymous auth", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-rl-peruser-anon", &mcpv1beta1.SessionStorageConfig{
					Provider: "redis",
					Address:  "redis:6379",
				})
				vmcp.Spec.Config.RateLimiting = &mcpv1beta1.RateLimitConfig{
					PerUser: &mcpv1beta1.RateLimitBucket{
						MaxTokens:    1,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				}

				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("config.rateLimiting.perUser requires incomingAuth.type oidc"))
			})

			It("should accept perUser rate limiting with oidc auth and redis session storage", func() {
				vmcp := newVirtualMCPServerWithSessionStorage("vmcp-rl-peruser-oidc", &mcpv1beta1.SessionStorageConfig{
					Provider: "redis",
					Address:  "redis:6379",
				})
				vmcp.Spec.IncomingAuth = &mcpv1beta1.IncomingAuthConfig{
					Type: "oidc",
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
						Name:     "oidc",
						Audience: "test-audience",
					},
				}
				vmcp.Spec.Config.RateLimiting = &mcpv1beta1.RateLimitConfig{
					PerUser: &mcpv1beta1.RateLimitBucket{
						MaxTokens:    1,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				}

				err := k8sClient.Create(ctx, vmcp)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
