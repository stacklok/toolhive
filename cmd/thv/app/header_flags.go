// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	httpval "github.com/stacklok/toolhive-core/validation/http"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport/middleware"
)

// validateHeaderNames checks that no header names are in the restricted set.
// This provides early CLI-level validation before middleware creation.
func validateHeaderNames(headers map[string]string) error {
	for name := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if _, blocked := middleware.RestrictedHeaders[canonical]; blocked {
			return fmt.Errorf("header %q is restricted and cannot be configured for forwarding", name)
		}
	}
	return nil
}

// parseHeaderForwardFlags parses the slice of headers,
// validates the format (Name=Value), and returns a map of headers.
func parseHeaderForwardFlags(headers []string) (map[string]string, error) {
	result := make(map[string]string, len(headers))

	for _, header := range headers {
		name, value, err := parseHeaderString(header)
		if err != nil {
			return nil, err
		}
		result[name] = value
	}

	if err := validateHeaderNames(result); err != nil {
		return nil, err
	}

	return result, nil
}

// parseHeaderString parses a single header string in the format Name=Value.
// The name must not be empty; the value may be empty.
// Validates header name and value for RFC 7230 compliance (rejects CRLF injection).
func parseHeaderString(header string) (string, string, error) {
	// Find the first equals sign
	idx := strings.Index(header, "=")
	if idx == -1 {
		return "", "", fmt.Errorf("invalid header format %q: expected Name=Value", header)
	}

	name := strings.TrimSpace(header[:idx])
	value := header[idx+1:] // Value keeps leading/trailing whitespace intentionally

	// Validate header name for RFC 7230 compliance (rejects CRLF, control chars)
	if err := httpval.ValidateHeaderName(name); err != nil {
		return "", "", fmt.Errorf("invalid header name in %q: %w", header, err)
	}

	// Validate header value for RFC 7230 compliance (rejects CRLF, control chars)
	// Only validate non-empty values since empty header values are allowed
	if value != "" {
		if err := httpval.ValidateHeaderValue(value); err != nil {
			return "", "", fmt.Errorf("invalid header value in %q: %w", header, err)
		}
	}

	return name, value, nil
}

// parseHeaderSecretFlags parses --remote-forward-headers-secret flags.
// Format: "HeaderName=secret-name" where secret-name is a key in the secrets manager.
// Returns a map of header name → secret name.
func parseHeaderSecretFlags(secretHeaders []string) (map[string]string, error) {
	result := make(map[string]string, len(secretHeaders))
	for _, entry := range secretHeaders {
		headerName, secretName, err := parseHeaderString(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid secret header format: %w", err)
		}
		if secretName == "" {
			return nil, fmt.Errorf("invalid secret header %q: secret name cannot be empty", entry)
		}
		result[headerName] = secretName
	}

	if err := validateHeaderNames(result); err != nil {
		return nil, err
	}

	return result, nil
}

// resolveHeaderSecrets resolves header secret references immediately using the secrets manager.
// This is used by thv proxy which does not persist RunConfig, so secrets must be resolved
// at startup rather than deferred to WithSecrets() as in thv run.
// Returns a map of header name → resolved secret value.
func resolveHeaderSecrets(secretHeaders map[string]string) (map[string]string, error) {
	if len(secretHeaders) == 0 {
		return nil, nil
	}

	cfgProvider := config.NewDefaultProvider()
	cfg := cfgProvider.GetConfig()

	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("failed to determine secrets provider type: %w", err)
	}

	secretManager, err := secrets.CreateSecretProvider(providerType)
	if err != nil {
		return nil, fmt.Errorf("failed to create secret provider: %w", err)
	}

	result := make(map[string]string, len(secretHeaders))
	for headerName, secretName := range secretHeaders {
		value, err := secretManager.GetSecret(context.Background(), secretName)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve secret %q for header %q: %w", secretName, headerName, err)
		}
		// Validate resolved secret value for RFC 7230 compliance (rejects CRLF, control chars)
		if value != "" {
			if err := httpval.ValidateHeaderValue(value); err != nil {
				return nil, fmt.Errorf("secret %q for header %q contains invalid value: %w", secretName, headerName, err)
			}
		}
		result[headerName] = value
	}
	return result, nil
}
