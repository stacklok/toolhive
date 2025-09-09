package networking

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid URLs
		{
			name:     "valid https url",
			input:    "https://example.com",
			expected: true,
		},
		{
			name:     "valid http url",
			input:    "http://example.com",
			expected: true,
		},
		{
			name:     "valid https url with path",
			input:    "https://example.com/path",
			expected: true,
		},
		{
			name:     "valid https url with query params",
			input:    "https://example.com/path?param=value",
			expected: true,
		},
		{
			name:     "valid https url with fragment",
			input:    "https://example.com/path#fragment",
			expected: true,
		},
		{
			name:     "valid https url with port",
			input:    "https://example.com:8080",
			expected: true,
		},
		{
			name:     "valid https url with user info",
			input:    "https://user:pass@example.com",
			expected: true,
		},

		// Invalid URLs
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "invalid URL",
			input:    "not-a-url",
			expected: false,
		},
		{
			name:     "unsupported scheme",
			input:    "ftp://example.com",
			expected: false,
		},
		{
			name:     "missing scheme",
			input:    "example.com",
			expected: false,
		},
		{
			name:     "missing host",
			input:    "https://",
			expected: false,
		},
		{
			name:     "missing host with path",
			input:    "https:///path",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsURL(tt.input)
			assert.Equal(t, tt.expected, result, "Input: %s", tt.input)
		})
	}
}

func TestIsLocalhost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid localhost hosts
		{
			name:     "localhost without port",
			input:    "localhost",
			expected: true,
		},
		{
			name:     "localhost with port",
			input:    "localhost:8080",
			expected: true,
		},
		{
			name:     "localhost with large port",
			input:    "localhost:65535",
			expected: true,
		},
		{
			name:     "127.0.0.1 without port",
			input:    "127.0.0.1",
			expected: true,
		},
		{
			name:     "127.0.0.1 with port",
			input:    "127.0.0.1:8080",
			expected: true,
		},
		{
			name:     "127.0.0.1 with large port",
			input:    "127.0.0.1:65535",
			expected: true,
		},
		{
			name:     "IPv6 localhost without port",
			input:    "[::1]",
			expected: true,
		},
		{
			name:     "IPv6 localhost with port",
			input:    "[::1]:8080",
			expected: true,
		},
		{
			name:     "IPv6 localhost with large port",
			input:    "[::1]:65535",
			expected: true,
		},

		// Invalid localhost hosts
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "random hostname",
			input:    "example.com",
			expected: false,
		},
		{
			name:     "random hostname with port",
			input:    "example.com:8080",
			expected: false,
		},
		{
			name:     "public IP without port",
			input:    "8.8.8.8",
			expected: false,
		},
		{
			name:     "public IP with port",
			input:    "8.8.8.8:8080",
			expected: false,
		},
		{
			name:     "private IP without port",
			input:    "192.168.1.1",
			expected: false,
		},
		{
			name:     "private IP with port",
			input:    "192.168.1.1:8080",
			expected: false,
		},
		{
			name:     "IPv6 public address",
			input:    "[2001:db8::1]",
			expected: false,
		},
		{
			name:     "IPv6 public address with port",
			input:    "[2001:db8::1]:8080",
			expected: false,
		},
		{
			name:     "localhost with invalid port",
			input:    "localhost:99999",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "127.0.0.1 with invalid port",
			input:    "127.0.0.1:99999",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "IPv6 localhost with invalid port",
			input:    "[::1]:99999",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "localhost with non-numeric port",
			input:    "localhost:abc",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "127.0.0.1 with non-numeric port",
			input:    "127.0.0.1:abc",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "IPv6 localhost with non-numeric port",
			input:    "[::1]:abc",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "localhost with empty port",
			input:    "localhost:",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "127.0.0.1 with empty port",
			input:    "127.0.0.1:",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "IPv6 localhost with empty port",
			input:    "[::1]:",
			expected: true, // Still matches the prefix check
		},
		{
			name:     "case insensitive localhost",
			input:    "LOCALHOST",
			expected: false, // Current implementation is case sensitive
		},
		{
			name:     "case insensitive localhost with port",
			input:    "LOCALHOST:8080",
			expected: false, // Current implementation is case sensitive
		},
		{
			name:     "mixed case localhost",
			input:    "LocalHost",
			expected: false, // Current implementation is case sensitive
		},
		{
			name:     "localhost with spaces",
			input:    "localhost ",
			expected: false,
		},
		{
			name:     "localhost with leading space",
			input:    " localhost",
			expected: false,
		},
		{
			name:     "127.0.0.1 with spaces",
			input:    "127.0.0.1 ",
			expected: false,
		},
		{
			name:     "127.0.0.1 with leading space",
			input:    " 127.0.0.1",
			expected: false,
		},
		{
			name:     "IPv6 localhost with spaces",
			input:    "[::1] ",
			expected: false,
		},
		{
			name:     "IPv6 localhost with leading space",
			input:    " [::1]",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsLocalhost(tt.input)
			assert.Equal(t, tt.expected, result, "Input: %s", tt.input)
		})
	}
}

