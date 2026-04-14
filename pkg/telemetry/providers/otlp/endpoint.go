// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import "strings"

// splitEndpointPath separates an OTLP endpoint string into its host:port and
// path components. The endpoint is expected to have its scheme already stripped
// (by NormalizeTelemetryConfig). If no path is present, basePath is empty.
func splitEndpointPath(endpoint string) (hostPort, basePath string) {
	idx := strings.Index(endpoint, "/")
	if idx < 0 {
		return endpoint, ""
	}
	return endpoint[:idx], strings.TrimRight(endpoint[idx:], "/")
}
