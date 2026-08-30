// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// PreflightSPIFFEStaticClientCollisions rejects static client IDs that already
// exist in durable storage. It runs before upstream DCR registration so a
// collision cannot leave an orphaned upstream registration.
func PreflightSPIFFEStaticClientCollisions(ctx context.Context, base storage.Storage, trust *SPIFFETrustConfig) error {
	registry, err := NewSPIFFEAssociationRegistry(trust)
	if err != nil {
		return fmt.Errorf("create SPIFFE association registry: %w", err)
	}
	clients, err := registry.staticClients()
	if err != nil {
		return err
	}
	return storage.PreflightSPIFFEStaticClientCollisions(ctx, base, clients)
}
