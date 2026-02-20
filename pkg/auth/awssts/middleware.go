// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package awssts provides AWS STS token exchange with SigV4 signing support.
package awssts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/stacklok/toolhive/pkg/auth"
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
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*Middleware) Close() error {
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
		if targetURL.Scheme == "" || targetURL.Host == "" {
			return fmt.Errorf("target URL must include scheme and host (e.g., https://example.com)")
		}
	}

	// Create the middleware
	// TODO(jakub): MiddlewareFactory interface does not accept a context; pass context.TODO
	// because we don't really have a better option here.
	mw, err := newAWSStsMiddleware(context.TODO(), params.AWSStsConfig, targetURL)
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

	// Create the role mapper
	roleMapper, err := NewRoleMapper(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create role mapper: %w", err)
	}

	// Create the SigV4 signer
	signer, err := newRequestSigner(cfg.Region, withService(cfg.GetService()))
	if err != nil {
		return nil, fmt.Errorf("failed to create SigV4 signer: %w", err)
	}

	// Determine session name claim
	sessionNameClaim := cfg.SessionNameClaim
	if sessionNameClaim == "" {
		sessionNameClaim = defaultSessionNameClaim
	}

	// Get session duration
	sessionDuration := cfg.GetSessionDuration()

	// Create the middleware function
	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, roleMapper, signer, sessionNameClaim, sessionDuration, targetURL)

	return &Middleware{
		middleware: middlewareFunc,
		exchanger:  exchanger,
	}, nil
}

// createAWSStsMiddlewareFunc creates the HTTP middleware function.
// targetURL is the remote MCP server URL used for SigV4 signing.
// SigV4 requires signing with the actual target host, not the proxy host.
func createAWSStsMiddlewareFunc(
	exchanger *Exchanger,
	roleMapper *RoleMapper,
	signer *requestSigner,
	sessionNameClaim string,
	sessionDuration int32,
	targetURL *url.URL,
) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get identity from the auth middleware.
			// Unlike token exchange/upstream swap middleware, AWS STS requires valid
			// credentials and cannot fall through — every request must be signed.
			identity, ok := auth.IdentityFromContext(r.Context())
			if !ok {
				slog.Warn("No identity found in context, rejecting request")
				http.Error(w, "Authentication required", http.StatusUnauthorized)
				return
			}

			// Extract JWT claims from identity
			claims := identity.Claims
			if claims == nil {
				slog.Warn("No claims in identity, rejecting request")
				http.Error(w, "Authentication required", http.StatusUnauthorized)
				return
			}

			// Use RoleMapper to select the appropriate IAM role based on claims
			roleArn, err := roleMapper.SelectRole(claims)
			if err != nil {
				slog.Warn("Failed to select IAM role", "error", err)
				http.Error(w, "Failed to determine IAM role", http.StatusForbidden)
				return
			}

			//nolint:gosec // G706: roleArn is from server config, not user input
			slog.Debug("Selected IAM role", "role_arn", roleArn)

			// Extract bearer token from request
			bearerToken, err := auth.ExtractBearerToken(r)
			if err != nil {
				slog.Warn("No valid Bearer token found", "error", err)
				http.Error(w, "Bearer token required", http.StatusUnauthorized)
				return
			}

			// Extract and validate session name from claims
			sessionName, err := extractSessionName(claims, sessionNameClaim)
			if err != nil {
				slog.Warn("Failed to extract session name", "error", err)
				http.Error(w, "Missing session name claim", http.StatusUnauthorized)
				return
			}
			if err := ValidateSessionName(sessionName); err != nil {
				slog.Warn("Invalid session name from claim", "claim", sessionNameClaim, "error", err)
				//nolint:gosec // G706: logged for debugging invalid input
				slog.Debug("Invalid session name value", "session_name", sessionName)
				http.Error(w, "Invalid session name", http.StatusUnauthorized)
				return
			}

			//nolint:gosec // G706: session name is from validated JWT claims
			slog.Debug("Exchanging token for AWS credentials", "session", sessionName)

			// Exchange token for AWS credentials via STS
			creds, err := exchanger.ExchangeToken(r.Context(), bearerToken, roleArn, sessionName, sessionDuration)
			if err != nil {
				slog.Warn("STS token exchange failed", "error", err)
				http.Error(w, "AWS credential exchange failed", http.StatusUnauthorized)
				return
			}

			// Sign the request with SigV4 using a clone so we don't permanently
			// overwrite r.Host / r.URL.Host — that rewriting is the reverse
			// proxy's responsibility, not ours. We only add the SigV4 headers.
			if err := signRequestForTarget(r, signer, creds, targetURL); err != nil {
				slog.Warn("Failed to sign request with SigV4", "error", err)
				http.Error(w, "Request signing failed", http.StatusInternalServerError)
				return
			}

			slog.Debug("Request signed with AWS SigV4")

			next.ServeHTTP(w, r)
		})
	}
}

