// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsentRequiredError(t *testing.T) {
	t.Parallel()

	t.Run("Unwrap preserves ErrUpstreamTokenNotFound classification", func(t *testing.T) {
		t.Parallel()
		err := &ConsentRequiredError{Provider: "github", AuthorizeURL: "https://thv.example.com/oauth/authorize"}
		assert.ErrorIs(t, err, ErrUpstreamTokenNotFound)
	})

	t.Run("errors.Is and errors.As traverse a fmt.Errorf wrap", func(t *testing.T) {
		t.Parallel()
		wrapped := fmt.Errorf("provider %q: %w", "github",
			&ConsentRequiredError{Provider: "github", AuthorizeURL: "https://thv.example.com/oauth/authorize"})
		var consentErr *ConsentRequiredError
		require.ErrorAs(t, wrapped, &consentErr)
		assert.Equal(t, "github", consentErr.Provider)
		assert.Equal(t, "https://thv.example.com/oauth/authorize", consentErr.AuthorizeURL)
		assert.ErrorIs(t, wrapped, ErrUpstreamTokenNotFound)
	})

	t.Run("Error contains provider and never token material", func(t *testing.T) {
		t.Parallel()
		err := &ConsentRequiredError{Provider: "github", AuthorizeURL: "https://thv.example.com/oauth/authorize"}
		msg := err.Error()
		assert.Contains(t, msg, "github")
		assert.Contains(t, msg, "https://thv.example.com/oauth/authorize")
		assert.NotContains(t, msg, "gho_")
		assert.NotContains(t, msg, "Bearer")

		bare := &ConsentRequiredError{Provider: "github"}
		assert.Contains(t, bare.Error(), "github")
		assert.NotContains(t, bare.Error(), "http")
	})
}

func TestFormatConsentRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		provider     string
		authorizeURL string
		wantHasURL   bool
	}{
		{
			name:         "with authorize URL",
			provider:     "github",
			authorizeURL: "https://thv.example.com/oauth/authorize",
			wantHasURL:   true,
		},
		{
			name:         "without authorize URL omits the key",
			provider:     "github",
			authorizeURL: "",
			wantHasURL:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := FormatConsentRequired(tt.provider, tt.authorizeURL)

			require.True(t, strings.HasPrefix(out, ConsentRequiredMarker+" "),
				"output must start with the marker followed by a space, got: %q", out)

			payloadJSON := strings.TrimPrefix(out, ConsentRequiredMarker+" ")
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(payloadJSON), &payload),
				"trailing payload must parse as JSON: %q", payloadJSON)

			assert.Equal(t, tt.provider, payload["provider"])
			if tt.wantHasURL {
				assert.Equal(t, tt.authorizeURL, payload["authorize_url"])
			} else {
				_, hasURL := payload["authorize_url"]
				assert.False(t, hasURL, "authorize_url key must be absent when empty")
			}
		})
	}
}
