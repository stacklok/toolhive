// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnavailableError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      *UnavailableError
		expected string
	}{
		{
			name: "with URL",
			err: &UnavailableError{
				URL: "https://example.com/registry",
				Err: fmt.Errorf("connection refused"),
			},
			expected: "upstream registry at https://example.com/registry is unavailable: connection refused",
		},
		{
			name: "without URL",
			err: &UnavailableError{
				Err: fmt.Errorf("timeout"),
			},
			expected: "upstream registry is unavailable: timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestUnavailableError_Unwrap(t *testing.T) {
	t.Parallel()

	inner := fmt.Errorf("registry API returned status 404")
	err := &UnavailableError{URL: "https://example.com", Err: inner}

	assert.Equal(t, inner, errors.Unwrap(err))
}

func TestUnavailableError_ErrorsAs(t *testing.T) {
	t.Parallel()

	inner := fmt.Errorf("registry API returned status 404")
	original := &UnavailableError{URL: "https://example.com", Err: inner}
	wrapped := fmt.Errorf("failed to create provider: %w", original)

	var target *UnavailableError
	require.True(t, errors.As(wrapped, &target))
	assert.Equal(t, "https://example.com", target.URL)
	assert.Equal(t, inner, target.Err)
}
