// Package awssts provides AWS STS token exchange with SigV4 signing support.
package awssts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Middleware type constant
const (
	MiddlewareType = "awssts"
)

// Default session name claim when not specified in config.
const defaultSessionNameClaim = "sub"

// MiddlewareParams represents the parameters for AWS STS middleware.
type MiddlewareParams struct {
	AWSStsConfig *Config `json:"aws_sts_config,omitempty"`
	// TargetURL is the remote MCP server URL for SigV4 signing.
	// The request must be signed with the target host, not the proxy host.
	TargetURL string `json:"target_url,omitempty"`
}

// Middleware wraps AWS STS middleware functionality.
type Middleware struct {
	middleware types.MiddlewareFunction
	exchanger  *Exchanger
	cache      *CredentialCache
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (m *Middleware) Close() error {
	// Clear the credential cache on shutdown
	if m.cache != nil {
		m.cache.Clear()
	}
	return nil
}

// CreateMiddleware is the factory function for AWS STS middleware.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal AWS STS middleware parameters: %w", err)
	}

	// AWS STS config is required when this middleware type is specified
	if params.AWSStsConfig == nil {
		return fmt.Errorf("AWS STS configuration is required but not provided")
	}

	// Validate configuration at startup
	if err := ValidateConfig(params.AWSStsConfig); err != nil {
		return fmt.Errorf("invalid AWS STS configuration: %w", err)
	}

	// Parse and validate target URL if provided
	var targetURL *url.URL
	if params.TargetURL != "" {
		var err error
		targetURL, err = url.Parse(params.TargetURL)
		if err != nil {
			return fmt.Errorf("invalid target URL: %w", err)
		}
	}

	// Create the middleware
	mw, err := newAWSStsMiddleware(context.Background(), params.AWSStsConfig, targetURL)
	if err != nil {
		return fmt.Errorf("failed to create AWS STS middleware: %w", err)
	}

	// Add middleware to runner
	runner.AddMiddleware(config.Type, mw)

	return nil
}

// newAWSStsMiddleware creates a new AWS STS middleware with all required components.
// targetURL is the remote MCP server URL used for SigV4 signing (can be nil if not proxying).
func newAWSStsMiddleware(ctx context.Context, cfg *Config, targetURL *url.URL) (*Middleware, error) {
	// Create the STS exchanger with regional endpoint
	exchanger, err := NewExchanger(ctx, cfg.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to create STS exchanger: %w", err)
	}

	// Create the credential cache
	cache := NewCredentialCache(DefaultCacheSize)

	// Create the role mapper
	roleMapper := NewRoleMapper(cfg)

	// Create the SigV4 signer
	signer := NewSigner(cfg.Region, cfg.GetService())

	// Determine session name claim
	sessionNameClaim := cfg.SessionNameClaim
	if sessionNameClaim == "" {
		sessionNameClaim = defaultSessionNameClaim
	}

	// Get session duration
	sessionDuration := cfg.GetSessionDuration()

	// Create the middleware function
	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, cache, roleMapper, signer, sessionNameClaim, sessionDuration, targetURL)

	return &Middleware{
		middleware: middlewareFunc,
		exchanger:  exchanger,
		cache:      cache,
	}, nil
}

