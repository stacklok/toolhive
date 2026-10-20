// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

func newConsentPolicyTestHandler(
	t *testing.T,
	issuer, authorizationBase string,
	upstreamCount int,
	options ...Option,
) (*Handler, error) {
	t.Helper()

	stor := mocks.NewMockStorage(gomock.NewController(t))
	config := &server.AuthorizationServerConfig{
		Config: &fosite.Config{AccessTokenIssuer: issuer},
		AuthorizationEndpointBaseURL: authorizationBase,
	}
	provider := fosite.NewOAuth2Provider(stor, config.Config)
	var upstreams []NamedUpstream
	if upstreamCount > 0 {
		upstreams = []NamedUpstream{{Name: "default", Provider: &mockIDPProvider{}}}
	}
	return NewHandler(provider, config, stor, upstreams, options...)
}

func TestNewHandlerConsentExemptClientIDsValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		issuer            string
		authorizationBase string
		upstreamCount     int
		clientIDs         []string
		wantErr           string
	}{
		{
			name:              "empty list preserves token-only HTTP behavior",
			issuer:            "http://auth.example.com",
			clientIDs:         []string{},
		},
		{name: "requires configured upstream", issuer: "https://auth.example.com", clientIDs: []string{"desktop-client"}, wantErr: "at least one configured upstream"},
		{name: "issuer must be HTTPS", issuer: "http://auth.example.com", upstreamCount: 1, clientIDs: []string{"desktop-client"}, wantErr: "HTTPS"},
		{name: "HTTP override must be HTTPS", issuer: "https://auth.example.com", authorizationBase: "http://login.example.com", upstreamCount: 1, clientIDs: []string{"desktop-client"}, wantErr: "HTTPS"},
		{name: "HTTPS override is effective over HTTP issuer", issuer: "http://auth.example.com", authorizationBase: "https://login.example.com", upstreamCount: 1, clientIDs: []string{"desktop-client"}},
		{name: "empty ID rejected", issuer: "https://auth.example.com", upstreamCount: 1, clientIDs: []string{""}, wantErr: "empty ID"},
		{name: "duplicate ID rejected", issuer: "https://auth.example.com", upstreamCount: 1, clientIDs: []string{"client", "client"}, wantErr: "duplicate ID"},
		{name: "URL-shaped ID rejected", issuer: "https://auth.example.com", upstreamCount: 1, clientIDs: []string{"//client.example.com/id"}, wantErr: "URL-shaped"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			options := []Option{WithConsentExemptClientIDs(tt.clientIDs)}
			handler, err := newConsentPolicyTestHandler(t, tt.issuer, tt.authorizationBase, tt.upstreamCount, options...)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, handler)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, handler)
		})
	}
}

func TestWithConsentExemptClientIDsCopiesInputAtCreation(t *testing.T) {
	t.Parallel()

	clientIDs := []string{"first-client"}
	option := WithConsentExemptClientIDs(clientIDs)
	clientIDs[0] = "mutated-client"

	handler, err := newConsentPolicyTestHandler(
		t, "https://auth.example.com", "", 1, option)
	require.NoError(t, err)
	require.Contains(t, handler.consentExemptClientIDSet, "first-client")
	require.NotContains(t, handler.consentExemptClientIDSet, "mutated-client")
}

func TestWithConsentExemptClientIDsRepeatedOptionsReplace(t *testing.T) {
	t.Parallel()

	handler, err := newConsentPolicyTestHandler(
		t,
		"https://auth.example.com",
		"",
		1,
		WithConsentExemptClientIDs([]string{"first-client"}),
		WithConsentExemptClientIDs([]string{"second-client"}),
	)
	require.NoError(t, err)
	require.NotContains(t, handler.consentExemptClientIDSet, "first-client")
	require.Contains(t, handler.consentExemptClientIDSet, "second-client")
}
