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

// GenerateUniqueXAAIDPSecretEnvVarName generates a unique environment variable name for XAA
// IdP client secrets, incorporating the ExternalAuthConfig name to ensure uniqueness across
// multiple configs. Used by both the converter and deployment controller for consistent naming.
//
// Example: For an ExternalAuthConfig named "my-auth-config", this returns:
// "TOOLHIVE_XAA_IDP_CLIENT_SECRET_MY_AUTH_CONFIG"
func GenerateUniqueXAAIDPSecretEnvVarName(configName string) string {
	sanitized := strings.ToUpper(strings.ReplaceAll(configName, "-", "_"))
	sanitized = envVarSanitizer.ReplaceAllString(sanitized, "_")
	return fmt.Sprintf("TOOLHIVE_XAA_IDP_CLIENT_SECRET_%s", sanitized)
}

// GenerateUniqueXAATargetSecretEnvVarName generates a unique environment variable name for XAA
// target AS client secrets, incorporating the ExternalAuthConfig name to ensure uniqueness across
// multiple configs. Used by both the converter and deployment controller for consistent naming.
//
// Example: For an ExternalAuthConfig named "my-auth-config", this returns:
// "TOOLHIVE_XAA_TARGET_CLIENT_SECRET_MY_AUTH_CONFIG"
func GenerateUniqueXAATargetSecretEnvVarName(configName string) string {
	sanitized := strings.ToUpper(strings.ReplaceAll(configName, "-", "_"))
	sanitized = envVarSanitizer.ReplaceAllString(sanitized, "_")
	return fmt.Sprintf("TOOLHIVE_XAA_TARGET_CLIENT_SECRET_%s", sanitized)
}

// Header-forward env-var helpers (constants + name generators + the shared
// header-name normalizer) moved to pkg/vmcp/headerforward/wirefmt so the
// runtime can consume them without inverting Go layering. Operator code
// imports them from there.
