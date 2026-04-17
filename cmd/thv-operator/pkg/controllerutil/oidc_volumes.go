// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

// AddOIDCConfigRefCABundleVolumes returns volumes and volume mounts for OIDC CA bundle
// from an MCPOIDCConfig's inline configuration. Returns nil slices if no CA bundle is configured.
func AddOIDCConfigRefCABundleVolumes(
	oidcConfig *mcpv1alpha1.MCPOIDCConfig,
) ([]corev1.Volume, []corev1.VolumeMount) {
	if oidcConfig == nil {
		return nil, nil
	}

	// Only inline type has CA bundle support
	if oidcConfig.Spec.Type != mcpv1alpha1.MCPOIDCConfigTypeInline || oidcConfig.Spec.Inline == nil {
		return nil, nil
	}

	caBundleRef := oidcConfig.Spec.Inline.CABundleRef
	if caBundleRef == nil || caBundleRef.ConfigMapRef == nil {
		return nil, nil
	}

	ref := caBundleRef.ConfigMapRef
	key := ref.Key
	if key == "" {
		key = validation.OIDCCABundleDefaultKey
	}
	volumeName := fmt.Sprintf("%s%s", validation.OIDCCABundleVolumePrefix, ref.Name)
	mountPath := fmt.Sprintf("%s/%s", validation.OIDCCABundleMountBasePath, ref.Name)

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
