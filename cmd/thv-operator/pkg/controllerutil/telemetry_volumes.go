// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

// AddTelemetryCABundleVolumes returns volumes and volume mounts for an OTLP CA bundle
// from an MCPTelemetryConfig's OpenTelemetry configuration.
// Returns nil slices if no CA bundle is configured.
func AddTelemetryCABundleVolumes(
	telemetryConfig *mcpv1beta1.MCPTelemetryConfig,
) ([]corev1.Volume, []corev1.VolumeMount) {
	if telemetryConfig == nil ||
		telemetryConfig.Spec.OpenTelemetry == nil ||
		telemetryConfig.Spec.OpenTelemetry.CABundleRef == nil ||
		telemetryConfig.Spec.OpenTelemetry.CABundleRef.ConfigMapRef == nil {
		return nil, nil
	}

	ref := telemetryConfig.Spec.OpenTelemetry.CABundleRef.ConfigMapRef
	key := ref.Key
	if key == "" {
		key = validation.TelemetryCABundleDefaultKey
	}
	volumeName := fmt.Sprintf("%s%s", validation.TelemetryCABundleVolumePrefix, ref.Name)
	mountPath := fmt.Sprintf("%s/%s", validation.TelemetryCABundleMountBasePath, ref.Name)

	volume := corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Items:                []corev1.KeyToPath{{Key: key, Path: key}},
			},
		},
	}
	volumeMount := corev1.VolumeMount{
		Name:      volumeName,
		MountPath: mountPath,
		ReadOnly:  true,
	}
	return []corev1.Volume{volume}, []corev1.VolumeMount{volumeMount}
}

// TelemetryCABundleFilePath returns the full file path where the CA bundle will be
// mounted in the proxyrunner container, or empty string if no CA bundle is configured.
func TelemetryCABundleFilePath(
	telemetryConfig *mcpv1beta1.MCPTelemetryConfig,
) string {
	if telemetryConfig == nil ||
		telemetryConfig.Spec.OpenTelemetry == nil ||
		telemetryConfig.Spec.OpenTelemetry.CABundleRef == nil ||
		telemetryConfig.Spec.OpenTelemetry.CABundleRef.ConfigMapRef == nil {
		return ""
	}

	ref := telemetryConfig.Spec.OpenTelemetry.CABundleRef.ConfigMapRef
	key := ref.Key
	if key == "" {
		key = validation.TelemetryCABundleDefaultKey
	}
	return fmt.Sprintf("%s/%s/%s", validation.TelemetryCABundleMountBasePath, ref.Name, key)
}
