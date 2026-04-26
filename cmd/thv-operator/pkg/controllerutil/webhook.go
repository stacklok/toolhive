// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/webhook"
)

const webhookMountBasePath = "/etc/toolhive/webhooks"

// GetWebhookConfigByName retrieves a MCPWebhookConfig from the cluster by its name
func GetWebhookConfigByName(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
) (*mcpv1beta1.MCPWebhookConfig, error) {
	var config mcpv1beta1.MCPWebhookConfig
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// ValidateMCPWebhookConfigSpec validates webhook specs against runtime semantic constraints.
// It intentionally skips filesystem existence checks because the operator mounts TLS secrets later.
func ValidateMCPWebhookConfigSpec(spec mcpv1beta1.MCPWebhookConfigSpec) error {
	var errs []error

	for i, validating := range spec.Validating {
		cfg := buildWebhookConfig(string(webhook.TypeValidating), validating)
		if err := cfg.ValidateDefinition(); err != nil {
			errs = append(errs, fmt.Errorf("validating webhook[%d] %q: %w", i, validating.Name, err))
		}
	}

	for i, mutating := range spec.Mutating {
		cfg := buildWebhookConfig(string(webhook.TypeMutating), mutating)
		if err := cfg.ValidateDefinition(); err != nil {
			errs = append(errs, fmt.Errorf("mutating webhook[%d] %q: %w", i, mutating.Name, err))
		}
	}

	return errors.Join(errs...)
}

// GetWebhookConfigForMCPServer retrieves the MCPWebhookConfig referenced by an MCPServer
func GetWebhookConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	mcpServer *mcpv1beta1.MCPServer,
) (*mcpv1beta1.MCPWebhookConfig, error) {
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
	webhookConfigRef *mcpv1beta1.WebhookConfigRef,
) ([]corev1.EnvVar, error) {
	var envVars []corev1.EnvVar
	if webhookConfigRef == nil {
		return envVars, nil
	}

	config, err := GetWebhookConfigByName(ctx, c, namespace, webhookConfigRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPWebhookConfig: %w", err)
	}

	// We collect secrets to avoid duplicates and sort the final list for stable pod specs.
	secretsToExpose := make(map[string]*mcpv1beta1.SecretKeyRef)

	addSecrets := func(kind string, webhooks []mcpv1beta1.WebhookSpec) {
		for _, w := range webhooks {
			if w.HMACSecretRef != nil {
				secretsToExpose[webhookSecretIdentifier(kind, w.Name, "hmac", w.HMACSecretRef)] = w.HMACSecretRef
			}
			if w.TLSConfig != nil {
				if w.TLSConfig.CASecretRef != nil {
					secretsToExpose[webhookSecretIdentifier(kind, w.Name, "tls_ca", w.TLSConfig.CASecretRef)] =
						w.TLSConfig.CASecretRef
				}
				if w.TLSConfig.ClientCertSecretRef != nil {
					secretsToExpose[webhookSecretIdentifier(kind, w.Name, "tls_client_cert",
						w.TLSConfig.ClientCertSecretRef)] = w.TLSConfig.ClientCertSecretRef
				}
				if w.TLSConfig.ClientKeySecretRef != nil {
					secretsToExpose[webhookSecretIdentifier(kind, w.Name, "tls_client_key",
						w.TLSConfig.ClientKeySecretRef)] = w.TLSConfig.ClientKeySecretRef
				}
			}
		}
	}

	addSecrets("validating", config.Spec.Validating)
	addSecrets("mutating", config.Spec.Mutating)

	secretIdentifiers := make([]string, 0, len(secretsToExpose))
	for identifier := range secretsToExpose {
		secretIdentifiers = append(secretIdentifiers, identifier)
	}
	slices.Sort(secretIdentifiers)

	for _, identifier := range secretIdentifiers {
		ref := secretsToExpose[identifier]
		envVars = append(envVars, corev1.EnvVar{
			Name: fmt.Sprintf("TOOLHIVE_SECRET_%s", identifier),
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

// GenerateWebhookVolumes creates volumes and mounts for TLS-backed webhook secrets.
func GenerateWebhookVolumes(
	ctx context.Context,
	c client.Client,
	namespace string,
	webhookConfigRef *mcpv1beta1.WebhookConfigRef,
) ([]corev1.Volume, []corev1.VolumeMount, error) {
	if webhookConfigRef == nil {
		return nil, nil, nil
	}

	config, err := GetWebhookConfigByName(ctx, c, namespace, webhookConfigRef.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get MCPWebhookConfig: %w", err)
	}

	type secretMount struct {
		ref       *mcpv1beta1.SecretKeyRef
		mountPath string
		fileName  string
	}

	mountsByIdentifier := make(map[string]secretMount)

	addSecretMounts := func(kind string, webhooks []mcpv1beta1.WebhookSpec) {
		for _, wh := range webhooks {
			if wh.TLSConfig == nil {
				continue
			}
			if wh.TLSConfig.CASecretRef != nil {
				identifier := webhookSecretIdentifier(kind, wh.Name, "tls_ca", wh.TLSConfig.CASecretRef)
				mountsByIdentifier[identifier] = secretMount{
					ref:       wh.TLSConfig.CASecretRef,
					mountPath: webhookTLSSecretMountPath(identifier, wh.TLSConfig.CASecretRef),
					fileName:  webhookSecretFileName(wh.TLSConfig.CASecretRef),
				}
			}
			if wh.TLSConfig.ClientCertSecretRef != nil {
				identifier := webhookSecretIdentifier(kind, wh.Name, "tls_client_cert", wh.TLSConfig.ClientCertSecretRef)
				mountsByIdentifier[identifier] = secretMount{
					ref:       wh.TLSConfig.ClientCertSecretRef,
					mountPath: webhookTLSSecretMountPath(identifier, wh.TLSConfig.ClientCertSecretRef),
					fileName:  webhookSecretFileName(wh.TLSConfig.ClientCertSecretRef),
				}
			}
			if wh.TLSConfig.ClientKeySecretRef != nil {
				identifier := webhookSecretIdentifier(kind, wh.Name, "tls_client_key", wh.TLSConfig.ClientKeySecretRef)
				mountsByIdentifier[identifier] = secretMount{
					ref:       wh.TLSConfig.ClientKeySecretRef,
					mountPath: webhookTLSSecretMountPath(identifier, wh.TLSConfig.ClientKeySecretRef),
					fileName:  webhookSecretFileName(wh.TLSConfig.ClientKeySecretRef),
				}
			}
		}
	}

	addSecretMounts(string(webhook.TypeValidating), config.Spec.Validating)
	addSecretMounts(string(webhook.TypeMutating), config.Spec.Mutating)

	identifiers := make([]string, 0, len(mountsByIdentifier))
	for identifier := range mountsByIdentifier {
		identifiers = append(identifiers, identifier)
	}
	slices.Sort(identifiers)

	volumes := make([]corev1.Volume, 0, len(identifiers))
	volumeMounts := make([]corev1.VolumeMount, 0, len(identifiers))

	for _, identifier := range identifiers {
		entry := mountsByIdentifier[identifier]
		volumeName := webhookVolumeName(identifier)

		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: entry.ref.Name,
					Items: []corev1.KeyToPath{{
						Key:  entry.ref.Key,
						Path: entry.fileName,
					}},
				},
			},
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: entry.mountPath,
			SubPath:   entry.fileName,
			ReadOnly:  true,
		})
	}

	return volumes, volumeMounts, nil
}

