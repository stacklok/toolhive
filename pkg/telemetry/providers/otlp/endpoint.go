// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import "strings"

// OTLP HTTP signal path suffixes appended when the endpoint includes a custom base path.
const (
	otlpTracesPath  = "/v1/traces"
	otlpMetricsPath = "/v1/metrics"
)

// splitEndpointPath separates an OTLP endpoint string into its host:port and
// path components. If no path is present, basePath is empty.
//
// The function defensively strips http:// and https:// prefixes so it works
// correctly even when the scheme has not been removed upstream (e.g. the CLI
// path, which does not call NormalizeTelemetryConfig).
func splitEndpointPath(endpoint string) (hostPort, basePath string) {
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")

	idx := strings.Index(endpoint, "/")
	if idx < 0 {
		return endpoint, ""
	}
	return endpoint[:idx], strings.TrimRight(endpoint[idx:], "/")
}
