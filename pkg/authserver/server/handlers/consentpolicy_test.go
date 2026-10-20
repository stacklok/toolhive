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
	issuer string,
	upstreamCount int,
	options ...Option,
) (*Handler, error) {
	t.Helper()

	stor := mocks.NewMockStorage(gomock.NewController(t))
	config := &server.AuthorizationServerConfig{
		Config: &fosite.Config{AccessTokenIssuer: issuer},
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

	t.Run("empty list preserves token-only HTTP behavior", func(t *testing.T) {
		t.Parallel()
		handler, err := newConsentPolicyTestHandler(t, "http://auth.example.com", 0)
		require.NoError(t, err)
		require.NotNil(t, handler)
	})
	t.Run("option reaches shared validation", func(t *testing.T) {
		t.Parallel()
		handler, err := newConsentPolicyTestHandler(
			t, "https://auth.example.com", 1,
			WithConsentExemptClientIDs([]string{"https://client.example.com"}),
		)
		require.ErrorContains(t, err, "URL-shaped")
		require.Nil(t, handler)
	})
}

func TestWithConsentExemptClientIDsCopiesInputAtCreation(t *testing.T) {
	t.Parallel()

	clientIDs := []string{"first-client"}
	option := WithConsentExemptClientIDs(clientIDs)
	clientIDs[0] = "mutated-client"

	handler, err := newConsentPolicyTestHandler(
		t, "https://auth.example.com", 1, option)
	require.NoError(t, err)
	require.Contains(t, handler.consentExemptClientIDSet, "first-client")
	require.NotContains(t, handler.consentExemptClientIDSet, "mutated-client")
}

func TestWithConsentExemptClientIDsRepeatedOptionsReplace(t *testing.T) {
	t.Parallel()

	handler, err := newConsentPolicyTestHandler(
		t,
		"https://auth.example.com",
		1,
		WithConsentExemptClientIDs([]string{"first-client"}),
		WithConsentExemptClientIDs([]string{"second-client"}),
	)
	require.NoError(t, err)
	require.NotContains(t, handler.consentExemptClientIDSet, "first-client")
	require.Contains(t, handler.consentExemptClientIDSet, "second-client")
}