func webhookSecretIdentifier(kind, webhookName, purpose string, ref *mcpv1beta1.SecretKeyRef) string {
	parts := []string{"WEBHOOK", kind, webhookName, purpose, ref.Name, ref.Key}
	sanitized := strings.ToUpper(strings.Join(parts, "_"))
	sanitized = strings.ReplaceAll(sanitized, "-", "_")
	return envVarSanitizer.ReplaceAllString(sanitized, "_")
}

// buildWebhookConfig generates a runner webhook.Config from an API WebhookSpec
func buildWebhookConfig(kind string, spec mcpv1beta1.WebhookSpec) webhook.Config {
	failurePolicy := webhook.FailurePolicyFail
	if spec.FailurePolicy != "" {
		failurePolicy = webhook.FailurePolicy(strings.ToLower(string(spec.FailurePolicy)))
	}

	cfg := webhook.Config{
		Name:          spec.Name,
		URL:           spec.URL,
		FailurePolicy: failurePolicy,
	}

	if spec.Timeout != nil {
		cfg.Timeout = spec.Timeout.Duration
	}

	if spec.HMACSecretRef != nil {
		cfg.HMACSecretRef = webhookSecretIdentifier(kind, spec.Name, "hmac", spec.HMACSecretRef)
	}

	if spec.TLSConfig != nil {
		tlsConfig := &webhook.TLSConfig{
			InsecureSkipVerify: spec.TLSConfig.InsecureSkipVerify,
		}
		if spec.TLSConfig.CASecretRef != nil {
			tlsConfig.CABundlePath = webhookTLSSecretMountPath(
				webhookSecretIdentifier(kind, spec.Name, "tls_ca", spec.TLSConfig.CASecretRef),
				spec.TLSConfig.CASecretRef,
			)
		}
		if spec.TLSConfig.ClientCertSecretRef != nil && spec.TLSConfig.ClientKeySecretRef != nil {
			tlsConfig.ClientCertPath = webhookTLSSecretMountPath(
				webhookSecretIdentifier(kind, spec.Name, "tls_client_cert", spec.TLSConfig.ClientCertSecretRef),
				spec.TLSConfig.ClientCertSecretRef,
			)
			tlsConfig.ClientKeyPath = webhookTLSSecretMountPath(
				webhookSecretIdentifier(kind, spec.Name, "tls_client_key", spec.TLSConfig.ClientKeySecretRef),
				spec.TLSConfig.ClientKeySecretRef,
			)
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
	webhookConfigRef *mcpv1beta1.WebhookConfigRef,
	options *[]runner.RunConfigBuilderOption,
) error {
	if webhookConfigRef == nil {
		return nil
	}

	config, err := GetWebhookConfigByName(ctx, c, namespace, webhookConfigRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get MCPWebhookConfig: %w", err)
	}

	if err := ValidateMCPWebhookConfigSpec(config.Spec); err != nil {
		return fmt.Errorf("invalid MCPWebhookConfig %s: %w", config.Name, err)
	}

	var validatingWebhooks []webhook.Config
	for _, v := range config.Spec.Validating {
		validatingWebhooks = append(validatingWebhooks, buildWebhookConfig(string(webhook.TypeValidating), v))
	}
	if len(validatingWebhooks) > 0 {
		*options = append(*options, runner.WithValidatingWebhooks(validatingWebhooks))
	}

	var mutatingWebhooks []webhook.Config
	for _, m := range config.Spec.Mutating {
		mutatingWebhooks = append(mutatingWebhooks, buildWebhookConfig(string(webhook.TypeMutating), m))
	}
	if len(mutatingWebhooks) > 0 {
		*options = append(*options, runner.WithMutatingWebhooks(mutatingWebhooks))
	}

	return nil
}

func webhookTLSSecretMountPath(identifier string, ref *mcpv1beta1.SecretKeyRef) string {
	return path.Join(webhookMountBasePath, strings.ToLower(identifier), webhookSecretFileName(ref))
}

func webhookSecretFileName(ref *mcpv1beta1.SecretKeyRef) string {
	fileName := strings.ReplaceAll(ref.Key, "/", "_")
	if fileName == "" {
		return "secret"
	}
	return fileName
}

func webhookVolumeName(identifier string) string {
	hash := CalculateConfigHash(identifier)
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return "whsec-" + strings.ToLower(hash)
}
