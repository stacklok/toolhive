// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

func TestNewSPIFFEClientResolver(t *testing.T) {
	t.Parallel()

	assert.Nil(t, newSPIFFEClientResolver(nil), "no SPIFFE trust must yield a nil resolver")

	jwt := testSPIFFEAssociation("jwt-client", "openid")
	jwt.PrincipalPattern = "spiffe://example.org/ns/default/jwt/*"
	jwt.Methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}
	x509 := testSPIFFEAssociation("x509-client", "openid")
	x509.PrincipalPattern = "spiffe://example.org/ns/default/x509/*"
	resolver := newSPIFFEClientResolver(newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{jwt, x509}))
	require.NotNil(t, resolver)

	tests := []struct {
		name     string
		spiffeID string
		clientID string
		method   SPIFFEAuthenticationMethod
		wantErr  bool
	}{
		{
			name: "JWT", method: SPIFFEAuthenticationMethodJWT,
			spiffeID: "spiffe://example.org/ns/default/jwt/one", clientID: "jwt-client",
		},
		{
			name: "X.509", method: SPIFFEAuthenticationMethodX509,
			spiffeID: "spiffe://example.org/ns/default/x509/one", clientID: "x509-client",
		},
		{
			name: "registry rejection", method: SPIFFEAuthenticationMethodX509,
			spiffeID: "spiffe://example.org/ns/default/jwt/one", clientID: "jwt-client", wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolver(context.Background(), tt.spiffeID, tt.clientID, tt.method)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, got, "an error must return a nil interface, not a typed nil")
				return
			}
			require.NoError(t, err)
			authenticated, ok := got.(*registration.AuthenticatedSPIFFEClient)
			require.True(t, ok)
			assert.Equal(t, tt.clientID, authenticated.GetID())
			assert.Equal(t, tt.spiffeID, authenticated.SPIFFEID().String())
			assert.Equal(t, tt.method, authenticated.AuthenticationMethod())
		})
	}
}
