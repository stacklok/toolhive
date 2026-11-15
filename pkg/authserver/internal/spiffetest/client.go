// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package spiffetest builds SPIFFE OAuth clients for tests. It must not
// import package authserver, so in-package tests there can use it.
package spiffetest

import (
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

type config struct {
	id        string
	grants    []string
	scopes    []string
	audiences []string
	resources []string
	spiffeID  string
	method    spiffeauth.SPIFFEAuthenticationMethod
}

// Option overrides one field of the client built by Client or
// AuthenticatedClient.
type Option func(*config)

// WithID sets the client ID.
func WithID(id string) Option { return func(c *config) { c.id = id } }

// WithGrantTypes sets the allowed grant types.
func WithGrantTypes(grants ...string) Option { return func(c *config) { c.grants = grants } }

// WithScopes sets the allowed scopes.
func WithScopes(scopes ...string) Option { return func(c *config) { c.scopes = scopes } }

// WithAudiences sets the allowed audiences.
func WithAudiences(audiences ...string) Option { return func(c *config) { c.audiences = audiences } }

// WithResources sets the allowed RFC 8707 resources.
func WithResources(resources ...string) Option { return func(c *config) { c.resources = resources } }

// WithSPIFFEID sets the SPIFFE ID of the client built by AuthenticatedClient.
func WithSPIFFEID(id string) Option { return func(c *config) { c.spiffeID = id } }

// WithAuthMethod sets the authentication method of the client built by
// AuthenticatedClient.
func WithAuthMethod(method spiffeauth.SPIFFEAuthenticationMethod) Option {
	return func(c *config) { c.method = method }
}

// Client returns a valid SPIFFE client with id "spiffe-client", the
// token-exchange grant, scope "openid", audience "https://api.example.com" and
// no resources, adjusted by opts.
func Client(t *testing.T, opts ...Option) *registration.SPIFFEClient {
	t.Helper()
	c := newConfig(opts)
	client, err := registration.NewSPIFFEClient(c.id, c.grants, c.scopes, c.audiences, c.resources)
	require.NoError(t, err)
	return client
}

// AuthenticatedClient returns the SPIFFE client Client would build, bound to a
// verified SPIFFE ID and method as the client-authentication resolver does. The
// SPIFFE ID is "spiffe://example.org/workload/test" and the method is JWT
// unless opts say otherwise.
func AuthenticatedClient(t *testing.T, opts ...Option) *registration.AuthenticatedSPIFFEClient {
	t.Helper()
	c := newConfig(opts)
	id, err := spiffeid.FromString(c.spiffeID)
	require.NoError(t, err)
	client, err := registration.NewAuthenticatedSPIFFEClient(Client(t, opts...), id, c.method)
	require.NoError(t, err)
	return client
}

func newConfig(opts []Option) config {
	c := config{
		id:        "spiffe-client",
		grants:    []string{oauthproto.GrantTypeTokenExchange},
		scopes:    []string{"openid"},
		audiences: []string{"https://api.example.com"},
		spiffeID:  "spiffe://example.org/workload/test",
		method:    spiffeauth.SPIFFEAuthenticationMethodJWT,
	}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}
