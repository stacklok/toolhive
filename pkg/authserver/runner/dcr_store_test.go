// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto/dcrkey"
)

func TestResolutionCredentialsRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Round(time.Second)
	key := dcr.Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "canonical", ScopesHash: dcrkey.ScopesHash([]string{"openid", "profile"}), RotationKey: "2026-10"}
	original := &dcr.Resolution{
		ClientID: "client", ClientSecret: "secret",
		AuthorizationEndpoint: "https://idp.example/authorize", TokenEndpoint: "https://idp.example/token",
		RegistrationAccessToken: "registration-token", RegistrationClientURI: "https://idp.example/register/client",
		TokenEndpointAuthMethod: "client_secret_basic", RedirectURI: "noncanonical",
		ClientIDIssuedAt: now, ClientSecretExpiresAt: now.Add(30 * 24 * time.Hour), CreatedAt: now,
	}
	creds := resolutionToCredentials(key, original)
	require.NotNil(t, creds)
	assert.Equal(t, key, creds.Key)
	assert.Empty(t, creds.ProviderName, "audit-only field has no Resolution counterpart")
	want := *original
	want.RedirectURI, want.ClientIDIssuedAt = key.RedirectURI, time.Time{}
	got := credentialsToResolution(creds)
	require.NotNil(t, got)
	assert.Equal(t, want, *got, "all persisted fields must round-trip")
	creds.ClientID = "mutated"
	assert.Equal(t, "client", original.ClientID)
	assert.Equal(t, "client", got.ClientID)
	got.ClientSecret = "mutated"
	assert.Equal(t, "secret", creds.ClientSecret)
	assert.Nil(t, resolutionToCredentials(key, nil))
	assert.Nil(t, credentialsToResolution(nil))
}

func TestDCRCredentialStore_Contract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := dcr.Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "redirect", ScopesHash: dcrkey.ScopesHash(nil)}
	registration := func(id string) *dcr.Resolution {
		return &dcr.Resolution{ClientID: id, ClientSecret: id + "-secret", AuthorizationEndpoint: "auth", TokenEndpoint: "token", CreatedAt: time.Now()}
	}
	t.Run("nil backend panics", func(t *testing.T) {
		t.Parallel()
		require.Panics(t, func() { NewDCRCredentialStore(nil) })
	})
	t.Run("claims copies and all five key fields", func(t *testing.T) {
		t.Parallel()
		store := newMemoryDCRStore(t)
		got, ok, err := store.Get(ctx, base)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Nil(t, got)
		keys := []dcr.Key{base, base, base, base, base, base}
		keys[1].Issuer = "other"
		keys[2].UpstreamID = "other"
		keys[3].RedirectURI = "other"
		keys[4].ScopesHash = dcrkey.ScopesHash([]string{"openid"})
		keys[5].RotationKey = "other"
		for i, key := range keys {
			candidate := registration(fmt.Sprint(i))
			original := *candidate
			claimed, err := store.PutIfAbsent(ctx, key, candidate)
			require.NoError(t, err)
			want := original
			want.RedirectURI = key.RedirectURI
			assert.Equal(t, want, *claimed)
			candidate.ClientID, claimed.ClientID = "input-mutated", "output-mutated"
			winner, err := store.PutIfAbsent(ctx, key, registration("loser"))
			require.NoError(t, err)
			assert.Equal(t, want, *winner)
			winner.ClientID = "winner-mutated"
			got, ok, err := store.Get(ctx, key)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, want, *got)
			got.ClientID = "get-mutated"
			got, _, err = store.Get(ctx, key)
			require.NoError(t, err)
			assert.Equal(t, want, *got)
		}
		_, err = store.PutIfAbsent(ctx, base, nil)
		require.ErrorContains(t, err, "must not be nil")
	})
	for _, tt := range []struct{ name, stored, want string }{
		{"matching", "stale", "fresh"}, {"changed", "newer", "newer"}, {"missing", "", "fresh"},
	} {
		t.Run("replacement/"+tt.name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryDCRStore(t)
			if tt.stored != "" {
				_, err := store.PutIfAbsent(ctx, base, registration(tt.stored))
				require.NoError(t, err)
			}
			replacer, ok := store.(dcr.Replacer)
			require.True(t, ok)
			got, err := replacer.ReplaceIfUnchanged(ctx, base, registration("stale"), registration("fresh"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.ClientID)
			got.ClientID = "mutated"
			read, ok, err := store.Get(ctx, base)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.want, read.ClientID)
			_, err = replacer.ReplaceIfUnchanged(ctx, base, nil, registration("fresh"))
			require.Error(t, err)
			_, err = replacer.ReplaceIfUnchanged(ctx, base, registration("stale"), nil)
			require.Error(t, err)
		})
	}
}

