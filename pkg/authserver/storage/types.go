// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package storage provides storage interfaces and implementations for the
// OAuth authorization server.
package storage

//go:generate mockgen -destination=mocks/mock_storage.go -package=mocks -source=types.go Storage,PendingAuthorizationStorage,ClientRegistry,UpstreamTokenStorage

import (
	"context"
	"errors"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
)

// Sentinel errors for storage operations.
// Use errors.Is() to check for these error types.
var (
	// ErrNotFound is returned when an item is not found in storage.
	ErrNotFound = errors.New("storage: item not found")

	// ErrExpired is returned when an item exists but has expired.
	ErrExpired = errors.New("storage: item expired")

	// ErrAlreadyExists is returned when attempting to create an item that already exists.
	ErrAlreadyExists = errors.New("storage: item already exists")

	// ErrInvalidBinding is returned when token binding validation fails
	// (e.g., subject or client ID mismatch).
	ErrInvalidBinding = errors.New("storage: token binding validation failed")
)

// DefaultPendingAuthorizationTTL is the default TTL for pending authorization requests.
const DefaultPendingAuthorizationTTL = 10 * time.Minute

// UpstreamTokens represents the tokens obtained from an upstream Identity Provider,
// stored with binding fields for security validation.
type UpstreamTokens struct {
	// AccessToken is the access token from the upstream IDP.
	AccessToken string

	// RefreshToken is the refresh token from the upstream IDP (if provided).
	RefreshToken string

	// IDToken is the ID token from the upstream IDP (for OIDC).
	IDToken string

	// ExpiresAt is when the access token expires.
	ExpiresAt time.Time

	// Subject is the user identifier from the IDP.
	// This binding field is validated on lookup to prevent cross-session attacks
	// by ensuring the JWT "sub" claim matches this value.
	Subject string

	// ClientID is the OAuth client that initiated the authorization.
	// This binding field is validated on lookup to prevent cross-session attacks
	// by ensuring the JWT "client_id" or "azp" claim matches this value.
	ClientID string
}

// IsExpired returns true if the access token has expired.
// The now parameter allows for deterministic testing.
func (t *UpstreamTokens) IsExpired(now time.Time) bool {
	return now.After(t.ExpiresAt)
}

// PendingAuthorization tracks a client's authorization request while they
// authenticate with the upstream IDP.
type PendingAuthorization struct {
	// ClientID is the ID of the OAuth client making the authorization request.
	ClientID string

	// RedirectURI is the client's callback URL where we'll redirect after authentication.
	RedirectURI string

	// State is the client's original state parameter for CSRF protection.
	State string

	// PKCEChallenge is the client's PKCE code challenge.
	PKCEChallenge string

	// PKCEMethod is the PKCE challenge method (must be "S256").
	PKCEMethod string

	// Scopes are the OAuth scopes requested by the client.
	Scopes []string

	// InternalState is our randomly generated state for correlating upstream callback.
	InternalState string

	// UpstreamPKCEVerifier is the PKCE code_verifier for the upstream IDP authorization.
	// This is generated when redirecting to the upstream IDP and used when exchanging
	// the authorization code. See RFC 7636.
	UpstreamPKCEVerifier string

	// UpstreamNonce is the OIDC nonce parameter sent to the upstream IDP for ID Token
	// replay protection. This is validated against the nonce claim in the returned
	// ID Token. See OIDC Core Section 3.1.2.1.
	UpstreamNonce string

	// CreatedAt is when the pending authorization was created.
	CreatedAt time.Time
}

// PendingAuthorizationStorage provides storage operations for pending authorization requests.
// These track the state of in-flight authorization requests while users authenticate
// with the upstream IDP, correlating the upstream callback with the original client request.
type PendingAuthorizationStorage interface {
	// StorePendingAuthorization stores a pending authorization request.
	// The state is used to correlate the upstream IDP callback.
	StorePendingAuthorization(ctx context.Context, state string, pending *PendingAuthorization) error

	// LoadPendingAuthorization retrieves a pending authorization by internal state.
	// Returns ErrNotFound if the state does not exist.
	// Returns ErrExpired if the pending authorization has expired.
	LoadPendingAuthorization(ctx context.Context, state string) (*PendingAuthorization, error)

	// DeletePendingAuthorization removes a pending authorization.
	// Returns ErrNotFound if the state does not exist.
	DeletePendingAuthorization(ctx context.Context, state string) error
}

// ClientRegistry provides client registration and lookup operations.
// It embeds fosite.ClientManager for client lookup (GetClient) and adds
// RegisterClient for dynamic client registration (RFC 7591).
type ClientRegistry interface {
	// ClientManager provides client lookup (GetClient)
	fosite.ClientManager

	// RegisterClient registers a new OAuth client.
	// This supports both static configuration and dynamic client registration (RFC 7591).
	// Returns ErrAlreadyExists if a client with the same ID already exists.
	RegisterClient(ctx context.Context, client fosite.Client) error
}

// UpstreamTokenStorage provides storage for tokens obtained from upstream identity providers.
// The auth server exposes this interface via Server.UpstreamTokenStorage() for use by
// middleware that needs to retrieve upstream tokens (e.g., token swap middleware that
// replaces JWT auth with upstream IDP tokens for backend requests).
type UpstreamTokenStorage interface {
	// StoreUpstreamTokens stores the upstream IDP tokens for a session.
	StoreUpstreamTokens(ctx context.Context, sessionID string, tokens *UpstreamTokens) error

	// GetUpstreamTokens retrieves the upstream IDP tokens for a session.
	// Returns ErrNotFound if the session does not exist.
	// Returns ErrExpired if the tokens have expired.
	// Returns ErrInvalidBinding if binding validation fails.
	GetUpstreamTokens(ctx context.Context, sessionID string) (*UpstreamTokens, error)

	// DeleteUpstreamTokens removes the upstream IDP tokens for a session.
	// Returns ErrNotFound if the session does not exist.
	DeleteUpstreamTokens(ctx context.Context, sessionID string) error
}

// Storage combines fosite storage interfaces with ToolHive-specific storage for
// upstream IDP tokens, pending authorization requests, and client registration.
// The auth server requires a Storage implementation; use NewMemoryStorage() for
// single-instance deployments or NewRedisStorage() for distributed deployments.
type Storage interface {
	// Embed segregated interfaces for IDP tokens, pending authorizations, and client registry.
	UpstreamTokenStorage
	PendingAuthorizationStorage
	ClientRegistry

	// AuthorizeCodeStorage provides authorization code storage
	oauth2.AuthorizeCodeStorage

	// AccessTokenStorage provides access token storage
	oauth2.AccessTokenStorage

	// RefreshTokenStorage provides refresh token storage
	oauth2.RefreshTokenStorage

	// TokenRevocationStorage provides token revocation per RFC 7009
	oauth2.TokenRevocationStorage

	// PKCERequestStorage provides PKCE storage
	pkce.PKCERequestStorage

	// Close releases any resources held by the storage implementation.
	// This should be called when the storage is no longer needed.
	Close() error
}
