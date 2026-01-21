// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package strategies provides authentication strategy implementations for Virtual MCP Server.
package strategies

// Metadata key names used in strategy configurations.
const (
	// MetadataHeaderName is the key for the HTTP header name in metadata.
	// Used by HeaderInjectionStrategy to identify which header to inject.
	MetadataHeaderName = "header_name"

	// MetadataHeaderValue is the key for the HTTP header value in metadata.
	// Used by HeaderInjectionStrategy to identify the value to inject.
	MetadataHeaderValue = "header_value"

	// MetadataHeaderValueEnv is the key for the environment variable name
	// that contains the header value. Used by converters during the conversion
	// phase to indicate that secret resolution is needed. This is replaced with
	// MetadataHeaderValue (containing the actual secret) before reaching the strategy.
	MetadataHeaderValueEnv = "header_value_env"
)
