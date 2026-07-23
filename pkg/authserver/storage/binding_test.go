// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureWarnLogs swaps in a buffered slog handler at warn level for the
// duration of the test and returns the captured output. Process-global, not
// safe for t.Parallel().
func captureWarnLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	orig := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// assertBindingExclusionWarn checks that the bulk-read binding exclusion WARN
// names the session, provider, and mismatch dimension — metadata only.
func assertBindingExclusionWarn(t *testing.T, out, sessionID, provider, dimension string) {
	t.Helper()
	assert.Contains(t, out, "excluding upstream token row: binding validation failed",
		"expected binding exclusion WARN; got: %s", out)
	assert.Contains(t, out, sessionID, "WARN should name the session")
	assert.Contains(t, out, provider, "WARN should name the provider")
	assert.Contains(t, out, dimension, "WARN should name the mismatch dimension")
}

// TestCheckUpstreamBinding covers the pure comparison logic of the binding
// helper: per-dimension empty-field rule and constant-time mismatch behavior.
func TestCheckUpstreamBinding(t *testing.T) {
	t.Parallel()

	stored := &UpstreamTokens{
		ProviderID:      "github",
		UserID:          "user-A",
		ClientID:        "client-A",
		UpstreamSubject: "subject-A",
	}

	tests := []struct {
		name     string
		stored   *UpstreamTokens
		expected *ExpectedBinding
		wantErr  bool
	}{
		{name: "nil stored row passes", stored: nil, expected: &ExpectedBinding{UserID: "user-B"}},
		{name: "nil expected with no resolver makes no assertion", stored: stored, expected: nil},
		{name: "expected UserID match", stored: stored, expected: &ExpectedBinding{UserID: "user-A"}},
		{name: "expected UserID mismatch", stored: stored, expected: &ExpectedBinding{UserID: "user-B"}, wantErr: true},
		{name: "client ID match", stored: stored, expected: &ExpectedBinding{ClientID: "client-A"}},
		{name: "client ID mismatch", stored: stored, expected: &ExpectedBinding{ClientID: "client-B"}, wantErr: true},
		{name: "upstream subject match", stored: stored, expected: &ExpectedBinding{UpstreamSubject: "subject-A"}},
		{name: "upstream subject mismatch", stored: stored, expected: &ExpectedBinding{UpstreamSubject: "subject-B"}, wantErr: true},
		{
			name:     "legacy row: empty stored fields pass with asserted expected",
			stored:   &UpstreamTokens{ProviderID: "github"},
			expected: &ExpectedBinding{UserID: "user-A", ClientID: "client-A", UpstreamSubject: "subject-A"},
		},
		{name: "empty expected fields skip the dimension", stored: stored, expected: &ExpectedBinding{}},
		{
			name:     "strict: legacy row (empty stored UserID) is rejected",
			stored:   &UpstreamTokens{ProviderID: "github"},
			expected: &ExpectedBinding{UserID: "user-A", Strict: true},
			wantErr:  true,
		},
		{
			name:     "strict: legacy row rejected even without asserted user",
			stored:   &UpstreamTokens{ProviderID: "github"},
			expected: &ExpectedBinding{Strict: true},
			wantErr:  true,
		},
		{
			name:     "strict: fully-owned row matching expected passes",
			stored:   stored,
			expected: &ExpectedBinding{UserID: "user-A", ClientID: "client-A", UpstreamSubject: "subject-A", Strict: true},
		},
		{
			name:     "strict: owned row with mismatched user still fails",
			stored:   stored,
			expected: &ExpectedBinding{UserID: "user-B", Strict: true},
			wantErr:  true,
		},
		{
			name:     "strict: row with UserID but empty other fields passes",
			stored:   &UpstreamTokens{ProviderID: "github", UserID: "user-A"},
			expected: &ExpectedBinding{UserID: "user-A", ClientID: "client-B", Strict: true},
		},
		{name: "equal-length user mismatch fails", stored: stored, expected: &ExpectedBinding{UserID: "user-B"}, wantErr: true},
		{
			name:     "differing-length user mismatch fails",
			stored:   stored,
			expected: &ExpectedBinding{UserID: "user-A-longer"},
			wantErr:  true,
		},
		{
			name:     "differing-length client mismatch fails",
			stored:   stored,
			expected: &ExpectedBinding{ClientID: "client-A-longer"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkUpstreamBinding(context.Background(), tt.stored, tt.expected)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidBinding)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestCheckUpstreamBinding_CtxUser covers the ctx-fallback user resolution
// (scenarios 1 and 2 of the read-side matrix at the helper level).
func TestCheckUpstreamBinding_CtxUser(t *testing.T) {
	t.Parallel()

	stored := &UpstreamTokens{ProviderID: "github", UserID: "user-A"}

	t.Run("nil expected, ctx user matches: row passes", func(t *testing.T) {
		t.Parallel()
		ctx := ContextWithBindingUser(context.Background(), "user-A")
		require.NoError(t, checkUpstreamBinding(ctx, stored, nil))
	})

	t.Run("nil expected, ctx user mismatches: ErrInvalidBinding", func(t *testing.T) {
		t.Parallel()
		ctx := ContextWithBindingUser(context.Background(), "user-B")
		err := checkUpstreamBinding(ctx, stored, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidBinding)
	})

	t.Run("explicit expected UserID wins over ctx user", func(t *testing.T) {
		t.Parallel()
		ctx := ContextWithBindingUser(context.Background(), "user-B")
		require.NoError(t, checkUpstreamBinding(ctx, stored, &ExpectedBinding{UserID: "user-A"}))
	})

	t.Run("ContextWithBindingUser with empty user leaves ctx unchanged", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		require.Equal(t, ctx, ContextWithBindingUser(ctx, ""))
	})
}
