// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// SPIFFEStorageDecorator resolves immutable configured SPIFFE clients before
// the dynamic DCR/CIMD storage backend. Static clients exist only in this
// overlay; they are never persisted or eligible for registration.
type SPIFFEStorageDecorator struct {
	storage.Storage
	clients map[string]*registration.SPIFFEClient
}

// NewSPIFFEStorageDecorator overlays clients from an immutable SPIFFE
// association registry over base. It checks the durable backend for client-ID
// collisions before creating the overlay, but it does not load bundles,
// authenticate SPIFFE credentials, or resolve SPIFFE IDs.
//
// A nil registry leaves base unchanged. The caller must install this
// decorator before accepting traffic and route all later registrations through
// the returned overlay. Under that startup invariant, the collision check and
// reserved-ID rejection prevent a dynamic client from claiming a static ID.
// This constructor cannot protect a base storage instance exposed concurrently;
// it deliberately creates no durable reservation because static clients must
// remain config-only and disappear when configuration is removed.
func NewSPIFFEStorageDecorator(
	ctx context.Context,
	base storage.Storage,
	registry *SPIFFEAssociationRegistry,
) (storage.Storage, error) {
	if base == nil {
		return nil, fmt.Errorf("storage is required")
	}
	if registry == nil {
		return base, nil
	}

	clientIDs := registry.clientIDs()
	if len(clientIDs) == 0 {
		return base, nil
	}

	if err := registry.preflightDurableCollisions(ctx, base); err != nil {
		return nil, err
	}

	clients := make(map[string]*registration.SPIFFEClient, len(clientIDs))
	for _, clientID := range clientIDs {
		client, found, err := registry.staticClient(clientID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("SPIFFE association registry lost client ID %q", clientID)
		}
		if _, exists := clients[client.GetID()]; exists {
			return nil, fmt.Errorf("%w: duplicate static SPIFFE client ID %q", storage.ErrAlreadyExists, client.GetID())
		}

		clients[client.GetID()] = client
	}

	return &SPIFFEStorageDecorator{Storage: base, clients: clients}, nil
}

// PreflightSPIFFEStaticClientCollisions rejects static client IDs that already
// exist in durable storage. It performs no network I/O and creates no durable
// reservation; configured SPIFFE clients remain static, config-only authority.
func PreflightSPIFFEStaticClientCollisions(
	ctx context.Context, base storage.Storage, trust *SPIFFETrustConfig,
) error {
	registry, err := NewSPIFFEAssociationRegistry(trust)
	if err != nil {
		return fmt.Errorf("create SPIFFE association registry: %w", err)
	}
	if registry == nil {
		return nil
	}
	return registry.preflightDurableCollisions(ctx, base)
}

func (r *SPIFFEAssociationRegistry) preflightDurableCollisions(ctx context.Context, base storage.Storage) error {
	if base == nil {
		return fmt.Errorf("storage is required")
	}
	for _, clientID := range r.clientIDs() {
		if _, err := storage.Unwrap(base).GetClient(ctx, clientID); err == nil {
			return fmt.Errorf("%w: static SPIFFE client ID %q collides with durable client", storage.ErrAlreadyExists, clientID)
		} else if !errors.Is(err, storage.ErrNotFound) && !errors.Is(err, fosite.ErrNotFound) {
			return fmt.Errorf("check durable client collision for SPIFFE client ID %q: %w", clientID, err)
		}
	}
	return nil
}

// GetClient returns a configured SPIFFE client before consulting the dynamic
// DCR/CIMD backend. Unknown spiffe:// IDs delegate normally and therefore
// cannot trigger CIMD, which accepts HTTPS client IDs only.
func (d *SPIFFEStorageDecorator) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	if client, ok := d.clients[id]; ok {
		return client, nil
	}
	return d.Storage.GetClient(ctx, id)
}

// RegisterClient rejects reserved static SPIFFE IDs and delegates ordinary DCR
// and CIMD client registration to the underlying storage.
func (d *SPIFFEStorageDecorator) RegisterClient(ctx context.Context, client fosite.Client) error {
	if client == nil {
		return fmt.Errorf("register client: client is required")
	}
	if _, reserved := d.clients[client.GetID()]; reserved {
		return fmt.Errorf("%w: client ID %q is reserved for a static SPIFFE client", storage.ErrAlreadyExists, client.GetID())
	}
	return d.Storage.RegisterClient(ctx, client)
}

// Unwrap returns the dynamic DCR/CIMD storage backend.
func (d *SPIFFEStorageDecorator) Unwrap() storage.Storage {
	return d.Storage
}
