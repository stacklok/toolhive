// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetSetupConfigFromEnv(t *testing.T) {
	t.Run("uses default values when env vars not set", func(t *testing.T) {
		// Use t.Setenv to set empty values, which ensures isolation
		// and automatic cleanup. Setting to empty string unsets the var.
		t.Setenv("POD_NAMESPACE", "")
		t.Setenv("WEBHOOK_SERVICE_NAME", "")
		t.Setenv("WEBHOOK_CONFIG_NAME", "")
		t.Setenv("ENABLE_WEBHOOKS", "")

		cfg := GetSetupConfigFromEnv()

		assert.Equal(t, "toolhive-operator-system", cfg.Namespace)
		assert.Equal(t, "toolhive-operator-webhook-service", cfg.ServiceName)
		assert.Equal(t, "toolhive-operator-validating-webhook-configuration", cfg.WebhookConfigName)
		assert.False(t, cfg.Enabled) // Defaults to false when ENABLE_WEBHOOKS is not set
	})

	t.Run("uses custom values from env vars", func(t *testing.T) {
		// t.Setenv automatically restores original values after test
		t.Setenv("POD_NAMESPACE", "custom-namespace")
		t.Setenv("WEBHOOK_SERVICE_NAME", "custom-service")
		t.Setenv("WEBHOOK_CONFIG_NAME", "custom-config")
		t.Setenv("ENABLE_WEBHOOKS", "true")

		cfg := GetSetupConfigFromEnv()

		assert.Equal(t, "custom-namespace", cfg.Namespace)
		assert.Equal(t, "custom-service", cfg.ServiceName)
		assert.Equal(t, "custom-config", cfg.WebhookConfigName)
		assert.True(t, cfg.Enabled)
	})

	t.Run("disables webhooks when ENABLE_WEBHOOKS is false", func(t *testing.T) {
		t.Setenv("ENABLE_WEBHOOKS", "false")

		cfg := GetSetupConfigFromEnv()

		assert.False(t, cfg.Enabled)
	})
}

func TestSetup(t *testing.T) {
	t.Parallel()

	t.Run("skips setup when disabled", func(t *testing.T) {
		t.Parallel()

		cfg := SetupConfig{
			Enabled: false,
		}

		err := Setup(context.Background(), cfg, nil)
		assert.NoError(t, err)
	})

	t.Run("generates certificates when enabled", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()

		cfg := SetupConfig{
			ServiceName: "test-service",
			Namespace:   "test-namespace",
		}

		// Create a cert generator with custom cert directory for isolated testing
		certGen := &CertGenerator{
			CertDir:     tempDir,
			ServiceName: cfg.ServiceName,
			Namespace:   cfg.Namespace,
		}

		// Generate certificates directly for testing
		_, err := certGen.EnsureCertificates()
		require.NoError(t, err)

		// Verify certificates were created
		assert.True(t, certGen.CertificatesExist())
	})
}

func TestInjectCABundle(t *testing.T) {
	t.Parallel()

	t.Run("successfully injects CA bundle", func(t *testing.T) {
		t.Parallel()

		caBundle := []byte("test-ca-bundle")
		webhookConfigName := "test-webhook-config"

		// Create a fake webhook configuration
		webhookConfig := &admissionv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: webhookConfigName,
			},
			Webhooks: []admissionv1.ValidatingWebhook{
				{
					Name: "test-webhook-1",
					ClientConfig: admissionv1.WebhookClientConfig{
						CABundle: nil,
					},
				},
				{
					Name: "test-webhook-2",
					ClientConfig: admissionv1.WebhookClientConfig{
						CABundle: []byte("old-ca-bundle"),
					},
				},
			},
		}

		// Create fake client with webhook configuration
		scheme := runtime.NewScheme()
		_ = admissionv1.AddToScheme(scheme)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(webhookConfig).
			Build()

		// Inject CA bundle
		err := InjectCABundle(context.Background(), fakeClient, webhookConfigName, caBundle)
		require.NoError(t, err)

		// Verify CA bundle was injected
		updatedConfig := &admissionv1.ValidatingWebhookConfiguration{}
		err = fakeClient.Get(context.Background(), client.ObjectKey{Name: webhookConfigName}, updatedConfig)
		require.NoError(t, err)

		assert.Equal(t, caBundle, updatedConfig.Webhooks[0].ClientConfig.CABundle)
		assert.Equal(t, caBundle, updatedConfig.Webhooks[1].ClientConfig.CABundle)
	})

	t.Run("returns error when webhook config does not exist", func(t *testing.T) {
		t.Parallel()

		caBundle := []byte("test-ca-bundle")
		webhookConfigName := "nonexistent-webhook-config"

		// Create fake client without webhook configuration
		scheme := runtime.NewScheme()
		_ = admissionv1.AddToScheme(scheme)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		// Attempt to inject CA bundle
		err := InjectCABundle(context.Background(), fakeClient, webhookConfigName, caBundle)
		assert.Error(t, err)
	})

	t.Run("does not update when CA bundle is already set", func(t *testing.T) {
		t.Parallel()

		caBundle := []byte("test-ca-bundle")
		webhookConfigName := "test-webhook-config"

		// Create a fake webhook configuration with CA bundle already set
		webhookConfig := &admissionv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: webhookConfigName,
			},
			Webhooks: []admissionv1.ValidatingWebhook{
				{
					Name: "test-webhook-1",
					ClientConfig: admissionv1.WebhookClientConfig{
						CABundle: caBundle,
					},
				},
			},
		}

		// Create fake client with webhook configuration
		scheme := runtime.NewScheme()
		_ = admissionv1.AddToScheme(scheme)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(webhookConfig).
			Build()

		// Inject CA bundle (should not update since it's already set)
		err := InjectCABundle(context.Background(), fakeClient, webhookConfigName, caBundle)
		require.NoError(t, err)

		// Verify no error occurred and config remains unchanged
		updatedConfig := &admissionv1.ValidatingWebhookConfiguration{}
		err = fakeClient.Get(context.Background(), client.ObjectKey{Name: webhookConfigName}, updatedConfig)
		require.NoError(t, err)

		assert.Equal(t, caBundle, updatedConfig.Webhooks[0].ClientConfig.CABundle)
	})
}
