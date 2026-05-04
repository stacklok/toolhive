// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/webhook"
)

func TestWebhookConfigHelpers(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	timeout := metav1.Duration{Duration: 10 * time.Second}

	config := &mcpv1alpha1.MCPWebhookConfig{
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
	ctx := t.Context()

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
		assert.Empty(t, envVars)
	})

	t.Run("GenerateWebhookVolumes", func(t *testing.T) {
		t.Parallel()
		volumes, mounts, err := GenerateWebhookVolumes(ctx, fakeClient, "default", server.Spec.WebhookConfigRef)
		require.NoError(t, err)
		require.Len(t, volumes, 4)
		require.Len(t, mounts, 4)

		hmacIdentifier := webhookSecretIdentifier("validating", "val1", webhookSecretPurposeHMAC,
			config.Spec.Validating[0].HMACSecretRef)
		caIdentifier := webhookSecretIdentifier(string(webhook.TypeMutating), "mut1", webhookSecretPurposeTLSCA,
			config.Spec.Mutating[0].TLSConfig.CASecretRef)
		clientCertIdentifier := webhookSecretIdentifier(string(webhook.TypeMutating), "mut1",
			webhookSecretPurposeTLSClientCert, config.Spec.Mutating[0].TLSConfig.ClientCertSecretRef)
		clientKeyIdentifier := webhookSecretIdentifier(string(webhook.TypeMutating), "mut1",
			webhookSecretPurposeTLSClientKey, config.Spec.Mutating[0].TLSConfig.ClientKeySecretRef)

		assert.Equal(t, []string{
			webhookSecretMountPath(caIdentifier, webhookCASecretFileName),
			webhookSecretMountPath(clientCertIdentifier, webhookClientCertSecretFileName),
			webhookSecretMountPath(clientKeyIdentifier, webhookClientKeySecretFileName),
			webhookSecretMountPath(hmacIdentifier, webhookHMACSecretFileName),
		}, []string{mounts[0].MountPath, mounts[1].MountPath, mounts[2].MountPath, mounts[3].MountPath})
		assert.Equal(t, []string{"ca.crt", "tls.crt", "tls.key", "hmac"},
			[]string{mounts[0].SubPath, mounts[1].SubPath, mounts[2].SubPath, mounts[3].SubPath})
		for _, mount := range mounts {
			assert.NotContains(t, mount.MountPath, "hmac-secret")
			assert.NotContains(t, mount.MountPath, "client-secret")
			assert.NotContains(t, mount.MountPath, "ca-secret")
		}
	})

	t.Run("GenerateWebhookVolumes configRef is nil", func(t *testing.T) {
		t.Parallel()
		volumes, mounts, err := GenerateWebhookVolumes(ctx, fakeClient, "default", nil)
		require.NoError(t, err)
		assert.Nil(t, volumes)
		assert.Nil(t, mounts)
	})

	t.Run("GenerateWebhookVolumes get error", func(t *testing.T) {
		t.Parallel()
		invalidRef := &mcpv1beta1.WebhookConfigRef{Name: "not-found"}
		volumes, mounts, err := GenerateWebhookVolumes(ctx, fakeClient, "default", invalidRef)
		require.Error(t, err)
		assert.Nil(t, volumes)
		assert.Nil(t, mounts)
		assert.Contains(t, err.Error(), "failed to get MCPWebhookConfig")
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
		assert.Equal(t,
			webhookSecretMountPath(
				webhookSecretIdentifier(string(webhook.TypeValidating), "val1", webhookSecretPurposeHMAC,
					config.Spec.Validating[0].HMACSecretRef),
				webhookHMACSecretFileName,
			),
			validatingConfig.HMACSecretRef)

		mutatingConfig := buildWebhookConfig(string(webhook.TypeMutating), config.Spec.Mutating[0])
		assert.Equal(t,
			webhookSecretMountPath(
				webhookSecretIdentifier(string(webhook.TypeMutating), "mut1", webhookSecretPurposeTLSCA,
					config.Spec.Mutating[0].TLSConfig.CASecretRef),
				webhookCASecretFileName,
			),
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

	t.Run("AddWebhookConfigOptions invalid config", func(t *testing.T) {
		t.Parallel()

		invalidConfig := &mcpv1alpha1.MCPWebhookConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "invalid-webhook",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPWebhookConfigSpec{
				Validating: []mcpv1beta1.WebhookSpec{{
					Name: "invalid-http",
					URL:  "http://policy.example.com",
				}},
			},
		}
		localClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(invalidConfig).
			Build()

		var opts []runner.RunConfigBuilderOption
		err := AddWebhookConfigOptions(ctx, localClient, "default",
			&mcpv1beta1.WebhookConfigRef{Name: invalidConfig.Name}, &opts)
		require.Error(t, err)
		assert.Empty(t, opts)
		assert.Contains(t, err.Error(), "invalid MCPWebhookConfig invalid-webhook")
		assert.Contains(t, err.Error(), "must use HTTPS")
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

func TestBuildWebhookConfig(t *testing.T) {
	t.Parallel()

	timeout := metav1.Duration{Duration: 3 * time.Second}
	spec := mcpv1beta1.WebhookSpec{
		Name:          "mutate",
		URL:           "https://mutate.example.com",
		Timeout:       &timeout,
		FailurePolicy: mcpv1beta1.WebhookFailurePolicyIgnore,
		HMACSecretRef: &mcpv1beta1.SecretKeyRef{Name: "hmac-secret", Key: "hmac.key"},
		TLSConfig: &mcpv1beta1.WebhookTLSConfig{
			InsecureSkipVerify:  true,
			CASecretRef:         &mcpv1beta1.SecretKeyRef{Name: "ca-secret", Key: "ca.crt"},
			ClientCertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "client-secret", Key: "tls.crt"},
		},
	}

	cfg := buildWebhookConfig(string(webhook.TypeMutating), spec)

	assert.Equal(t, spec.Name, cfg.Name)
	assert.Equal(t, spec.URL, cfg.URL)
	assert.Equal(t, timeout.Duration, cfg.Timeout)
	assert.Equal(t, webhook.FailurePolicyIgnore, cfg.FailurePolicy)
	assert.NotEmpty(t, cfg.HMACSecretRef)
	require.NotNil(t, cfg.TLSConfig)
	assert.True(t, cfg.TLSConfig.InsecureSkipVerify)
	assert.NotEmpty(t, cfg.TLSConfig.CABundlePath)
	assert.Empty(t, cfg.TLSConfig.ClientCertPath, "client cert path requires a matching client key")
	assert.Empty(t, cfg.TLSConfig.ClientKeyPath, "client key path requires a matching client cert")
}
