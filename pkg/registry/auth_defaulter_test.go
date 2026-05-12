// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAuthDefaults(t *testing.T) {
	t.Parallel()

	const (
		discoveredIssuer   = "https://disco.example.com"
		discoveredClientID = "disco-client"
		flagIssuer         = "https://flag.example.com"
		flagClientID       = "flag-client"
	)

	defaulterOK := func(_ context.Context) (string, string, error) {
		return discoveredIssuer, discoveredClientID, nil
	}
	defaulterErr := func(_ context.Context) (string, string, error) {
		return "", "", errors.New("config server unreachable")
	}

	tests := []struct {
		name         string
		issuer       string
		clientID     string
		defaulter    AuthDefaulter
		wantIssuer   string
		wantClientID string
	}{
		{
			name:         "explicit values take precedence over defaulter",
			issuer:       flagIssuer,
			clientID:     flagClientID,
			defaulter:    defaulterOK,
			wantIssuer:   flagIssuer,
			wantClientID: flagClientID,
		},
		{
			name:         "no explicit values and defaulter succeeds returns discovered values",
			defaulter:    defaulterOK,
			wantIssuer:   discoveredIssuer,
			wantClientID: discoveredClientID,
		},
		{
			name:      "no explicit values and defaulter fails falls back to empty",
			defaulter: defaulterErr,
		},
		{
			name: "no explicit values and nil defaulter returns empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotIssuer, gotClientID := ResolveAuthDefaults(
				t.Context(), tt.issuer, tt.clientID, tt.defaulter,
			)

			require.Equal(t, tt.wantIssuer, gotIssuer)
			assert.Equal(t, tt.wantClientID, gotClientID)
		})
	}
}

//nolint:paralleltest // Mutates global registry auth defaulter singleton
func TestRegisterAuthDefaulter(t *testing.T) {
	original := ActiveAuthDefaulter()
	t.Cleanup(func() { RegisterAuthDefaulter(original) })

	const wantIssuer = "https://reg.example.com"
	RegisterAuthDefaulter(func(_ context.Context) (string, string, error) {
		return wantIssuer, "reg-client", nil
	})

	active := ActiveAuthDefaulter()
	require.NotNil(t, active)
	issuer, clientID, err := active(t.Context())
	require.NoError(t, err)
	assert.Equal(t, wantIssuer, issuer)
	assert.Equal(t, "reg-client", clientID)

	RegisterAuthDefaulter(nil)
	assert.Nil(t, ActiveAuthDefaulter())
}
