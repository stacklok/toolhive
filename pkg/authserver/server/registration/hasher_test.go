// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"crypto/sha256"
	"testing"

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
}
