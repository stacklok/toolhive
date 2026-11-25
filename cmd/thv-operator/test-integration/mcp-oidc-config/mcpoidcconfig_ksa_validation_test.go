// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// The resolver builds system:serviceaccount:<namespace>:<serviceAccount> from
// these fields, so a value that is not a Kubernetes name would produce a
// subject no token can carry and lock out every caller. Admission rejects it.
var _ = Describe("MCPOIDCConfig kubernetesServiceAccount validation",
	Label("k8s", "validation"), func() {
		newKSAConfig := func(name, serviceAccount, namespace string) *mcpv1beta1.MCPOIDCConfig {
			return &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1beta1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: serviceAccount,
						Namespace:      namespace,
					},
				},
			}
		}

		DescribeTable("admission",
			func(name, serviceAccount, namespace, wantErr string) {
				err := k8sClient.Create(ctx, newKSAConfig(name, serviceAccount, namespace))
				if wantErr == "" {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(wantErr))
			},
			Entry("accepts both empty", "ksa-empty", "", "", ""),
			Entry("accepts a service account name", "ksa-sa", "mcp-client", "", ""),
			Entry("accepts a dotted service account name", "ksa-sa-dots", "team.mcp-client", "", ""),
			Entry("accepts a namespace", "ksa-ns", "", "team-a", ""),
			Entry("rejects a full subject as serviceAccount", "ksa-bad-sa-subject",
				"system:serviceaccount:team-a:mcp-client", "", "spec.kubernetesServiceAccount.serviceAccount"),
			Entry("rejects an uppercase serviceAccount", "ksa-bad-sa-upper",
				"MCPClient", "", "spec.kubernetesServiceAccount.serviceAccount"),
			Entry("rejects a namespace with a colon", "ksa-bad-ns-colon",
				"", "team-a:", "spec.kubernetesServiceAccount.namespace"),
			Entry("rejects a dotted namespace", "ksa-bad-ns-dots",
				"", "team.a", "spec.kubernetesServiceAccount.namespace"),
		)
	})
