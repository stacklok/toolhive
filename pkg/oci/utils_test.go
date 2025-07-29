package oci

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsOCIReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      string
		expected bool
	}{
		{
			name:     "valid registry reference",
			ref:      "registry.example.com/repo:tag",
			expected: true,
		},
		{
			name:     "docker hub reference",
			ref:      "docker.io/library/nginx:latest",
			expected: true,
		},
		{
			name:     "localhost registry",
			ref:      "localhost:5000/test:v1.0",
			expected: true,
		},
		{
			name:     "simple image name",
			ref:      "nginx:latest",
			expected: true,
		},
		{
			name:     "empty string",
			ref:      "",
			expected: false,
		},
		{
			name:     "invalid characters",
			ref:      "invalid:reference:with:too:many:colons",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsOCIReference(tt.ref)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      string
		expected string
	}{
		{
			name:     "reference with tag",
			ref:      "registry.example.com/repo:v1.0",
			expected: "v1.0",
		},
		{
			name:     "reference with latest tag",
			ref:      "registry.example.com/repo:latest",
			expected: "latest",
		},
		{
			name:     "reference without tag",
			ref:      "registry.example.com/repo",
			expected: "",
		},
		{
			name:     "reference with multiple colons",
			ref:      "registry.example.com:5000/repo:v1.0",
			expected: "v1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ExtractTag(tt.ref)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetCurrentTimestamp(t *testing.T) {
	t.Parallel()

	timestamp := GetCurrentTimestamp()
	assert.Equal(t, "2000-01-01T00:00:00Z", timestamp)
}

func TestValidateReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ref       string
		expectErr bool
	}{
		{
			name:      "valid reference",
			ref:       "registry.example.com/repo:tag",
			expectErr: false,
		},
		{
			name:      "invalid reference",
			ref:       "invalid:reference:with:too:many:colons",
			expectErr: true,
		},
		{
			name:      "empty reference",
			ref:       "",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateReference(tt.ref)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
