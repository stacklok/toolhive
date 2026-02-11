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

//go:generate mockgen -destination=mocks/mock_storage.go -package=mocks -source=types.go Storage,PendingAuthorizationStorage,ClientRegistry,UpstreamTokenStorage,UserStorage

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

// UpstreamTokens represents tokens obtained from an upstream Identity Provider.
// These tokens are stored with binding fields for security validation and
// ProviderID for multi-IDP support.
type UpstreamTokens struct {
	// ProviderID identifies which upstream provider issued these tokens.
	// Example values: "google", "github", "okta"
	ProviderID string

	// Token values from the upstream IDP
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresAt    time.Time

	// Security binding fields - validated on lookup to prevent cross-session attacks

	// UserID is the internal ToolHive user ID (references User.ID).
	// This is NOT the upstream provider's subject - it's our stable internal identifier.
	// In multi-IDP scenarios, the same UserID may have tokens from multiple providers.
	UserID string

	// UpstreamSubject is the "sub" claim from the upstream IDP's ID token.
	// This enables validation that tokens match the expected upstream identity
	// and supports audit logging of which upstream identity was used.
	UpstreamSubject string

	// ClientID is the OAuth client that initiated the authorization.
	// Tokens should only be accessible to the same client that obtained them.
	ClientID string
}

// IsExpired returns true if the access token has expired.
// The now parameter allows for deterministic testing.
func (t *UpstreamTokens) IsExpired(now time.Time) bool {
	return now.After(t.ExpiresAt)
}

// User represents a user account in the authorization server.
// A user can have multiple linked provider identities.
// The User.ID is used as the "sub" claim in JWTs issued by ToolHive,
// providing a stable identity across multiple upstream identity providers.
type User struct {
	// ID is the internal, stable user identifier (UUID format).
	// This value is used as the "sub" claim in ToolHive-issued JWTs.
	ID string

	// CreatedAt is when the user account was created.
	CreatedAt time.Time

	// UpdatedAt is when the user account was last modified.
	UpdatedAt time.Time
}

