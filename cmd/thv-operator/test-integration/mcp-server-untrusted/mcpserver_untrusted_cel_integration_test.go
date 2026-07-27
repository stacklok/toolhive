// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// validUntrustedSpec returns a fully R1-R6-compliant untrusted MCPServer spec
// (untrusted + egressPolicy with one provider + groupRef). Cases mutate it to
// violate exactly one rule at a time.
func validUntrustedSpec() mcpv1beta1.MCPServerSpec {
	return mcpv1beta1.MCPServerSpec{
		Image:     "example/mcp-server:latest",
		Untrusted: true,
		GroupRef:  &mcpv1beta1.MCPGroupRef{Name: "test-group"},
		EgressPolicy: &mcpv1beta1.EgressPolicy{
			Providers: []mcpv1beta1.ProviderEgress{{
				Provider:     "github",
				AllowedHosts: []string{"api.github.com"},
			}},
		},
	}
}

var _ = Describe("CEL Validation for MCPServer untrusted mode", Label("k8s", "cel", "validation"), func() {
	type celCase struct {
		mutate  func(*mcpv1beta1.MCPServerSpec) // nil leaves the valid baseline untouched
		wantErr string                          // empty means admission must accept
	}

	DescribeTable("R1-R6 admission rules",
		func(c celCase) {
			spec := validUntrustedSpec()
			if c.mutate != nil {
				c.mutate(&spec)
			}
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "untrusted-cel-",
					Namespace:    "default",
				},
				Spec: spec,
			}
			err := untrustedK8sClient.Create(untrustedCtx, server)
			if c.wantErr == "" {
				Expect(err).NotTo(HaveOccurred())
				Expect(untrustedK8sClient.Delete(untrustedCtx, server)).To(Succeed())
				return
			}
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(c.wantErr))
		},
		Entry("R1a rejects untrusted without egressPolicy", celCase{
			mutate:  func(s *mcpv1beta1.MCPServerSpec) { s.EgressPolicy = nil },
			wantErr: "egressPolicy with at least one provider is required when untrusted is true",
		}),
		Entry("R1b rejects untrusted with empty providers (schema MinItems fires first)", celCase{
			mutate:  func(s *mcpv1beta1.MCPServerSpec) { s.EgressPolicy.Providers = nil },
			wantErr: "Required value",
		}),
		Entry("R1c accepts untrusted with one valid provider", celCase{}),
		Entry("R2a rejects untrusted without groupRef", celCase{
			mutate:  func(s *mcpv1beta1.MCPServerSpec) { s.GroupRef = nil },
			wantErr: "untrusted workloads must belong to an MCPGroup fronted by a VirtualMCPServer",
		}),
		Entry("R2b accepts untrusted with groupRef", celCase{}),
		Entry("R3a rejects untrusted with spec.secrets", celCase{
			mutate: func(s *mcpv1beta1.MCPServerSpec) {
				s.Secrets = []mcpv1beta1.SecretRef{{Name: "creds", Key: "token", TargetEnvName: "API_TOKEN"}}
			},
			wantErr: "spec.secrets is forbidden when untrusted is true",
		}),
		Entry("R3b accepts untrusted without secrets", celCase{}),
		Entry("R4a rejects untrusted with podTemplateSpec", celCase{
			mutate: func(s *mcpv1beta1.MCPServerSpec) {
				s.PodTemplateSpec = &runtime.RawExtension{
					Raw: []byte(`{"spec":{"containers":[{"name":"mcp","env":[{"name":"A","value":"b"}]}]}}`),
				}
			},
			wantErr: "podTemplateSpec is forbidden when untrusted is true",
		}),
		Entry("R4b accepts untrusted without podTemplateSpec", celCase{}),
		Entry("R5a rejects untrusted with sessionAffinity None", celCase{
			mutate:  func(s *mcpv1beta1.MCPServerSpec) { s.SessionAffinity = "None" },
			wantErr: "untrusted workloads require sessionAffinity ClientIP",
		}),
		Entry("R5b accepts untrusted with sessionAffinity omitted (defaults ClientIP)", celCase{}),
		Entry("R5c accepts untrusted with sessionAffinity ClientIP", celCase{
			mutate: func(s *mcpv1beta1.MCPServerSpec) { s.SessionAffinity = "ClientIP" },
		}),
		Entry("R6a rejects untrusted with backendReplicas", celCase{
			mutate:  func(s *mcpv1beta1.MCPServerSpec) { s.BackendReplicas = ptr.To(int32(2)) },
			wantErr: "backendReplicas is managed by the untrusted-mode session lifecycle and must not be set",
		}),
		Entry("R6b accepts untrusted without backendReplicas", celCase{}),
		Entry("Trusted is inert for all rules at once", celCase{
			mutate: func(s *mcpv1beta1.MCPServerSpec) {
				s.Untrusted = false
				s.EgressPolicy = nil
				s.GroupRef = nil
				s.Secrets = []mcpv1beta1.SecretRef{{Name: "creds", Key: "token", TargetEnvName: "API_TOKEN"}}
				s.PodTemplateSpec = &runtime.RawExtension{
					Raw: []byte(`{"spec":{"containers":[{"name":"mcp","env":[{"name":"A","value":"b"}]}]}}`),
				}
				s.SessionAffinity = "None"
				s.BackendReplicas = ptr.To(int32(2))
			},
		}),
	)

	Context("EgressPolicy schema validation", func() {
		It("should reject a provider without allowedHosts", func() {
			spec := validUntrustedSpec()
			spec.EgressPolicy.Providers[0].AllowedHosts = nil
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "untrusted-no-hosts", Namespace: "default"},
				Spec:       spec,
			}
			err := untrustedK8sClient.Create(untrustedCtx, server)
			Expect(err).To(HaveOccurred())
		})

		It("should reject an allowedHost with an invalid hostname", func() {
			spec := validUntrustedSpec()
			spec.EgressPolicy.Providers[0].AllowedHosts = []string{"https://api.github.com:443"}
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "untrusted-bad-host", Namespace: "default"},
				Spec:       spec,
			}
			err := untrustedK8sClient.Create(untrustedCtx, server)
			Expect(err).To(HaveOccurred())
		})

		It("should accept a one-label wildcard host", func() {
			spec := validUntrustedSpec()
			spec.EgressPolicy.Providers[0].AllowedHosts = []string{"*.githubusercontent.com"}
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "untrusted-wildcard", Namespace: "default"},
				Spec:       spec,
			}
			Expect(untrustedK8sClient.Create(untrustedCtx, server)).To(Succeed())
			Expect(untrustedK8sClient.Delete(untrustedCtx, server)).To(Succeed())
		})

		It("should reject an invalid HTTP method", func() {
			spec := validUntrustedSpec()
			spec.EgressPolicy.Providers[0].AllowedMethods = []string{"YEET"}
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "untrusted-bad-method", Namespace: "default"},
				Spec:       spec,
			}
			err := untrustedK8sClient.Create(untrustedCtx, server)
			Expect(err).To(HaveOccurred())
		})

		It("should reject a duplicate provider name", func() {
			spec := validUntrustedSpec()
			spec.EgressPolicy.Providers = append(spec.EgressPolicy.Providers, mcpv1beta1.ProviderEgress{
				Provider:     "github",
				AllowedHosts: []string{"github.com"},
			})
			server := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "untrusted-dup-provider", Namespace: "default"},
				Spec:       spec,
			}
			err := untrustedK8sClient.Create(untrustedCtx, server)
			Expect(err).To(HaveOccurred())
		})
	})
})
