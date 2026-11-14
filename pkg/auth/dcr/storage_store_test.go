// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/oauthproto/dcrkey"
	"github.com/stacklok/toolhive/pkg/oauthproto/dcrstore"
	"github.com/stacklok/toolhive/pkg/oauthproto/dcrstore/mocks"
	"github.com/stacklok/toolhive/pkg/storageerr"
)

func TestResolutionCredentialsRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Round(time.Second)
	key := Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "canonical", ScopesHash: dcrkey.ScopesHash([]string{"openid", "profile"}), RotationKey: "2026-10"}
	original := &Resolution{
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
		{name: "missing read claims", readErr: fmt.Errorf("missing: %w", storageerr.ErrNotFound)},
		{name: "missing read claim fails", readErr: storageerr.ErrNotFound, claimErr: failure},
		{name: "CAS conflict claims winner", updateErr: dcrstore.ErrDCRCredentialsChanged},
		{name: "CAS disappearance claims winner", updateErr: fmt.Errorf("missing: %w", storageerr.ErrNotFound)},
		{name: "CAS failure", updateErr: failure},
		{name: "CAS conflict claim failure", updateErr: dcrstore.ErrDCRCredentialsChanged, claimErr: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			backend := mocks.NewMockStore(gomock.NewController(t))
			key := Key{Issuer: "issuer"}
			physical := &dcrstore.Credentials{Key: key, ClientID: "old", ProviderName: "physical-only", CreatedAt: time.Now()}
			if tt.changed {
				physical.ClientID = "winner"
			}
			backend.EXPECT().GetDCRCredentials(ctx, key).Return(physical, tt.readErr)
			fallback := errors.Is(tt.readErr, storageerr.ErrNotFound) || errors.Is(tt.updateErr, storageerr.ErrNotFound) || errors.Is(tt.updateErr, dcrstore.ErrDCRCredentialsChanged)
			if tt.readErr == nil && !tt.changed {
				backend.EXPECT().UpdateDCRCredentialsIfUnchanged(ctx, gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, candidate, witness *dcrstore.Credentials) (*dcrstore.Credentials, error) {
						require.Same(t, physical, witness, "CAS must use the freshly re-read physical row, not reconstructed expected")
						assert.Equal(t, "fresh", candidate.ClientID)
						assert.Equal(t, key, candidate.Key)
						return candidate, tt.updateErr
					})
			}
			authoritative := &dcrstore.Credentials{Key: key, ClientID: "claim-winner"}
			if fallback {
				backend.EXPECT().StoreDCRCredentialsIfAbsent(ctx, gomock.Any()).Return(authoritative, tt.claimErr)
			}
			expected := &Resolution{ClientID: "old"}
			replacement := &Resolution{ClientID: "fresh"}
			got, err := NewStorageBackedStore(backend).(Replacer).ReplaceIfUnchanged(ctx, key, expected, replacement)
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
		{"failure", failure}, {"wrapped miss", fmt.Errorf("missing: %w", storageerr.ErrNotFound)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend := mocks.NewMockStore(gomock.NewController(t))
			ctx := context.Background()
			key := Key{Issuer: "issuer"}
			store := NewStorageBackedStore(backend)
			backend.EXPECT().GetDCRCredentials(ctx, key).Return(nil, tt.err)
			got, ok, err := store.Get(ctx, key)
			assert.Nil(t, got)
			assert.False(t, ok)
			if errors.Is(tt.err, storageerr.ErrNotFound) {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, failure)
			}
			backend.EXPECT().StoreDCRCredentialsIfAbsent(ctx, gomock.Any()).Return(nil, tt.err)
			got, err = store.PutIfAbsent(ctx, key, &Resolution{ClientID: "client"})
			require.ErrorIs(t, err, tt.err)
			assert.Nil(t, got)
		})
	}
}
