package remote

import (
	"time"

	"golang.org/x/oauth2"
)

// BearerTokenSource implements oauth2.TokenSource for static bearer tokens.
// It returns a token with the bearer token value as the access token.
type BearerTokenSource struct {
	token string
}

// NewBearerTokenSource creates a new BearerTokenSource with the provided bearer token.
func NewBearerTokenSource(bearerToken string) *BearerTokenSource {
	return &BearerTokenSource{
		token: bearerToken,
	}
}

// Token returns an oauth2.Token with the bearer token as the access token.
// For static bearer tokens, this always returns the same token.
func (b *BearerTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{
		AccessToken: b.token,
		TokenType:   "Bearer",
		Expiry:      time.Time{}, // No expiry for static bearer tokens
	}, nil
}
