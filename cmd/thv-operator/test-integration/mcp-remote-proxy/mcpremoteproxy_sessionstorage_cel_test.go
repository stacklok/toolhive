// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/test-integration/testutil"
)

// remoteProxyAtVersion returns proxy as an object of the given served API
// version. v1alpha1 reuses the v1beta1 spec type, so only the wrapper differs.
func remoteProxyAtVersion(proxy *mcpv1beta1.MCPRemoteProxy, version string) client.Object {
	if version == mcpv1beta1.GroupVersion.Version {
		return proxy
	}
	return &mcpv1alpha1.MCPRemoteProxy{ObjectMeta: proxy.ObjectMeta, Spec: proxy.Spec}
}

var _ = Describe("CEL Validation for sessionStorage.tls on MCPRemoteProxy",
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

		for _, version := range testutil.ServedAPIVersions {
			for _, tc := range testutil.SessionStorageTLSCases() {
				It(fmt.Sprintf("%s %s", version, tc.Name), func() {
					proxy := v1beta1test.NewMCPRemoteProxy(fmt.Sprintf("rp-tls-%s-%s", version, tc.Name), testNamespace,
						v1beta1test.WithRemoteProxyURL("https://example.com"),
						v1beta1test.WithRemoteProxySessionStorage(tc.SessionStorage))
					err := k8sClient.Create(testCtx, remoteProxyAtVersion(proxy, version))
					if tc.WantErr == "" {
						Expect(err).NotTo(HaveOccurred())
						return
					}
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring(tc.WantErr))
				})
			}
		}
	})
