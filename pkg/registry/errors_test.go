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

// TestLegacyFormatError_Error covers the two surface forms (with and without
// URL) so CLI users keep seeing the migration hint and remote callers also see
// where the legacy content came from.
func TestLegacyFormatError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            *LegacyFormatError
		expectedPrefix string
	}{
		{
			name:           "with URL",
			err:            &LegacyFormatError{URL: "https://example.com/registry.json"},
			expectedPrefix: "registry at https://example.com/registry.json: ",
		},
		{
			name:           "without URL",
			err:            &LegacyFormatError{},
			expectedPrefix: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			msg := tt.err.Error()
			if tt.expectedPrefix != "" {
				assert.Truef(t,
					len(msg) > len(tt.expectedPrefix) && msg[:len(tt.expectedPrefix)] == tt.expectedPrefix,
					"expected message to start with %q, got %q", tt.expectedPrefix, msg)
			}
			// Migration hint must always be present so CLI users get the
			// actionable step regardless of whether a URL was attached.
			assert.Contains(t, msg, "legacy ToolHive format")
			assert.Contains(t, msg, "thv registry convert")
		})
	}
}

// TestLegacyFormatError_ErrorsIs verifies the Is method matches any
// *LegacyFormatError regardless of URL. This keeps errors.Is(err, errLegacyFormat)
// working when the returned error carries a populated URL from a remote source.
func TestLegacyFormatError_ErrorsIs(t *testing.T) {
	t.Parallel()

	withURL := &LegacyFormatError{URL: "https://example.com/registry.json"}
	sentinel := &LegacyFormatError{}

	assert.True(t, errors.Is(withURL, sentinel),
		"errors.Is(withURL, sentinel) should match via Is method")
	assert.True(t, errors.Is(sentinel, withURL),
		"errors.Is(sentinel, withURL) should match via Is method")

	// Wrapped via fmt.Errorf should still match through Unwrap chain.
	wrapped := fmt.Errorf("custom registry at %s is not reachable: %w", withURL.URL, withURL)
	assert.True(t, errors.Is(wrapped, sentinel),
		"errors.Is should follow the Unwrap chain to find a *LegacyFormatError")

	// A different error type must not match.
	other := &UnavailableError{URL: "https://example.com", Err: fmt.Errorf("boom")}
	assert.False(t, errors.Is(other, sentinel),
		"errors.Is must not cross-match *UnavailableError as legacy")
}

// TestLegacyFormatError_ErrorsAs verifies the typed extraction used by the
// API layer to surface a structured 502 response with the source URL.
func TestLegacyFormatError_ErrorsAs(t *testing.T) {
	t.Parallel()

	original := &LegacyFormatError{URL: "https://example.com/registry.json"}
	wrapped := fmt.Errorf("custom registry at %s is not reachable: %w", original.URL, original)

	var target *LegacyFormatError
	require.True(t, errors.As(wrapped, &target),
		"errors.As must extract *LegacyFormatError from the Unwrap chain")
	assert.Equal(t, "https://example.com/registry.json", target.URL)
}
