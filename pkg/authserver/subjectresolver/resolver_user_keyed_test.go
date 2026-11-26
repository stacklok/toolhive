// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package subjectresolver_test

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
	"github.com/stacklok/toolhive/pkg/authserver/subjectresolver"
)

// TestResolveThroughUserKeyedStorage proves the resolver reads the user and the
// provider identity through the user-keyed wrapper with no canonical user in the
// context, as it does when it runs on a redemption request. The wrapper passes
// those reads through unchanged; this guards against keying them on the user.
func TestResolveThroughUserKeyedStorage(t *testing.T) {
	t.Parallel()

	backends := map[string]func(*testing.T) storage.Storage{
		"memory": func(t *testing.T) storage.Storage {
			t.Helper()
			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })
			return stor
		},
		"redis": func(t *testing.T) storage.Storage {
			t.Helper()
			client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
			t.Cleanup(func() { _ = client.Close() })
			return storage.NewRedisStorageWithClient(client, "resolver:{test}:")
		},
	}
	for name, newBackend := range backends {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stor, err := storage.NewUserKeyedUpstreamStorage(newBackend(t))
			require.NoError(t, err)
			ctx := t.Context()
			now := time.Now()
			require.NoError(t, stor.CreateUser(ctx, &storage.User{ID: "user-1", CreatedAt: now, UpdatedAt: now}))
			require.NoError(t, stor.CreateProviderIdentity(ctx, &storage.ProviderIdentity{
				UserID: "user-1", ProviderID: "corp-idp", ProviderSubject: "idp-sub-1", LinkedAt: now,
			}))

			resolver, err := subjectresolver.New(map[string]subjectresolver.Provider{
				idpIssuer: {Name: "corp-idp", SubjectClaim: "sub"},
			}, stor)
			require.NoError(t, err)

			got, err := resolver.Resolve(ctx, subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "idp-sub-1"})
			require.NoError(t, err)
			assert.Equal(t, "user-1", got)

			_, err = resolver.Resolve(ctx, subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "nobody"})
			assert.ErrorIs(t, err, subject.ErrUnknown)
		})
	}
}
