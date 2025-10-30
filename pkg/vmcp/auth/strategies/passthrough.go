package strategies

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/vmcp/auth"
)

// PassThroughStrategy forwards the client's token to the backend as-is.
// This strategy requires no configuration and is used when the backend
// trusts the same identity provider as the vMCP server.
//
// The strategy extracts the client's token from the context (which must be
// set by the IncomingAuthenticator) and forwards it unchanged in the
// Authorization header of the backend request.
//
// This is the simplest authentication strategy and is appropriate when:
//   - The backend and vMCP server share the same identity provider
//   - The backend can validate the same token format (e.g., JWT from same issuer)
//   - No token transformation or exchange is required
type PassThroughStrategy struct{}

// NewPassThroughStrategy creates a new PassThroughStrategy instance.
func NewPassThroughStrategy() *PassThroughStrategy {
	return &PassThroughStrategy{}
}

// Name returns the strategy identifier.
func (*PassThroughStrategy) Name() string {
	return "pass_through"
}

// Authenticate performs authentication by forwarding the client's token to the backend.
//
// This method:
//  1. Retrieves the identity from the context (set by IncomingAuthenticator)
//  2. Validates that a token is present
//  3. Sets the Authorization header with the token and token type
//
// Parameters:
//   - ctx: Request context containing the authenticated identity
//   - req: The HTTP request to authenticate
//   - metadata: Strategy-specific configuration (unused for pass-through)
//
// Returns an error if:
//   - No identity is found in the context
//   - The identity has no token
func (*PassThroughStrategy) Authenticate(ctx context.Context, req *http.Request, _ map[string]any) error {
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return errors.New("no identity found in context")
	}

	if identity.Token == "" {
		return errors.New("identity has no token")
	}

	// Default to "Bearer" token type if not specified
	tokenType := identity.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}

	// Forward the client's token to the backend
	req.Header.Set("Authorization", fmt.Sprintf("%s %s", tokenType, identity.Token))
	return nil
}

// Validate checks if the strategy configuration is valid.
// PassThroughStrategy requires no metadata, so this always returns nil.
func (*PassThroughStrategy) Validate(_ map[string]any) error {
	// No metadata required for pass-through
	return nil
}
