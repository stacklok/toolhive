// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/stacklok/toolhive/pkg/networking"
)

// UserInfoFieldMapping maps provider-specific field names to standard UserInfo fields.
// This allows adapting non-standard provider responses to the canonical UserInfo structure.
//
// Example for GitHub:
//
//	mapping := &UserInfoFieldMapping{
//	    SubjectField: "id",
//	}
type UserInfoFieldMapping struct {
	// SubjectField is the field name for the user ID (e.g., "sub" for OIDC, "id" for GitHub).
	// Default: "sub"
	SubjectField string
}

// DefaultSubjectField is the default field name for subject resolution.
const DefaultSubjectField = "sub"

// GetSubjectField returns the configured subject field or the default.
func (m *UserInfoFieldMapping) GetSubjectField() string {
	if m != nil && m.SubjectField != "" {
		return m.SubjectField
	}
	return DefaultSubjectField
}

// ResolveSubject extracts the subject (user ID) from claims using the configured mapping.
// Returns an error if no subject can be resolved (subject is required).
func (m *UserInfoFieldMapping) ResolveSubject(claims map[string]any) (string, error) {
	field := m.GetSubjectField()
	val, ok := claims[field]
	if !ok {
		return "", fmt.Errorf("subject claim not found (tried field: %q)", field)
	}

	switch v := val.(type) {
	case string:
		if v == "" {
			return "", fmt.Errorf("subject claim %q is empty", field)
		}
		return v, nil
	case float64:
		// JSON numbers are always float64; format as integer for user IDs
		return strconv.FormatFloat(v, 'f', 0, 64), nil
	default:
		return "", fmt.Errorf("subject claim %q has unsupported type %T", field, val)
	}
}

// UserInfoConfig contains configuration for fetching user information from an upstream provider.
// This supports both standard OIDC UserInfo endpoints and custom provider-specific endpoints.
// Authentication is always performed using Bearer token in the Authorization header.
type UserInfoConfig struct {
	// EndpointURL is the URL of the userinfo endpoint (required).
	EndpointURL string

	// HTTPMethod is the HTTP method to use (default: GET).
	HTTPMethod string

	// AdditionalHeaders contains extra headers to include in the request.
	AdditionalHeaders map[string]string

	// FieldMapping contains custom field mapping configuration.
	// If nil, standard OIDC field names are used ("sub", "name", "email").
	FieldMapping *UserInfoFieldMapping
}

// Validate checks that UserInfoConfig has all required fields and valid values.
func (c *UserInfoConfig) Validate() error {
	if c.EndpointURL == "" {
		return errors.New("endpoint_url is required")
	}

	parsed, err := url.Parse(c.EndpointURL)
	if err != nil {
		return errors.New("endpoint_url must be a valid URL")
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("endpoint_url must be an absolute URL with scheme and host")
	}

	if parsed.Scheme != networking.HttpScheme && parsed.Scheme != networking.HttpsScheme {
		return errors.New("endpoint_url must use http or https scheme")
	}

	// HTTP scheme is only allowed for loopback addresses (consistent with validateRedirectURI)
	if parsed.Scheme == networking.HttpScheme && !networking.IsLocalhost(parsed.Host) {
		return errors.New("endpoint_url with http scheme requires loopback address (127.0.0.1, ::1, or localhost)")
	}

	// Validate HTTP method if specified (OIDC Core Section 5.3.1 allows GET and POST)
	if c.HTTPMethod != "" && c.HTTPMethod != http.MethodGet && c.HTTPMethod != http.MethodPost {
		return errors.New("http_method must be GET or POST")
	}

	return nil
}
