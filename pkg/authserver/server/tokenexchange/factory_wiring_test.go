// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"testing"
	"time"

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
		{"IDJAGIssuanceFactory", func() (any, error) { return IDJAGIssuanceFactory(issuers, nil, nil, nil) }},
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

// TestFactoryOptions_Refusals pins that a factory refuses an option it does
// not apply, and that IDJAGIssuanceFactory refuses a client lifespan outside
// [MinClientAccessTokenLifespan, MaxAccessTokenLifespan]. No case sets
// trusted issuers, so no shared validator is needed: in
// FactoryWithSharedTrustedIssuerValidator the shared-validator check runs
// before the lifespan refusal.
func TestFactoryOptions_Refusals(t *testing.T) {
	t.Parallel()

	idJAG := func(option FactoryOption) func() (any, error) {
		return func() (any, error) { return IDJAGIssuanceFactory(nil, nil, nil, nil, option) }
	}
	lifespans := func(id string, d time.Duration) FactoryOption {
		return WithClientAccessTokenLifespans(map[string]time.Duration{id: d})
	}
	tests := []struct {
		name    string
		build   func() (any, error)
		wantErr string
	}{
		{
			name: "token exchange refuses client lifespans",
			build: func() (any, error) {
				return Factory(time.Hour, nil, nil, nil, lifespans("client", time.Minute))
			},
			wantErr: "per-client access token lifespans apply to ID-JAG redemption only",
		},
		{name: "ID-JAG refuses a zero lifespan", build: idJAG(lifespans("client", 0)), wantErr: "must be at least 2s"},
		{name: "ID-JAG refuses a negative lifespan", build: idJAG(lifespans("client", -time.Second)), wantErr: "must be at least 2s"},
		{name: "ID-JAG refuses a lifespan just under the floor", build: idJAG(lifespans("client", 2*time.Second-time.Millisecond)), wantErr: "must be at least 2s"},
		{name: "ID-JAG refuses a lifespan one second above the maximum", build: idJAG(lifespans("client", 24*time.Hour+time.Second)), wantErr: "at most 24h0m0s"},
		{name: "ID-JAG refuses an empty client ID", build: idJAG(lifespans("", time.Minute)), wantErr: "empty client ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := tt.build()
			assert.ErrorContains(t, err, tt.wantErr)
			assert.Nil(t, f)
		})
	}
}
