// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package spiffetest builds SPIFFE OAuth clients for tests. It must not
// import package authserver, so in-package tests there can use it.
package spiffetest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

type config struct {
	id        string
	scopes    []string
	audiences []string
	resources []string
}

// Option overrides one field of the client built by Client.
type Option func(*config)

// WithID sets the client ID.
func WithID(id string) Option { return func(c *config) { c.id = id } }

// WithScopes sets the allowed scopes.
func WithScopes(scopes ...string) Option { return func(c *config) { c.scopes = scopes } }

// WithAudiences sets the allowed audiences.
func WithAudiences(audiences ...string) Option { return func(c *config) { c.audiences = audiences } }

// WithResources sets the allowed RFC 8707 resources.
func WithResources(resources ...string) Option { return func(c *config) { c.resources = resources } }

// Client returns a valid SPIFFE client with id "spiffe-client", scope
// "openid", audience "https://api.example.com" and no resources, adjusted
// by opts.
func Client(t *testing.T, opts ...Option) *registration.SPIFFEClient {
	t.Helper()
	c := config{
		id:        "spiffe-client",
		scopes:    []string{"openid"},
		audiences: []string{"https://api.example.com"},
	}
	for _, opt := range opts {
		opt(&c)
	}
	client, err := registration.NewSPIFFEClient(c.id, c.scopes, c.audiences, c.resources)
	require.NoError(t, err)
	return client
}
