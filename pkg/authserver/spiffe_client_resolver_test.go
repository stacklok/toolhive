// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestSPIFFEClientResolver(t *testing.T) {
	t.Parallel()

	associations := []SPIFFEClientAuthRunConfig{
		{
			TrustDomainRef:   "production",
			PrincipalPattern: "spiffe://example.org/ns/default/exact",
			ClientID:         "exact-client",
			Methods:          []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT},
			GrantTypes:       []string{SPIFFEGrantTypeClientCredentials},
			Scopes:           []string{"openid"},
			Resources:        []string{"https://resource.example.com"},
			Audiences:        []string{"https://audience.example.com"},
		},
		{
			TrustDomainRef:   "production",
			PrincipalPattern: "spiffe://example.org/ns/default/workloads/*",
			ClientID:         "wildcard-client",
			Methods:          []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			GrantTypes:       []string{SPIFFEGrantTypeClientCredentials},
			Scopes:           []string{"openid"},
			Resources:        []string{"https://resource.example.com"},
			Audiences:        []string{"https://audience.example.com"},
		},
	}

	tests := []struct {
		name       string
		spiffeID   string
		clientID   string
		selector   *string
		method     SPIFFEAuthenticationMethod
		client     fosite.Client
		wantErr    string
		wantMethod SPIFFEAuthenticationMethod
	}{
		{
			name:       "exact association preserves concrete SPIFFE principal",
			spiffeID:   "spiffe://example.org/ns/default/exact",
			clientID:   "exact-client",
			method:     SPIFFEAuthenticationMethodJWT,
			wantMethod: SPIFFEAuthenticationMethodJWT,
		},
		{
			name:       "wildcard association preserves concrete descendant",
			spiffeID:   "spiffe://example.org/ns/default/workloads/one",
			clientID:   "wildcard-client",
			method:     SPIFFEAuthenticationMethodX509,
			wantMethod: SPIFFEAuthenticationMethodX509,
		},
		{
			name:       "empty client ID selector derives client from identity",
			spiffeID:   "spiffe://example.org/ns/default/exact",
			clientID:   "exact-client",
			selector:   func() *string { s := ""; return &s }(),
			method:     SPIFFEAuthenticationMethodJWT,
			wantMethod: SPIFFEAuthenticationMethodJWT,
		},
		{
			name:     "regular storage client is rejected",
			spiffeID: "spiffe://example.org/ns/default/exact",
			clientID: "exact-client",
			method:   SPIFFEAuthenticationMethodJWT,
			client:   &fosite.DefaultClient{ID: "exact-client"},
			wantErr:  "not a static SPIFFE client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := newTestSPIFFEAssociationRegistry(t, associations)
			store := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = store.Close() })
			client := tt.client
			if client == nil {
				staticClients, err := registry.staticClients()
				require.NoError(t, err)
				var found bool
				client, found = staticClients[tt.clientID]
				require.True(t, found)
			}
			require.NoError(t, store.RegisterClient(context.Background(), client))

			selector := tt.clientID
			if tt.selector != nil {
				selector = *tt.selector
			}
			resolved, err := newSPIFFEClientResolver(registry, store)(
				context.Background(), tt.spiffeID, selector, spiffeauth.SPIFFEAuthenticationMethod(tt.method),
			)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, resolved)
				return
			}
			require.NoError(t, err)
			authenticated, ok := resolved.(*registration.AuthenticatedSPIFFEClient)
			require.True(t, ok)
			assert.Equal(t, tt.clientID, authenticated.GetID())
			assert.Equal(t, tt.spiffeID, authenticated.Principal().SPIFFEID())
			assert.Equal(t, spiffeauth.SPIFFEAuthenticationMethod(tt.wantMethod), authenticated.Principal().AuthenticationMethod())
		})
	}
}

func TestSPIFFEClientResolverAbsentWithoutRegistry(t *testing.T) {
	t.Parallel()

	store := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = store.Close() })
	assert.Nil(t, newSPIFFEClientResolver(nil, store))
}
