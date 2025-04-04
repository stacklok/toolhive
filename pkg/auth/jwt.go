// Package auth provides authentication and authorization utilities.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

// DebugLoggingEnabled controls whether debug logging is enabled
var DebugLoggingEnabled = true

// debugLog logs a debug message if debug logging is enabled
func debugLog(format string, args ...interface{}) {
	if DebugLoggingEnabled {
		log.Printf("[AUTH-DEBUG] "+format, args...)
	}
}

// Common errors
var (
	ErrNoToken           = errors.New("no token provided")
	ErrInvalidToken      = errors.New("invalid token")
	ErrTokenExpired      = errors.New("token expired")
	ErrInvalidIssuer     = errors.New("invalid issuer")
	ErrInvalidAudience   = errors.New("invalid audience")
	ErrMissingJWKSURL    = errors.New("missing JWKS URL")
	ErrFailedToFetchJWKS = errors.New("failed to fetch JWKS")
)

// JWTValidator validates JWT tokens.
type JWTValidator struct {
	// OIDC configuration
	issuer     string
	audience   string
	jwksURL    string
	clientID   string
	jwksClient *jwk.Cache

	// No need for additional caching as jwk.Cache handles it
}

// JWTValidatorConfig contains configuration for the JWT validator.
type JWTValidatorConfig struct {
	// Issuer is the OIDC issuer URL (e.g., https://accounts.google.com)
	Issuer string

	// Audience is the expected audience for the token
	Audience string

	// JWKSURL is the URL to fetch the JWKS from
	JWKSURL string

	// ClientID is the OIDC client ID
	ClientID string
}

// NewJWTValidatorConfig creates a new JWTValidatorConfig with the provided parameters
func NewJWTValidatorConfig(issuer, audience, jwksURL, clientID string) *JWTValidatorConfig {
	// Only create a config if at least one parameter is provided
	if issuer == "" && audience == "" && jwksURL == "" && clientID == "" {
		return nil
	}

	return &JWTValidatorConfig{
		Issuer:   issuer,
		Audience: audience,
		JWKSURL:  jwksURL,
		ClientID: clientID,
	}
}

// NewJWTValidator creates a new JWT validator.
func NewJWTValidator(ctx context.Context, config JWTValidatorConfig) (*JWTValidator, error) {
	if config.JWKSURL == "" {
		return nil, ErrMissingJWKSURL
	}

	// Create a new JWKS client with auto-refresh
	cache := jwk.NewCache(ctx)

	// Register the JWKS URL with the cache
	err := cache.Register(config.JWKSURL)
	if err != nil {
		return nil, fmt.Errorf("failed to register JWKS URL: %w", err)
	}

	return &JWTValidator{
		issuer:     config.Issuer,
		audience:   config.Audience,
		jwksURL:    config.JWKSURL,
		clientID:   config.ClientID,
		jwksClient: cache,
	}, nil
}

// getKeyFromJWKS gets the key from the JWKS.
func (v *JWTValidator) getKeyFromJWKS(ctx context.Context, token *jwt.Token) (interface{}, error) {
	// Validate the signing method
	if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
		if DebugLoggingEnabled {
			debugLog("Unexpected signing method: %v", token.Header["alg"])
		}
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}

	// Get the key ID from the token header
	kid, ok := token.Header["kid"].(string)
	if !ok {
		if DebugLoggingEnabled {
			debugLog("Token header missing kid")
		}
		return nil, fmt.Errorf("token header missing kid")
	}
	
	if DebugLoggingEnabled {
		debugLog("Looking up key ID: %s", kid)
	}

	// Get the key set from the JWKS
	keySet, err := v.jwksClient.Get(ctx, v.jwksURL)
	if err != nil {
		if DebugLoggingEnabled {
			debugLog("Failed to get JWKS: %v", err)
		}
		return nil, fmt.Errorf("failed to get JWKS: %w", err)
	}

	// Get the key with the matching key ID
	key, found := keySet.LookupKeyID(kid)
	if !found {
		if DebugLoggingEnabled {
			debugLog("Key ID %s not found in JWKS", kid)
		}
		return nil, fmt.Errorf("key ID %s not found in JWKS", kid)
	}

	// Get the raw key
	var rawKey interface{}
	if err := key.Raw(&rawKey); err != nil {
		if DebugLoggingEnabled {
			debugLog("Failed to get raw key: %v", err)
		}
		return nil, fmt.Errorf("failed to get raw key: %w", err)
	}
	
	if DebugLoggingEnabled {
		debugLog("Successfully retrieved key from JWKS")
	}

	return rawKey, nil
}

