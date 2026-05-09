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

// HeaderForwardPlaintextEnvVarPrefix is the prefix the operator uses when
// emitting per-(owner, header) literal-value env vars on a workload pod for
// MCPServerEntry / MCPRemoteProxy headerForward.AddPlaintextHeaders entries.
// The vMCP runtime walks env vars matching this prefix at startup to
// reconstruct the per-backend HeaderForwardConfig in static mode.
const HeaderForwardPlaintextEnvVarPrefix = "TOOLHIVE_HEADER_PLAINTEXT_"

// HeaderForwardSecretEnvVarPrefix is the prefix the operator uses when
// emitting per-(owner, header) valueFrom.secretKeyRef env vars on a
// workload pod for MCPServerEntry / MCPRemoteProxy
// headerForward.AddHeadersFromSecret entries. The full env var name is
// TOOLHIVE_SECRET_HEADER_FORWARD_<HEADER>_<OWNER>; the prefix below
// matches the literal "TOOLHIVE_SECRET_HEADER_FORWARD_" segment so the
// vMCP runtime can iterate them at startup.
const HeaderForwardSecretEnvVarPrefix = "TOOLHIVE_SECRET_HEADER_FORWARD_"

// normalizeHeaderForEnvVar applies the canonical sanitization used in every
// header-forward env-var name: uppercase, hyphens to underscores, anything
// outside [A-Z0-9_] also to underscore. Both the secret-ref and the
// plaintext-value emitters MUST share this function so a header that
// round-trips through one branch never collides with the other.
func normalizeHeaderForEnvVar(s string) string {
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
	sanitizedHeader := normalizeHeaderForEnvVar(headerName)
	sanitizedOwner := normalizeHeaderForEnvVar(ownerName)

	// Build the secret identifier (what gets stored in RunConfig.AddHeadersFromSecret)
	secretIdentifier = fmt.Sprintf("HEADER_FORWARD_%s_%s", sanitizedHeader, sanitizedOwner)

	// Build the full env var name (TOOLHIVE_SECRET_ prefix + identifier)
	// This follows the pattern expected by secrets.EnvironmentProvider
	envVarName = fmt.Sprintf("TOOLHIVE_SECRET_%s", secretIdentifier)

	return envVarName, secretIdentifier
}

// GenerateHeaderForwardPlaintextEnvVarName generates the environment variable
// name for a per-(owner, header) literal plaintext header value emitted on a
// workload pod. The vMCP runtime walks variables with the
// HeaderForwardPlaintextEnvVarPrefix at startup, parses the suffix back into
// (ownerName, headerName), and constructs the per-backend HeaderForwardConfig.
//
// The header name and owner sanitization is identical to the secret-ref
// generator (shared via normalizeHeaderForEnvVar) so a header that goes
// through one path never collides with the other.
//
// Returns:
//   - envVarName: full env var name (e.g. "TOOLHIVE_HEADER_PLAINTEXT_X_API_KEY_MY_OWNER")
//   - normalizedHeader: the sanitized header segment ("X_API_KEY")
//   - normalizedOwner: the sanitized owner segment ("MY_OWNER")
func GenerateHeaderForwardPlaintextEnvVarName(ownerName, headerName string) (envVarName, normalizedHeader, normalizedOwner string) {
	normalizedHeader = normalizeHeaderForEnvVar(headerName)
	normalizedOwner = normalizeHeaderForEnvVar(ownerName)
	envVarName = fmt.Sprintf("%s%s_%s", HeaderForwardPlaintextEnvVarPrefix, normalizedHeader, normalizedOwner)
	return envVarName, normalizedHeader, normalizedOwner
}
