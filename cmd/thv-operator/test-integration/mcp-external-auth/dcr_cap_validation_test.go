// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// These tests exercise the admission rules on storage.maxDCRClients and
// registrationRateLimit through the real apiserver (envtest).
// EmbeddedAuthServerConfig is shared by MCPExternalAuthConfig and
// VirtualMCPServer, so one CRD's generated schema covers both.
var _ = Describe("EmbeddedAuthServerConfig DCR cap and registration rate validation", func() {
	const namespace = "default"

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	type validationCase struct {
		name            string
		maxDCRClients   *int32
		rateLimit       *mcpv1beta1.RateLimitBucket
		expectedMessage string
	}

	bucket := func(maxTokens int32, refill time.Duration) *mcpv1beta1.RateLimitBucket {
		return &mcpv1beta1.RateLimitBucket{MaxTokens: maxTokens, RefillPeriod: metav1.Duration{Duration: refill}}
	}

	cases := []validationCase{
		{name: "unlimited DCR client cap", maxDCRClients: ptr.To[int32](0)},
		{name: "positive DCR client cap", maxDCRClients: ptr.To[int32](50000)},
		{
			name:            "negative DCR client cap",
			maxDCRClients:   ptr.To[int32](-1),
			expectedMessage: "should be greater than or equal to 0",
		},
		{name: "positive refill period", rateLimit: bucket(5, 5*time.Second)},
		{
			name:            "zero refill period",
			rateLimit:       bucket(5, 0),
			expectedMessage: "registrationRateLimit.refillPeriod must be positive",
		},
		{
			name:            "negative refill period",
			rateLimit:       bucket(5, -time.Second),
			expectedMessage: "registrationRateLimit.refillPeriod must be positive",
		},
		{
			name:            "zero maxTokens",
			rateLimit:       bucket(0, time.Second),
			expectedMessage: "should be greater than or equal to 1",
		},
	}

	for i, c := range cases {
		name := fmt.Sprintf("dcr-cap-validation-%d", i)
		It(c.name, func() {
			cfg := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: "embeddedAuthServer",
					EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						Storage: &mcpv1beta1.AuthServerStorageConfig{
							Type:          mcpv1beta1.AuthServerStorageTypeMemory,
							MaxDCRClients: c.maxDCRClients,
						},
						RegistrationRateLimit: c.rateLimit,
						UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
							Name: "github",
							Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
							OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
								AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
								TokenEndpoint:         "https://github.com/login/oauth/access_token",
								ClientID:              "test-client-id",
							},
						}},
					},
				},
			}
			err := k8sClient.Create(ctx, cfg)
			if c.expectedMessage == "" {
				Expect(err).NotTo(HaveOccurred(), "expected apiserver to admit config: %s", c.name)
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
				})
				return
			}
			Expect(err).To(HaveOccurred(), "expected apiserver to reject config: %s", c.name)
			Expect(err.Error()).To(ContainSubstring(c.expectedMessage))
		})
	}
})
