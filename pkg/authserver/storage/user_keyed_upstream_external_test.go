// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// An authenticated Identity, even one carrying a platform user, is not a
// canonical user: the wrapper must never key a row off it. This lives in an
// external test package because pkg/auth depends on storage.
func TestUserKeyedUpstreamStorage_IgnoresIdentity(t *testing.T) {
	t.Parallel()
	m := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = m.Close() })
	dec, err := storage.NewUserKeyedUpstreamStorage(m)
	require.NoError(t, err)

	id := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "sub", PlatformUserID: "platform-user"}}
	ctx := auth.WithIdentity(context.Background(), id)

	_, err = dec.GetUpstreamTokens(ctx, "s", "p")
	assert.ErrorIs(t, err, storage.ErrNoCanonicalUser)
	assert.ErrorIs(t, dec.StoreUpstreamTokens(ctx, "s", "p", &storage.UpstreamTokens{UserID: "platform-user"}),
		storage.ErrNoCanonicalUser)
}