// ProviderIdentity links a user to an upstream identity provider.
// Multiple identities can be linked to a single user for account linking,
// enabling users to authenticate via different providers (e.g., Google and GitHub)
// while maintaining a single ToolHive identity.
type ProviderIdentity struct {
	// UserID is the internal user ID this identity belongs to.
	UserID string

	// ProviderID identifies the upstream provider (e.g., "google", "github").
	ProviderID string

	// ProviderSubject is the subject identifier from the upstream provider.
	ProviderSubject string

	// LinkedAt is when this identity was linked to the user.
	LinkedAt time.Time

	// LastUsedAt is when this identity was last used to authenticate.
	LastUsedAt time.Time
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

// UserStorage provides user and provider identity management operations.
// This interface supports multi-IDP scenarios where a single user can authenticate
// via multiple upstream identity providers (e.g., Google and GitHub).
//
// The User type represents the internal ToolHive identity. Its ID becomes the "sub"
// claim in issued JWTs, providing a stable identity across multiple providers.
//
// ProviderIdentity links a user to a specific upstream provider. The
// (ProviderID, ProviderSubject) pair uniquely identifies an upstream identity.
//
// # Account Linking Security
//
// When implementing account linking (one User with multiple ProviderIdentities),
// callers MUST verify user consent before linking. See OAuth 2.0 Security BCP.
//
// TODO(auth): When implementing double-hop auth (Company IDP -> External IDP),
// add the following to this interface:
//   - DeleteProviderIdentity(providerID, subject) - unlink specific provider
//   - Add Primary field to ProviderIdentity to distinguish Company IDP from linked External IDPs
//   - Add ConsentRecord tracking for external provider linking
type UserStorage interface {
	// CreateUser creates a new user account.
	// Returns ErrAlreadyExists if a user with the same ID already exists.
	CreateUser(ctx context.Context, user *User) error

	// GetUser retrieves a user by their internal ID.
	// Returns ErrNotFound if the user does not exist.
	GetUser(ctx context.Context, id string) (*User, error)

	// DeleteUser removes a user account.
	// Returns ErrNotFound if the user does not exist.
	DeleteUser(ctx context.Context, id string) error

	// CreateProviderIdentity links a provider identity to a user.
	// For account linking scenarios, caller MUST verify user owns the target User
	// (typically via active authenticated session) before linking a new provider.
	// Returns ErrAlreadyExists if this provider identity is already linked.
	CreateProviderIdentity(ctx context.Context, identity *ProviderIdentity) error

	// GetProviderIdentity retrieves a provider identity by provider ID and subject.
	// This is the primary lookup path during authentication callbacks.
	// Returns ErrNotFound if the identity does not exist.
	GetProviderIdentity(ctx context.Context, providerID, providerSubject string) (*ProviderIdentity, error)

	// UpdateProviderIdentityLastUsed updates the LastUsedAt timestamp for a provider identity.
	// This should be called after each successful authentication via this identity.
	// The timestamp supports OIDC auth_time claim when clients use max_age parameter.
	// Returns ErrNotFound if the identity does not exist.
	UpdateProviderIdentityLastUsed(ctx context.Context, providerID, providerSubject string, lastUsedAt time.Time) error

	// GetUserProviderIdentities returns all provider identities linked to a user.
	// This enables queries like "when did this user last authenticate via any provider"
	// which is needed for OIDC max_age enforcement.
	// Returns an empty slice (not error) if the user exists but has no linked identities.
	// Returns ErrNotFound if the user does not exist.
	GetUserProviderIdentities(ctx context.Context, userID string) ([]*ProviderIdentity, error)
}

// Storage combines fosite storage interfaces with ToolHive-specific storage for
// upstream IDP tokens, pending authorization requests, and client registration.
// The auth server requires a Storage implementation; use NewMemoryStorage() for
// single-instance deployments or NewRedisStorage() for distributed deployments.
//
// # Fosite Interface Segregation
//
// Fosite splits storage into separate interfaces (AuthorizeCodeStorage, AccessTokenStorage,
// RefreshTokenStorage, PKCERequestStorage) following the Interface Segregation Principle.
// This enables:
//   - Feature composition: Only enable OAuth features you need
//   - Testing isolation: Mock specific interfaces for focused tests
//   - Clear contracts: Each interface documents its requirements
//
// # Key Design Patterns
//
// All token storage methods store fosite.Requester (not just token values) because token
// validation requires the full authorization context (client, scopes, session).
//
// Methods use two key types:
//   - Signature: Cryptographic token identifier for token-specific operations
//   - Request ID: Grant identifier for finding all tokens from one authorization
//
// See doc.go for comprehensive documentation of fosite's storage design.
type Storage interface {
	// Embed segregated interfaces for IDP tokens, pending authorizations, client registry,
	// and user management for multi-IDP support.
	UpstreamTokenStorage
	PendingAuthorizationStorage
	ClientRegistry
	UserStorage

	// AuthorizeCodeStorage provides authorization code storage for the Authorization Code
	// Grant (RFC 6749 Section 4.1). Authorization codes are one-time-use and short-lived.
	// CreateAuthorizeCodeSession stores by code, GetAuthorizeCodeSession retrieves by code,
	// InvalidateAuthorizeCodeSession marks as used (subsequent Gets return ErrInvalidatedAuthorizeCode).
	oauth2.AuthorizeCodeStorage

	// AccessTokenStorage provides access token session storage. Methods use "signature"
	// (derived from token value) as the key for O(1) lookup when validating tokens.
	// The stored fosite.Requester contains the full authorization context including
	// the Session with expiration times via session.GetExpiresAt(fosite.AccessToken).
	oauth2.AccessTokenStorage

	// RefreshTokenStorage provides refresh token session storage. CreateRefreshTokenSession
	// accepts an accessSignature to link refresh tokens to their access tokens for rotation.
	// RotateRefreshToken uses requestID to invalidate both the refresh token and all
	// related access tokens from the same authorization grant.
	oauth2.RefreshTokenStorage

	// TokenRevocationStorage provides token revocation per RFC 7009. RevokeAccessToken
	// and RevokeRefreshToken take requestID (not signature) because RFC 7009 requires
	// revoking a refresh token SHOULD also invalidate associated access tokens, which
	// requires finding all tokens by their common grant identifier.
	oauth2.TokenRevocationStorage

	// PKCERequestStorage provides PKCE challenge/verifier storage (RFC 7636).
	// Stores the code_challenge during authorization, validates code_verifier during
	// token exchange. Keyed by the same code/signature as the authorization code.
	pkce.PKCERequestStorage

	// Health checks connectivity to the storage backend.
	// Returns nil if the storage is healthy and reachable.
	Health(ctx context.Context) error

	// Close releases any resources held by the storage implementation.
	// This should be called when the storage is no longer needed.
	Close() error
}
