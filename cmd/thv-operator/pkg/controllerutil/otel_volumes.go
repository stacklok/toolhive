// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

// AddOTelCABundleVolumes returns volumes and volume mounts for an OTel CA bundle.
// Returns nil slices if no CA bundle is configured.
func AddOTelCABundleVolumes(
	caBundleRef *mcpv1alpha1.CABundleSource,
) ([]corev1.Volume, []corev1.VolumeMount) {
	if caBundleRef == nil || caBundleRef.ConfigMapRef == nil {
		return nil, nil
	}

	ref := caBundleRef.ConfigMapRef
	key := ref.Key
	if key == "" {
		key = validation.OTelCABundleDefaultKey
	}
	volumeName := fmt.Sprintf("%s%s", validation.OTelCABundleVolumePrefix, ref.Name)
	mountPath := fmt.Sprintf("%s/%s", validation.OTelCABundleMountBasePath, ref.Name)

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

// ComputeOTelCABundlePath computes the CA bundle mount path from a CABundleSource
// for OTel configuration. Returns empty string if caBundleRef is nil or has no ConfigMapRef.
func ComputeOTelCABundlePath(caBundleRef *mcpv1alpha1.CABundleSource) string {
	if caBundleRef == nil || caBundleRef.ConfigMapRef == nil {
		return ""
	}
	ref := caBundleRef.ConfigMapRef
	key := ref.Key
	if key == "" {
		key = validation.OTelCABundleDefaultKey
	}
	return fmt.Sprintf("%s/%s/%s", validation.OTelCABundleMountBasePath, ref.Name, key)
}
