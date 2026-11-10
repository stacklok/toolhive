// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto/dcrkey"
	"github.com/stacklok/toolhive/pkg/oauthproto/dcrstore"
	"github.com/stacklok/toolhive/pkg/storageerr"
)

var _ func(storage.DCRCredentialStore) dcr.CredentialStore = dcr.NewStorageBackedStore

// The DCR route is deliberately distinct from the broad storage handle, as in
// a decorated backend that dispatches different capabilities to different stores.
type routedDCRBackend struct {
	storage.Storage
	storage.DCRCredentialStore
}

func (*routedDCRBackend) Unwrap() storage.Storage {
	panic("DCR adapter must not unwrap its backend")
}

func TestDCRStorageSentinelCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		legacy  error
		neutral error
	}{
		{"not found", storage.ErrNotFound, storageerr.ErrNotFound},
		{"concurrent change", storage.ErrDCRCredentialsChanged, dcrstore.ErrDCRCredentialsChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, tt.legacy == tt.neutral, "legacy and neutral names must be the same value")
			assert.ErrorIs(t, fmt.Errorf("wrapped: %w", tt.legacy), tt.neutral)
		})
	}
}

func newRoutedAdapter(t *testing.T) (dcr.CredentialStore, *mocks.MockDCRCredentialStore) {
	t.Helper()
	backend := mocks.NewMockDCRCredentialStore(gomock.NewController(t))
	broad := mocks.NewMockStorage(gomock.NewController(t))
	// The legacy spelling must remain assignable to the adapter's parameter.
	var legacy storage.DCRCredentialStore = backend
	return dcr.NewStorageBackedStore(&routedDCRBackend{Storage: broad, DCRCredentialStore: legacy}), backend
}

func TestDCRStorageAdapter_ReplaceUsesPhysicalRowAsWitness(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	key := dcr.Key{
		Issuer:      "issuer",
		UpstreamID:  "upstream",
		RedirectURI: "redirect",
		ScopesHash:  dcrkey.ScopesHash(nil),
		RotationKey: "rotation",
	}
	physical := &storage.DCRCredentials{
		Key: key, ClientID: "old", ProviderName: "physical-only", CreatedAt: time.Now(),
	}
	winner := &storage.DCRCredentials{Key: key, ClientID: "winner", ClientSecret: "winner-secret"}
	store, backend := newRoutedAdapter(t)

	replacer, ok := store.(dcr.Replacer)
	require.True(t, ok)
	var embedded dcr.CredentialStore = struct{ dcr.CredentialStore }{store}
	_, ok = embedded.(dcr.Replacer)
	assert.False(t, ok, "embedding only CredentialStore must not expose replacement")

	gomock.InOrder(
		backend.EXPECT().GetDCRCredentials(ctx, key).Return(physical, nil),
		backend.EXPECT().UpdateDCRCredentialsIfUnchanged(ctx, gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, candidate, witness *storage.DCRCredentials) (*storage.DCRCredentials, error) {
				require.Same(t, physical, witness)
				assert.Equal(t, "physical-only", witness.ProviderName)
				assert.Equal(t, "fresh", candidate.ClientID)
				return nil, fmt.Errorf("conflict: %w", storage.ErrDCRCredentialsChanged)
			}),
		backend.EXPECT().StoreDCRCredentialsIfAbsent(ctx, gomock.Any()).Return(winner, nil),
		backend.EXPECT().GetDCRCredentials(ctx, key).Return(winner, nil),
	)
	got, err := replacer.ReplaceIfUnchanged(
		ctx, key, &dcr.Resolution{ClientID: "old"}, &dcr.Resolution{ClientID: "fresh"})
	require.NoError(t, err)
	assert.Equal(t, "winner", got.ClientID)

	// Neither the returned value nor a later read aliases the backend's row.
	got.ClientID = "mutated"
	read, found, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "winner", read.ClientID)
	read.ClientSecret = "mutated"
	assert.Equal(t, "winner-secret", winner.ClientSecret)
}

func TestDCRStorageAdapter_MissIdentities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		miss error
	}{
		{"wrapped storage sentinel", fmt.Errorf("storage miss: %w", storage.ErrNotFound)},
		{
			"fosite error wrapping the sentinel",
			fosite.ErrNotFound.WithHint("missing DCR row").WithWrap(storage.ErrNotFound),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			key := dcr.Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "redirect", ScopesHash: "h"}
			store, backend := newRoutedAdapter(t)
			backend.EXPECT().GetDCRCredentials(ctx, key).Return(nil, tt.miss)

			got, found, err := store.Get(ctx, key)
			require.NoError(t, err)
			assert.False(t, found)
			assert.Nil(t, got)
		})
	}
}

func newTestRegistration(id string) *dcr.Resolution {
	return &dcr.Resolution{
		ClientID:              id,
		ClientSecret:          id + "-secret",
		AuthorizationEndpoint: "auth",
		TokenEndpoint:         "token",
		CreatedAt:             time.Now(),
	}
}

