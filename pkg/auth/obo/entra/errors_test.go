// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
)

func TestExchangeError_UnwrapsToSentinel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind entra.ErrorKind
		want error // nil means: matches none of the sentinels
	}{
		{"interaction required", entra.KindInteractionRequired, entra.ErrInteractionRequired},
		{"invalid grant", entra.KindInvalidGrant, entra.ErrInvalidGrant},
		{"invalid client", entra.KindInvalidClient, entra.ErrInvalidClient},
		{"transient", entra.KindTransient, entra.ErrTransient},
		{"unknown", entra.KindUnknown, nil},
	}

	allSentinels := []error{
		entra.ErrInteractionRequired, entra.ErrInvalidGrant,
		entra.ErrInvalidClient, entra.ErrTransient,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := error(&entra.ExchangeError{Kind: tt.kind})
			// Also assert matching survives one layer of fmt.Errorf wrapping,
			// the way callers propagate it.
			wrapped := fmt.Errorf("obo: %w", err)

			for _, s := range allSentinels {
				if tt.want != nil && errors.Is(tt.want, s) {
					assert.ErrorIs(t, err, s)
					assert.ErrorIs(t, wrapped, s)
				} else {
					assert.NotErrorIs(t, err, s)
					assert.NotErrorIs(t, wrapped, s)
				}
			}
		})
	}
}

func TestExchangeError_ErrorString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         *entra.ExchangeError
		wantContain []string
	}{
		{
			name:        "oauth error and description",
			err:         &entra.ExchangeError{Kind: entra.KindInvalidGrant, OAuthError: "invalid_grant", Description: "AADSTS50013", HTTPStatus: 400},
			wantContain: []string{"invalid grant", "invalid_grant", "AADSTS50013"},
		},
		{
			name:        "status only when body had no fields",
			err:         &entra.ExchangeError{Kind: entra.KindTransient, HTTPStatus: 503},
			wantContain: []string{"transient failure", "503"},
		},
		{
			name:        "transport failure with neither status nor body",
			err:         &entra.ExchangeError{Kind: entra.KindTransient, Description: "unable to reach the token endpoint"},
			wantContain: []string{"transient failure", "unable to reach"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.err.Error()
			for _, want := range tt.wantContain {
				assert.Contains(t, got, want)
			}
		})
	}
}

func TestExchangeError_AsExtractsClaims(t *testing.T) {
	t.Parallel()

	const claims = `{"access_token":{"acrs":{"essential":true}}}`
	err := fmt.Errorf("wrapped: %w", &entra.ExchangeError{
		Kind:   entra.KindInteractionRequired,
		Claims: claims,
	})

	var exErr *entra.ExchangeError
	require.ErrorAs(t, err, &exErr)
	assert.Equal(t, claims, exErr.Claims)
	assert.ErrorIs(t, err, entra.ErrInteractionRequired)
}
