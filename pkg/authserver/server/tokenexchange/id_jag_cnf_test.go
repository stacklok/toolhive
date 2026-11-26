// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIDJAGHandler_RefusesConfirmationClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ValidatedClaims)
	}{
		{
			name: "cnf object in the full claim set and Extra",
			mutate: func(c *ValidatedClaims) {
				cnf := map[string]any{"jkt": "thumbprint"}
				c.Claims = map[string]any{"cnf": cnf}
				c.Extra["cnf"] = cnf
			},
		},
		{name: "cnf object in Extra only", mutate: func(c *ValidatedClaims) { c.Extra["cnf"] = map[string]any{"jkt": "thumbprint"} }},
		{name: "cnf object in the full claim set only", mutate: func(c *ValidatedClaims) {
			c.Claims = map[string]any{"cnf": map[string]any{"jkt": "thumbprint"}}
		}},
		{name: "cnf null", mutate: func(c *ValidatedClaims) { c.Extra["cnf"] = nil }},
		{name: "cnf null in the full claim set only", mutate: func(c *ValidatedClaims) { c.Claims = map[string]any{"cnf": nil} }},
		{name: "cnf empty object", mutate: func(c *ValidatedClaims) { c.Extra["cnf"] = map[string]any{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validIDJAGClaims()
			tt.mutate(claims)
			consumer := &recordingAssertionConsumer{}
			h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: claims}, consumer)
			req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)

			err := h.HandleTokenEndpointRequest(context.Background(), req)

			requireRefusal(t, err, "invalid_grant", "invalid_assertion: The ID-JAG assertion must not carry a 'cnf' claim.")
			assert.Empty(t, consumer.key, "a refused assertion must not be consumed")
		})
	}
}
