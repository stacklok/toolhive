// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestJWTBearerGrantFactories_RequireSharedValidator pins that both
// redemption factories refuse to build without a caller-owned shared validator.
func TestJWTBearerGrantFactories_RequireSharedValidator(t *testing.T) {
	t.Parallel()

	issuers := []TrustedIssuer{testRedemptionIssuer(idJAGTestIssuer, nil)}
	tests := []struct {
		name  string
		build func() (any, error)
	}{
		{"JWTBearerIssuanceFactory", func() (any, error) { return JWTBearerIssuanceFactory(issuers, nil) }},
		{"IDJAGIssuanceFactory", func() (any, error) { return IDJAGIssuanceFactory(issuers, nil, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := tt.build()
			assert.ErrorContains(t, err, "a shared validator built via NewSharedTrustedIssuerValidator is required")
			assert.Nil(t, f)
		})
	}
}
