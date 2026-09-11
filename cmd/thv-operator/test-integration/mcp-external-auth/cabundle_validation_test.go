// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// CABundleSource is shared by multiple CRDs, so exercising it through the
// generated MCPExternalAuthConfig schema verifies the admission constraint.
var _ = Describe("CABundleSource CEL validation", func() {
	const namespace = "default"

	makeAuthConfig := func(name string, caBundleRef *mcpv1beta1.CABundleSource) *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: "embeddedAuthServer",
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer: "https://auth.example.com",
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
						Name: "github",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
							TokenEndpoint:         "https://github.com/login/oauth/access_token",
							ClientID:              "test-client-id",
							CABundleRef:           caBundleRef,
						},
					}},
				},
			},
		}
	}

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	type validationCase struct {
		name        string
		caBundleRef *mcpv1beta1.CABundleSource
		shouldAdmit bool
	}

	cases := []validationCase{
		{
			name:        "caBundleRef omitted",
			caBundleRef: nil,
			shouldAdmit: true,
		},
		{
			name:        "configMapRef omitted",
			caBundleRef: &mcpv1beta1.CABundleSource{},
			shouldAdmit: false,
		},
		{
			name: "configMapRef name empty",
			caBundleRef: &mcpv1beta1.CABundleSource{
				ConfigMapRef: &corev1.ConfigMapKeySelector{Key: "ca.crt"},
			},
			shouldAdmit: false,
		},
		{
			name: "configMapRef name supplied",
			caBundleRef: &mcpv1beta1.CABundleSource{
				ConfigMapRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "ca-bundle"},
					Key:                  "ca.crt",
				},
			},
			shouldAdmit: true,
		},
	}

	for index, testCase := range cases {
		It(testCase.name, func() {
			config := makeAuthConfig(fmt.Sprintf("ca-bundle-validation-%d", index), testCase.caBundleRef)

			err := k8sClient.Create(ctx, config)
			if testCase.shouldAdmit {
				Expect(err).NotTo(HaveOccurred(), "expected apiserver to admit config: %s", testCase.name)
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, config)).To(Succeed())
				})
				return
			}

			Expect(err).To(HaveOccurred(), "expected apiserver to reject config: %s", testCase.name)
			Expect(err.Error()).To(ContainSubstring("configMapRef.name is required and must be non-empty"))
		})
	}
})
