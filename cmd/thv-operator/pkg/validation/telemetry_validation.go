// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

const (
	// TelemetryCABundleVolumePrefix is the prefix used for telemetry CA bundle volume names.
	TelemetryCABundleVolumePrefix = "otel-ca-bundle-"

	// TelemetryCABundleMountBasePath is the base path where telemetry CA bundle ConfigMaps are mounted.
	// The full mount path is: TelemetryCABundleMountBasePath + "/" + configMapName
	// The full file path is: TelemetryCABundleMountBasePath + "/" + configMapName + "/" + key
	TelemetryCABundleMountBasePath = "/config/certs/otel"

	// TelemetryCABundleDefaultKey is the default key name used when not specified in caBundleRef.
	TelemetryCABundleDefaultKey = "ca.crt"
)
