package networking

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsRemoteURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid remote URLs
		{
			name:     "valid https remote url",
			input:    "https://api.github.com",
			expected: true,
		},
		{
			name:     "valid http remote url",
			input:    "http://example.com",
			expected: true,
		},
		{
			name:     "valid https remote url with path",
			input:    "https://api.github.com/v3/users",
			expected: true,
		},
		{
			name:     "valid https remote url with port",
			input:    "https://api.github.com:443",
			expected: true,
		},
		{
			name:     "valid https remote url with query params",
			input:    "https://api.github.com/users?page=1&per_page=10",
			expected: true,
		},
		{
			name:     "valid https remote url with fragment",
			input:    "https://api.github.com/users#section",
			expected: true,
		},
		{
			name:     "valid https remote url with public IP",
			input:    "https://8.8.8.8",
			expected: true,
		},
		{
			name:     "valid https remote url with public IP and port",
			input:    "https://8.8.8.8:443",
			expected: true,
		},

		// Invalid remote URLs (localhost)
		{
			name:     "localhost without port",
			input:    "http://localhost",
			expected: false,
		},
		{
			name:     "localhost with port",
			input:    "http://localhost:8080",
			expected: false,
		},
		{
			name:     "127.0.0.1 without port",
			input:    "http://127.0.0.1",
			expected: false,
		},
		{
			name:     "127.0.0.1 with port",
			input:    "http://127.0.0.1:8080",
			expected: false,
		},
		{
			name:     "IPv6 localhost",
			input:    "http://[::1]:8080",
			expected: false,
		},

		{
			name:     "private IP 10.0.0.1",
			input:    "http://10.0.0.1",
			expected: true,
		},
		{
			name:     "private IP 172.16.0.1",
			input:    "http://172.16.0.1:8080",
			expected: true,
		},
		{
			name:     "private IP 192.168.1.1",
			input:    "https://192.168.1.1",
			expected: true,
		},
		{
			name:     "link-local IPv4",
			input:    "http://169.254.0.1",
			expected: true,
		},
		{
			name:     "link-local IPv6",
			input:    "http://[fe80::1]",
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
			name:     "missing scheme",
			input:    "example.com",
			expected: false,
		},
		{
			name:     "unsupported scheme",
			input:    "ftp://example.com",
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
			result := IsRemoteURL(tt.input)
			assert.Equal(t, tt.expected, result, "Input: %s", tt.input)
		})
	}
}

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
			input:    "https://api.github.com",
			expected: true,
		},
		{
			name:     "valid http url",
			input:    "http://example.com",
			expected: true,
		},
		{
			name:     "valid https url with localhost",
			input:    "https://localhost:8080",
			expected: true,
		},
		{
			name:     "valid http url with private IP",
			input:    "http://192.168.1.1",
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
			name:     "missing scheme",
			input:    "example.com",
			expected: false,
		},
		{
			name:     "unsupported scheme",
			input:    "ftp://example.com",
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
