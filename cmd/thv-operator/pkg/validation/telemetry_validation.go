// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

const (
	// OTelCABundleVolumePrefix is the prefix used for OTel CA bundle volume names.
	OTelCABundleVolumePrefix = "otel-ca-bundle-"

	// OTelCABundleMountBasePath is the base path where OTel CA bundle ConfigMaps are mounted.
	OTelCABundleMountBasePath = "/config/otel-certs"

	// OTelCABundleDefaultKey is the default key name used when not specified in caBundleRef.
	OTelCABundleDefaultKey = "ca.crt"
)
