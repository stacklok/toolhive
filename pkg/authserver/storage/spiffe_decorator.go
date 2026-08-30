// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

// SPIFFEStorageDecorator resolves immutable configured SPIFFE clients before
// the dynamic DCR/CIMD storage backend. Static clients exist only in this
// overlay; they are never persisted or eligible for registration.
type SPIFFEStorageDecorator struct {
	Storage
	clients map[string]fosite.Client
}

// NewSPIFFEStorageDecorator overlays static SPIFFE clients over base. It checks
// the durable backend for client-ID collisions before creating the overlay.
func NewSPIFFEStorageDecorator(ctx context.Context, base Storage, clients map[string]fosite.Client) (Storage, error) {
	if base == nil {
		return nil, fmt.Errorf("storage is required")
	}
	if len(clients) == 0 {
		return base, nil
	}
	privateClients := maps.Clone(clients)
	for clientID, client := range privateClients {
		if client == nil {
			return nil, fmt.Errorf("static SPIFFE client %q is required", clientID)
		}
		if client.GetID() != clientID {
			return nil, fmt.Errorf("static SPIFFE client map key %q does not match client ID %q", clientID, client.GetID())
		}
	}
	if err := preflightDurableCollisions(ctx, base, privateClients); err != nil {
		return nil, err
	}
	return &SPIFFEStorageDecorator{Storage: base, clients: privateClients}, nil
}

// PreflightSPIFFEStaticClientCollisions checks configured static client IDs
// against durable storage without creating a persistent reservation.
func PreflightSPIFFEStaticClientCollisions(ctx context.Context, base Storage, clients map[string]fosite.Client) error {
	if base == nil {
		return fmt.Errorf("storage is required")
	}
	return preflightDurableCollisions(ctx, base, clients)
}

func preflightDurableCollisions(ctx context.Context, base Storage, clients map[string]fosite.Client) error {
	for clientID := range clients {
		if client, err := Unwrap(base).GetClient(ctx, clientID); err == nil {
			kind := "operator-declared"
			if registration.DCRIssued(client) {
				kind = "DCR-issued"
			}
			return fmt.Errorf(
				"%w: static SPIFFE client ID %q collides with an existing %s client; "+
					"remove the stale registration from durable storage before configuring this association",
				ErrAlreadyExists, clientID, kind,
			)
		} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, fosite.ErrNotFound) {
			return fmt.Errorf("check durable client collision for SPIFFE client ID %q: %w", clientID, err)
		}
	}
	return nil
}

// GetClient returns a configured SPIFFE client before consulting the dynamic backend.
func (d *SPIFFEStorageDecorator) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	if client, ok := d.clients[id]; ok {
		return client, nil
	}
	return d.Storage.GetClient(ctx, id)
}

// RegisterClient rejects reserved static SPIFFE IDs and delegates other registrations.
func (d *SPIFFEStorageDecorator) RegisterClient(ctx context.Context, client fosite.Client) error {
	if client == nil {
		return fmt.Errorf("register client: client is required")
	}
	if _, reserved := d.clients[client.GetID()]; reserved {
		return fmt.Errorf("%w: client ID %q is reserved for a static SPIFFE client", ErrAlreadyExists, client.GetID())
	}
	return d.Storage.RegisterClient(ctx, client)
}

// Unwrap returns the dynamic DCR/CIMD storage backend.
func (d *SPIFFEStorageDecorator) Unwrap() Storage { return d.Storage }
