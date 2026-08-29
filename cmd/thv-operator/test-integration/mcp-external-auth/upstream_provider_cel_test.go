// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// These tests exercise the generated EmbeddedAuthServerConfig upstream-provider
// CEL rule through the real apiserver (envtest). The type is shared with
// VirtualMCPServer, whose admission coverage follows the established shared-schema
// pattern in confidential_client_transport_cel_test.go.
var _ = Describe("MCPExternalAuthConfig upstream-provider CEL validation", Label("k8s", "validation"), func() {
	const namespace = "default"

	makeConfig := func(name string, embeddedAuthServer map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "toolhive.stacklok.dev/v1beta1",
			"kind":       "MCPExternalAuthConfig",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"type":               "embeddedAuthServer",
				"embeddedAuthServer": embeddedAuthServer,
			},
		}}
	}

	type validationCase struct {
		name               string
		embeddedAuthServer map[string]interface{}
		shouldAdmit        bool
	}

	cases := []validationCase{
		{
			name: "omitted upstream providers without an alternative",
			embeddedAuthServer: map[string]interface{}{
				"issuer": "https://auth.example.com",
			},
		},
		{
			name: "explicitly empty upstream providers without an alternative",
			embeddedAuthServer: map[string]interface{}{
				"issuer":            "https://auth.example.com",
				"upstreamProviders": []interface{}{},
			},
		},
		{
			name: "delegate client without upstream providers",
			embeddedAuthServer: map[string]interface{}{
				"issuer": "https://auth.example.com",
				"delegateClients": []interface{}{map[string]interface{}{
					"clientId": "delegate-client",
					"clientSecretRef": map[string]interface{}{
						"name": "delegate-secret",
						"key":  "credential",
					},
					"scopes":    []interface{}{"openid"},
					"audiences": []interface{}{"https://api.example.com"},
				}},
			},
			shouldAdmit: true,
		},
		{
			name: "trusted issuer with jwt bearer grant without upstream providers",
			embeddedAuthServer: map[string]interface{}{
				"issuer": "https://auth.example.com",
				"trustedIssuers": []interface{}{map[string]interface{}{
					"issuerUrl": "https://issuer.example.com",
					"jwtBearerGrant": map[string]interface{}{
						"maxAssertionAge": "1m",
						"subjectBindings": []interface{}{map[string]interface{}{
							"subject":          "external-subject",
							"allowedResources": []interface{}{"https://mcp.example.com"},
						}},
					},
				}},
			},
			shouldAdmit: true,
		},
		{
			name: "trusted issuer without jwt bearer grant and without upstream providers",
			embeddedAuthServer: map[string]interface{}{
				"issuer": "https://auth.example.com",
				"trustedIssuers": []interface{}{map[string]interface{}{
					"issuerUrl":              "https://issuer.example.com",
					"expectedAudience":       "https://mcp.example.com",
					"allowedDelegateClients": []interface{}{"delegate-client"},
				}},
			},
		},
		{
			name: "non-empty upstream providers",
			embeddedAuthServer: map[string]interface{}{
				"issuer": "https://auth.example.com",
				"upstreamProviders": []interface{}{map[string]interface{}{
					"name": "upstream",
					"type": "oidc",
					"oidcConfig": map[string]interface{}{
						"issuerUrl": "https://upstream.example.com",
						"clientId":  "client-id",
					},
				}},
			},
			shouldAdmit: true,
		},
	}

	for i, tc := range cases {
		name := fmt.Sprintf("upstream-provider-validation-%d", i)
		It(tc.name, func() {
			config := makeConfig(name, tc.embeddedAuthServer)
			err := k8sClient.Create(ctx, config)
			if tc.shouldAdmit {
				Expect(err).NotTo(HaveOccurred(), "expected apiserver to admit config: %s", tc.name)
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, config)).To(Succeed())
				})
				return
			}

			Expect(err).To(HaveOccurred(), "expected apiserver to reject config: %s", tc.name)
			Expect(err.Error()).To(ContainSubstring(
				"at least one upstream provider is required unless delegateClients or a trustedIssuer with jwtBearerGrant is configured"))
		})
	}
})
