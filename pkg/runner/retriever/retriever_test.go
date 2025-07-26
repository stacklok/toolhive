package retriever

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize logger to prevent nil pointer dereference in tests
	logger.Initialize()
}

func TestTryLoadRunConfigFromOCI_InvalidReference(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Test with truly invalid reference that will fail parsing
	_, err := tryLoadRunConfigFromOCI(ctx, "invalid:reference:with:too:many:colons")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a runtime configuration artifact")
}

func TestTryLoadRunConfigFromOCI_NonExistentRegistry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Test with non-existent registry (should fail with network/connection error)
	_, err := tryLoadRunConfigFromOCI(ctx, "nonexistent.registry.example.com/test:latest")
	assert.Error(t, err)
	// The error should indicate it's not a runtime configuration artifact
	// (after network error handling)
}

func TestContainsAuthError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		errStr   string
		expected bool
	}{
		{
			name:     "authentication required",
			errStr:   "authentication required",
			expected: true,
		},
		{
			name:     "unauthorized",
			errStr:   "unauthorized access",
			expected: true,
		},
		{
			name:     "401 status",
			errStr:   "HTTP 401 error",
			expected: true,
		},
		{
			name:     "403 status",
			errStr:   "HTTP 403 forbidden",
			expected: true,
		},
		{
			name:     "forbidden",
			errStr:   "access forbidden",
			expected: true,
		},
		{
			name:     "access denied",
			errStr:   "access denied to resource",
			expected: true,
		},
		{
			name:     "invalid credentials",
			errStr:   "invalid credentials provided",
			expected: true,
		},
		{
			name:     "network error",
			errStr:   "connection timeout",
			expected: false,
		},
		{
			name:     "other error",
			errStr:   "some other error",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := containsAuthError(tt.errStr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestContainsNetworkError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		errStr   string
		expected bool
	}{
		{
			name:     "connection refused",
			errStr:   "connection refused",
			expected: true,
		},
		{
			name:     "timeout",
			errStr:   "request timeout",
			expected: true,
		},
		{
			name:     "network unreachable",
			errStr:   "network unreachable",
			expected: true,
		},
		{
			name:     "no such host",
			errStr:   "no such host found",
			expected: true,
		},
		{
			name:     "connection reset",
			errStr:   "connection reset by peer",
			expected: true,
		},
		{
			name:     "dial tcp",
			errStr:   "dial tcp: connection failed",
			expected: true,
		},
		{
			name:     "auth error",
			errStr:   "authentication required",
			expected: false,
		},
		{
			name:     "other error",
			errStr:   "some other error",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := containsNetworkError(tt.errStr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestContains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{
			name:     "substring at beginning",
			s:        "hello world",
			substr:   "hello",
			expected: true,
		},
		{
			name:     "substring at end",
			s:        "hello world",
			substr:   "world",
			expected: true,
		},
		{
			name:     "substring in middle",
			s:        "hello world",
			substr:   "lo wo",
			expected: true,
		},
		{
			name:     "exact match",
			s:        "hello",
			substr:   "hello",
			expected: true,
		},
		{
			name:     "substring not found",
			s:        "hello world",
			substr:   "xyz",
			expected: false,
		},
		{
			name:     "empty substring",
			s:        "hello world",
			substr:   "",
			expected: true,
		},
		{
			name:     "empty string",
			s:        "",
			substr:   "hello",
			expected: false,
		},
		{
			name:     "both empty",
			s:        "",
			substr:   "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := contains(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIndexOfSubstring(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		s        string
		substr   string
		expected int
	}{
		{
			name:     "substring at beginning",
			s:        "hello world",
			substr:   "hello",
			expected: 0,
		},
		{
			name:     "substring at end",
			s:        "hello world",
			substr:   "world",
			expected: 6,
		},
		{
			name:     "substring in middle",
			s:        "hello world",
			substr:   "lo",
			expected: 3,
		},
		{
			name:     "substring not found",
			s:        "hello world",
			substr:   "xyz",
			expected: -1,
		},
		{
			name:     "empty substring",
			s:        "hello world",
			substr:   "",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := indexOfSubstring(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetMCPServer_WithOCIReference tests the integration of OCI reference detection
// in the main GetMCPServer function
func TestGetMCPServer_WithOCIReference(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Test with a non-existent OCI reference (should fall back to normal flow)
	imageURL, metadata, err := GetMCPServer(ctx, "nonexistent.registry.example.com/test:latest", "", "disabled")

	// This should either:
	// 1. Return an error due to network issues (expected)
	// 2. Fall back to treating it as a regular image and proceed with normal flow
	if err != nil {
		// If it errors, it should be due to image pulling/verification, not OCI detection
		assert.NotContains(t, err.Error(), "failed to pull as runtime configuration artifact")
	} else {
		// If it succeeds, it should return the original reference
		assert.Equal(t, "nonexistent.registry.example.com/test:latest", imageURL)
		assert.Nil(t, metadata) // No registry metadata for unknown images
	}
}