func TestDCRCredentialStore_NilBackendPanics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "dcr: NewStorageBackedStore: backend must not be nil", func() {
		dcr.NewStorageBackedStore(nil)
	})
}

func TestDCRCredentialStore_KeyFieldsAndCopies(t *testing.T) {
	t.Parallel()

	base := dcr.Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "redirect", ScopesHash: dcrkey.ScopesHash(nil)}
	tests := []struct {
		name   string
		mutate func(*dcr.Key)
	}{
		{"base", func(*dcr.Key) {}},
		{"issuer", func(k *dcr.Key) { k.Issuer = "other" }},
		{"upstream id", func(k *dcr.Key) { k.UpstreamID = "other" }},
		{"redirect uri", func(k *dcr.Key) { k.RedirectURI = "other" }},
		{"scopes hash", func(k *dcr.Key) { k.ScopesHash = dcrkey.ScopesHash([]string{"openid"}) }},
		{"rotation key", func(k *dcr.Key) { k.RotationKey = "other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := newMemoryDCRStore(t)
			key := base
			tt.mutate(&key)

			got, ok, err := store.Get(ctx, key)
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Nil(t, got)

			candidate := newTestRegistration("first")
			want := *candidate
			want.RedirectURI = key.RedirectURI
			claimed, err := store.PutIfAbsent(ctx, key, candidate)
			require.NoError(t, err)
			assert.Equal(t, want, *claimed)

			// Mutating the input or any returned value must not reach the store.
			candidate.ClientID, claimed.ClientID = "input-mutated", "output-mutated"
			winner, err := store.PutIfAbsent(ctx, key, newTestRegistration("loser"))
			require.NoError(t, err)
			assert.Equal(t, want, *winner)
			winner.ClientID = "winner-mutated"
			got, ok, err = store.Get(ctx, key)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, want, *got)

			// A different key is a different row.
			if tt.name != "base" {
				_, ok, err = store.Get(ctx, base)
				require.NoError(t, err)
				assert.False(t, ok, "mutating %s must address a distinct row", tt.name)
			}
		})
	}

	t.Run("nil candidate", func(t *testing.T) {
		t.Parallel()
		_, err := newMemoryDCRStore(t).PutIfAbsent(context.Background(), base, nil)
		require.ErrorContains(t, err, "must not be nil")
	})
}

func TestDCRCredentialStore_Replacement(t *testing.T) {
	t.Parallel()

	base := dcr.Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "redirect", ScopesHash: dcrkey.ScopesHash(nil)}
	tests := []struct{ name, stored, want string }{
		{"matching", "stale", "fresh"},
		{"changed", "newer", "newer"},
		{"missing", "", "fresh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := newMemoryDCRStore(t)
			if tt.stored != "" {
				_, err := store.PutIfAbsent(ctx, base, newTestRegistration(tt.stored))
				require.NoError(t, err)
			}
			replacer, ok := store.(dcr.Replacer)
			require.True(t, ok)

			got, err := replacer.ReplaceIfUnchanged(ctx, base, newTestRegistration("stale"), newTestRegistration("fresh"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.ClientID)
			got.ClientID = "mutated"
			read, ok, err := store.Get(ctx, base)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.want, read.ClientID)

			_, err = replacer.ReplaceIfUnchanged(ctx, base, nil, newTestRegistration("fresh"))
			require.Error(t, err)
			_, err = replacer.ReplaceIfUnchanged(ctx, base, newTestRegistration("stale"), nil)
			require.Error(t, err)
		})
	}
}

func TestDCRCredentialStore_SameKeyHasSingleWinner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newMemoryDCRStore(t)
	key := dcr.Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "redirect", ScopesHash: dcrkey.ScopesHash(nil)}
	const contenders = 32

	results := make([]string, contenders)
	errs := make([]error, contenders)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := store.PutIfAbsent(ctx, key, newTestRegistration(fmt.Sprint("contender-", i)))
			errs[i] = err
			if err == nil {
				results[i] = got.ClientID
			}
		}()
	}
	close(start)
	wg.Wait()

	for i := range contenders {
		require.NoError(t, errs[i])
		assert.Equal(t, results[0], results[i], "every contender must receive the same authoritative winner")
	}
	stored, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, results[0], stored.ClientID)
}

func TestDCRCredentialStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	store := newMemoryDCRStore(t)
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				issuer := "shared"
				if i%2 != 0 {
					issuer = fmt.Sprint(worker)
				}
				key := dcr.Key{Issuer: issuer, UpstreamID: "upstream", RedirectURI: "redirect", ScopesHash: fmt.Sprint(i)}
				_, err := store.PutIfAbsent(context.Background(), key, newTestRegistration(fmt.Sprint(worker)))
				if err != nil {
					errs <- err
					return
				}
				_, _, err = store.Get(context.Background(), key)
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for adapter operations")
	}
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
