// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func testSPIFFEClients(t *testing.T) map[string]fosite.Client {
	t.Helper()
	client, err := registration.NewSPIFFEClient(
		"spiffe-client",
		[]string{"openid"},
		[]string{"https://api.example.com"},
	)
	require.NoError(t, err)
	return map[string]fosite.Client{client.GetID(): client}
}

func TestSPIFFEStorageDecoratorStaticClients(t *testing.T) {
	t.Parallel()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, testSPIFFEClients(t))
	require.NoError(t, err)
	client, err := decorated.GetClient(context.Background(), "spiffe-client")
	require.NoError(t, err)
	assert.Equal(t, "spiffe-client", client.GetID())
	assert.Empty(t, client.GetResponseTypes())
	require.ErrorIs(t, decorated.RegisterClient(context.Background(), &fosite.DefaultClient{ID: "spiffe-client"}), ErrAlreadyExists)
}

// TestSPIFFEStorageDecoratorReconcileConfiguredClientRejectsReservedID mirrors
// TestSPIFFEStorageDecoratorStaticClients' RegisterClient assertion, but for
// ReconcileConfiguredClient: an operator-declared client must not be able to
// reconcile onto a reserved static SPIFFE client ID either.
func TestSPIFFEStorageDecoratorReconcileConfiguredClientRejectsReservedID(t *testing.T) {
	t.Parallel()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, testSPIFFEClients(t))
	require.NoError(t, err)

	err = decorated.ReconcileConfiguredClient(context.Background(), &fosite.DefaultClient{ID: "spiffe-client"})
	require.ErrorIs(t, err, ErrAlreadyExists)
}

// TestSPIFFEStorageDecoratorDurablyClaimsPlaceholder pins the race fix:
// construction durably claims each configured client ID in the base backend
// with an inert placeholder (never the real, unauthenticatable-by-design
// SPIFFE client), so a concurrent DCR registration on another replica cannot
// claim the same ID after this replica's construction completes.
func TestSPIFFEStorageDecoratorDurablyClaimsPlaceholder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	_, err := NewSPIFFEStorageDecorator(ctx, base, testSPIFFEClients(t))
	require.NoError(t, err)

	claimed, err := base.GetClient(ctx, "spiffe-client")
	require.NoError(t, err, "construction must durably claim the client ID in the base backend")
	assert.Empty(t, claimed.GetGrantTypes(), "the durable placeholder must carry no grant types")
	assert.Empty(t, claimed.GetResponseTypes(), "the durable placeholder must carry no response types")
	assert.Nil(t, claimed.GetHashedSecret(), "the durable placeholder must carry no secret")
	assert.False(t, registration.DCRIssued(claimed))
}

// TestSPIFFEStorageDecoratorRestartIsIdempotent pins the restart case:
// reconstructing the decorator with the SAME association config reclaims the
// identical placeholder and succeeds, rather than colliding with itself.
func TestSPIFFEStorageDecoratorRestartIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	_, err := NewSPIFFEStorageDecorator(ctx, base, testSPIFFEClients(t))
	require.NoError(t, err)

	_, err = NewSPIFFEStorageDecorator(ctx, base, testSPIFFEClients(t))
	require.NoError(t, err, "reconstructing with unchanged association config must be idempotent")
}

