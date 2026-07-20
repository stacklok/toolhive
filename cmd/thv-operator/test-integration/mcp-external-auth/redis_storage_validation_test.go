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

// These tests exercise the CEL XValidation rules on RedisStorageConfig through
// the real apiserver (envtest). They guard against regressions like the one
// where the rules referenced self.addr / self.clusterMode without a has()
// guard and rejected every sentinel-only config with "no such key: addr".
var _ = Describe("MCPExternalAuthConfig RedisStorageConfig CEL validation", func() {
	const namespace = "default"

	// makeAuthConfig returns a minimum-valid embeddedAuthServer config whose
	// only varying piece is the Redis storage block.
	makeAuthConfig := func(name string, redis *mcpv1beta1.RedisStorageConfig) *mcpv1beta1.MCPExternalAuthConfig {
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
						},
					}},
					Storage: &mcpv1beta1.AuthServerStorageConfig{
						Type:  mcpv1beta1.AuthServerStorageTypeRedis,
						Redis: redis,
					},
				},
			},
		}
	}

	aclUserConfig := &mcpv1beta1.RedisACLUserConfig{
		PasswordSecretRef: &mcpv1beta1.SecretKeyRef{
			Name: "redis-credentials",
			Key:  "password",
		},
	}

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	type validationCase struct {
		name        string
		redis       *mcpv1beta1.RedisStorageConfig
		shouldAdmit bool
		errMatch    string // substring expected on rejection; ignored when shouldAdmit is true
	}

	cases := []validationCase{
		{
			name: "sentinel only (the github-mcp-demo case)",
			redis: &mcpv1beta1.RedisStorageConfig{
				SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
					MasterName: "mymaster",
					SentinelService: &mcpv1beta1.SentinelServiceRef{
						Name:      "rfs-redis",
						Namespace: "redis",
					},
				},
				ACLUserConfig: aclUserConfig,
			},
			shouldAdmit: true,
		},
		{
			name: "standalone via addr",
			redis: &mcpv1beta1.RedisStorageConfig{
				Addr:          "redis.example.com:6379",
				ACLUserConfig: aclUserConfig,
			},
			shouldAdmit: true,
		},
		{
			name: "cluster mode with addr",
			redis: &mcpv1beta1.RedisStorageConfig{
				Addr:          "redis-cluster.example.com:6379",
				ClusterMode:   true,
				ACLUserConfig: aclUserConfig,
			},
			shouldAdmit: true,
		},
		{
			name: "neither addr nor sentinelConfig set",
			redis: &mcpv1beta1.RedisStorageConfig{
				ACLUserConfig: aclUserConfig,
			},
			shouldAdmit: false,
			errMatch:    "exactly one of addr or sentinelConfig must be set",
		},
		{
			name: "both addr and sentinelConfig set",
			redis: &mcpv1beta1.RedisStorageConfig{
				Addr: "redis.example.com:6379",
				SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
					MasterName:    "mymaster",
					SentinelAddrs: []string{"sentinel-0.example.com:26379"},
				},
				ACLUserConfig: aclUserConfig,
			},
			shouldAdmit: false,
			errMatch:    "exactly one of addr or sentinelConfig must be set",
		},
		{
			name: "clusterMode without addr",
			redis: &mcpv1beta1.RedisStorageConfig{
				ClusterMode: true,
				SentinelConfig: &mcpv1beta1.RedisSentinelConfig{
					MasterName:    "mymaster",
					SentinelAddrs: []string{"sentinel-0.example.com:26379"},
				},
				ACLUserConfig: aclUserConfig,
			},
			shouldAdmit: false,
			errMatch:    "clusterMode requires addr to be set",
		},
	}

	for i, c := range cases {
		name := fmt.Sprintf("redis-storage-validation-%d", i)
		It(c.name, func() {
			cfg := makeAuthConfig(name, c.redis)
			err := k8sClient.Create(ctx, cfg)
			if c.shouldAdmit {
				Expect(err).NotTo(HaveOccurred(),
					"expected apiserver to admit config: %s", c.name)
				DeferCleanup(func() {
					Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
				})
				return
			}
			Expect(err).To(HaveOccurred(),
				"expected apiserver to reject config: %s", c.name)
			Expect(err.Error()).To(ContainSubstring(c.errMatch))
		})
	}
})