func TestDCRCredentialStore_ReplacementBackendOutcomes(t *testing.T) {
	t.Parallel()
	failure := errors.New("backend failure")
	for _, tt := range []struct {
		name                         string
		readErr, updateErr, claimErr error
		changed                      bool
	}{
		{name: "physical witness"},
		{name: "already replaced", changed: true},
		{name: "read failure", readErr: failure},
		{name: "missing read claims", readErr: fmt.Errorf("missing: %w", storage.ErrNotFound)},
		{name: "missing read claim fails", readErr: storage.ErrNotFound, claimErr: failure},
		{name: "CAS conflict claims winner", updateErr: storage.ErrDCRCredentialsChanged},
		{name: "CAS disappearance claims winner", updateErr: fmt.Errorf("missing: %w", storage.ErrNotFound)},
		{name: "CAS failure", updateErr: failure},
		{name: "CAS conflict claim failure", updateErr: storage.ErrDCRCredentialsChanged, claimErr: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			backend := mocks.NewMockDCRCredentialStore(gomock.NewController(t))
			key := dcr.Key{Issuer: "issuer"}
			physical := &storage.DCRCredentials{Key: key, ClientID: "old", ProviderName: "physical-only", CreatedAt: time.Now()}
			if tt.changed {
				physical.ClientID = "winner"
			}
			backend.EXPECT().GetDCRCredentials(ctx, key).Return(physical, tt.readErr)
			fallback := errors.Is(tt.readErr, storage.ErrNotFound) || errors.Is(tt.updateErr, storage.ErrNotFound) || errors.Is(tt.updateErr, storage.ErrDCRCredentialsChanged)
			if tt.readErr == nil && !tt.changed {
				backend.EXPECT().UpdateDCRCredentialsIfUnchanged(ctx, gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, candidate, witness *storage.DCRCredentials) (*storage.DCRCredentials, error) {
						require.Same(t, physical, witness, "CAS must use the freshly re-read physical row, not reconstructed expected")
						assert.Equal(t, "fresh", candidate.ClientID)
						assert.Equal(t, key, candidate.Key)
						return candidate, tt.updateErr
					})
			}
			authoritative := &storage.DCRCredentials{Key: key, ClientID: "claim-winner"}
			if fallback {
				backend.EXPECT().StoreDCRCredentialsIfAbsent(ctx, gomock.Any()).Return(authoritative, tt.claimErr)
			}
			expected := &dcr.Resolution{ClientID: "old"}
			replacement := &dcr.Resolution{ClientID: "fresh"}
			got, err := NewDCRCredentialStore(backend).(dcr.Replacer).ReplaceIfUnchanged(ctx, key, expected, replacement)
			if errors.Is(tt.readErr, failure) || errors.Is(tt.updateErr, failure) || tt.claimErr != nil {
				require.ErrorIs(t, err, failure)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			wantID := "fresh"
			if tt.changed {
				wantID = "winner"
			}
			if fallback {
				wantID = "claim-winner"
			}
			assert.Equal(t, wantID, got.ClientID)
			got.ClientID = "mutated"
			assert.NotEqual(t, "mutated", physical.ClientID)
			assert.Equal(t, "claim-winner", authoritative.ClientID)
			assert.Equal(t, "old", expected.ClientID)
			assert.Equal(t, "fresh", replacement.ClientID)
		})
	}
}

func TestDCRCredentialStore_GetAndPutErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("backend failure")
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"failure", failure}, {"wrapped miss", fmt.Errorf("missing: %w", storage.ErrNotFound)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend := mocks.NewMockDCRCredentialStore(gomock.NewController(t))
			ctx := context.Background()
			key := dcr.Key{Issuer: "issuer"}
			store := NewDCRCredentialStore(backend)
			backend.EXPECT().GetDCRCredentials(ctx, key).Return(nil, tt.err)
			got, ok, err := store.Get(ctx, key)
			assert.Nil(t, got)
			assert.False(t, ok)
			if errors.Is(tt.err, storage.ErrNotFound) {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, failure)
			}
			backend.EXPECT().StoreDCRCredentialsIfAbsent(ctx, gomock.Any()).Return(nil, tt.err)
			got, err = store.PutIfAbsent(ctx, key, &dcr.Resolution{ClientID: "client"})
			require.ErrorIs(t, err, tt.err)
			assert.Nil(t, got)
		})
	}
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
				_, err := store.PutIfAbsent(context.Background(), key, &dcr.Resolution{ClientID: fmt.Sprint(worker), AuthorizationEndpoint: "auth", TokenEndpoint: "token"})
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
