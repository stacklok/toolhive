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

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
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
			name:      "zero expiration time treated as non-expiring",
			expiresAt: time.Time{},
			checkTime: now,
			want:      false,
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

func TestUpstreamTokenRowIDResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		resolver              UpstreamTokenStorage
		delimiterRowsDistinct bool
	}{
		{name: "memory", resolver: NewMemoryStorage(), delimiterRowsDistinct: true},
		{name: "redis", resolver: &RedisStorage{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if memory, ok := tt.resolver.(*MemoryStorage); ok {
				t.Cleanup(func() { _ = memory.Close() })
			}

			first, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), "session:one", "provider:one")
			if err != nil {
				t.Fatalf("resolve first row: %v", err)
			}
			again, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), "session:one", "provider:one")
			if err != nil {
				t.Fatalf("resolve same row: %v", err)
			}
			otherSession, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), "session", "one:provider:one")
			if err != nil {
				t.Fatalf("resolve delimiter row: %v", err)
			}
			otherProvider, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), "session:one", "provider:two")
			if err != nil {
				t.Fatalf("resolve different provider: %v", err)
			}

			if first == "" {
				t.Error("row ID must not be empty")
			}
			if first != again {
				t.Errorf("same row IDs differ: %q and %q", first, again)
			}
			if tt.delimiterRowsDistinct {
				if first == otherSession {
					t.Errorf("distinct delimiter rows must have distinct IDs: %q and %q", first, otherSession)
				}
			} else if first != otherSession {
				t.Errorf("equivalent Redis row keys must have equal IDs: %q and %q", first, otherSession)
			}
			if first == otherProvider || otherSession == otherProvider {
				t.Errorf("distinct rows must have distinct IDs: %q, %q, %q", first, otherSession, otherProvider)
			}
			if strings.Contains(string(first), "session") || strings.Contains(string(first), "provider") {
				t.Errorf("row ID must be opaque, got %q", first)
			}

			for _, input := range [][2]string{{"", "provider"}, {"session", ""}} {
				_, err := tt.resolver.ResolveUpstreamTokenRowID(context.Background(), input[0], input[1])
				if !errors.Is(err, fosite.ErrInvalidRequest) {
					t.Errorf("resolve(%q, %q) error = %v, want invalid request", input[0], input[1], err)
				}
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
		return
	}

	if cfg.Type != TypeMemory {
		t.Errorf("DefaultConfig().Type = %q, want %q", cfg.Type, TypeMemory)
	}
}
