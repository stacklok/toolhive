// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJWTBearerHandler_RefusesConfirmationClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ValidatedClaims)
	}{
		{name: "cnf object", mutate: func(c *ValidatedClaims) {
			c.Claims = map[string]any{"cnf": map[string]any{"jkt": "thumbprint"}}
			c.Extra = map[string]any{"cnf": map[string]any{"jkt": "thumbprint"}}
		}},
		{name: "cnf null", mutate: func(c *ValidatedClaims) { c.Extra = map[string]any{"cnf": nil} }},
		{name: "cnf empty object", mutate: func(c *ValidatedClaims) { c.Claims = map[string]any{"cnf": map[string]any{}} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := refusalClaims()
			tt.mutate(claims)
			consumer := &recordingAssertionConsumer{}
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: claims}, refusalPolicies(t))
			deps.consumer = consumer
			handler := newTestJWTBearerHandler(t, deps)
			req := newJWTBearerRequest(map[string][]string{
				"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)},
				"resource":  {refusalResource},
			})

			err := handler.HandleTokenEndpointRequest(context.Background(), req)

			requireRefusal(t, err, "invalid_grant", "invalid_assertion: The JWT bearer assertion must not carry a 'cnf' claim.")
			assert.Empty(t, consumer.key, "a refused assertion must not be consumed")
		})
	}
}