func TestSPIFFEStorageDecoratorRejectsInvalidClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		clients map[string]fosite.Client
		wantErr string
	}{
		{
			name:    "nil client",
			clients: map[string]fosite.Client{"spiffe-client": nil},
			wantErr: `static SPIFFE client "spiffe-client" is required`,
		},
		{
			name: "map key does not match client ID",
			clients: map[string]fosite.Client{
				"map-key": &fosite.DefaultClient{ID: "client-id"},
			},
			wantErr: `map key "map-key" does not match client ID "client-id"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := NewMemoryStorage()
			t.Cleanup(func() { _ = base.Close() })

			_, err := NewSPIFFEStorageDecorator(context.Background(), base, tt.clients)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSPIFFEStorageDecoratorClonesClientMap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	clients := testSPIFFEClients(t)

	decorated, err := NewSPIFFEStorageDecorator(ctx, base, clients)
	require.NoError(t, err)
	delete(clients, "spiffe-client")
	clients["added-client"] = &fosite.DefaultClient{ID: "added-client"}

	client, err := decorated.GetClient(ctx, "spiffe-client")
	require.NoError(t, err)
	assert.Equal(t, "spiffe-client", client.GetID())
	require.ErrorIs(t, decorated.RegisterClient(ctx, &fosite.DefaultClient{ID: "spiffe-client"}), ErrAlreadyExists)

	_, err = decorated.GetClient(ctx, "added-client")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, decorated.RegisterClient(ctx, &fosite.DefaultClient{ID: "added-client"}))
}

func TestSPIFFEStorageDecoratorRejectsDurableCollision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	dcrIssued, err := registration.New(registration.Config{
		ID:                      "spiffe-client",
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone,
		RedirectURIs:            []string{"https://app.example/cb"},
	})
	require.NoError(t, err)
	require.True(t, registration.DCRIssued(dcrIssued), "precondition: seeded client must be DCR-issued")
	require.NoError(t, base.RegisterClient(ctx, dcrIssued))

	_, err = NewSPIFFEStorageDecorator(ctx, base, testSPIFFEClients(t))
	require.ErrorIs(t, err, ErrAlreadyExists)
	assert.Contains(t, err.Error(), "DCR-issued")
}

// TestSPIFFEStorageDecoratorRejectsDifferentConfiguredClientCollision covers
// the case ReconcileConfiguredClient's fingerprint check adds: a *different*
// configured client (not DCR-issued) already durably claiming a reserved ID
// -- e.g. a stale placeholder from a prior, differently-scoped association --
// must still fail construction loudly rather than silently diverging.
func TestSPIFFEStorageDecoratorRejectsDifferentConfiguredClientCollision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	// Pre-seed a durably-claimed placeholder with a different fingerprint
	// (mismatched scopes) than the one the real association will produce.
	mismatched := &fosite.DefaultClient{ID: "spiffe-client", Scopes: []string{"a-different-scope"}}
	require.NoError(t, base.ReconcileConfiguredClient(ctx, mismatched))

	_, err := NewSPIFFEStorageDecorator(ctx, base, testSPIFFEClients(t))
	require.ErrorIs(t, err, ErrAlreadyExists)
	assert.Contains(t, err.Error(), "different configured client")
}

func TestSPIFFEStorageDecoratorLeavesEmptyClientsUnchanged(t *testing.T) {
	t.Parallel()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, nil)
	require.NoError(t, err)
	assert.Same(t, base, decorated)
}

// TestSPIFFEStorageDecoratorPlaceholderInertOnRawRedisReadback is the
// regression test for PR #6474 review finding 1: the durable placeholder
// written by PreflightSPIFFEStaticClientCollisions must remain unusable even
// when read back through the RAW, unwrapped Redis storage — simulating an
// old or mid-rollout replica with no SPIFFE overlay reaching the placeholder
// through the ordinary fosite.ClientManager.GetClient path.
// fosite.DefaultClient.GetGrantTypes/GetResponseTypes silently default to a
// real grant/response type ("authorization_code" / "code") whenever the
// underlying field is empty, and clientFromStored used to reconstruct a bare
// *fosite.DefaultClient on every read, reintroducing exactly that defaulting
// for a row that must never satisfy it. Without the storedClient.Reserved fix
// in clientFromStored, this test fails with non-empty grant/response types.
func TestSPIFFEStorageDecoratorPlaceholderInertOnRawRedisReadback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	raw, mr := newTestRedisStorage(t)
	t.Cleanup(func() { _ = raw.Close(); mr.Close() })

	require.NoError(t, PreflightSPIFFEStaticClientCollisions(ctx, raw, testSPIFFEClients(t)))

	client, err := raw.GetClient(ctx, "spiffe-client")
	require.NoError(t, err)

	assert.Empty(t, client.GetGrantTypes(), "placeholder must carry no usable grant type on raw Redis readback")
	assert.Empty(t, client.GetResponseTypes(), "placeholder must carry no usable response type on raw Redis readback")
	assert.Nil(t, client.GetHashedSecret(), "placeholder must carry no usable secret on raw Redis readback")
	assert.False(t, client.IsPublic(), "placeholder must not read back as a public client")
	assert.True(t, registration.BackChannelOnly(client),
		"placeholder must carry the explicit back-channel-only marker on raw Redis readback, "+
			"not rely solely on empty grant/response types")
}

// TestSPIFFEStorageDecoratorDurableClaimRedisBacked exercises the full
// durable-claim flow (the point of PR #6474) against a real RedisStorage
// backend rather than MemoryStorage: the original cross-replica race (review
// finding 1's precondition) only manifests with a shared backend, since
// MemoryStorage is per-process.
func TestSPIFFEStorageDecoratorDurableClaimRedisBacked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base, mr := newTestRedisStorage(t)
	t.Cleanup(func() { _ = base.Close(); mr.Close() })

	_, err := NewSPIFFEStorageDecorator(ctx, base, testSPIFFEClients(t))
	require.NoError(t, err)

	// Durably written: readable via GetClient on the raw Redis storage.
	claimed, err := base.GetClient(ctx, "spiffe-client")
	require.NoError(t, err)
	assert.Empty(t, claimed.GetGrantTypes())

	// Reconstructing with the same config twice is idempotent (restart case).
	_, err = NewSPIFFEStorageDecorator(ctx, base, testSPIFFEClients(t))
	require.NoError(t, err)

	// Reconstructing with a different/conflicting association at the same ID
	// fails loudly.
	conflicting, err := registration.NewSPIFFEClient(
		"spiffe-client", []string{"a-different-scope"}, []string{"https://api.example.com"})
	require.NoError(t, err)
	_, err = NewSPIFFEStorageDecorator(ctx, base, map[string]fosite.Client{"spiffe-client": conflicting})
	require.ErrorIs(t, err, ErrAlreadyExists)
}

// TestSPIFFEStorageDecorator_ConsumeAssertionJWTDelegatesToWrapped is the
// regression test for PR #6474 review finding 2: SPIFFEStorageDecorator must
// forward JWT-bearer assertion-replay consumption to its immediately-wrapped
// storage (one level down), never by unwrapping past it, so replay protection
// is actually enforced (not just accidentally reachable) when this decorator
// sits in the chain. MemoryStorage's own ConsumeAssertionJWT does the real
// replay bookkeeping here, proving the forward is live rather than a stub.
func TestSPIFFEStorageDecorator_ConsumeAssertionJWTDelegatesToWrapped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	decorated, err := NewSPIFFEStorageDecorator(ctx, base, testSPIFFEClients(t))
	require.NoError(t, err)
	consumer, ok := decorated.(AssertionJWTConsumer)
	require.True(t, ok, "SPIFFEStorageDecorator must implement AssertionJWTConsumer")

	exp := time.Now().Add(time.Hour)
	require.NoError(t, consumer.ConsumeAssertionJWT(ctx, "jwt-bearer", "https://issuer.example", "jti", exp))
	require.ErrorIs(t,
		consumer.ConsumeAssertionJWT(ctx, "jwt-bearer", "https://issuer.example", "jti", exp),
		fosite.ErrJTIKnown)
}

// TestSPIFFEStorageDecorator_ConsumeAssertionJWTFailsClosedWithoutBackendCapability
// mirrors CIMDStorageDecorator's equivalent test: when the wrapped storage
// does not implement AssertionJWTConsumer, SPIFFEStorageDecorator must fail
// closed with an error naming the concrete wrapped type, not silently succeed
// or panic. storageWithoutAssertionJWTConsumer is defined in
// cimd_decorator_test.go and reused here.
//
// The decorator is built directly (not via NewSPIFFEStorageDecorator) because
// the constructor's preflight durably claims each client ID against the base
// backend, which would call methods on the embedded nil Storage this fake
// deliberately doesn't implement.
func TestSPIFFEStorageDecorator_ConsumeAssertionJWTFailsClosedWithoutBackendCapability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	decorated := &SPIFFEStorageDecorator{Storage: storageWithoutAssertionJWTConsumer{}, clients: testSPIFFEClients(t)}

	err := decorated.ConsumeAssertionJWT(ctx, "jwt-bearer", "https://issuer.example", "jti", time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support assertion JWT replay consumption")
}
