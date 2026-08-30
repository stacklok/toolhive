// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSPIFFEClient(t *testing.T) {
	t.Parallel()

	scopes := []string{"openid"}
	audiences := []string{"https://api.example.com"}
	client, err := NewSPIFFEClient("spiffe-client", scopes, audiences)
	require.NoError(t, err)

	scopes[0] = "changed"
	audiences[0] = "changed"

	assert.Equal(t, "spiffe-client", client.GetID())
	assert.Nil(t, client.GetHashedSecret())
	assert.Nil(t, client.GetRedirectURIs())
	assert.Nil(t, client.GetResponseTypes())
	assert.False(t, client.IsPublic())
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", client.GetGrantTypes()[0])
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, []string{"https://api.example.com"}, client.Audiences())
	assert.Equal(t, []string{"https://api.example.com"}, []string(client.GetAudience()))

	client.GetScopes()[0] = "mutated"
	client.Audiences()[0] = "mutated"
	client.GetAudience()[0] = "mutated"
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, []string{"https://api.example.com"}, client.Audiences())
	assert.Equal(t, []string{"https://api.example.com"}, []string(client.GetAudience()))

	_, err = NewSPIFFEClient("disabled-exchange", nil, nil)
	require.Error(t, err)
}

func TestNewSPIFFEClient_RequiresID(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEClient("", nil, nil)
	require.Error(t, err)
}

func TestNewSPIFFEClient_RequiresAudiences(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEClient(
		"spiffe-client",
		[]string{"openid"},
		nil,
	)
	require.EqualError(t, err, "SPIFFE client scopes and audiences are required")
}
