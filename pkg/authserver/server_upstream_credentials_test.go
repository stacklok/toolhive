// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

// credentialRecordingProvider is an upstream that can replace its client.
type credentialRecordingProvider struct {
	upstream.OAuth2Provider
	got []upstream.ClientCredentials
}

func (p *credentialRecordingProvider) UpdateClientCredentials(creds upstream.ClientCredentials) error {
	p.got = append(p.got, creds)
	return nil
}

func TestUpdateUpstreamClientCredentials(t *testing.T) {
	t.Parallel()
	newTestServer := func(t *testing.T, names []string, providers ...upstream.OAuth2Provider) Server {
		t.Helper()
		stor := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = stor.Close() })
		upstreams := make([]UpstreamConfig, 0, len(names))
		for _, name := range names {
			upstreams = append(upstreams, UpstreamConfig{
				Name: name, Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig(),
			})
		}
		next := 0
		srv, err := newServer(t.Context(), Config{
			Issuer:           "https://example.com",
			KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
			HMACSecrets:      &servercrypto.HMACSecrets{Current: validHMACSecret()},
			Upstreams:        upstreams,
			AllowedAudiences: []string{"https://mcp.example.com"},
			UpstreamFactory: func(context.Context, *UpstreamConfig) (upstream.OAuth2Provider, error) {
				p := providers[next]
				next++
				return p, nil
			},
		}, stor)
		require.NoError(t, err)
		return srv
	}
	creds := upstream.ClientCredentials{ClientID: "replacement"}

	capable := &credentialRecordingProvider{}
	srv := newTestServer(t, []string{"capable", "static"},
		capable, upstreammocks.NewMockOAuth2Provider(gomock.NewController(t)))
	require.NoError(t, UpdateUpstreamClientCredentials(srv, "capable", creds))
	assert.Equal(t, []upstream.ClientCredentials{creds}, capable.got)
	require.ErrorContains(t, UpdateUpstreamClientCredentials(srv, "static", creds), "cannot replace")
	require.ErrorContains(t, UpdateUpstreamClientCredentials(srv, "missing", creds), "unknown upstream")

	// An empty name addresses the single unnamed upstream.
	unnamed := &credentialRecordingProvider{}
	require.NoError(t, UpdateUpstreamClientCredentials(newTestServer(t, []string{""}, unnamed), "", creds))
	assert.Len(t, unnamed.got, 1)

	// A Server without the capability is refused.
	require.Error(t, UpdateUpstreamClientCredentials(struct{ Server }{srv}, "capable", creds))
}
