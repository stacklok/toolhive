// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toolcalldetails

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	argsHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	toolCall  = `{"type":"` + Type + `","server":"github","tool":"pull_request_read","arguments_sha256":"` + argsHash + `","operation_id":"op-1"}`
	listCall  = `{"type":"` + Type + `","server":"github","method":"tools/list"}`
	otherType = `{"type":"https://example.com/other"}`
)

func newRegistry(t *testing.T) *authorizationdetails.Registry {
	t.Helper()
	types := map[string]authorizationdetails.Type{}
	Register(types)
	registry, err := authorizationdetails.New(types)
	require.NoError(t, err)
	return registry
}

func TestRegister_Validation(t *testing.T) {
	t.Parallel()
	registry := newRegistry(t)

	tests := []struct {
		name   string
		value  string
		reason string
	}{
		{name: "tool call", value: "[" + toolCall + "]"},
		{name: "list call", value: "[" + listCall + "]"},
		{name: "second entry", value: "[" + listCall + "," + listCall + "]", reason: ReasonSingleEntry},
		{
			name:   "unknown field",
			value:  "[" + strings.Replace(listCall, "}", `,"locations":["x"]}`, 1) + "]",
			reason: ReasonMalformedEntry,
		},
		{
			name:   "bad hash",
			value:  "[" + strings.Replace(toolCall, argsHash, strings.ToUpper(argsHash), 1) + "]",
			reason: ReasonInvalidArgs,
		},
		{
			name:   "mixed shapes",
			value:  "[" + strings.Replace(toolCall, "}", `,"method":"tools/list"}`, 1) + "]",
			reason: ReasonMalformedEntry,
		},
		{
			name:   "missing field",
			value:  "[" + strings.Replace(toolCall, `,"operation_id":"op-1"`, "", 1) + "]",
			reason: ReasonMalformedEntry,
		},
		{
			name:   "list method outside the exact set",
			value:  "[" + strings.Replace(listCall, "tools/list", "tools/call/list", 1) + "]",
			reason: ReasonInvalidMethod,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pending, err := registry.Parse(tt.value, oauthproto.GrantTypeJWTBearer)
			require.NoError(t, err)
			granted, err := pending.Grant(context.Background(), authorizationdetails.Request{})
			if tt.reason == "" {
				require.NoError(t, err)
				assert.JSONEq(t, tt.value, string(granted))
				return
			}
			var refusal authorizationdetails.Error
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, authorizationdetails.CodeInvalidAuthorizationDetails, refusal.Code)
			assert.Equal(t, tt.reason, refusal.Reason)
		})
	}
}

func TestRegister_GrantTypes(t *testing.T) {
	t.Parallel()
	registry := newRegistry(t)

	_, err := registry.Parse("["+listCall+"]", oauthproto.GrantTypeTokenExchange)
	require.ErrorIs(t, err, authorizationdetails.ErrInvalid, "the type must not be valid on token exchange")
	_, err = registry.Parse("["+otherType+"]", oauthproto.GrantTypeJWTBearer)
	require.ErrorIs(t, err, authorizationdetails.ErrInvalid, "unregistered types are refused")
}