// createAWSStsMiddlewareFunc creates the HTTP middleware function.
// targetURL is the remote MCP server URL used for SigV4 signing.
// SigV4 requires signing with the actual target host, not the proxy host.
func createAWSStsMiddlewareFunc(
	exchanger *Exchanger,
	cache *CredentialCache,
	roleMapper *RoleMapper,
	signer *Signer,
	sessionNameClaim string,
	sessionDuration int32,
	targetURL *url.URL,
) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get identity from the auth middleware
			identity, ok := auth.IdentityFromContext(r.Context())
			if !ok {
				logger.Debug("No identity found in context, proceeding without AWS STS signing")
				next.ServeHTTP(w, r)
				return
			}

			// Extract JWT claims from identity
			claims := identity.Claims
			logger.Debugf("AWS STS middleware received claims: %+v", claims)
			if claims == nil {
				logger.Debug("No claims in identity, proceeding without AWS STS signing")
				next.ServeHTTP(w, r)
				return
			}

			// Use RoleMapper to select the appropriate IAM role based on claims
			roleArn, err := roleMapper.SelectRole(claims)
			if err != nil {
				logger.Warnf("Failed to select IAM role: %v", err)
				http.Error(w, "Failed to determine IAM role", http.StatusForbidden)
				return
			}

			logger.Debugf("Selected IAM role: %s", roleArn)

			// Extract bearer token from request
			bearerToken, err := auth.ExtractBearerToken(r)
			if err != nil {
				logger.Debugf("No valid Bearer token found (%v), proceeding without AWS STS signing", err)
				next.ServeHTTP(w, r)
				return
			}

			// Check credential cache first
			creds := cache.Get(roleArn, bearerToken)
			if creds != nil {
				logger.Debug("Using cached AWS credentials")
			} else {
				// Extract session name from claims
				sessionName := extractSessionName(claims, sessionNameClaim)

				logger.Debugf("Exchanging token for AWS credentials (session: %s)", sessionName)

				// Exchange token for AWS credentials via STS
				creds, err = exchanger.ExchangeToken(r.Context(), bearerToken, roleArn, sessionName, sessionDuration)
				if err != nil {
					logger.Warnf("STS token exchange failed: %v", err)
					http.Error(w, "AWS credential exchange failed", http.StatusUnauthorized)
					return
				}

				// Cache the credentials
				cache.Set(roleArn, bearerToken, creds)
				logger.Debug("Cached new AWS credentials")
			}

			// Set the request URL and Host to the target before signing.
			// SigV4 requires the signature to match the actual target host.
			// The reverse proxy will handle the actual URL rewriting, but we need
			// to sign with the target host to generate a valid signature.
			if targetURL != nil {
				r.URL.Scheme = targetURL.Scheme
				r.URL.Host = targetURL.Host
				r.Host = targetURL.Host
				logger.Debugf("Set request URL to target for SigV4 signing: %s", targetURL.Host)
			}

			// Sign the request with SigV4
			// Note: SigV4 signing must be the last middleware before sending the request
			if err := signer.SignRequest(r.Context(), r, creds); err != nil {
				logger.Warnf("Failed to sign request with SigV4: %v", err)
				http.Error(w, "Request signing failed", http.StatusInternalServerError)
				return
			}

			logger.Debug("Request signed with AWS SigV4")

			next.ServeHTTP(w, r)
		})
	}
}

// extractSessionName extracts the session name from JWT claims.
// Falls back to "toolhive-session" if the claim is not found.
func extractSessionName(claims map[string]interface{}, claimName string) string {
	if value, ok := claims[claimName]; ok {
		if strValue, ok := value.(string); ok && strValue != "" {
			// Sanitize session name for AWS (max 64 chars, alphanumeric + =,.@-)
			return sanitizeSessionName(strValue)
		}
	}
	return "toolhive-session"
}

// sanitizeSessionName ensures the session name conforms to AWS requirements.
// AWS allows: alphanumeric characters, plus (+), equals (=), comma (,),
// period (.), at (@), underscore (_), and hyphen (-).
// Maximum length is 64 characters.
func sanitizeSessionName(name string) string {
	// Convert to runes for proper character handling
	result := make([]rune, 0, len(name))
	for _, r := range name {
		if isAllowedSessionNameChar(r) {
			result = append(result, r)
		} else {
			// Replace disallowed characters with underscore
			result = append(result, '_')
		}
	}

	// Truncate to 64 characters
	if len(result) > 64 {
		result = result[:64]
	}

	// Ensure non-empty
	if len(result) == 0 {
		return "toolhive-session"
	}

	return string(result)
}

// isAllowedSessionNameChar returns true if the character is allowed in AWS session names.
func isAllowedSessionNameChar(r rune) bool {
	// Alphanumeric
	if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
		return true
	}
	// Special allowed characters: + = , . @ _ -
	switch r {
	case '+', '=', ',', '.', '@', '_', '-':
		return true
	}
	return false
}
