// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otherClient embeds *fosite.DefaultClient but is not one of the concrete
// types MarkConfigured accepts.
type otherClient struct {
	*fosite.DefaultClient
}

func TestMarkConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		client fosite.Client
		wantOK bool
	}{
		{name: "default client", client: &fosite.DefaultClient{ID: "c"}, wantOK: true},
		{
			name: "oidc client",
			client: &fosite.DefaultOpenIDConnectClient{
				DefaultClient:           &fosite.DefaultClient{ID: "c"},
				TokenEndpointAuthMethod: "client_secret_basic",
			},
			wantOK: true,
		},
		{name: "unknown concrete type", client: otherClient{DefaultClient: &fosite.DefaultClient{ID: "c"}}},
		{name: "nil", client: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			marked, ok := MarkConfigured(tt.client)
			require.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				assert.Nil(t, marked)
				return
			}
			assert.True(t, IsConfigured(marked))
			assert.False(t, DCRIssued(marked), "a configured client must not satisfy DCRIssued")
			assert.Equal(t, tt.client.GetID(), marked.GetID())
			assert.False(t, IsConfigured(tt.client), "the input must not be mutated")
		})
	}
}
