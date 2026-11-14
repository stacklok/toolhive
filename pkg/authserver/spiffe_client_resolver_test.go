// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestNewSPIFFEClientResolverUsesResolvedClientID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		clientID string
		pattern  string
		spiffeID string
	}{
		{
			name:     "exact association",
			clientID: "exact-client",
			pattern:  "spiffe://example.org/ns/default/agent",
			spiffeID: "spiffe://example.org/ns/default/agent",
		},
		{
			name:     "wildcard association",
			clientID: "wildcard-client",
			pattern:  "spiffe://example.org/ns/default/*",
			spiffeID: "spiffe://example.org/ns/default/agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			association := testSPIFFEAssociation(tt.clientID, "openid")
			association.PrincipalPattern = tt.pattern
			association.Methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}
			registry := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{association})
			clients, err := registry.staticClients()
			require.NoError(t, err)

			base := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = base.Close() })
			stor, err := storage.NewSPIFFEStorageDecorator(context.Background(), base, clients)
			require.NoError(t, err)

			resolver := newSPIFFEClientResolver(registry, stor)
			require.NotNil(t, resolver)
			client, err := resolver(
				context.Background(), tt.spiffeID, "", spiffeauth.SPIFFEAuthenticationMethodJWT,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.clientID, client.GetID())
		})
	}
}