// signRequestForTarget signs the request with SigV4 for the given target host
// without permanently modifying r.Host or r.URL. When targetURL is non-nil, a
// clone is used for signing so that only the SigV4 headers are copied back to
// the original request; the reverse proxy's Director is left to handle host
// rewriting. When targetURL is nil the request is signed in-place.
func signRequestForTarget(r *http.Request, signer *requestSigner, creds *aws.Credentials, targetURL *url.URL) error {
	if targetURL == nil {
		return signer.SignRequest(r.Context(), r, creds)
	}

	// Buffer the body so both the signing clone and the original request
	// can read it. The SigV4 signer consumes the body to compute the
	// payload hash and then replaces it on the request it receives.
	// Because Clone() shares the same Body reader, we must buffer once
	// and provide fresh readers to each side.
	var bodyBytes []byte
	if r.Body != nil && r.Body != http.NoBody {
		var err error
		bodyBytes, err = io.ReadAll(io.LimitReader(r.Body, maxPayloadSize+1))
		if err != nil {
			return fmt.Errorf("failed to read request body for signing: %w", err)
		}
		if len(bodyBytes) > maxPayloadSize {
			return fmt.Errorf("request body exceeds maximum size of %d bytes", maxPayloadSize)
		}
		_ = r.Body.Close()
	}

	// Build a signing-only clone with the target host.
	signingReq := r.Clone(r.Context())
	signingReq.URL.Scheme = targetURL.Scheme
	signingReq.URL.Host = targetURL.Host
	signingReq.Host = targetURL.Host
	if bodyBytes != nil {
		signingReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		signingReq.ContentLength = int64(len(bodyBytes))
	}

	//nolint:gosec // G706: target host is from server configuration
	slog.Debug("Signing request for target host", "host", targetURL.Host)

	if err := signer.SignRequest(r.Context(), signingReq, creds); err != nil {
		return err
	}

	// Copy only the SigV4 headers back — these are the only headers the
	// AWS SDK v4 signer sets during SignHTTP.
	r.Header.Set("Authorization", signingReq.Header.Get("Authorization"))
	r.Header.Set("X-Amz-Date", signingReq.Header.Get("X-Amz-Date"))
	if tok := signingReq.Header.Get("X-Amz-Security-Token"); tok != "" {
		r.Header.Set("X-Amz-Security-Token", tok)
	}

	// Restore the body on the original request for downstream handlers
	// (the reverse proxy and tracingTransport both read it again).
	if bodyBytes != nil {
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
	}

	return nil
}

// extractSessionName extracts the session name from JWT claims.
// Returns an error if the configured claim is missing or empty, since a missing
// claim likely indicates a misconfiguration and would produce untraceable
// CloudTrail entries.
//
// The returned value is passed directly to AWS STS as RoleSessionName.
// and returns a clear error if the value doesn't conform.
//
// See: https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRoleWithWebIdentity.html
func extractSessionName(claims map[string]interface{}, claimName string) (string, error) {
	value, ok := claims[claimName]
	if !ok {
		return "", fmt.Errorf("claim %q not found in token", claimName)
	}
	strValue, ok := value.(string)
	if !ok || strValue == "" {
		return "", fmt.Errorf("claim %q is not a non-empty string", claimName)
	}
	return strValue, nil
}
