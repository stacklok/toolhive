// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"
	"os"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var setupLogger = log.Log.WithName("webhook-setup")

// SetupConfig contains configuration for webhook setup
type SetupConfig struct {
	// ServiceName is the name of the webhook service
	ServiceName string

	// Namespace is the namespace where the operator runs
	Namespace string

	// WebhookConfigName is the name of the ValidatingWebhookConfiguration
	WebhookConfigName string

	// Enabled controls whether webhooks should be configured
	Enabled bool
}

// Setup performs the complete webhook setup process:
// 1. Generates self-signed certificates
// 2. Injects CA bundle into ValidatingWebhookConfiguration (if it exists)
func Setup(ctx context.Context, cfg SetupConfig, k8sClient client.Client) error {
	if !cfg.Enabled {
		setupLogger.Info("Webhooks are disabled, skipping certificate generation")
		return nil
	}

	setupLogger.Info("Setting up webhook certificates", "service", cfg.ServiceName, "namespace", cfg.Namespace)

	// Generate certificates
	certGen := NewCertGenerator(cfg.ServiceName, cfg.Namespace)
	caBundle, err := certGen.EnsureCertificates()
	if err != nil {
		return fmt.Errorf("failed to ensure certificates: %w", err)
	}

	setupLogger.Info("Successfully ensured webhook certificates exist")

	// If k8sClient is provided, try to inject CA bundle into webhook configuration
	if k8sClient != nil && cfg.WebhookConfigName != "" {
		if err := InjectCABundle(ctx, k8sClient, cfg.WebhookConfigName, caBundle); err != nil {
			// Log error but don't fail - the webhook configuration might not exist yet
			// (e.g., during initial operator deployment)
			setupLogger.Info("Could not inject CA bundle into webhook configuration (this is expected during initial deployment)",
				"webhookConfig", cfg.WebhookConfigName,
				"error", err.Error(),
			)
		} else {
			setupLogger.Info("Successfully injected CA bundle into webhook configuration",
				"webhookConfig", cfg.WebhookConfigName,
			)
		}
	}

	return nil
}

// InjectCABundle injects the CA bundle into a ValidatingWebhookConfiguration
func InjectCABundle(ctx context.Context, k8sClient client.Client, webhookConfigName string, caBundle []byte) error {
	// Use exponential backoff to retry fetching the webhook configuration
	// This handles cases where the resource might not be immediately available
	// after operator deployment, without adding unnecessary latency when it exists
	backoff := wait.Backoff{
		Duration: 100 * time.Millisecond, // Start with 100ms
		Factor:   2.0,                    // Double each retry
		Jitter:   0.1,                    // Add 10% jitter to prevent thundering herd
		Steps:    5,                      // Retry up to 5 times (total ~3.1 seconds max)
	}

	var webhookConfig *admissionv1.ValidatingWebhookConfiguration

	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		webhookConfig = &admissionv1.ValidatingWebhookConfiguration{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: webhookConfigName}, webhookConfig); err != nil {
			if apierrors.IsNotFound(err) {
				// Resource not found, retry
				setupLogger.V(1).Info("ValidatingWebhookConfiguration not found, will retry", "webhookConfig", webhookConfigName)
				return false, nil
			}
			// Other errors might be transient (e.g., connection issues), retry
			setupLogger.V(1).Info("Transient error fetching ValidatingWebhookConfiguration, will retry", "error", err.Error())
			return false, nil
		}
		// Success
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to get ValidatingWebhookConfiguration after retries: %w", err)
	}

	// Update CA bundle for all webhooks
	updated := false
	for i := range webhookConfig.Webhooks {
		if webhookConfig.Webhooks[i].ClientConfig.CABundle == nil ||
			string(webhookConfig.Webhooks[i].ClientConfig.CABundle) != string(caBundle) {
			webhookConfig.Webhooks[i].ClientConfig.CABundle = caBundle
			updated = true
		}
	}

	// Only update if something changed
	if updated {
		if err := k8sClient.Update(ctx, webhookConfig); err != nil {
			return fmt.Errorf("failed to update ValidatingWebhookConfiguration: %w", err)
		}
		setupLogger.Info("Updated CA bundle for all webhooks in configuration", "webhookConfig", webhookConfigName)
	} else {
		setupLogger.Info("CA bundle already up to date", "webhookConfig", webhookConfigName)
	}

	return nil
}

// GetSetupConfigFromEnv creates a SetupConfig from environment variables
func GetSetupConfigFromEnv() SetupConfig {
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "toolhive-operator-system"
	}

	serviceName := os.Getenv("WEBHOOK_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "toolhive-operator-webhook-service"
	}

	webhookConfigName := os.Getenv("WEBHOOK_CONFIG_NAME")
	if webhookConfigName == "" {
		webhookConfigName = "toolhive-operator-validating-webhook-configuration"
	}

	// Check if webhooks are enabled (default to false to match Helm chart default)
	// When webhooks are disabled, the webhook volume is not mounted and the read-only
	// filesystem prevents certificate generation, so defaulting to true would cause crashes.
	enabled := false
	if envVal := os.Getenv("ENABLE_WEBHOOKS"); envVal != "" {
		enabled = envVal == "true" || envVal == "1"
	}

	return SetupConfig{
		ServiceName:       serviceName,
		Namespace:         namespace,
		WebhookConfigName: webhookConfigName,
		Enabled:           enabled,
	}
}
