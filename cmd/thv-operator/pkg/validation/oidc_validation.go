// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"fmt"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	// maxK8sVolumeName is the maximum length for a Kubernetes volume name (RFC 1123 label)
	maxK8sVolumeName = 63

	// OIDCCABundleVolumePrefix is the prefix used for OIDC CA bundle volume names.
	// Used by controllerutil/oidc_volumes.go when creating volumes.
	OIDCCABundleVolumePrefix = "oidc-ca-bundle-"

	// OIDCCABundleMountBasePath is the base path where OIDC CA bundle ConfigMaps are mounted.
	// The full mount path is: OIDCCABundleMountBasePath + "/" + configMapName
	// The full file path is: OIDCCABundleMountBasePath + "/" + configMapName + "/" + key
	// Used by both controllerutil/oidc_volumes.go and oidc/resolver.go.
	OIDCCABundleMountBasePath = "/config/certs"

	// OIDCCABundleDefaultKey is the default key name used when not specified in caBundleRef.
	OIDCCABundleDefaultKey = "ca.crt"

	// maxConfigMapNameForCABundle is the maximum ConfigMap name length that fits in a volume name
	maxConfigMapNameForCABundle = maxK8sVolumeName - len(OIDCCABundleVolumePrefix)
)

// ValidateCABundleSource validates the CABundleSource configuration.
// It ensures that configMapRef is specified when CABundleRef is provided,
// and that the ConfigMap name is short enough to fit in a Kubernetes volume name.
// Returns nil if ref is nil (no CA bundle configured).
func ValidateCABundleSource(ref *mcpv1alpha1.CABundleSource) error {
	if ref == nil {
		return nil
	}
	if ref.ConfigMapRef == nil {
		return fmt.Errorf("configMapRef must be specified in caBundleRef")
	}
	if ref.ConfigMapRef.Name == "" {
		return fmt.Errorf("configMapRef.name must be specified")
	}
	// Check that the ConfigMap name won't cause the volume name to exceed K8s limits
	if len(ref.ConfigMapRef.Name) > maxConfigMapNameForCABundle {
		return fmt.Errorf("configMapRef.name %q is too long (%d chars); maximum is %d characters to fit in Kubernetes volume name",
			ref.ConfigMapRef.Name, len(ref.ConfigMapRef.Name), maxConfigMapNameForCABundle)
	}
	return nil
}
