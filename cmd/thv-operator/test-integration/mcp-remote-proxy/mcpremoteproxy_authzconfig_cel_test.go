// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
)

// newRemoteProxyWithAuthz builds a minimal MCPRemoteProxy whose authz pair is
// the subject of the CEL XValidation rule under test.
func newRemoteProxyWithAuthz(
	namespace, name string,
	authzConfig *mcpv1beta1.AuthzConfigRef,
	authzConfigRef *mcpv1beta1.MCPAuthzConfigReference,
) *mcpv1beta1.MCPRemoteProxy {
	return v1beta1test.NewMCPRemoteProxy(name, namespace,
		v1beta1test.WithRemoteProxyURL("https://example.com"),
		v1beta1test.WithRemoteProxyPort(0),
		v1beta1test.WithRemoteProxyAuthzConfig(authzConfig),
		v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
			p.Spec.AuthzConfigRef = authzConfigRef
		}),
	)
}

var _ = Describe("CEL Validation for authzConfig vs authzConfigRef on MCPRemoteProxy",
	Label("k8s", "remoteproxy", "cel", "validation"), func() {
		var (
			testCtx       context.Context
			testNamespace string
		)

		BeforeEach(func() {
			testCtx = context.Background()
			testNamespace = createTestNamespace(testCtx)
		})

		AfterEach(func() {
			deleteTestNamespace(testCtx, testNamespace)
		})

		It("should accept only inline authzConfig", func() {
			proxy := newRemoteProxyWithAuthz(
				testNamespace, "rp-authzmutex-inline-only",
				&mcpv1beta1.AuthzConfigRef{
					Type:   "inline",
					Inline: &mcpv1beta1.InlineAuthzConfig{Policies: []string{"permit(principal, action, resource);"}},
				},
				nil,
			)
			Expect(k8sClient.Create(testCtx, proxy)).To(Succeed())
		})

		It("should accept only authzConfigRef", func() {
			proxy := newRemoteProxyWithAuthz(
				testNamespace, "rp-authzmutex-ref-only",
				nil,
				&mcpv1beta1.MCPAuthzConfigReference{Name: "shared-authz"},
			)
			Expect(k8sClient.Create(testCtx, proxy)).To(Succeed())
		})

		It("should reject when both authzConfig and authzConfigRef are set", func() {
			proxy := newRemoteProxyWithAuthz(
				testNamespace, "rp-authzmutex-both",
				&mcpv1beta1.AuthzConfigRef{
					Type:   "inline",
					Inline: &mcpv1beta1.InlineAuthzConfig{Policies: []string{"permit(principal, action, resource);"}},
				},
				&mcpv1beta1.MCPAuthzConfigReference{Name: "shared-authz"},
			)
			err := k8sClient.Create(testCtx, proxy)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("authzConfig and authzConfigRef are mutually exclusive"))
		})
	})
