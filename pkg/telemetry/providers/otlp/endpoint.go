// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import "strings"

// Default URL path suffixes for the OTLP/HTTP protocol, as defined in the
// OpenTelemetry specification:
// https://opentelemetry.io/docs/specs/otlp/#otlphttp-request
//
// The Go OTLP SDK normally appends these automatically. However, when the user
// provides a custom base path (e.g. "/api/public/otel" for Langfuse), we must
// call WithURLPath which replaces the entire path. In that case we concatenate
// the base path with the appropriate suffix ourselves (e.g.
// "/api/public/otel" + "/v1/traces").
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
