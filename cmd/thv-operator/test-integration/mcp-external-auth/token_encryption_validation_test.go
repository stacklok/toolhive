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

// These tests exercise the CEL XValidation rules on TokenEncryptionConfig and
// the storage-type gate through the real apiserver (envtest):
//   - keySecretRef.name must not be empty (the reconcile-time resolver fails
//     closed on an empty name; admission must reject it first);
//   - tokenEncryption requires storage type 'redis' (the RunConfig shape and
//     untrusted-mode egress only support the Redis backend).
var _ = Describe("MCPExternalAuthConfig tokenEncryption CEL validation", func() {
	const namespace = "default"

	// makeAuthConfig returns a minimum-valid embeddedAuthServer config whose
	// only varying piece is the storage block.
	makeAuthConfig := func(name string, storage *mcpv1beta1.AuthServerStorageConfig) *mcpv1beta1.MCPExternalAuthConfig {
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
					Storage: storage,
				},
			},
		}
	}

	redisStorage := func(te *mcpv1beta1.TokenEncryptionConfig) *mcpv1beta1.AuthServerStorageConfig {
		return &mcpv1beta1.AuthServerStorageConfig{
			Type: mcpv1beta1.AuthServerStorageTypeRedis,
			Redis: &mcpv1beta1.RedisStorageConfig{
				Addr: "redis.example.com:6379",
				ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
					PasswordSecretRef: &mcpv1beta1.SecretKeyRef{
						Name: "redis-credentials",
						Key:  "password",
					},
				},
			},
			TokenEncryption: te,
		}
	}

	BeforeEach(func() {
		_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	})

	type validationCase struct {
		name        string
		storage     *mcpv1beta1.AuthServerStorageConfig
		shouldAdmit bool
		errMatch    string // substring expected on rejection; ignored when shouldAdmit is true
	}

	cases := []validationCase{
		{
			name: "tokenEncryption on redis storage with a named keySecretRef",
			storage: redisStorage(&mcpv1beta1.TokenEncryptionConfig{
				ActiveKeyID:  "kek-1",
				KeySecretRef: corev1.LocalObjectReference{Name: "my-kek-secret"},
			}),
			shouldAdmit: true,
		},
		{
			name:        "no tokenEncryption on redis storage",
			storage:     redisStorage(nil),
			shouldAdmit: true,
		},
		{
			name: "empty keySecretRef.name is rejected",
			storage: redisStorage(&mcpv1beta1.TokenEncryptionConfig{
				ActiveKeyID:  "kek-1",
				KeySecretRef: corev1.LocalObjectReference{Name: ""},
			}),
			shouldAdmit: false,
			errMatch:    "keySecretRef.name must not be empty",
		},
		{
			name: "tokenEncryption on memory storage is rejected",
			storage: &mcpv1beta1.AuthServerStorageConfig{
				Type: mcpv1beta1.AuthServerStorageTypeMemory,
				TokenEncryption: &mcpv1beta1.TokenEncryptionConfig{
					ActiveKeyID:  "kek-1",
					KeySecretRef: corev1.LocalObjectReference{Name: "my-kek-secret"},
				},
			},
			shouldAdmit: false,
			errMatch:    "tokenEncryption requires storage type 'redis'",
		},
	}

	for i, c := range cases {
		name := fmt.Sprintf("token-encryption-validation-%d", i)
		It(c.name, func() {
			cfg := makeAuthConfig(name, c.storage)
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
