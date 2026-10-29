// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestExchangeRefreshTokenCaps(t *testing.T) {
	t.Parallel()

	t.Run("nil block gives nil policy caps", func(t *testing.T) {
		t.Parallel()
		caps, err := exchangeRefreshTokenCaps(nil)
		require.NoError(t, err)
		assert.Nil(t, caps)
	})

	t.Run("both caps keyed by token-type URN", func(t *testing.T) {
		t.Parallel()
		caps, err := exchangeRefreshTokenCaps(&DelegateClientExchangeRefreshTokens{
			IDJAGMaxLifetime: "12h", RefreshTokenMaxLifetime: "720h",
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]time.Duration{
			oauthproto.TokenTypeIDJAG:        12 * time.Hour,
			oauthproto.TokenTypeRefreshToken: 720 * time.Hour,
		}, caps)
	})

	t.Run("empty cap is left out", func(t *testing.T) {
		t.Parallel()
		caps, err := exchangeRefreshTokenCaps(&DelegateClientExchangeRefreshTokens{RefreshTokenMaxLifetime: "1h"})
		require.NoError(t, err)
		assert.Equal(t, map[string]time.Duration{oauthproto.TokenTypeRefreshToken: time.Hour}, caps)
	})

	t.Run("unparsable cap is an error", func(t *testing.T) {
		t.Parallel()
		_, err := exchangeRefreshTokenCaps(&DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: "soon"})
		require.ErrorContains(t, err, "id_jag_max_lifetime")
	})
}

func TestConfiguredClientPolicies(t *testing.T) {
	t.Parallel()

	clients := []DelegateClient{
		{ClientID: "no-exchange"},
		{
			ClientID:      "any-mode",
			TokenExchange: &DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensAny},
		},
		{
			ClientID:      "with-caps",
			TokenExchange: &DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted, APIAudience: "https://api.example.com", AllowedSourceClients: []string{"tui"}, ExchangeRefreshTokens: &DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: "12h", RefreshTokenMaxLifetime: "720h"}},
		},
		{
			ClientID:      "token-exchange-only",
			TokenExchange: &DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted, APIAudience: "https://api.example.com"},
		},
		{
			ClientID:      "id-jag-only",
			TokenExchange: &DelegateClientTokenExchange{SelfIssuedSubjectTokens: SelfIssuedSubjectTokensRestricted, ExchangeRefreshTokens: &DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: "1h"}},
		},
	}

	policies, err := configuredClientPolicies(clients)
	require.NoError(t, err)

	// Only restricted clients get an entry, keyed by their own ID.
	assert.NotContains(t, policies, "no-exchange")
	assert.NotContains(t, policies, "any-mode")
	require.Contains(t, policies, "with-caps")
	require.Contains(t, policies, "token-exchange-only")
	require.Contains(t, policies, "id-jag-only")

	assert.Equal(t, []string{"tui"}, policies["with-caps"].AllowedSourceClients)
	assert.Equal(t, "https://api.example.com", policies["with-caps"].APIAudience)
	assert.Equal(t, map[string]time.Duration{
		oauthproto.TokenTypeIDJAG:        12 * time.Hour,
		oauthproto.TokenTypeRefreshToken: 720 * time.Hour,
	}, policies["with-caps"].ExchangeRefreshTokens)

	// Absent block: the policy field stays nil.
	assert.Nil(t, policies["token-exchange-only"].ExchangeRefreshTokens)

	// A single cap is keyed only by its own grant kind.
	assert.Equal(t, map[string]time.Duration{oauthproto.TokenTypeIDJAG: time.Hour},
		policies["id-jag-only"].ExchangeRefreshTokens)
	assert.NotContains(t, policies["id-jag-only"].ExchangeRefreshTokens, oauthproto.TokenTypeRefreshToken)
}

func TestConfiguredClientPoliciesNoTokenExchange(t *testing.T) {
	t.Parallel()

	policies, err := configuredClientPolicies([]DelegateClient{{ClientID: "plain"}})
	require.NoError(t, err)
	assert.Nil(t, policies)
}
