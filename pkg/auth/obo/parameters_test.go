// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/obo"
)

func TestMiddlewareParameters_ExchangeTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		audience string
		scopes   []string
		want     string
	}{
		{name: "single scope", scopes: []string{"api://backend/.default"}, want: "api://backend/.default"},
		{
			name:   "multiple scopes are space-joined",
			scopes: []string{"api://backend/.default", "offline_access"},
			want:   "api://backend/.default offline_access",
		},
		{
			name:     "scopes win over audience",
			audience: "api://backend",
			scopes:   []string{"api://backend/.default"},
			want:     "api://backend/.default",
		},
		{name: "audience used when no scopes", audience: "api://backend", want: "api://backend"},
		{
			name:     "audience used when scopes are blank",
			audience: "api://backend",
			scopes:   []string{"", "  "},
			want:     "api://backend",
		},
		{name: "empty when neither set", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &obo.MiddlewareParameters{Audience: tt.audience, Scopes: tt.scopes}
			assert.Equal(t, tt.want, p.ExchangeTarget())
		})
	}
}

func TestMiddlewareParameters_Validate_Valid(t *testing.T) {
	t.Parallel()

	valid := obo.MiddlewareParameters{
		TokenURL:           "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		ClientID:           "client-id",
		Scopes:             []string{"api://backend/.default"},
		ClientSecretEnvVar: "TOOLHIVE_OBO_CLIENT_SECRET",
	}

	t.Run("inbound mode (no provider name)", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, valid.Validate())
	})

	t.Run("upstream-provider mode", func(t *testing.T) {
		t.Parallel()
		p := valid
		p.SubjectTokenProviderName = "entra"
		require.NoError(t, p.Validate())
	})

	t.Run("audience instead of scopes", func(t *testing.T) {
		t.Parallel()
		p := valid
		p.Scopes = nil
		p.Audience = "api://backend"
		require.NoError(t, p.Validate())
	})
}

func TestMiddlewareParameters_Validate_Invalid(t *testing.T) {
	t.Parallel()

	base := obo.MiddlewareParameters{
		TokenURL:           "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		ClientID:           "client-id",
		Scopes:             []string{"api://backend/.default"},
		ClientSecretEnvVar: "TOOLHIVE_OBO_CLIENT_SECRET",
	}

	tests := []struct {
		name        string
		mutate      func(p *obo.MiddlewareParameters)
		wantErrText string
	}{
		{name: "missing tokenUrl", mutate: func(p *obo.MiddlewareParameters) { p.TokenURL = "" }, wantErrText: "tokenUrl is required"},
		{name: "missing clientId", mutate: func(p *obo.MiddlewareParameters) { p.ClientID = "" }, wantErrText: "clientId is required"},
		{
			name:        "missing clientSecretEnvVar",
			mutate:      func(p *obo.MiddlewareParameters) { p.ClientSecretEnvVar = "" },
			wantErrText: "clientSecretEnvVar is required",
		},
		{
			name:        "missing both scopes and audience",
			mutate:      func(p *obo.MiddlewareParameters) { p.Scopes = nil; p.Audience = "" },
			wantErrText: "one of scopes or audience is required",
		},
		{
			name:        "negative cacheSkewSeconds",
			mutate:      func(p *obo.MiddlewareParameters) { p.CacheSkewSeconds = -1 },
			wantErrText: "cacheSkewSeconds must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := base
			tt.mutate(&p)
			err := p.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErrText)
		})
	}
}
