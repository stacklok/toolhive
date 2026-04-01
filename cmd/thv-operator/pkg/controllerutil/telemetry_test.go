// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestGenerateOpenTelemetryEnvVarsFromRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		telemetryConfig  *mcpv1alpha1.MCPTelemetryConfig
		ref              *mcpv1alpha1.MCPTelemetryConfigReference
		resourceName     string
		namespace        string
		expectedEnvVars  []corev1.EnvVar
		expectedNilSlice bool
	}{
		{
			name:             "nil telemetryConfig returns nil",
			telemetryConfig:  nil,
			ref:              &mcpv1alpha1.MCPTelemetryConfigReference{Name: "test-config"},
			resourceName:     "my-server",
			namespace:        "default",
			expectedNilSlice: true,
		},
		{
			name: "nil ref returns nil",
			telemetryConfig: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: mcpv1alpha1.MCPTelemetryConfigSpec{},
			},
			ref:              nil,
			resourceName:     "my-server",
			namespace:        "default",
			expectedNilSlice: true,
		},
		{
			name: "basic case with service name override",
			telemetryConfig: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: mcpv1alpha1.MCPTelemetryConfigSpec{},
			},
			ref: &mcpv1alpha1.MCPTelemetryConfigReference{
				Name:        "test-config",
				ServiceName: "custom-service",
			},
			resourceName: "my-server",
			namespace:    "production",
			expectedEnvVars: []corev1.EnvVar{
				{
					Name:  "OTEL_RESOURCE_ATTRIBUTES",
					Value: "service.name=custom-service,service.namespace=production",
				},
			},
		},
		{
			name: "empty ServiceName in ref falls back to resourceName",
			telemetryConfig: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: mcpv1alpha1.MCPTelemetryConfigSpec{},
			},
			ref: &mcpv1alpha1.MCPTelemetryConfigReference{
				Name:        "test-config",
				ServiceName: "",
			},
			resourceName: "fallback-server",
			namespace:    "default",
			expectedEnvVars: []corev1.EnvVar{
				{
					Name:  "OTEL_RESOURCE_ATTRIBUTES",
					Value: "service.name=fallback-server,service.namespace=default",
				},
			},
		},
		{
			name: "sensitive headers produce env vars with SecretKeyRef",
			telemetryConfig: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: mcpv1alpha1.MCPTelemetryConfigSpec{
					SensitiveHeaders: []mcpv1alpha1.SensitiveHeader{
						{
							Name: "Authorization",
							SecretKeyRef: mcpv1alpha1.SecretKeyRef{
								Name: "otel-secret",
								Key:  "auth-token",
							},
						},
						{
							Name: "X-API-Key",
							SecretKeyRef: mcpv1alpha1.SecretKeyRef{
								Name: "api-secrets",
								Key:  "api-key",
							},
						},
					},
				},
			},
			ref: &mcpv1alpha1.MCPTelemetryConfigReference{
				Name:        "test-config",
				ServiceName: "my-service",
			},
			resourceName: "my-server",
			namespace:    "default",
			expectedEnvVars: []corev1.EnvVar{
				{
					Name:  "OTEL_RESOURCE_ATTRIBUTES",
					Value: "service.name=my-service,service.namespace=default",
				},
				{
					Name: "TOOLHIVE_OTEL_HEADER_AUTHORIZATION",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "otel-secret",
							},
							Key: "auth-token",
						},
					},
				},
				{
					Name: "TOOLHIVE_OTEL_HEADER_X_API_KEY",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "api-secrets",
							},
							Key: "api-key",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := GenerateOpenTelemetryEnvVarsFromRef(
				tt.telemetryConfig, tt.ref, tt.resourceName, tt.namespace,
			)

			if tt.expectedNilSlice {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expectedEnvVars, result)
		})
	}
}

func TestNormalizeHeaderEnvVarName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple lowercase",
			input:    "authorization",
			expected: "AUTHORIZATION",
		},
		{
			name:     "dashes become underscores",
			input:    "X-API-Key",
			expected: "X_API_KEY",
		},
		{
			name:     "already uppercase with dashes",
			input:    "X-CUSTOM-HEADER",
			expected: "X_CUSTOM_HEADER",
		},
		{
			name:     "no dashes",
			input:    "Authorization",
			expected: "AUTHORIZATION",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := normalizeHeaderEnvVarName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
