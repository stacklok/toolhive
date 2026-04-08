// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

const (
	// OTelCABundleVolumePrefix is the prefix used for OTel CA bundle volume names.
	// This MUST be the same length as OIDCCABundleVolumePrefix because
	// ValidateCABundleSource uses the OIDC prefix length for the max ConfigMap name check.
	OTelCABundleVolumePrefix = "otel-ca-bundle-"

	// OTelCABundleMountBasePath is the base path where OTel CA bundle ConfigMaps are mounted.
	OTelCABundleMountBasePath = "/config/otel-certs"

	// OTelCABundleDefaultKey is the default key name used when not specified in caBundleRef.
	OTelCABundleDefaultKey = "ca.crt"
)

// Compile-time assertion: OTel and OIDC CA bundle volume prefixes must be the same
// length because ValidateCABundleSource computes the max ConfigMap name length from
// OIDCCABundleVolumePrefix and is used for both OIDC and OTel CA bundle validation.
var _ [len(OIDCCABundleVolumePrefix)]byte = [len(OTelCABundleVolumePrefix)]byte{}
