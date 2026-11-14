// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// These tests exercise the admission rules on dcrConfig.registrationProbeInterval
// through the real apiserver (envtest). The VirtualMCPServer case guards
// against its generated copy of the schema drifting from MCPExternalAuthConfig.
var _ = Describe("DCRUpstreamConfig registrationProbeInterval validation", func() {
	const (
		namespace  = "default"
		minMessage = "registrationProbeInterval must be at least 1m"
	)

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	type validationCase struct {
		name            string
		interval        *metav1.Duration
		expectedMessage string
	}

	interval := func(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

	embeddedAuthServer := func(c validationCase) *mcpv1beta1.EmbeddedAuthServerConfig {
		return &mcpv1beta1.EmbeddedAuthServerConfig{
			Issuer: "https://auth.example.com",
			UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
				Name: "idp",
				Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
				OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					Scopes:                []string{"openid"},
					DCRConfig: &mcpv1beta1.DCRUpstreamConfig{
						RegistrationEndpoint:      "https://idp.example.com/register",
						RegistrationProbeInterval: c.interval,
					},
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
		{name: "omitted"},
		{name: "exactly the minimum", interval: interval(time.Minute)},
		{name: "above the minimum", interval: interval(90 * time.Second)},
		{name: "hours", interval: interval(time.Hour)},
		{name: "below the minimum", interval: interval(30 * time.Second), expectedMessage: minMessage},
		{name: "zero", interval: interval(0), expectedMessage: minMessage},
	}

	for i, c := range cases {
		name := fmt.Sprintf("dcr-probe-interval-%d", i)
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

	// A typed client always serializes a valid duration, so malformed input
	// goes through an unstructured object.
	for i, raw := range []string{"5", "1d", "soon"} {
		name := fmt.Sprintf("dcr-probe-interval-malformed-%d", i)
		It("MCPExternalAuthConfig: malformed "+raw, func() {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "toolhive.stacklok.dev", Version: "v1beta1", Kind: "MCPExternalAuthConfig",
			})
			obj.SetName(name)
			obj.SetNamespace(namespace)
			Expect(unstructured.SetNestedField(obj.Object, map[string]any{
				"type": "embeddedAuthServer",
				"embeddedAuthServer": map[string]any{
					"issuer": "https://auth.example.com",
					"upstreamProviders": []any{map[string]any{
						"name": "idp",
						"type": "oauth2",
						"oauth2Config": map[string]any{
							"authorizationEndpoint": "https://idp.example.com/authorize",
							"tokenEndpoint":         "https://idp.example.com/token",
							"scopes":                []any{"openid"},
							"dcrConfig": map[string]any{
								"registrationEndpoint":      "https://idp.example.com/register",
								"registrationProbeInterval": raw,
							},
						},
					}},
				},
			}, "spec")).To(Succeed())
			err := k8sClient.Create(ctx, obj)
			Expect(err).To(HaveOccurred(), "expected apiserver to reject %q", raw)
			Expect(err.Error()).To(ContainSubstring("registrationProbeInterval"))
		})
	}

	It("VirtualMCPServer: below the minimum", func() {
		c := validationCase{name: "below the minimum", interval: interval(30 * time.Second), expectedMessage: minMessage}
		expectAdmission(v1beta1test.NewVirtualMCPServer("dcr-probe-interval-vmcp", namespace,
			v1beta1test.WithVMCPGroupRef("test-group"),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{Group: "test-group"}),
			v1beta1test.WithVMCPAuthServerConfig(embeddedAuthServer(c)),
		), c)
	})
})