// validateClaims validates the claims in the token.
func (v *JWTValidator) validateClaims(claims jwt.MapClaims) error {
	// Validate the issuer if provided
	if v.issuer != "" {
		issuer, err := claims.GetIssuer()
		if err != nil || issuer != v.issuer {
			if DebugLoggingEnabled {
				debugLog("Invalid issuer: expected=%s, got=%s", v.issuer, issuer)
			}
			return ErrInvalidIssuer
		}
		if DebugLoggingEnabled {
			debugLog("Issuer validated: %s", issuer)
		}
	}

	// Validate the audience if provided
	if v.audience != "" {
		audiences, err := claims.GetAudience()
		if err != nil {
			if DebugLoggingEnabled {
				debugLog("Failed to get audience from claims: %v", err)
			}
			return ErrInvalidAudience
		}

		found := false
		for _, aud := range audiences {
			if aud == v.audience {
				found = true
				break
			}
		}

		if !found {
			if DebugLoggingEnabled {
				debugLog("Invalid audience: expected=%s, got=%v", v.audience, audiences)
			}
			return ErrInvalidAudience
		}
		
		if DebugLoggingEnabled {
			debugLog("Audience validated: %v", audiences)
		}
	}

	// Validate the expiration time
	expirationTime, err := claims.GetExpirationTime()
	if err != nil || expirationTime == nil || expirationTime.Before(time.Now()) {
		if DebugLoggingEnabled {
			if err != nil {
				debugLog("Failed to get expiration time: %v", err)
			} else if expirationTime == nil {
				debugLog("Expiration time is nil")
			} else {
				debugLog("Token expired: expiration=%v, now=%v", expirationTime, time.Now())
			}
		}
		return ErrTokenExpired
	}
	
	if DebugLoggingEnabled {
		debugLog("Expiration time validated: %v", expirationTime)
	}

	return nil
}

// ValidateToken validates a JWT token.
func (v *JWTValidator) ValidateToken(ctx context.Context, tokenString string) (jwt.MapClaims, error) {
	if DebugLoggingEnabled {
		debugLog("Validating token: %s...", truncateToken(tokenString))
	}
	
	// Parse the token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return v.getKeyFromJWKS(ctx, token)
	})

	if err != nil {
		if DebugLoggingEnabled {
			debugLog("Failed to parse token: %v", err)
		}
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	// Check if the token is valid
	if !token.Valid {
		if DebugLoggingEnabled {
			debugLog("Token is invalid")
		}
		return nil, ErrInvalidToken
	}

	// Get the claims
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		if DebugLoggingEnabled {
			debugLog("Failed to get claims from token")
		}
		return nil, fmt.Errorf("failed to get claims from token")
	}
	
	if DebugLoggingEnabled {
		debugLog("Successfully extracted claims from token: %+v", claims)
	}

	// Validate the claims
	if err := v.validateClaims(claims); err != nil {
		if DebugLoggingEnabled {
			debugLog("Failed to validate claims: %v", err)
		}
		return nil, err
	}
	
	if DebugLoggingEnabled {
		debugLog("Claims validated successfully")
	}

	return claims, nil
}

// ClaimsContextKey is the key used to store claims in the request context.
type ClaimsContextKey struct{}

// Middleware creates an HTTP middleware that validates JWT tokens.
// truncateToken truncates a token for logging purposes
func truncateToken(token string) string {
	if len(token) <= 10 {
		return token
	}
	return token[:10] + "..."
}

func (v *JWTValidator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if DebugLoggingEnabled {
			debugLog("JWT middleware processing request: method=%s, path=%s", r.Method, r.URL.Path)
		}
		
		// Get the token from the Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			if DebugLoggingEnabled {
				debugLog("Authorization header missing")
			}
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		// Check if the Authorization header has the Bearer prefix
		if !strings.HasPrefix(authHeader, "Bearer ") {
			if DebugLoggingEnabled {
				debugLog("Invalid Authorization header format: %s", authHeader)
			}
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		// Extract the token
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")

		// Validate the token
		claims, err := v.ValidateToken(r.Context(), tokenString)
		if err != nil {
			if DebugLoggingEnabled {
				debugLog("Token validation failed: %v", err)
			}
			http.Error(w, fmt.Sprintf("Invalid token: %v", err), http.StatusUnauthorized)
			return
		}

		// Add the claims to the request context using a proper key type
		ctx := context.WithValue(r.Context(), ClaimsContextKey{}, claims)
		
		if DebugLoggingEnabled {
			debugLog("JWT validation successful, added claims to context")
		}
		
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
