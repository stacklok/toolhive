// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSHA256Hasher(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	secret := []byte("dGhpcyBpcyBhIDMyLWJ5dGUgY2xpZW50IHNlY3JldCE") // 43-char base64url shape

	hash, err := SHA256Hasher.Hash(ctx, secret)
	require.NoError(t, err)
	require.Len(t, hash, sha256.Size)

	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{"correct secret", secret, false},
		{"wrong secret", []byte("dGhpcyBpcyBhIDMyLWJ5dGUgY2xpZW50IHNlY3JldmE"), true},
		{"truncated secret", secret[:20], true},
		{"empty secret", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := SHA256Hasher.Compare(ctx, hash, tt.data)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}

	t.Run("rejects truncated hash", func(t *testing.T) {
		t.Parallel()
		assert.Error(t, SHA256Hasher.Compare(ctx, hash[:sha256.Size-1], secret))
	})

	// Pins the absence of a per-attempt KDF: 100 wrong-secret comparisons must
	// complete in a bound consistent with SHA-256, not bcrypt (a single
	// cost-10 bcrypt verify is ~50-100ms; 100 of them would take seconds).
	t.Run("no per-attempt KDF cost", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		for range 100 {
			_ = SHA256Hasher.Compare(ctx, hash, []byte("wrong"))
		}
		assert.Less(t, time.Since(start), 100*time.Millisecond,
			"100 wrong-secret comparisons must be far below one bcrypt verify")
	})
}
