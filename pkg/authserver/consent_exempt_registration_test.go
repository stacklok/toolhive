// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"cmp"
	"context"
	"errors"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

type consentLookupContextKey struct{}

type consentLookupStorage struct {
	storage.Storage
	client    fosite.Client
	lookupErr error
	lookups   []string
	ctxValue  any
}

func (s *consentLookupStorage) Unwrap() storage.Storage {
	return s.Storage
}

func (s *consentLookupStorage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	s.lookups = append(s.lookups, id)
	s.ctxValue = ctx.Value(consentLookupContextKey{})
	return s.client, s.lookupErr
}

type markedBackChannelConsentClient struct {
	*fosite.DefaultClient
	registration.BackChannelOnlyMarker
}

type noResponseConsentClient struct {
	*fosite.DefaultClient
}

func (noResponseConsentClient) GetResponseTypes() fosite.Arguments { return nil }

func eligibleConsentClient() *fosite.DefaultClient {
	return &fosite.DefaultClient{
		ID:            "exempt-client",
		Secret:        []byte("secret"),
		GrantTypes:    fosite.Arguments{oauthproto.GrantTypeAuthorizationCode},
		ResponseTypes: fosite.Arguments{oauthproto.ResponseTypeCode},
		RedirectURIs:  []string{"https://client.example.com/callback"},
	}
}

func consentExemptConfig(factory UpstreamProviderFactory) Config {
	cfg := validConsentPolicyConfig()
	cfg.KeyProvider = keys.NewGeneratingProvider(keys.DefaultAlgorithm)
	cfg.ConsentExemptClientIDs = []string{"exempt-client"}
	cfg.UpstreamFactory = factory
	return cfg
}

func TestNewValidatesConsentExemptClientRegistrations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		client    fosite.Client
		lookupErr error
		// wantErr defaults to "missing or ineligible".
		wantErr string
	}{
		{name: "missing registration", lookupErr: storage.ErrNotFound},
		{name: "public client", client: func() fosite.Client { c := eligibleConsentClient(); c.Public = true; return c }()},
		{name: "explicit back-channel marker", client: &markedBackChannelConsentClient{DefaultClient: eligibleConsentClient()}},
		{name: "empty response types", client: &noResponseConsentClient{DefaultClient: eligibleConsentClient()}},
		// A confidential token-exchange-only row has the legacy delegate shape, so
		// admission hides it as an undeclared configured client before eligibility
		// is checked.
		{name: "legacy delegate shape is hidden by admission", wantErr: "not_found", client: func() fosite.Client {
			c := eligibleConsentClient()
			c.GrantTypes = fosite.Arguments{oauthproto.GrantTypeTokenExchange}
			return c
		}()},
		{name: "authorization-code grant missing", client: func() fosite.Client {
			c := eligibleConsentClient()
			c.GrantTypes = fosite.Arguments{"client_credentials"}
			return c
		}()},
		{name: "code response type missing", client: func() fosite.Client {
			c := eligibleConsentClient()
			c.ResponseTypes = fosite.Arguments{"token"}
			return c
		}()},
		{name: "redirect URI missing", client: func() fosite.Client {
			c := eligibleConsentClient()
			c.RedirectURIs = nil
			return c
		}()},
		{name: "storage failure", lookupErr: errors.New("storage unavailable")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			baseStore := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = baseStore.Close() })
			decorated := &consentLookupStorage{Storage: baseStore, client: tt.client, lookupErr: tt.lookupErr}
			factoryCalls := 0
			factory := func(context.Context, *UpstreamConfig) (upstream.OAuth2Provider, error) {
				factoryCalls++
				return nil, errors.New("upstream factory should not run")
			}
			ctx := context.WithValue(context.Background(), consentLookupContextKey{}, "startup-context")

			_, err := newServer(ctx, consentExemptConfig(factory), decorated)
			require.Error(t, err)
			require.Empty(t, factoryCalls, "registration validation must precede upstream construction")
			require.Equal(t, []string{"exempt-client"}, decorated.lookups)
			require.Equal(t, "startup-context", decorated.ctxValue)
			if tt.lookupErr != nil {
				require.ErrorIs(t, err, tt.lookupErr)
			} else {
				require.ErrorContains(t, err, cmp.Or(tt.wantErr, "missing or ineligible"))
			}
		})
	}
}

func TestNewConsentExemptClientSyntaxPrecedesStorageLookup(t *testing.T) {
	t.Parallel()

	baseStore := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = baseStore.Close() })
	decorated := &consentLookupStorage{Storage: baseStore}
	cfg := consentExemptConfig(nil)
	cfg.ConsentExemptClientIDs = []string{"https://client.example.com"}

	_, err := newServer(context.Background(), cfg, decorated)
	require.ErrorContains(t, err, "URL-shaped")
	require.Empty(t, decorated.lookups)
}

func TestNewConsentExemptLookupUsesDecoratedStorage(t *testing.T) {
	t.Parallel()

	baseStore := storage.NewMemoryStorage()
	decorated := &consentLookupStorage{Storage: baseStore, client: eligibleConsentClient()}
	ctrl := gomock.NewController(t)
	mockUpstream := upstreammocks.NewMockOAuth2Provider(ctrl)
	factory := func(context.Context, *UpstreamConfig) (upstream.OAuth2Provider, error) {
		return mockUpstream, nil
	}

	srv, err := newServer(context.Background(), consentExemptConfig(factory), decorated)
	require.NoError(t, err)
	require.NotNil(t, srv)
	t.Cleanup(func() { require.NoError(t, srv.Close()) })
	require.Equal(t, []string{"exempt-client"}, decorated.lookups)
	// The backend has no such registration; construction succeeds only because
	// the effective storage decorator supplied it.
	_, err = baseStore.GetClient(context.Background(), "exempt-client")
	require.Error(t, err)
}