func TestAddressReferencesPrivateIp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		address     string
		expectError bool
	}{
		// Public IP addresses (should not error)
		{
			name:        "public IP with port",
			address:     "8.8.8.8:80",
			expectError: false,
		},
		{
			name:        "public IP with different port",
			address:     "1.1.1.1:443",
			expectError: false,
		},

		// Private IP addresses (should error)
		{
			name:        "localhost IP with port",
			address:     "127.0.0.1:8080",
			expectError: true,
		},
		{
			name:        "RFC1918 10.x.x.x with port",
			address:     "10.0.0.1:80",
			expectError: true,
		},
		{
			name:        "RFC1918 172.16.x.x with port",
			address:     "172.16.0.1:80",
			expectError: true,
		},
		{
			name:        "RFC1918 192.168.x.x with port",
			address:     "192.168.1.1:80",
			expectError: true,
		},
		{
			name:        "link-local 169.254.x.x with port",
			address:     "169.254.1.1:80",
			expectError: true,
		},
		{
			name:        "IPv6 loopback with port",
			address:     "[::1]:8080",
			expectError: true,
		},
		{
			name:        "IPv6 link-local with port",
			address:     "[fe80::1]:80",
			expectError: true,
		},
		{
			name:        "IPv6 unique local with port",
			address:     "[fc00::1]:80",
			expectError: true,
		},

		// Invalid addresses (should error due to parsing)
		{
			name:        "invalid address format",
			address:     "invalid-address",
			expectError: true,
		},
		{
			name:        "missing port",
			address:     "8.8.8.8",
			expectError: true,
		},
		{
			name:        "empty address",
			address:     "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := AddressReferencesPrivateIp(tt.address)
			if tt.expectError {
				assert.Error(t, err, "Expected error for address: %s", tt.address)
			} else {
				assert.NoError(t, err, "Expected no error for address: %s", tt.address)
			}
		})
	}
}

func TestValidateEndpointURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		endpoint       string
		skipValidation bool
		expectError    bool
	}{
		// Valid HTTPS URLs (should not error)
		{
			name:        "valid HTTPS URL",
			endpoint:    "https://example.com",
			expectError: false,
		},
		{
			name:        "valid HTTPS URL with path",
			endpoint:    "https://example.com/api/v1",
			expectError: false,
		},
		{
			name:        "valid HTTPS URL with port",
			endpoint:    "https://example.com:8443",
			expectError: false,
		},

		// Localhost URLs with HTTP (should not error)
		{
			name:        "localhost HTTP URL",
			endpoint:    "http://localhost:8080",
			expectError: false,
		},
		{
			name:        "127.0.0.1 HTTP URL",
			endpoint:    "http://127.0.0.1:8080",
			expectError: false,
		},
		{
			name:        "IPv6 localhost HTTP URL",
			endpoint:    "http://[::1]:8080",
			expectError: false,
		},

		// Non-localhost HTTP URLs (should error)
		{
			name:        "HTTP URL for non-localhost",
			endpoint:    "http://example.com",
			expectError: true,
		},
		{
			name:        "HTTP URL with public IP",
			endpoint:    "http://8.8.8.8:80",
			expectError: true,
		},

		// Invalid URLs (should error)
		{
			name:        "invalid URL format",
			endpoint:    "not-a-url",
			expectError: true,
		},
		{
			name:        "empty URL",
			endpoint:    "",
			expectError: true,
		},
		{
			name:        "unsupported scheme",
			endpoint:    "ftp://example.com",
			expectError: true,
		},

		// Skip validation cases (should not error)
		{
			name:           "HTTP URL with validation skipped",
			endpoint:       "http://example.com",
			skipValidation: true,
			expectError:    false,
		},
		{
			name:           "invalid URL with validation skipped",
			endpoint:       "not-a-url",
			skipValidation: true,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateEndpointURLWithSkip(tt.endpoint, tt.skipValidation)
			if tt.expectError {
				assert.Error(t, err, "Expected error for endpoint: %s", tt.endpoint)
			} else {
				assert.NoError(t, err, "Expected no error for endpoint: %s", tt.endpoint)
			}
		})
	}
}
