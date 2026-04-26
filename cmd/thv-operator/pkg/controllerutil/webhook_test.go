// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/webhook"
)

func TestWebhookConfigHelpers(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	timeout := metav1.Duration{Duration: 10 * time.Second}

	config := &mcpv1beta1.MCPWebhookConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-webhook",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPWebhookConfigSpec{
			Validating: []mcpv1beta1.WebhookSpec{
				{
					Name:          "val1",
					URL:           "https://val1",
					Timeout:       &timeout,
					HMACSecretRef: &mcpv1beta1.SecretKeyRef{Name: "hmac-secret", Key: "key"},
				},
			},
			Mutating: []mcpv1beta1.WebhookSpec{
				{
					Name: "mut1",
					URL:  "https://mut1",
					TLSConfig: &mcpv1beta1.WebhookTLSConfig{
						CASecretRef:         &mcpv1beta1.SecretKeyRef{Name: "ca-secret", Key: "ca.crt"},
						ClientCertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "client-secret", Key: "tls.crt"},
						ClientKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "client-secret", Key: "tls.key"},
					},
				},
			},
		},
	}

	server := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			WebhookConfigRef: &mcpv1beta1.WebhookConfigRef{Name: "test-webhook"},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config, server).Build()
	ctx := context.Background()

	t.Run("GetWebhookConfigByName", func(t *testing.T) {
		t.Parallel()
		res, err := GetWebhookConfigByName(ctx, fakeClient, "default", "test-webhook")
		require.NoError(t, err)
		assert.Equal(t, "test-webhook", res.Name)

		_, err = GetWebhookConfigByName(ctx, fakeClient, "default", "not-found")
		assert.Error(t, err)
	})

	t.Run("GetWebhookConfigForMCPServer", func(t *testing.T) {
		t.Parallel()
		res, err := GetWebhookConfigForMCPServer(ctx, fakeClient, server)
		require.NoError(t, err)
		assert.Equal(t, "test-webhook", res.Name)

		serverNoRef := &mcpv1beta1.MCPServer{}
		resNoRef, errNoRef := GetWebhookConfigForMCPServer(ctx, fakeClient, serverNoRef)
		require.NoError(t, errNoRef)
		assert.Nil(t, resNoRef)
	})

	t.Run("GenerateWebhookEnvVars", func(t *testing.T) {
		t.Parallel()
		envVars, err := GenerateWebhookEnvVars(ctx, fakeClient, "default", server.Spec.WebhookConfigRef)
		require.NoError(t, err)
		require.Len(t, envVars, 4)

		var keys []string
		var names []string
		for _, e := range envVars {
			keys = append(keys, e.ValueFrom.SecretKeyRef.Name)
			names = append(names, e.Name)
		}
		assert.Contains(t, keys, "hmac-secret")
		assert.Contains(t, keys, "ca-secret")
		assert.Contains(t, keys, "client-secret")
		assert.Equal(t, []string{
			"TOOLHIVE_SECRET_WEBHOOK_MUTATING_MUT1_TLS_CA_CA_SECRET_CA_CRT",
			"TOOLHIVE_SECRET_WEBHOOK_MUTATING_MUT1_TLS_CLIENT_CERT_CLIENT_SECRET_TLS_CRT",
			"TOOLHIVE_SECRET_WEBHOOK_MUTATING_MUT1_TLS_CLIENT_KEY_CLIENT_SECRET_TLS_KEY",
			"TOOLHIVE_SECRET_WEBHOOK_VALIDATING_VAL1_HMAC_HMAC_SECRET_KEY",
		}, names)
	})

	t.Run("GenerateWebhookVolumes", func(t *testing.T) {
		t.Parallel()
		volumes, mounts, err := GenerateWebhookVolumes(ctx, fakeClient, "default", server.Spec.WebhookConfigRef)
		require.NoError(t, err)
		require.Len(t, volumes, 3)
		require.Len(t, mounts, 3)

		assert.Equal(t, []string{
			"/etc/toolhive/webhooks/webhook_mutating_mut1_tls_ca_ca_secret_ca_crt/ca.crt",
			"/etc/toolhive/webhooks/webhook_mutating_mut1_tls_client_cert_client_secret_tls_crt/tls.crt",
			"/etc/toolhive/webhooks/webhook_mutating_mut1_tls_client_key_client_secret_tls_key/tls.key",
		}, []string{mounts[0].MountPath, mounts[1].MountPath, mounts[2].MountPath})
		assert.Equal(t, []string{"ca.crt", "tls.crt", "tls.key"}, []string{mounts[0].SubPath, mounts[1].SubPath, mounts[2].SubPath})
	})

	t.Run("GenerateWebhookEnvVars configRef is nil", func(t *testing.T) {
		t.Parallel()
		envVars, err := GenerateWebhookEnvVars(ctx, fakeClient, "default", nil)
		require.NoError(t, err)
		assert.Empty(t, envVars)
	})

	t.Run("GenerateWebhookEnvVars get error", func(t *testing.T) {
		t.Parallel()
		// Using a ref that doesn't exist
		invalidRef := &mcpv1beta1.WebhookConfigRef{Name: "not-found"}
		_, err := GenerateWebhookEnvVars(ctx, fakeClient, "default", invalidRef)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get MCPWebhookConfig")
	})

	t.Run("AddWebhookConfigOptions", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		err := AddWebhookConfigOptions(ctx, fakeClient, "default", server.Spec.WebhookConfigRef, &opts)
		require.NoError(t, err)
		assert.Len(t, opts, 2)

		validatingConfig := buildWebhookConfig(string(webhook.TypeValidating), config.Spec.Validating[0])
		assert.Equal(t, webhook.FailurePolicyFail, validatingConfig.FailurePolicy)
		assert.Equal(t, "WEBHOOK_VALIDATING_VAL1_HMAC_HMAC_SECRET_KEY", validatingConfig.HMACSecretRef)

		mutatingConfig := buildWebhookConfig(string(webhook.TypeMutating), config.Spec.Mutating[0])
		assert.Equal(t,
			"/etc/toolhive/webhooks/webhook_mutating_mut1_tls_ca_ca_secret_ca_crt/ca.crt",
			mutatingConfig.TLSConfig.CABundlePath,
		)
	})

	t.Run("AddWebhookConfigOptions configRef is nil", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		err := AddWebhookConfigOptions(ctx, fakeClient, "default", nil, &opts)
		require.NoError(t, err)
		assert.Empty(t, opts)
	})

	t.Run("AddWebhookConfigOptions get error", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		// Using a ref that doesn't exist
		invalidRef := &mcpv1beta1.WebhookConfigRef{Name: "not-found"}
		err := AddWebhookConfigOptions(ctx, fakeClient, "default", invalidRef, &opts)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get MCPWebhookConfig")
	})

	t.Run("ValidateMCPWebhookConfigSpec", func(t *testing.T) {
		t.Parallel()
		err := ValidateMCPWebhookConfigSpec(config.Spec)
		require.NoError(t, err)

		invalid := mcpv1beta1.MCPWebhookConfigSpec{
			Validating: []mcpv1beta1.WebhookSpec{{
				Name: "invalid-http",
				URL:  "http://policy.example.com",
			}},
		}
		err = ValidateMCPWebhookConfigSpec(invalid)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must use HTTPS")
	})
}
