// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTokens_IsExpired(t *testing.T) {
	t.Parallel()

	t.Run("nil tokens returns true (treated as expired)", func(t *testing.T) {
		t.Parallel()
		var tokens *Tokens
		assert.True(t, tokens.IsExpired())
	})

	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "token already expired",
			expiresAt: time.Now().Add(-1 * time.Hour),
			want:      true,
		},
		{
			name:      "token expires within buffer period",
			expiresAt: time.Now().Add(15 * time.Second),
			want:      true,
		},
		{
			name:      "token expires exactly at buffer boundary",
			expiresAt: time.Now().Add(tokenExpirationBuffer),
			want:      true,
		},
		{
			name:      "token expires just after buffer period",
			expiresAt: time.Now().Add(tokenExpirationBuffer + 1*time.Second),
			want:      false,
		},
		{
			name:      "token expires well in the future",
			expiresAt: time.Now().Add(1 * time.Hour),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokens := &Tokens{
				AccessToken: "test-token",
				ExpiresAt:   tt.expiresAt,
			}
			got := tokens.IsExpired()
			assert.Equal(t, tt.want, got)
		})
	}
}
