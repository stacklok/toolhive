package common

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testSchemeHTTPS = "https"

func TestExtractForwardedHeaders(t *testing.T) {
	t.Parallel()

	t.Run("trust enabled extracts all headers", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-Proto", testSchemeHTTPS)
		req.Header.Set("X-Forwarded-Host", "example.com")
		req.Header.Set("X-Forwarded-Port", "8080")
		req.Header.Set("X-Forwarded-Prefix", "/api")

		headers := ExtractForwardedHeaders(req, true)

		if headers.Proto != testSchemeHTTPS {
			t.Errorf("Expected Proto to be https, got %s", headers.Proto)
		}
		if headers.Host != "example.com" {
			t.Errorf("Expected Host to be example.com, got %s", headers.Host)
		}
		if headers.Port != "8080" {
			t.Errorf("Expected Port to be 8080, got %s", headers.Port)
		}
		if headers.Prefix != "/api" {
			t.Errorf("Expected Prefix to be /api, got %s", headers.Prefix)
		}
	})

	t.Run("trust disabled only extracts Proto", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-Proto", testSchemeHTTPS)
		req.Header.Set("X-Forwarded-Host", "example.com")
		req.Header.Set("X-Forwarded-Port", "8080")
		req.Header.Set("X-Forwarded-Prefix", "/api")

		headers := ExtractForwardedHeaders(req, false)

		if headers.Proto != testSchemeHTTPS {
			t.Errorf("Expected Proto to be https, got %s", headers.Proto)
		}
		if headers.Host != "" {
			t.Errorf("Expected Host to be empty, got %s", headers.Host)
		}
		if headers.Port != "" {
			t.Errorf("Expected Port to be empty, got %s", headers.Port)
		}
		if headers.Prefix != "" {
			t.Errorf("Expected Prefix to be empty, got %s", headers.Prefix)
		}
	})

	t.Run("missing headers return empty strings", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)

		headers := ExtractForwardedHeaders(req, true)

		if headers.Proto != "" {
			t.Errorf("Expected Proto to be empty, got %s", headers.Proto)
		}
		if headers.Host != "" {
			t.Errorf("Expected Host to be empty, got %s", headers.Host)
		}
		if headers.Port != "" {
			t.Errorf("Expected Port to be empty, got %s", headers.Port)
		}
		if headers.Prefix != "" {
			t.Errorf("Expected Prefix to be empty, got %s", headers.Prefix)
		}
	})

	t.Run("Proto always extracted regardless of trust", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-Proto", testSchemeHTTPS)

		// Test with trust disabled
		headers := ExtractForwardedHeaders(req, false)
		if headers.Proto != testSchemeHTTPS {
			t.Errorf("Expected Proto to be https with trust disabled, got %s", headers.Proto)
		}

		// Test with trust enabled
		headers = ExtractForwardedHeaders(req, true)
		if headers.Proto != testSchemeHTTPS {
			t.Errorf("Expected Proto to be https with trust enabled, got %s", headers.Proto)
		}
	})
}

func TestJoinHostPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		host     string
		port     string
		expected string
	}{
		{
			name:     "empty port returns host unchanged",
			host:     "example.com",
			port:     "",
			expected: "example.com",
		},
		{
			name:     "host without port adds port",
			host:     "example.com",
			port:     "8080",
			expected: "example.com:8080",
		},
		{
			name:     "host with existing port strips and adds new port",
			host:     "example.com:9090",
			port:     "8080",
			expected: "example.com:8080",
		},
		{
			name:     "IPv6 host without port adds port",
			host:     "::1",
			port:     "8080",
			expected: "[::1]:8080",
		},
		{
			name:     "IPv6 host with existing port strips and adds new port",
			host:     "[::1]:9090",
			port:     "8080",
			expected: "[::1]:8080",
		},
		{
			name:     "localhost without port adds port",
			host:     "localhost",
			port:     "3000",
			expected: "localhost:3000",
		},
		{
			name:     "localhost with existing port strips and adds new port",
			host:     "localhost:9090",
			port:     "3000",
			expected: "localhost:3000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := JoinHostPort(tt.host, tt.port)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}
