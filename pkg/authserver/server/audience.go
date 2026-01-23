// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/ory/fosite"
)

// ErrInvalidTarget is the RFC 8707 error for invalid or unauthorized resource parameters.
// This error is returned when:
// - The resource URI format is invalid (not absolute, has fragment, wrong scheme)
// - The resource is not in the server's allowed audiences list
var ErrInvalidTarget = &fosite.RFC6749Error{
	ErrorField:       "invalid_target",
	DescriptionField: "The requested resource is invalid, unknown, or malformed.",
	CodeField:        http.StatusBadRequest,
}

// ValidateAudienceURI validates that a resource URI conforms to RFC 8707 requirements.
// According to RFC 8707, a valid resource parameter must be:
// - An absolute URI (has scheme and host)
// - No fragment component
// - Use http or https scheme
func ValidateAudienceURI(resource string) error {
	if resource == "" {
		return nil // Empty resource is valid (means no audience binding requested)
	}

	parsed, err := url.Parse(resource)
	if err != nil {
		return ErrInvalidTarget.WithHintf("Resource parameter is not a valid URI: %s", err.Error())
	}

	// Must be absolute (have a scheme)
	if !parsed.IsAbs() {
		return ErrInvalidTarget.WithHint("Resource must be an absolute URI")
	}

	// Must have a host
	if parsed.Host == "" {
		return ErrInvalidTarget.WithHint("Resource must include a host")
	}

	// Must not have a fragment (RFC 8707 Section 2)
	if parsed.Fragment != "" {
		return ErrInvalidTarget.WithHint("Resource must not contain a fragment")
	}

	// Only allow http or https schemes for security
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ErrInvalidTarget.WithHint("Resource must use http or https scheme")
	}

	return nil
}

// ValidateAudienceAllowed checks if the resource is in the allowed audiences list.
// Returns nil if allowed, or ErrInvalidTarget if not.
//
// Security: An empty allowedAudiences list means NO audiences are permitted (secure default).
func ValidateAudienceAllowed(resource string, allowedAudiences []string) error {
	if resource == "" {
		return nil // No resource requested, nothing to validate
	}

	// Secure default: empty allowlist means reject all
	if len(allowedAudiences) == 0 {
		return ErrInvalidTarget.WithHint("No resource audiences are configured on this server")
	}

	// Exact string matching
	if slices.Contains(allowedAudiences, resource) {
		return nil
	}

	return ErrInvalidTarget.WithHintf("Resource %q is not a registered audience", resource)
}
