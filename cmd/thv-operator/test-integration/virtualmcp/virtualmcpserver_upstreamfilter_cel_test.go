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

// These tests exercise the authServerUpstreamFilter CEL rules through the real
// apiserver. The reconciler re-validates deeper reference-integrity invariants
// (every referenced provider exists, mandatory-first-provider is never
// referenced) in Go — those checks are covered by unit tests on
// validateAuthServerUpstreamFilter in the controller package.

// upstreamFilterTestOAuth2Upstream returns a minimal OAuth2 upstream provider
// usable in CEL fixtures. OAuth2 is preferred over OIDC here because it
// avoids the OIDC discovery URL requirement and keeps every fixture
// construction below the test's own clarity threshold.
func upstreamFilterTestOAuth2Upstream(name string) mcpv1beta1.UpstreamProviderConfig {
	return mcpv1beta1.UpstreamProviderConfig{
		Name: name,
		Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
		OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
			AuthorizationEndpoint: "https://example.com/oauth/authorize",
			TokenEndpoint:         "https://example.com/oauth/token",
			ClientID:              "test-client-id",
		},
	}
}

// newVirtualMCPServerWithUpstreamFilter builds a minimal VirtualMCPServer with
// an optional AuthServerConfig and AuthServerUpstreamFilter so each test can
// vary just the pair under CEL validation.
func newVirtualMCPServerWithUpstreamFilter(
	name string,
	authServerConfig *mcpv1beta1.EmbeddedAuthServerConfig,
	filter *mcpv1beta1.AuthServerUpstreamFilterConfig,
) *mcpv1beta1.VirtualMCPServer {
	opts := []v1beta1test.VirtualMCPServerOption{
		v1beta1test.WithVMCPGroupRef("test-group"),
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type: "anonymous",
		}),
		v1beta1test.WithVMCPConfig(vmcpconfig.Config{
			Group: "test-group",
		}),
		v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
			v.Spec.AuthServerUpstreamFilter = filter
		}),
	}
	if authServerConfig != nil {
		opts = append(opts, v1beta1test.WithVMCPAuthServerConfig(authServerConfig))
	}
	return v1beta1test.NewVirtualMCPServer(name, "default", opts...)
}

var _ = Describe("CEL Validation for authServerUpstreamFilter on VirtualMCPServer",
	Label("k8s", "cel", "validation"), func() {

		// A reusable authServerConfig with the minimum two upstreams the
		// filter requires. The mandatory first upstream is "okta".
		twoUpstreamAuthServer := func() *mcpv1beta1.EmbeddedAuthServerConfig {
			return &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://authserver.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					upstreamFilterTestOAuth2Upstream("okta"),
					upstreamFilterTestOAuth2Upstream("jira"),
				},
			}
		}

		validRule := func() mcpv1beta1.UpstreamFilterGroupRule {
			return mcpv1beta1.UpstreamFilterGroupRule{
				Groups:            []string{"engineering"},
				UpstreamProviders: []string{"jira"},
			}
		}

		Context("authServerUpstreamFilter requires authServerConfig", func() {
			It("rejects filter without authServerConfig", func() {
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-no-authserver",
					nil,
					&mcpv1beta1.AuthServerUpstreamFilterConfig{
						Rules: []mcpv1beta1.UpstreamFilterGroupRule{validRule()},
					},
				)
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("authServerUpstreamFilter requires authServerConfig"))
			})

			It("accepts authServerConfig without filter (filter is optional)", func() {
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-absent",
					twoUpstreamAuthServer(),
					nil,
				)
				Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
			})
		})

		Context("authServerUpstreamFilter requires at least two upstream providers", func() {
			It("rejects filter with only one configured upstream", func() {
				authServer := &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer: "https://authserver.example.com",
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
						upstreamFilterTestOAuth2Upstream("okta"),
					},
				}
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-one-upstream",
					authServer,
					&mcpv1beta1.AuthServerUpstreamFilterConfig{
						Rules: []mcpv1beta1.UpstreamFilterGroupRule{validRule()},
					},
				)
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("requires at least two configured upstream providers"))
			})

			It("accepts filter with exactly two configured upstreams", func() {
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-two-upstreams",
					twoUpstreamAuthServer(),
					&mcpv1beta1.AuthServerUpstreamFilterConfig{
						Rules: []mcpv1beta1.UpstreamFilterGroupRule{validRule()},
					},
				)
				Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
			})
		})

		Context("OpenAPI structural constraints on the filter fields", func() {
			It("rejects a filter with no rules", func() {
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-no-rules",
					twoUpstreamAuthServer(),
					&mcpv1beta1.AuthServerUpstreamFilterConfig{
						Rules: []mcpv1beta1.UpstreamFilterGroupRule{},
					},
				)
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
			})

			It("rejects a rule with empty groups", func() {
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-empty-groups",
					twoUpstreamAuthServer(),
					&mcpv1beta1.AuthServerUpstreamFilterConfig{
						Rules: []mcpv1beta1.UpstreamFilterGroupRule{{
							Groups:            []string{},
							UpstreamProviders: []string{"jira"},
						}},
					},
				)
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
			})

			It("rejects a rule with empty upstreamProviders", func() {
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-empty-upstreams",
					twoUpstreamAuthServer(),
					&mcpv1beta1.AuthServerUpstreamFilterConfig{
						Rules: []mcpv1beta1.UpstreamFilterGroupRule{{
							Groups:            []string{"engineering"},
							UpstreamProviders: []string{},
						}},
					},
				)
				err := k8sClient.Create(ctx, vmcp)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("defaultUpstreams on the filter", func() {
			It("accepts a filter with defaultUpstreams set", func() {
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-with-defaults",
					twoUpstreamAuthServer(),
					&mcpv1beta1.AuthServerUpstreamFilterConfig{
						Rules:            []mcpv1beta1.UpstreamFilterGroupRule{validRule()},
						DefaultUpstreams: []string{"jira"},
					},
				)
				Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
			})

			It("accepts a filter with empty defaultUpstreams", func() {
				vmcp := newVirtualMCPServerWithUpstreamFilter(
					"vmcp-filter-empty-defaults",
					twoUpstreamAuthServer(),
					&mcpv1beta1.AuthServerUpstreamFilterConfig{
						Rules:            []mcpv1beta1.UpstreamFilterGroupRule{validRule()},
						DefaultUpstreams: []string{},
					},
				)
				Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
			})
		})
	})
