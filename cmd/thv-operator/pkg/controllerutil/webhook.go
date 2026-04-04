// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/webhook"
)

// GetWebhookConfigByName retrieves a MCPWebhookConfig from the cluster by its name
func GetWebhookConfigByName(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
) (*mcpv1alpha1.MCPWebhookConfig, error) {
	var config mcpv1alpha1.MCPWebhookConfig
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// GetWebhookConfigForMCPServer retrieves the MCPWebhookConfig referenced by an MCPServer
func GetWebhookConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	mcpServer *mcpv1alpha1.MCPServer,
) (*mcpv1alpha1.MCPWebhookConfig, error) {
	if mcpServer.Spec.WebhookConfigRef == nil {
		return nil, nil
	}

	return GetWebhookConfigByName(ctx, c, mcpServer.Namespace, mcpServer.Spec.WebhookConfigRef.Name)
}

// GenerateWebhookEnvVars generates environment variables for webhook secret references.
// These expose the necessary secrets as TOOLHIVE_SECRET_{secret-name} so they can be
// correctly extracted and processed by the runner environment provider.
func GenerateWebhookEnvVars(
	ctx context.Context,
	c client.Client,
	namespace string,
	webhookConfigRef *mcpv1alpha1.WebhookConfigRef,
) ([]corev1.EnvVar, error) {
	var envVars []corev1.EnvVar
	if webhookConfigRef == nil {
		return envVars, nil
	}

	config, err := GetWebhookConfigByName(ctx, c, namespace, webhookConfigRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPWebhookConfig: %w", err)
	}

	// We collect secrets to avoid duplicates
	secretsToExpose := make(map[string]*mcpv1alpha1.SecretKeyRef)

	addSecrets := func(webhooks []mcpv1alpha1.WebhookSpec) {
		for _, w := range webhooks {
			if w.HMACSecretRef != nil {
				secretsToExpose[w.HMACSecretRef.Name] = w.HMACSecretRef
			}
			if w.TLSConfig != nil {
				if w.TLSConfig.CASecretRef != nil {
					secretsToExpose[w.TLSConfig.CASecretRef.Name] = w.TLSConfig.CASecretRef
				}
				if w.TLSConfig.ClientCertSecretRef != nil {
					secretsToExpose[w.TLSConfig.ClientCertSecretRef.Name] = w.TLSConfig.ClientCertSecretRef
				}
				if w.TLSConfig.ClientKeySecretRef != nil {
					secretsToExpose[w.TLSConfig.ClientKeySecretRef.Name] = w.TLSConfig.ClientKeySecretRef
				}
			}
		}
	}

	addSecrets(config.Spec.Validating)
	addSecrets(config.Spec.Mutating)

	for name, ref := range secretsToExpose {
		envVarName := fmt.Sprintf("TOOLHIVE_SECRET_%s", name)
		envVars = append(envVars, corev1.EnvVar{
			Name: envVarName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ref.Name,
					},
					Key: ref.Key,
				},
			},
		})
	}

	return envVars, nil
}

// buildWebhookConfig generates a runner webhook.Config from an API WebhookSpec
func buildWebhookConfig(spec mcpv1alpha1.WebhookSpec) webhook.Config {
	cfg := webhook.Config{
		Name:          spec.Name,
		URL:           spec.URL,
		FailurePolicy: webhook.FailurePolicy(strings.ToLower(string(spec.FailurePolicy))),
	}

	if spec.Timeout != nil {
		cfg.Timeout = spec.Timeout.Duration
	}

	if spec.HMACSecretRef != nil {
		cfg.HMACSecretRef = fmt.Sprintf("%s,target=hmac", spec.HMACSecretRef.Name)
	}

	if spec.TLSConfig != nil {
		tlsConfig := &webhook.TLSConfig{
			InsecureSkipVerify: spec.TLSConfig.InsecureSkipVerify,
		}
		if spec.TLSConfig.CASecretRef != nil {
			tlsConfig.CABundlePath = fmt.Sprintf("%s,target=ca_bundle", spec.TLSConfig.CASecretRef.Name)
		}
		if spec.TLSConfig.ClientCertSecretRef != nil && spec.TLSConfig.ClientKeySecretRef != nil {
			tlsConfig.ClientCertPath = fmt.Sprintf("%s,target=client_cert", spec.TLSConfig.ClientCertSecretRef.Name)
			tlsConfig.ClientKeyPath = fmt.Sprintf("%s,target=client_key", spec.TLSConfig.ClientKeySecretRef.Name)
		}
		cfg.TLSConfig = tlsConfig
	}

	return cfg
}

// AddWebhookConfigOptions translates an MCPWebhookConfig to run config builder options.
func AddWebhookConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	webhookConfigRef *mcpv1alpha1.WebhookConfigRef,
	options *[]runner.RunConfigBuilderOption,
) error {
	if webhookConfigRef == nil {
		return nil
	}

	config, err := GetWebhookConfigByName(ctx, c, namespace, webhookConfigRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get MCPWebhookConfig: %w", err)
	}

	var validatingWebhooks []webhook.Config
	for _, v := range config.Spec.Validating {
		validatingWebhooks = append(validatingWebhooks, buildWebhookConfig(v))
	}
	if len(validatingWebhooks) > 0 {
		*options = append(*options, runner.WithValidatingWebhooks(validatingWebhooks))
	}

	var mutatingWebhooks []webhook.Config
	for _, m := range config.Spec.Mutating {
		mutatingWebhooks = append(mutatingWebhooks, buildWebhookConfig(m))
	}
	if len(mutatingWebhooks) > 0 {
		*options = append(*options, runner.WithMutatingWebhooks(mutatingWebhooks))
	}

	return nil
}
