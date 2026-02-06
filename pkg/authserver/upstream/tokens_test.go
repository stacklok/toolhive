// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

	// Use a fixed reference time to avoid race conditions in time-based tests
	now := time.Now()

	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "token already expired",
			expiresAt: now.Add(-1 * time.Hour),
			want:      true,
		},
		{
			name:      "token expires within buffer period",
			expiresAt: now.Add(15 * time.Second),
			want:      true,
		},
		{
			name:      "token expires exactly at buffer boundary",
			expiresAt: now.Add(tokenExpirationBuffer),
			want:      true,
		},
		{
			name:      "token expires just after buffer period",
			expiresAt: now.Add(tokenExpirationBuffer + 1*time.Second),
			want:      false,
		},
		{
			name:      "token expires well in the future",
			expiresAt: now.Add(1 * time.Hour),
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
			// Use IsExpiredAt with the same reference time to avoid race conditions
			got := tokens.IsExpiredAt(now)
			assert.Equal(t, tt.want, got)
		})
	}
}
