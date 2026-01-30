// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package upstream

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/stacklok/toolhive/pkg/networking"
)

// UserInfoFieldMapping maps provider-specific field names to standard UserInfo fields.
// This allows adapting non-standard provider responses to the canonical UserInfo structure.
//
// Each field supports an ordered list of claim names to try. The first non-empty value
// found will be used. This enables compatibility with providers that use non-standard
// claim names (e.g., GitHub uses "id" instead of "sub", "login" instead of "preferred_username").
//
// Example for GitHub:
//
//	mapping := &UserInfoFieldMapping{
//	    SubjectFields: []string{"id", "login"},
//	    NameFields:    []string{"name", "login"},
//	    EmailFields:   []string{"email"},
//	}
type UserInfoFieldMapping struct {
	// SubjectFields is an ordered list of field names to try for the user ID.
	// The first non-empty value found will be used.
	// Default: ["sub"]
	SubjectFields []string `json:"subject_fields,omitempty" yaml:"subject_fields,omitempty"`

	// NameFields is an ordered list of field names to try for the display name.
	// The first non-empty value found will be used.
	// Default: ["name"]
	NameFields []string `json:"name_fields,omitempty" yaml:"name_fields,omitempty"`

	// EmailFields is an ordered list of field names to try for the email address.
	// The first non-empty value found will be used.
	// Default: ["email"]
	EmailFields []string `json:"email_fields,omitempty" yaml:"email_fields,omitempty"`
}

// Default field names for standard OIDC claims.
var (
	// DefaultSubjectFields is the default field name for subject resolution.
	DefaultSubjectFields = []string{"sub"}

	// DefaultNameFields is the default field name for name resolution.
	DefaultNameFields = []string{"name"}

	// DefaultEmailFields is the default field name for email resolution.
	DefaultEmailFields = []string{"email"}
)

// GetSubjectFields returns the configured subject fields or the default.
func (m *UserInfoFieldMapping) GetSubjectFields() []string {
	if m != nil && len(m.SubjectFields) > 0 {
		return m.SubjectFields
	}
	return DefaultSubjectFields
}

// GetNameFields returns the configured name fields or the default.
func (m *UserInfoFieldMapping) GetNameFields() []string {
	if m != nil && len(m.NameFields) > 0 {
		return m.NameFields
	}
	return DefaultNameFields
}

// GetEmailFields returns the configured email fields or the default.
func (m *UserInfoFieldMapping) GetEmailFields() []string {
	if m != nil && len(m.EmailFields) > 0 {
		return m.EmailFields
	}
	return DefaultEmailFields
}

// ResolveField attempts to extract a string value from claims using an ordered list of field names.
// Returns the first non-empty string value found, or an empty string if none found.
// This function handles type conversion gracefully - non-string values are skipped.
func ResolveField(claims map[string]any, fields []string) string {
	for _, field := range fields {
		if val, ok := claims[field]; ok {
			switch v := val.(type) {
			case string:
				if v != "" {
					return v
				}
			case float64:
				// Handle numeric IDs (e.g., GitHub returns numeric "id")
				return fmt.Sprintf("%.0f", v)
			case int:
				return fmt.Sprintf("%d", v)
			case int64:
				return fmt.Sprintf("%d", v)
			}
		}
	}
	return ""
}

// ResolveSubject extracts the subject (user ID) from claims using the configured mapping.
// Returns an error if no subject can be resolved (subject is required).
func (m *UserInfoFieldMapping) ResolveSubject(claims map[string]any) (string, error) {
	fields := m.GetSubjectFields()
	sub := ResolveField(claims, fields)
	if sub == "" {
		return "", fmt.Errorf("subject claim not found (tried fields: %v)", fields)
	}
	return sub, nil
}

// ResolveName extracts the display name from claims using the configured mapping.
// Returns an empty string if no name is found (name is optional).
func (m *UserInfoFieldMapping) ResolveName(claims map[string]any) string {
	return ResolveField(claims, m.GetNameFields())
}

// ResolveEmail extracts the email from claims using the configured mapping.
// Returns an empty string if no email is found (email is optional).
func (m *UserInfoFieldMapping) ResolveEmail(claims map[string]any) string {
	return ResolveField(claims, m.GetEmailFields())
}

// UserInfoConfig contains configuration for fetching user information from an upstream provider.
// This supports both standard OIDC UserInfo endpoints and custom provider-specific endpoints.
// Authentication is always performed using Bearer token in the Authorization header.
type UserInfoConfig struct {
	// EndpointURL is the URL of the userinfo endpoint (required).
	EndpointURL string `json:"endpoint_url" yaml:"endpoint_url"`

	// HTTPMethod is the HTTP method to use (default: GET).
	HTTPMethod string `json:"http_method,omitempty" yaml:"http_method,omitempty"`

	// AdditionalHeaders contains extra headers to include in the request.
	AdditionalHeaders map[string]string `json:"additional_headers,omitempty" yaml:"additional_headers,omitempty"`

	// FieldMapping contains custom field mapping configuration.
	// If nil, standard OIDC field names are used ("sub", "name", "email").
	FieldMapping *UserInfoFieldMapping `json:"field_mapping,omitempty" yaml:"field_mapping,omitempty"`
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
