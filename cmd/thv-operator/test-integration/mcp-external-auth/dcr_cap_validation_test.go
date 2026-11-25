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
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// These tests exercise the admission rules on storage.maxDCRClients and
// registrationRateLimit through the real apiserver (envtest).
// EmbeddedAuthServerConfig is shared by MCPExternalAuthConfig and
// VirtualMCPServer, but each CRD carries its own generated copy of the schema,
// so the VirtualMCPServer cases guard against the two drifting apart.
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

	embeddedAuthServer := func(c validationCase) *mcpv1beta1.EmbeddedAuthServerConfig {
		return &mcpv1beta1.EmbeddedAuthServerConfig{
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
		}
	}

	expectAdmission := func(obj client.Object, c validationCase) {
		err := k8sClient.Create(ctx, obj)
		if c.expectedMessage == "" {
			Expect(err).NotTo(HaveOccurred(), "expected apiserver to admit config: %s", c.name)
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
			})
			return
		}
		Expect(err).To(HaveOccurred(), "expected apiserver to reject config: %s", c.name)
		Expect(err.Error()).To(ContainSubstring(c.expectedMessage))
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
		It("MCPExternalAuthConfig: "+c.name, func() {
			expectAdmission(&mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type:               "embeddedAuthServer",
					EmbeddedAuthServer: embeddedAuthServer(c),
				},
			}, c)
		})
	}

	vmcpCases := []validationCase{
		{name: "positive DCR client cap", maxDCRClients: ptr.To[int32](50000)},
		{
			name:            "zero refill period",
			rateLimit:       bucket(5, 0),
			expectedMessage: "registrationRateLimit.refillPeriod must be positive",
		},
	}

	for i, c := range vmcpCases {
		name := fmt.Sprintf("dcr-cap-validation-vmcp-%d", i)
		It("VirtualMCPServer: "+c.name, func() {
			expectAdmission(v1beta1test.NewVirtualMCPServer(name, namespace,
				v1beta1test.WithVMCPGroupRef("test-group"),
				v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
				v1beta1test.WithVMCPConfig(vmcpconfig.Config{Group: "test-group"}),
				v1beta1test.WithVMCPAuthServerConfig(embeddedAuthServer(c)),
			), c)
		})
	}
})
