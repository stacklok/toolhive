// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive/pkg/oauthproto/dcrstore"
	"github.com/stacklok/toolhive/pkg/storageerr"
)

var (
	_ CredentialStore = (*storageBackedStore)(nil)
	_ Replacer        = (*storageBackedStore)(nil)
)

// NewStorageBackedStore adapts the supplied backend to the resolver's store.
// Shared storage enables cross-replica credential sharing, not distributed
// registration singleflight. The caller owns the backend's lifecycle; decorators
// are preserved, and the adapter never closes or unwraps the backend.
// Panics if backend is nil.
func NewStorageBackedStore(backend dcrstore.Store) CredentialStore {
	if backend == nil {
		panic("dcr: NewStorageBackedStore: backend must not be nil")
	}
	return &storageBackedStore{backend: backend}
}

type storageBackedStore struct{ backend dcrstore.Store }

func (s *storageBackedStore) Get(ctx context.Context, key Key) (*Resolution, bool, error) {
	creds, err := s.backend.GetDCRCredentials(ctx, key)
	if errors.Is(err, storageerr.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return credentialsToResolution(creds), true, nil
}

func (s *storageBackedStore) PutIfAbsent(ctx context.Context, key Key, resolution *Resolution) (*Resolution, error) {
	if resolution == nil {
		return nil, fmt.Errorf("dcr: resolution must not be nil")
	}
	authoritative, err := s.backend.StoreDCRCredentialsIfAbsent(ctx, resolutionToCredentials(key, resolution))
	if err != nil {
		return nil, err
	}
	return credentialsToResolution(authoritative), nil
}

// ReplaceIfUnchanged re-reads the physical row as the CAS witness: Resolution
// does not carry every persisted field. ClientID identifies the registration.
// A changed or missing CAS row falls back to claiming the authoritative value.
func (s *storageBackedStore) ReplaceIfUnchanged(
	ctx context.Context, key Key, expected, replacement *Resolution,
) (*Resolution, error) {
	if expected == nil || replacement == nil {
		return nil, fmt.Errorf("dcr: expected and replacement resolutions must not be nil")
	}
	creds := resolutionToCredentials(key, replacement)
	claim := func() (*Resolution, error) {
		authoritative, err := s.backend.StoreDCRCredentialsIfAbsent(ctx, creds)
		if err != nil {
			return nil, err
		}
		return credentialsToResolution(authoritative), nil
	}
	current, err := s.backend.GetDCRCredentials(ctx, key)
	switch {
	case errors.Is(err, storageerr.ErrNotFound):
		return claim()
	case err != nil:
		return nil, err
	case current.ClientID != expected.ClientID:
		return credentialsToResolution(current), nil
	}
	updated, err := s.backend.UpdateDCRCredentialsIfUnchanged(ctx, creds, current)
	if errors.Is(err, dcrstore.ErrDCRCredentialsChanged) || errors.Is(err, storageerr.ErrNotFound) {
		return claim()
	}
	if err != nil {
		return nil, err
	}
	return credentialsToResolution(updated), nil
}

// resolutionToCredentials drops informational ClientIDIssuedAt and stores
// RedirectURI only in the canonical key. CreatedAt and expiry survive restarts.
func resolutionToCredentials(key Key, res *Resolution) *dcrstore.Credentials {
	if res == nil {
		return nil
	}
	return &dcrstore.Credentials{
		Key:                     key,
		ClientID:                res.ClientID,
		ClientSecret:            res.ClientSecret,
		TokenEndpointAuthMethod: res.TokenEndpointAuthMethod,
		RegistrationAccessToken: res.RegistrationAccessToken,
		RegistrationClientURI:   res.RegistrationClientURI,
		AuthorizationEndpoint:   res.AuthorizationEndpoint,
		TokenEndpoint:           res.TokenEndpoint,
		CreatedAt:               res.CreatedAt,
		ClientSecretExpiresAt:   res.ClientSecretExpiresAt,
	}
}

// credentialsToResolution recovers RedirectURI from the key and leaves the
// unpersisted ClientIDIssuedAt zero. Both converters return independent copies.
func credentialsToResolution(creds *dcrstore.Credentials) *Resolution {
	if creds == nil {
		return nil
	}
	return &Resolution{
		ClientID:                creds.ClientID,
		ClientSecret:            creds.ClientSecret,
		AuthorizationEndpoint:   creds.AuthorizationEndpoint,
		TokenEndpoint:           creds.TokenEndpoint,
		RegistrationAccessToken: creds.RegistrationAccessToken,
		RegistrationClientURI:   creds.RegistrationClientURI,
		TokenEndpointAuthMethod: creds.TokenEndpointAuthMethod,
		RedirectURI:             creds.Key.RedirectURI,
		ClientSecretExpiresAt:   creds.ClientSecretExpiresAt,
		CreatedAt:               creds.CreatedAt,
	}
}
