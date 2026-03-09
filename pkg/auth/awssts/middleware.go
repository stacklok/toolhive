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

const (
	MiddlewareType = "awssts"
)

const defaultSessionNameClaim = "sub"

type MiddlewareParams struct {
	AWSStsConfig *Config `json:"aws_sts_config,omitempty"`
	TargetURL    string  `json:"target_url,omitempty"`
}

type bearerErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

type Middleware struct {
	middleware types.MiddlewareFunction
	exchanger  *Exchanger
}

func (m *Middleware) Handler() types.MiddlewareFunction { return m.middleware }
func (*Middleware) Close() error                        { return nil }

func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal AWS STS middleware parameters: %w", err)
	}
	if params.AWSStsConfig == nil {
		return fmt.Errorf("AWS STS configuration is required but not provided")
	}
	if err := ValidateConfig(params.AWSStsConfig); err != nil {
		return fmt.Errorf("invalid AWS STS configuration: %w", err)
	}

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

	mw, err := newAWSStsMiddleware(context.TODO(), params.AWSStsConfig, targetURL)
	if err != nil {
		return fmt.Errorf("failed to create AWS STS middleware: %w", err)
	}
	runner.AddMiddleware(config.Type, mw)
	return nil
}

func newAWSStsMiddleware(ctx context.Context, cfg *Config, targetURL *url.URL) (*Middleware, error) {
	exchanger, err := NewExchanger(ctx, cfg.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to create STS exchanger: %w", err)
	}
	roleMapper, err := NewRoleMapper(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create role mapper: %w", err)
	}
	signer, err := newRequestSigner(cfg.Region, withService(cfg.GetService()))
	if err != nil {
		return nil, fmt.Errorf("failed to create SigV4 signer: %w", err)
	}

	sessionNameClaim := cfg.SessionNameClaim
	if sessionNameClaim == "" {
		sessionNameClaim = defaultSessionNameClaim
	}
	sessionDuration := cfg.GetSessionDuration()
	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, roleMapper, signer, sessionNameClaim, sessionDuration, targetURL)

	return &Middleware{middleware: middlewareFunc, exchanger: exchanger}, nil
}

func writeBearerUnauthorized(w http.ResponseWriter, errCode, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error=%q, error_description=%q`, errCode, description))
	w.WriteHeader(http.StatusUnauthorized)
	if err := json.NewEncoder(w).Encode(bearerErrorResponse{Error: errCode, ErrorDescription: description}); err != nil {
		slog.Warn("Failed to encode bearer error response", "error", err)
	}
}

func createAWSStsMiddlewareFunc(exchanger *Exchanger, roleMapper *RoleMapper, signer *requestSigner, sessionNameClaim string, sessionDuration int32, targetURL *url.URL) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := auth.IdentityFromContext(r.Context())
			if !ok {
				slog.Warn("No identity found in context, rejecting request")
				writeBearerUnauthorized(w, "invalid_token", "Authentication required")
				return
			}

			claims := identity.Claims
			if claims == nil {
				slog.Warn("No claims in identity, rejecting request")
				writeBearerUnauthorized(w, "invalid_token", "Authentication required")
				return
			}

			roleArn, err := roleMapper.SelectRole(claims)
			if err != nil {
				slog.Warn("Failed to select IAM role", "error", err)
				http.Error(w, "Failed to determine IAM role", http.StatusForbidden)
				return
			}
			slog.Debug("Selected IAM role", "role_arn", roleArn)

			bearerToken, err := auth.ExtractBearerToken(r)
			if err != nil {
				slog.Warn("No valid Bearer token found", "error", err)
				writeBearerUnauthorized(w, "invalid_request", "Bearer token required")
				return
			}

			sessionName, err := extractSessionName(claims, sessionNameClaim)
			if err != nil {
				slog.Warn("Failed to extract session name", "error", err)
				writeBearerUnauthorized(w, "invalid_token", "Missing session name claim")
				return
			}
			if err := ValidateSessionName(sessionName); err != nil {
				slog.Warn("Invalid session name from claim", "claim", sessionNameClaim, "error", err)
				slog.Debug("Invalid session name value", "session_name", sessionName)
				writeBearerUnauthorized(w, "invalid_token", "Invalid session name")
				return
			}

			slog.Debug("Exchanging token for AWS credentials", "session", sessionName)
			creds, err := exchanger.ExchangeToken(r.Context(), bearerToken, roleArn, sessionName, sessionDuration)
			if err != nil {
				slog.Warn("STS token exchange failed", "error", err)
				writeBearerUnauthorized(w, "invalid_token", "AWS credential exchange failed")
				return
			}

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

func signRequestForTarget(r *http.Request, signer *requestSigner, creds *aws.Credentials, targetURL *url.URL) error {
	if targetURL == nil {
		return signer.SignRequest(r.Context(), r, creds)
	}

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

	signingReq := r.Clone(r.Context())
	signingReq.URL.Scheme = targetURL.Scheme
	signingReq.URL.Host = targetURL.Host
	signingReq.Host = targetURL.Host
	if bodyBytes != nil {
		signingReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		signingReq.ContentLength = int64(len(bodyBytes))
	}

	slog.Debug("Signing request for target host", "host", targetURL.Host)
	if err := signer.SignRequest(r.Context(), signingReq, creds); err != nil {
		return err
	}

	r.Header.Set("Authorization", signingReq.Header.Get("Authorization"))
	r.Header.Set("X-Amz-Date", signingReq.Header.Get("X-Amz-Date"))
	if tok := signingReq.Header.Get("X-Amz-Security-Token"); tok != "" {
		r.Header.Set("X-Amz-Security-Token", tok)
	}

	if bodyBytes != nil {
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
	}
	return nil
}

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
