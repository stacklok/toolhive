// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// GenerateOpenTelemetryEnvVarsFromRef generates OpenTelemetry environment variables
// from an MCPTelemetryConfig resource and its per-server reference overrides.
// This includes OTEL_RESOURCE_ATTRIBUTES and secret-backed sensitive header env vars.
func GenerateOpenTelemetryEnvVarsFromRef(
	telemetryConfig *mcpv1alpha1.MCPTelemetryConfig,
	ref *mcpv1alpha1.MCPTelemetryConfigReference,
	resourceName string,
	namespace string,
) []corev1.EnvVar {
	if telemetryConfig == nil || ref == nil {
		return nil
	}

	serviceName := ref.ServiceName
	if serviceName == "" {
		serviceName = resourceName
	}

	envVars := []corev1.EnvVar{{
		Name:  "OTEL_RESOURCE_ATTRIBUTES",
		Value: fmt.Sprintf("service.name=%s,service.namespace=%s", serviceName, namespace),
	}}

	// Inject sensitive headers as env vars so the proxy runner can merge them
	// into the OTLP exporter at startup. Each header becomes:
	//   TOOLHIVE_OTEL_HEADER_<NORMALIZED_NAME>=<secret value>
	if telemetryConfig.Spec.OpenTelemetry != nil {
		for _, sh := range telemetryConfig.Spec.OpenTelemetry.SensitiveHeaders {
			envVarName := "TOOLHIVE_OTEL_HEADER_" + normalizeHeaderEnvVarName(sh.Name)
			envVars = append(envVars, corev1.EnvVar{
				Name: envVarName,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: sh.SecretKeyRef.Name,
						},
						Key: sh.SecretKeyRef.Key,
					},
				},
			})
		}
	}

	return envVars
}

// normalizeHeaderEnvVarName converts a header name to a valid env var suffix.
// Dashes become underscores and the result is uppercased.
func normalizeHeaderEnvVarName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}
