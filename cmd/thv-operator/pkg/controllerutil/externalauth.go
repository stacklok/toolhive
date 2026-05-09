// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllerutil provides utility functions for Kubernetes controllers.
package controllerutil

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	envVarSanitizer = regexp.MustCompile(`[^A-Z0-9_]`)
)

// GenerateUniqueTokenExchangeEnvVarName generates a unique environment variable name for token exchange
// client secrets, incorporating the ExternalAuthConfig name to ensure uniqueness.
// This function is used by both the converter and deployment controller to ensure consistent
// environment variable naming across the system.
//
// Example: For an ExternalAuthConfig named "my-auth-config", this returns:
// "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET_MY_AUTH_CONFIG"
func GenerateUniqueTokenExchangeEnvVarName(configName string) string {
	// Sanitize config name for use in env var (uppercase, replace invalid chars with underscore)
	sanitized := strings.ToUpper(strings.ReplaceAll(configName, "-", "_"))
	// Remove any remaining invalid characters (keep only alphanumeric and underscore)
	sanitized = envVarSanitizer.ReplaceAllString(sanitized, "_")
	return fmt.Sprintf("TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET_%s", sanitized)
}

// GenerateUniqueHeaderInjectionEnvVarName generates a unique environment variable name for header injection
// values, incorporating the ExternalAuthConfig name to ensure uniqueness.
// This function is used by both the converter and deployment controller to ensure consistent
// environment variable naming across the system.
//
// Example: For an ExternalAuthConfig named "my-auth-config", this returns:
// "TOOLHIVE_HEADER_INJECTION_VALUE_MY_AUTH_CONFIG"
func GenerateUniqueHeaderInjectionEnvVarName(configName string) string {
	// Sanitize config name for use in env var (uppercase, replace invalid chars with underscore)
	sanitized := strings.ToUpper(strings.ReplaceAll(configName, "-", "_"))
	// Remove any remaining invalid characters (keep only alphanumeric and underscore)
	sanitized = envVarSanitizer.ReplaceAllString(sanitized, "_")
	return fmt.Sprintf("TOOLHIVE_HEADER_INJECTION_VALUE_%s", sanitized)
}

// HeaderForwardManifestEnvVarPrefix is the prefix the operator uses when
// emitting one JSON-encoded manifest env var per backend on a workload pod
// for MCPServerEntry / MCPRemoteProxy headerForward configuration.
//
// The full env var name is "TOOLHIVE_HEADER_FORWARD_<NORMALIZED_OWNER>";
// the value is JSON-encoded vmcp.HeaderForwardConfig with original header
// names preserved (uppercased/sanitized normalization can't recover hyphens
// or original casing on the receive side, so we carry the literal names in
// the JSON value instead). Plaintext header values appear inline; secret-
// backed entries carry only the secret identifier — the actual Secret
// value rides a sibling TOOLHIVE_SECRET_HEADER_FORWARD_<H>_<OWNER> env var
// emitted via valueFrom.secretKeyRef and resolved at request time by
// secrets.EnvironmentProvider.
const HeaderForwardManifestEnvVarPrefix = "TOOLHIVE_HEADER_FORWARD_"

// NormalizeHeaderForEnvVar applies the canonical sanitization used in every
// header-forward env-var name: uppercase, hyphens to underscores, anything
// outside [A-Z0-9_] also to underscore. Both the secret-ref and the
// plaintext-value emitters MUST share this function so a header that
// round-trips through one branch never collides with the other.
func NormalizeHeaderForEnvVar(s string) string {
	upper := strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
	return envVarSanitizer.ReplaceAllString(upper, "_")
}

// GenerateHeaderForwardSecretEnvVarName generates the environment variable name for a header forward secret.
// The generated name follows the TOOLHIVE_SECRET_<identifier> pattern expected by the EnvironmentProvider.
//
// Parameters:
//   - ownerName: The name of the resource that owns the header forwarding configuration
//     (an MCPRemoteProxy or an MCPServerEntry).
//   - headerName: The HTTP header name (e.g., "X-API-Key")
//
// Returns the full environment variable name (e.g., "TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_MY_OWNER")
// and the secret identifier portion (e.g., "HEADER_FORWARD_X_API_KEY_MY_OWNER") for use in RunConfig.
func GenerateHeaderForwardSecretEnvVarName(ownerName, headerName string) (envVarName, secretIdentifier string) {
	sanitizedHeader := NormalizeHeaderForEnvVar(headerName)
	sanitizedOwner := NormalizeHeaderForEnvVar(ownerName)

	// Build the secret identifier (what gets stored in RunConfig.AddHeadersFromSecret)
	secretIdentifier = fmt.Sprintf("HEADER_FORWARD_%s_%s", sanitizedHeader, sanitizedOwner)

	// Build the full env var name (TOOLHIVE_SECRET_ prefix + identifier)
	// This follows the pattern expected by secrets.EnvironmentProvider
	envVarName = fmt.Sprintf("TOOLHIVE_SECRET_%s", secretIdentifier)

	return envVarName, secretIdentifier
}

// GenerateHeaderForwardManifestEnvVarName generates the environment variable
// name for the JSON-encoded headerForward manifest emitted per backend on
// the vMCP pod. One env var per backend; the JSON value carries every
// configured header (plaintext values inline, secret entries by identifier).
//
// Returns the full env var name (e.g. "TOOLHIVE_HEADER_FORWARD_MY_OWNER")
// and the normalized owner segment ("MY_OWNER") used when iterating env
// vars matching HeaderForwardManifestEnvVarPrefix.
func GenerateHeaderForwardManifestEnvVarName(ownerName string) (envVarName, normalizedOwner string) {
	normalizedOwner = NormalizeHeaderForEnvVar(ownerName)
	envVarName = fmt.Sprintf("%s%s", HeaderForwardManifestEnvVarPrefix, normalizedOwner)
	return envVarName, normalizedOwner
}
