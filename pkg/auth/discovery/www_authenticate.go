package discovery

import (
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/logger"
)

// ParseWWWAuthenticate parses the WWW-Authenticate header to extract authentication information
// Supports multiple authentication schemes and complex header formats
func ParseWWWAuthenticate(header string) (*AuthInfo, error) {
	// Trim whitespace and handle empty headers
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, fmt.Errorf("empty WWW-Authenticate header")
	}

	// Check for OAuth/Bearer authentication
	// Note: We don't split by comma because Bearer parameters can contain commas in quoted values
	if strings.HasPrefix(header, "Bearer") {
		authInfo := &AuthInfo{Type: "OAuth"}

		// Extract parameters after "Bearer"
		params := strings.TrimSpace(strings.TrimPrefix(header, "Bearer"))
		if params != "" {
			// Parse parameters (realm, scope, resource_metadata, etc.)
			realm := ExtractParameter(params, "realm")
			if realm != "" {
				authInfo.Realm = realm
			}

			// RFC 9728: Check for resource_metadata parameter
			resourceMetadata := ExtractParameter(params, "resource_metadata")
			if resourceMetadata != "" {
				authInfo.ResourceMetadata = resourceMetadata
			}

			// Extract error information if present
			errorParam := ExtractParameter(params, "error")
			if errorParam != "" {
				authInfo.Error = errorParam
			}

			errorDesc := ExtractParameter(params, "error_description")
			if errorDesc != "" {
				authInfo.ErrorDescription = errorDesc
			}
		}

		return authInfo, nil
	}

	// Check for OAuth-specific schemes
	if strings.HasPrefix(header, "OAuth") {
		authInfo := &AuthInfo{Type: "OAuth"}

		// Extract parameters after "OAuth"
		params := strings.TrimSpace(strings.TrimPrefix(header, "OAuth"))
		if params != "" {
			// Parse parameters (realm, scope, etc.)
			realm := ExtractParameter(params, "realm")
			if realm != "" {
				authInfo.Realm = realm
			}

			// RFC 9728: Check for resource_metadata parameter
			resourceMetadata := ExtractParameter(params, "resource_metadata")
			if resourceMetadata != "" {
				authInfo.ResourceMetadata = resourceMetadata
			}
		}

		return authInfo, nil
	}

	// Currently only OAuth-based authentication is supported
	// Basic and Digest authentication are not implemented
	if strings.HasPrefix(header, "Basic") || strings.HasPrefix(header, "Digest") {
		logger.Debugf("Unsupported authentication scheme: %s", header)
		return nil, fmt.Errorf("unsupported authentication scheme: %s", strings.Split(header, " ")[0])
	}

	return nil, fmt.Errorf("no supported authentication type found in header: %s", header)
}

// ExtractParameter extracts a parameter value from an authentication header
// Handles both quoted and unquoted values according to RFC 2617 and RFC 6750
func ExtractParameter(params, paramName string) string {
	// Parameters can be separated by comma or space
	// Handle both paramName=value and paramName="value" formats

	// First try to find the parameter with equals sign
	searchStr := paramName + "="
	idx := strings.Index(params, searchStr)
	if idx == -1 {
		return ""
	}

	// Extract the value after the equals sign
	valueStart := idx + len(searchStr)
	if valueStart >= len(params) {
		return ""
	}

	remainder := params[valueStart:]

	// Check if the value is quoted
	if strings.HasPrefix(remainder, `"`) {
		// Find the closing quote
		endIdx := 1
		for endIdx < len(remainder) {
			if remainder[endIdx] == '"' && (endIdx == 1 || remainder[endIdx-1] != '\\') {
				// Found unescaped closing quote
				value := remainder[1:endIdx]
				// Unescape any escaped quotes
				value = strings.ReplaceAll(value, `\"`, `"`)
				return value
			}
			endIdx++
		}
		// No closing quote found, return empty
		return ""
	}

	// Unquoted value - find the end (comma, space, or end of string)
	endIdx := 0
	for endIdx < len(remainder) {
		if remainder[endIdx] == ',' || remainder[endIdx] == ' ' {
			break
		}
		endIdx++
	}

	return strings.TrimSpace(remainder[:endIdx])
}
