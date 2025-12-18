package authserver

//go:generate mockgen -destination=mocks/mock_storage.go -package=mocks -source=storage.go Storage,IDPTokenStorage

import (
	"context"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
)

// IDPTokens represents the tokens obtained from an upstream Identity Provider.
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
