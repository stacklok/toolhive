// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

// AddOIDCCABundleVolumes returns volumes and volume mounts for OIDC CA bundle.
// Returns nil slices if no CA bundle is configured.
func AddOIDCCABundleVolumes(
	oidcConfig *mcpv1alpha1.OIDCConfigRef,
) ([]corev1.Volume, []corev1.VolumeMount) {
	if oidcConfig == nil {
		return nil, nil
	}

	// Get CABundleRef based on config type
	var caBundleRef *mcpv1alpha1.CABundleSource
	switch oidcConfig.Type {
	case mcpv1alpha1.OIDCConfigTypeInline:
		if oidcConfig.Inline != nil {
			caBundleRef = oidcConfig.Inline.CABundleRef
		}
	case mcpv1alpha1.OIDCConfigTypeConfigMap:
		if oidcConfig.ConfigMap != nil {
			caBundleRef = oidcConfig.ConfigMap.CABundleRef
		}
	}

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
