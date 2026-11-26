// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizationdetails

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// TestIDJAGIssuanceKey pins the opt-in key to the ID-JAG token-type URN and
// the token-exchange grant type the hooks see to the RFC 8693 one.
func TestIDJAGIssuanceKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, oauthproto.TokenTypeIDJAG, IDJAGIssuance)
	assert.Equal(t, oauthproto.GrantTypeTokenExchange, grantTypeTokenExchange)
}

// TestRegistry_ParseIDJAGIssuance proves a type is accepted on an ID-JAG
// issuance request only when it lists IDJAGIssuance, that listing the
// token-exchange grant type neither implies nor excludes it, and that the
// hooks see the token-exchange grant type.
func TestRegistry_ParseIDJAGIssuance(t *testing.T) {
	t.Parallel()

	keep := func(_ context.Context, req Request) (Result, error) {
		return Result{Entries: req.Entries}, nil
	}
	registry, err := New(map[string]Type{
		"exchange-only": {GrantTypes: []string{grantTypeExchange}, Grant: keep},
		"issuance-only": {GrantTypes: []string{IDJAGIssuance}, Grant: keep},
		"both":          {GrantTypes: []string{grantTypeExchange, IDJAGIssuance}, Grant: keep},
	})
	require.NoError(t, err, "New accepts the opt-in key")

	for _, tc := range []struct {
		name        string
		issuance    error // ParseIDJAGIssuance
		exchange    error // Parse for token exchange
		wantGranted bool
	}{
		{name: "exchange-only", issuance: ErrUnknownType},
		{name: "issuance-only", exchange: ErrUnknownType, wantGranted: true},
		{name: "both", wantGranted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := `[{"type":"` + tc.name + `"}]`

			_, err := registry.Parse(raw, grantTypeExchange)
			if tc.exchange != nil {
				require.ErrorIs(t, err, tc.exchange)
			} else {
				require.NoError(t, err)
			}

			pending, err := registry.ParseIDJAGIssuance(raw)
			if tc.issuance != nil {
				require.ErrorIs(t, err, tc.issuance)
				return
			}
			require.NoError(t, err)
			subset, err := pending.GrantSubset(t.Context(), Request{})
			require.NoError(t, err)
			assert.Equal(t, json.RawMessage(raw), subset.Entries)
		})
	}

}

// TestRegistry_ParseIDJAGIssuanceGrantType proves the hooks of an ID-JAG
// issuance request see the token-exchange grant type, not the opt-in key.
func TestRegistry_ParseIDJAGIssuanceGrantType(t *testing.T) {
	t.Parallel()

	var seen string
	registry, err := New(map[string]Type{"a": {GrantTypes: []string{IDJAGIssuance},
		Grant: func(_ context.Context, req Request) (Result, error) {
			seen = req.GrantType
			return Result{Entries: req.Entries}, nil
		}}})
	require.NoError(t, err)
	pending, err := registry.ParseIDJAGIssuance(`[{"type":"a"}]`)
	require.NoError(t, err)
	_, err = pending.GrantSubset(t.Context(), Request{})
	require.NoError(t, err)
	assert.Equal(t, grantTypeExchange, seen)
}
