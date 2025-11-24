// Package validation provides functions for validating input data.
package validation

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/http/httpguts"
)

var validGroupNameRegex = regexp.MustCompile(`^[a-z0-9_\-\s]+$`)

// ValidateGroupName validates that a group name only contains allowed characters:
// lowercase alphanumeric, underscore, dash, and space.
// It also enforces no leading/trailing/consecutive spaces and disallows null bytes.
func ValidateGroupName(name string) error {
	if name == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("group name cannot be empty or consist only of whitespace")
	}

	// Check for null bytes explicitly
	if strings.Contains(name, "\x00") {
		return fmt.Errorf("group name cannot contain null bytes")
	}

	// Enforce lowercase-only group names
	if name != strings.ToLower(name) {
		return fmt.Errorf("group name must be lowercase")
	}

	// Validate characters
	if !validGroupNameRegex.MatchString(name) {
		return fmt.Errorf("group name can only contain lowercase alphanumeric characters, underscores, dashes, and spaces: %q", name)
	}

	// Check for leading/trailing whitespace
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("group name cannot have leading or trailing whitespace: %q", name)
	}

	// Check for consecutive spaces
	if strings.Contains(name, "  ") {
		return fmt.Errorf("group name cannot contain consecutive spaces: %q", name)
	}

	return nil
}

// ValidateHTTPHeaderName validates that a string is a valid HTTP header name per RFC 7230.
// It checks for CRLF injection, control characters, and ensures RFC token compliance.
func ValidateHTTPHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("header name cannot be empty")
	}

	// Length limit to prevent DoS
	if len(name) > 256 {
		return fmt.Errorf("header name exceeds maximum length of 256 bytes")
	}

	// Use httpguts validation (same as Go's HTTP/2 implementation)
	if !httpguts.ValidHeaderFieldName(name) {
		return fmt.Errorf("invalid HTTP header name: contains invalid characters")
	}

	return nil
}

// ValidateHTTPHeaderValue validates that a string is a valid HTTP header value per RFC 7230.
// It checks for CRLF injection and control characters.
func ValidateHTTPHeaderValue(value string) error {
	if value == "" {
		return fmt.Errorf("header value cannot be empty")
	}

	// Length limit to prevent DoS (common HTTP server limit)
	if len(value) > 8192 {
		return fmt.Errorf("header value exceeds maximum length of 8192 bytes")
	}

	// Use httpguts validation
	if !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("invalid HTTP header value: contains control characters")
	}

	return nil
}

// ValidateResourceURI validates that a resource URI conforms to MCP specification requirements
// for canonical URIs (RFC 8707).
// This is used for user-provided values that should not be normalized.
//
// According to MCP spec, a valid canonical URI must:
// - Include a scheme (http/https)
// - Include a host
// - Not contain fragments
func ValidateResourceURI(resourceURI string) error {
	if resourceURI == "" {
		return fmt.Errorf("resource URI cannot be empty")
	}

	// Parse the URI
	parsed, err := url.Parse(resourceURI)
	if err != nil {
		return fmt.Errorf("invalid resource URI: %w", err)
	}

	// Must have a scheme
	if parsed.Scheme == "" {
		return fmt.Errorf("resource URI must include a scheme (e.g., https://): %s", resourceURI)
	}

	// Must have a host
	if parsed.Host == "" {
		return fmt.Errorf("resource URI must include a host: %s", resourceURI)
	}

	// Must not contain fragments
	if parsed.Fragment != "" {
		return fmt.Errorf("resource URI must not contain fragments (#): %s", resourceURI)
	}

	return nil
}
