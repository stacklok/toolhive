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

//go:generate mockgen -destination=mocks/mock_storage.go -package=mocks -source=types.go Storage,IDPTokenStorage

import (
	"context"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
)

// DefaultPendingAuthorizationTTL is the default TTL for pending authorization requests.
const DefaultPendingAuthorizationTTL = 10 * time.Minute

// IDPTokens represents the tokens obtained from an upstream Identity Provider,
// stored with binding fields for security validation.
type IDPTokens struct {
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
func (t *IDPTokens) IsExpired() bool {
	return time.Now().After(t.ExpiresAt)
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

	// CreatedAt is when the pending authorization was created.
	CreatedAt time.Time
}

// Storage combines fosite storage interfaces with IDP token storage
// for the centralized OAuth authorization server.
type Storage interface {
	// fosite.ClientManager provides client management
	fosite.ClientManager

	// oauth2.AuthorizeCodeStorage provides authorization code storage
	oauth2.AuthorizeCodeStorage

	// oauth2.AccessTokenStorage provides access token storage
	oauth2.AccessTokenStorage

	// oauth2.RefreshTokenStorage provides refresh token storage
	oauth2.RefreshTokenStorage

	// pkce.PKCERequestStorage provides PKCE storage
	pkce.PKCERequestStorage

	// StoreIDPTokens stores the upstream IDP tokens for a session.
	// The sessionID should correspond to the session ID in the authorization server.
	StoreIDPTokens(ctx context.Context, sessionID string, tokens *IDPTokens) error

	// GetIDPTokens retrieves the upstream IDP tokens for a session.
	GetIDPTokens(ctx context.Context, sessionID string) (*IDPTokens, error)

	// DeleteIDPTokens removes the upstream IDP tokens for a session.
	DeleteIDPTokens(ctx context.Context, sessionID string) error

	// StorePendingAuthorization stores a pending authorization request.
	// The state is used to correlate the upstream IDP callback.
	StorePendingAuthorization(ctx context.Context, state string, pending *PendingAuthorization) error

	// LoadPendingAuthorization retrieves a pending authorization by internal state.
	LoadPendingAuthorization(ctx context.Context, state string) (*PendingAuthorization, error)

	// DeletePendingAuthorization removes a pending authorization.
	DeletePendingAuthorization(ctx context.Context, state string) error

	// RegisterClient registers a new OAuth client.
	// This supports both static configuration and dynamic client registration (RFC 7591).
	RegisterClient(client fosite.Client)
}

// IDPTokenStorage provides storage for upstream IDP tokens.
// This is a subset of Storage for implementations that only need IDP token storage.
type IDPTokenStorage interface {
	// StoreIDPTokens stores the upstream IDP tokens for a session.
	StoreIDPTokens(ctx context.Context, sessionID string, tokens *IDPTokens) error

	// GetIDPTokens retrieves the upstream IDP tokens for a session.
	GetIDPTokens(ctx context.Context, sessionID string) (*IDPTokens, error)

	// DeleteIDPTokens removes the upstream IDP tokens for a session.
	DeleteIDPTokens(ctx context.Context, sessionID string) error
}
