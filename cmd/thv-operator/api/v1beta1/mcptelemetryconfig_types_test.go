// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestMCPTelemetryConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    *MCPTelemetryConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "nil openTelemetry passes all validation",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: nil,
				},
			},
			expectErr: false,
		},
		{
			name: "valid config with no caBundleRef",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel.example.com:4317",
						Tracing:  &OpenTelemetryTracingConfig{Enabled: true},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "valid config with caBundleRef",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel.example.com:4317",
						Tracing:  &OpenTelemetryTracingConfig{Enabled: true},
						CABundleRef: &CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-ca-bundle",
								},
								Key: "ca.crt",
							},
						},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "caBundleRef with nil configMapRef fails",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel.example.com:4317",
						Tracing:  &OpenTelemetryTracingConfig{Enabled: true},
						CABundleRef: &CABundleSource{
							ConfigMapRef: nil,
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "openTelemetry.caBundleRef.configMapRef must be specified",
		},
		{
			name: "caBundleRef with empty configMapRef name fails",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel.example.com:4317",
						Tracing:  &OpenTelemetryTracingConfig{Enabled: true},
						CABundleRef: &CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "",
								},
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "openTelemetry.caBundleRef.configMapRef.name must not be empty",
		},
		{
			name: "endpoint without signals fails before CA bundle check",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel.example.com:4317",
					},
				},
			},
			expectErr: true,
			errMsg:    "endpoint requires at least one of tracing or metrics to be enabled",
		},
		{
			name: "insecure with caBundleRef fails mutual exclusivity check",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "http://otel.example.com:4317",
						Insecure: true,
						Tracing:  &OpenTelemetryTracingConfig{Enabled: true},
						CABundleRef: &CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-ca-bundle",
								},
								Key: "ca.crt",
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "caBundleRef cannot be specified when insecure is true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.expectErr {
				require.Error(t, err, "expected validation to fail")
				assert.Contains(t, err.Error(), tt.errMsg, "error message should match")
			} else {
				assert.NoError(t, err, "expected validation to pass")
			}
		})
	}
}

func TestMCPTelemetryConfig_validateCABundle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    *MCPTelemetryConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "nil openTelemetry returns nil",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: nil,
				},
			},
			expectErr: false,
		},
		{
			name: "nil caBundleRef returns nil",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						CABundleRef: nil,
					},
				},
			},
			expectErr: false,
		},
		{
			name: "nil configMapRef returns error",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						CABundleRef: &CABundleSource{
							ConfigMapRef: nil,
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "openTelemetry.caBundleRef.configMapRef must be specified",
		},
		{
			name: "empty configMapRef name returns error",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						CABundleRef: &CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "",
								},
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "openTelemetry.caBundleRef.configMapRef.name must not be empty",
		},
		{
			name: "valid configMapRef with name and key",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						CABundleRef: &CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-ca-bundle",
								},
								Key: "ca.crt",
							},
						},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "valid configMapRef with name only",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						CABundleRef: &CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "ca-certificates",
								},
							},
						},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "insecure with caBundleRef returns error",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						Insecure: true,
						CABundleRef: &CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-ca",
								},
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "caBundleRef cannot be specified when insecure is true",
		},
		{
			name: "configMapRef name exceeding volume name limit returns error",
			config: &MCPTelemetryConfig{
				Spec: MCPTelemetryConfigSpec{
					OpenTelemetry: &MCPTelemetryOTelConfig{
						CABundleRef: &CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									// 50 chars exceeds the 48-char limit (63 - len("otel-ca-bundle-"))
									Name: "a-very-long-configmap-name-that-exceeds-the-limits",
								},
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "is too long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.validateCABundle()
			if tt.expectErr {
				require.Error(t, err, "expected validation to fail")
				assert.Contains(t, err.Error(), tt.errMsg, "error message should match")
			} else {
				assert.NoError(t, err, "expected validation to pass")
			}
		})
	}
}
