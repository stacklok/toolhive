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

// GenerateHeaderForwardSecretEnvVarName generates the environment variable name for a header forward secret.
// The generated name follows the TOOLHIVE_SECRET_<identifier> pattern expected by the EnvironmentProvider.
//
// Parameters:
//   - ownerName: The name of the CRD instance that owns the Secret reference.
//     For MCPRemoteProxy this is the proxy name; for MCPServerEntry backends
//     consumed by a VirtualMCPServer this is the entry name. Scoping the
//     identifier by ownerName ensures multiple owners sharing a pod cannot
//     collide on env var names.
//   - headerName: The HTTP header name (e.g., "X-API-Key").
//
// Returns the full environment variable name (e.g., "TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_MY_OWNER")
// and the secret identifier portion (e.g., "HEADER_FORWARD_X_API_KEY_MY_OWNER") for use in RunConfig
// or vMCP StaticBackendConfig.
func GenerateHeaderForwardSecretEnvVarName(ownerName, headerName string) (envVarName, secretIdentifier string) {
	// Sanitize header name for use in env var (uppercase, replace hyphens with underscore)
	sanitizedHeader := strings.ToUpper(strings.ReplaceAll(headerName, "-", "_"))
	sanitizedHeader = envVarSanitizer.ReplaceAllString(sanitizedHeader, "_")

	// Sanitize owner name for use in env var
	sanitizedOwner := strings.ToUpper(strings.ReplaceAll(ownerName, "-", "_"))
	sanitizedOwner = envVarSanitizer.ReplaceAllString(sanitizedOwner, "_")

	// Build the secret identifier (what gets stored in RunConfig.AddHeadersFromSecret
	// or StaticBackendConfig.HeaderForward.AddHeadersFromSecret).
	secretIdentifier = fmt.Sprintf("HEADER_FORWARD_%s_%s", sanitizedHeader, sanitizedOwner)

	// Build the full env var name (TOOLHIVE_SECRET_ prefix + identifier)
	// This follows the pattern expected by secrets.EnvironmentProvider
	envVarName = fmt.Sprintf("TOOLHIVE_SECRET_%s", secretIdentifier)

	return envVarName, secretIdentifier
}
