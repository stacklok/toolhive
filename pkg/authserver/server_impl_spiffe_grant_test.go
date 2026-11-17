// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestIDJAGExplicitGrantClientIDs(t *testing.T) {
	t.Parallel()

	t.Run("includes SPIFFE association clients and explicit delegate clients", func(t *testing.T) {
		t.Parallel()
		trust, err := NewSPIFFETrustConfig([]SPIFFETrustDomainRunConfig{{
			Name:         "production",
			TrustDomain:  "example.org",
			Methods:      []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			BundleSource: validFileBundleSource(),
		}}, &InboundGrantsRunConfig{SPIFFEClientAuth: []SPIFFEClientAuthRunConfig{
			withPrincipal(testSPIFFEAssociation("spiffe-client-a", "openid"), "spiffe://example.org/ns/default/agent-a"),
			withPrincipal(testSPIFFEAssociation("spiffe-client-b", "openid"), "spiffe://example.org/ns/default/agent-b"),
		}}, []string{"openid"}, []string{"https://audience.example.com"})
		require.NoError(t, err)

		cfg := Config{
			DelegateClients: []DelegateClient{
				{ClientID: "delegate-explicit", GrantTypes: []string{oauthproto.GrantTypeTokenExchange}},
				{ClientID: "delegate-implicit"},
			},
			SPIFFETrust: trust,
		}

		ids := idJAGExplicitGrantClientIDs(cfg)
		assert.ElementsMatch(t, []string{"delegate-explicit", "spiffe-client-a", "spiffe-client-b"}, ids)
	})

	t.Run("no SPIFFE trust yields only explicit delegate clients", func(t *testing.T) {
		t.Parallel()
		cfg := Config{
			DelegateClients: []DelegateClient{
				{ClientID: "delegate-explicit", GrantTypes: []string{oauthproto.GrantTypeJWTBearer}},
				{ClientID: "delegate-implicit"},
			},
		}

		assert.Equal(t, []string{"delegate-explicit"}, idJAGExplicitGrantClientIDs(cfg))
	})
}

func withPrincipal(association SPIFFEClientAuthRunConfig, principal string) SPIFFEClientAuthRunConfig {
	association.PrincipalPattern = principal
	return association
}
