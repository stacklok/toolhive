// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package consentpolicy

import (
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestIsBackChannelOnlyClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		client fosite.Client
		want   bool
	}{
		{
			name: "interactive authorization client",
			client: &fosite.DefaultClient{
				GrantTypes:    fosite.Arguments{oauthproto.GrantTypeAuthorizationCode},
				ResponseTypes: fosite.Arguments{oauthproto.ResponseTypeCode},
			},
		},
		{
			name: "no response types",
			client: noResponseClient{DefaultClient: &fosite.DefaultClient{
				GrantTypes: fosite.Arguments{oauthproto.GrantTypeAuthorizationCode},
			}},
			want: true,
		},
		{
			name: "token exchange only",
			client: &fosite.DefaultClient{
				GrantTypes: fosite.Arguments{oauthproto.GrantTypeTokenExchange},
			},
			want: true,
		},
		{
			name: "explicit back-channel marker despite interactive metadata",
			client: &markedInteractiveClient{DefaultClient: &fosite.DefaultClient{
				GrantTypes:    fosite.Arguments{oauthproto.GrantTypeAuthorizationCode},
				ResponseTypes: fosite.Arguments{oauthproto.ResponseTypeCode},
			}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, IsBackChannelOnlyClient(tt.client))
		})
	}
}

func TestIsEligibleClient(t *testing.T) {
	t.Parallel()

	validClient := func() *fosite.DefaultClient {
		return &fosite.DefaultClient{
			ID:            "client",
			Secret:        []byte("secret"),
			GrantTypes:    fosite.Arguments{oauthproto.GrantTypeAuthorizationCode},
			ResponseTypes: fosite.Arguments{oauthproto.ResponseTypeCode},
			RedirectURIs:  []string{"https://client.example/callback"},
		}
	}
	tests := []struct {
		name   string
		client fosite.Client
		want   bool
	}{
		{name: "eligible confidential authorization client", client: validClient(), want: true},
		{name: "nil client"},
		{name: "public client", client: func() *fosite.DefaultClient { c := validClient(); c.Public = true; return c }()},
		{name: "missing authorization-code grant", client: func() *fosite.DefaultClient {
			c := validClient()
			c.GrantTypes = fosite.Arguments{"refresh_token"}
			return c
		}()},
		{name: "missing code response type", client: func() *fosite.DefaultClient {
			c := validClient()
			c.ResponseTypes = fosite.Arguments{"token"}
			return c
		}()},
		{name: "missing redirect URI", client: func() *fosite.DefaultClient { c := validClient(); c.RedirectURIs = nil; return c }()},
		{name: "marked back-channel client with otherwise valid metadata", client: &markedInteractiveClient{DefaultClient: validClient()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, IsEligibleClient(tt.client))
		})
	}
}

type noResponseClient struct {
	*fosite.DefaultClient
}

func (noResponseClient) GetResponseTypes() fosite.Arguments { return nil }

type markedInteractiveClient struct {
	*fosite.DefaultClient
	registration.BackChannelOnlyMarker
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		clientIDs         []string
		authorizationBase string
		issuer            string
		upstreamCount     int
		wantErr           string
	}{
		{name: "empty list preserves existing behavior"},
		{name: "opaque ID allowed", clientIDs: []string{"opaque-client"}, issuer: "https://auth.example.com", upstreamCount: 1},
		{name: "regular ID allowed", clientIDs: []string{"desktop-client"}, issuer: "https://auth.example.com", upstreamCount: 1},
		{name: "empty ID rejected", clientIDs: []string{""}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "empty ID"},
		{name: "duplicate ID rejected", clientIDs: []string{"client", "client"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "duplicate ID"},
		{name: "absolute HTTP URL rejected", clientIDs: []string{"http://client.example.com"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "absolute CIMD URL rejected", clientIDs: []string{"https://client.example.com/metadata.json"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "opaque URI rejected as URL-shaped", clientIDs: []string{"custom:opaque-client"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "network path with authority rejected", clientIDs: []string{"//client.example.com/id"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "leading whitespace URL rejected", clientIDs: []string{" https://x"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "malformed URL rejected", clientIDs: []string{"%zz"}, issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "URL-shaped"},
		{name: "requires upstream", clientIDs: []string{"client"}, issuer: "https://auth.example.com", wantErr: "at least one configured upstream"},
		{name: "requires HTTPS issuer", clientIDs: []string{"client"}, issuer: "http://auth.example.com", upstreamCount: 1, wantErr: "HTTPS"},
		{name: "insecure HTTP issuer still rejected", clientIDs: []string{"client"}, issuer: "http://auth.example.com", upstreamCount: 1, wantErr: "HTTPS"},
		{name: "override is effective base", clientIDs: []string{"client"}, authorizationBase: "http://login.example.com", issuer: "https://auth.example.com", upstreamCount: 1, wantErr: "HTTPS"},
		{name: "HTTPS override permits non-HTTPS issuer", clientIDs: []string{"client"}, authorizationBase: "https://login.example.com", issuer: "http://auth.example.com", upstreamCount: 1},
		{name: "missing HTTPS authority rejected", clientIDs: []string{"client"}, issuer: "https:opaque", upstreamCount: 1, wantErr: "HTTPS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tt.clientIDs, tt.authorizationBase, tt.issuer, tt.upstreamCount)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
