package networking

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsRemoteURL(t *testing.T) {
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

		// Valid remote URLs (private IPs are now considered remote)
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
			result := IsRemoteURL(tt.input)
			assert.Equal(t, tt.expected, result, "Input: %s", tt.input)
		})
	}
}

func TestIsURL(t *testing.T) {
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
			result := IsURL(tt.input)
			assert.Equal(t, tt.expected, result, "Input: %s", tt.input)
		})
	}
}
