// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"testing"
	"time"
)

func TestUpstreamTokens_IsExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		expiresAt time.Time
		checkTime time.Time
		want      bool
	}{
		{
			name:      "not expired - future expiration",
			expiresAt: now.Add(time.Hour),
			checkTime: now,
			want:      false,
		},
		{
			name:      "expired - past expiration",
			expiresAt: now.Add(-time.Hour),
			checkTime: now,
			want:      true,
		},
		{
			name:      "not expired - exact boundary (equal time)",
			expiresAt: now,
			checkTime: now,
			want:      false, // time.After returns false when times are equal
		},
		{
			name:      "expired - 1 nanosecond after expiration",
			expiresAt: now,
			checkTime: now.Add(time.Nanosecond),
			want:      true,
		},
		{
			name:      "not expired - 1 nanosecond before expiration",
			expiresAt: now,
			checkTime: now.Add(-time.Nanosecond),
			want:      false,
		},
		{
			name:      "expired - zero expiration time",
			expiresAt: time.Time{},
			checkTime: now,
			want:      true,
		},
		{
			name:      "not expired - zero check time with future expiration",
			expiresAt: now,
			checkTime: time.Time{},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokens := &UpstreamTokens{
				ExpiresAt: tt.expiresAt,
			}

			got := tokens.IsExpired(tt.checkTime)
			if got != tt.want {
				t.Errorf("IsExpired(%v) = %v, want %v (expiresAt=%v)",
					tt.checkTime, got, tt.want, tt.expiresAt)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}

	if cfg.Type != TypeMemory {
		t.Errorf("DefaultConfig().Type = %q, want %q", cfg.Type, TypeMemory)
	}
}
