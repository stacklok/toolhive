// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
	"github.com/stacklok/toolhive/pkg/authserver/subjectresolver"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func TestNewSubjectResolver(t *testing.T) {
	t.Parallel()

	const idpIssuer = "https://idp.example.com"
	resolveIssuer := tokenexchange.TrustedIssuer{
		IssuerURL: "https://as1.example.com",
		JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{
			SubjectMode: tokenexchange.SubjectModeResolve,
		},
	}
	oidcUpstream := func(name, claim string) UpstreamConfig {
		return UpstreamConfig{
			Name: name, Type: UpstreamProviderTypeOIDC,
			OIDCConfig: &upstream.OIDCConfig{Issuer: idpIssuer, SubjectClaim: claim},
		}
	}

	t.Run("no resolve issuer builds nothing", func(t *testing.T) {
		t.Parallel()
		got, err := newSubjectResolver(Config{TrustedIssuers: []tokenexchange.TrustedIssuer{{IssuerURL: "https://as1.example.com"}}}, nil)
		require.NoError(t, err)
		assert.True(t, got == nil, "want a nil interface, got %T", got)
	})
	t.Run("resolve issuer without upstreams", func(t *testing.T) {
		t.Parallel()
		_, err := newSubjectResolver(Config{TrustedIssuers: []tokenexchange.TrustedIssuer{resolveIssuer}}, nil)
		require.ErrorContains(t, err, "needs an OIDC identity provider")
	})

	tests := []struct {
		name         string
		upstream     UpstreamConfig
		wantClaim    string
		wantProvider string
	}{
		{"defaults", oidcUpstream("", ""), "sub", "default"},
		{"configured claim and name", oidcUpstream("corp", "oid"), "oid", "corp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := storagemocks.NewMockUserStorage(gomock.NewController(t))
			store.EXPECT().GetProviderIdentity(gomock.Any(), tt.wantProvider, "v").
				Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
			store.EXPECT().GetUser(gomock.Any(), "user-1").Return(&storage.User{ID: "user-1"}, nil)
			cfg := Config{
				Upstreams:      []UpstreamConfig{tt.upstream},
				TrustedIssuers: []tokenexchange.TrustedIssuer{resolveIssuer},
			}

			got, err := newSubjectResolver(cfg, store)

			require.NoError(t, err)
			user, err := got.Resolve(context.Background(),
				subject.Ref{Issuer: idpIssuer, Claim: tt.wantClaim, Value: "v"})
			require.NoError(t, err)
			assert.Equal(t, "user-1", user)
			_, err = got.Resolve(context.Background(),
				subject.Ref{Issuer: "https://other.example.com", Claim: tt.wantClaim, Value: "v"})
			require.ErrorIs(t, err, subject.ErrNotConfigured)
			_, err = got.Resolve(context.Background(),
				subject.Ref{Issuer: idpIssuer, Claim: "other_claim", Value: "v"})
			require.ErrorIs(t, err, subject.ErrNotConfigured)
		})
	}
}

func TestNewSubjectResolver_OnlyFirstUpstreamIsMapped(t *testing.T) {
	t.Parallel()

	const sharedIssuer = "https://idp.example.com"
	oidc := func(name, claim string) UpstreamConfig {
		return UpstreamConfig{
			Name: name, Type: UpstreamProviderTypeOIDC,
			OIDCConfig: &upstream.OIDCConfig{Issuer: sharedIssuer, SubjectClaim: claim},
		}
	}
	cfg := Config{
		Upstreams: []UpstreamConfig{oidc("first", "sub"), oidc("second", "oid")},
		TrustedIssuers: []tokenexchange.TrustedIssuer{{
			IssuerURL:      "https://as1.example.com",
			JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{SubjectMode: tokenexchange.SubjectModeResolve},
		}},
	}
	store := storagemocks.NewMockUserStorage(gomock.NewController(t))
	// Only the first upstream's name is ever looked up, even though the second
	// upstream shares its issuer.
	store.EXPECT().GetProviderIdentity(gomock.Any(), "first", "v").
		Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "user-1").Return(&storage.User{ID: "user-1"}, nil)

	resolver, err := newSubjectResolver(cfg, store)
	require.NoError(t, err)

	user, err := resolver.Resolve(context.Background(), subject.Ref{Issuer: sharedIssuer, Claim: "sub", Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, "user-1", user)

	_, err = resolver.Resolve(context.Background(), subject.Ref{Issuer: sharedIssuer, Claim: "oid", Value: "v"})
	require.ErrorIs(t, err, subject.ErrNotConfigured, "the second upstream's claim must not resolve")
}

func TestBuildJWTBearerFactoriesForConfig(t *testing.T) {
	t.Parallel()

	issuers := []tokenexchange.TrustedIssuer{{
		IssuerURL:         "https://as1.example.com",
		JWKSURL:           testJWKSURL(t),
		InsecureAllowHTTP: true,
		AllowPrivateIPs:   true,
		JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{
			MaxAssertionAge:        "10m",
			SubjectMode:            tokenexchange.SubjectModeResolve,
			AcceptedAssertionTypes: []tokenexchange.JWTBearerAssertionType{tokenexchange.JWTBearerAssertionTypeIDJAG},
			AcceptedAudiences:      []string{"https://example.com"},
		},
	}}
	shared := newTestSharedValidator(t, issuers)
	cfg := Config{
		Upstreams: []UpstreamConfig{{
			Name: "corp", Type: UpstreamProviderTypeOIDC,
			OIDCConfig: &upstream.OIDCConfig{Issuer: "https://idp.example.com", SubjectClaim: "oid"},
		}},
		TrustedIssuers: issuers,
	}

	factories, err := buildJWTBearerFactoriesForConfig(cfg, shared, storagemocks.NewMockStorage(gomock.NewController(t)))

	require.NoError(t, err)
	assert.Len(t, factories, 2)
}

func TestValidateSingleIssuerPerProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		providers map[string]subjectresolver.Provider
		wantErr   string
	}{
		{
			name: "one issuer for the provider",
			providers: map[string]subjectresolver.Provider{
				"https://idp.example.com": {Name: "idp", SubjectClaim: "sub"},
			},
		},
		{
			name: "two providers with one issuer each",
			providers: map[string]subjectresolver.Provider{
				"https://idp.example.com":   {Name: "idp", SubjectClaim: "sub"},
				"https://other.example.com": {Name: "other", SubjectClaim: "sub"},
			},
		},
		{
			name: "two issuers for one provider",
			providers: map[string]subjectresolver.Provider{
				"https://idp.example.com":   {Name: "idp", SubjectClaim: "sub"},
				"https://okta.example.com":  {Name: "idp", SubjectClaim: "uid"},
				"https://other.example.com": {Name: "other", SubjectClaim: "sub"},
			},
			wantErr: `provider "idp" maps to more than one issuer (https://idp.example.com, https://okta.example.com); ` +
				"a second issuer for one provider is not supported yet because rows are stamped with the sign-in issuer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSingleIssuerPerProvider(tt.providers)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
