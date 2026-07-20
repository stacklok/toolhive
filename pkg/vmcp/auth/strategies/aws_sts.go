// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// awsStsContext holds the per-config roleMapper, exchanger, signer, and session duration.
type awsStsContext struct {
	roleMapper      *awssts.RoleMapper
	exchanger       *awssts.Exchanger
	signer          *awssts.RequestSigner
	sessionDuration int32
}

// AwsStsStrategy authenticates backend requests using AWS STS token exchange and SigV4 signing.
//
// For each authenticated request, the strategy:
//  1. Extracts the bearer token and JWT claims from the incoming identity
//  2. Selects the appropriate IAM role using a CEL-based role mapper
//  3. Exchanges the identity token for temporary AWS credentials via AssumeRoleWithWebIdentity
//  4. Signs the outgoing request with SigV4 using the temporary credentials
//
// Required configuration fields (in BackendAuthStrategy.AwsSts):
//   - Region: AWS region for STS endpoint and SigV4 signing
//
// At least one of the following must also be configured:
//   - FallbackRoleArn: IAM role ARN to assume when no role mappings match
//   - RoleMappings: CEL-based rules for mapping JWT claims to IAM roles
//
// This strategy is appropriate when:
//   - The backend is an AWS-managed MCP server requiring SigV4 authentication
//   - Role selection should be derived from the incoming caller's JWT claims
//
// The strategy is safe for concurrent use. It maintains a per-config cache of
// roleMapper and exchanger instances, keyed by a SHA-256 hash over all fields of
// the AwsStsConfig (region, service, role mappings including Claim/Matcher/Priority,
// fallback ARN, and session claims). Cache entries are created on first use (via
// Validate or Authenticate) and shared across all requests with the same configuration.
type AwsStsStrategy struct {
	mu     sync.RWMutex
	cached map[string]*awsStsContext
}

// NewAwsStsStrategy creates a new AwsStsStrategy instance.
func NewAwsStsStrategy() *AwsStsStrategy {
	return &AwsStsStrategy{
		cached: make(map[string]*awsStsContext),
	}
}

// Name returns the strategy identifier.
func (*AwsStsStrategy) Name() string {
	return authtypes.StrategyTypeAwsSts
}

// Authenticate performs AWS STS token exchange and SigV4 signing for the request.
//
// This method:
//  1. Skips authentication for health check requests (no user identity to use)
//  2. Builds an awssts.Config from the strategy configuration
//  3. Delegates to authenticateWithConfig to perform the STS exchange and signing
//
// Parameters:
//   - ctx: Request context containing the authenticated identity (or health check marker)
//   - req: The HTTP request to authenticate; modified in place with SigV4 headers
//   - strategy: Backend auth strategy containing AwsSts configuration
//
// Returns an error if:
//   - The AwsSts configuration is nil or missing a required field
//   - No identity is found in the context
//   - Role selection fails (no matching mapping and no fallback)
//   - The STS exchange fails
//   - SigV4 signing fails
func (s *AwsStsStrategy) Authenticate(
	ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
	// Health checks have no user identity — skip authentication.
	if healthcontext.IsHealthCheck(ctx) {
		return nil
	}

	if strategy == nil || strategy.AwsSts == nil {
		return fmt.Errorf("aws_sts configuration required")
	}

	cfg := toAwsStsConfig(strategy.AwsSts)
	stsCtx, err := s.getOrCreateContext(ctx, cfg)
	if err != nil {
		return err
	}

	return authenticateWithCached(ctx, req, cfg, stsCtx)
}

// authenticateWithCached performs the STS token exchange and SigV4 signing
// for an outgoing request using pre-built components from awsStsContext.
func authenticateWithCached(
	ctx context.Context,
	req *http.Request,
	cfg *awssts.Config,
	stsCtx *awsStsContext,
) error {
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("no identity found in context")
	}

	if identity.Claims == nil {
		return fmt.Errorf("no claims in identity")
	}

	roleArn, err := selectRole(stsCtx.roleMapper, identity.Claims)
	if err != nil {
		return err
	}

	var bearerToken string
	if cfg.SubjectProviderName != "" {
		bearerToken = identity.UpstreamTokens[cfg.SubjectProviderName] // nil map safe in Go
		if bearerToken == "" {
			return fmt.Errorf("provider %q: %w", cfg.SubjectProviderName, authtypes.ErrUpstreamTokenNotFound)
		}
	} else {
		// Fall back to the original incoming token captured in the identity.
		// req is the outgoing backend request being constructed and is not
		// guaranteed to carry the caller's Authorization header.
		if identity.Token == "" {
			return fmt.Errorf("identity has no token")
		}
		slog.Debug("aws_sts: SubjectProviderName empty, falling back to identity.Token")
		bearerToken = identity.Token
	}

	sessionName, err := resolveSessionName(cfg, identity.Claims)
	if err != nil {
		return err
	}

	creds, err := stsCtx.exchanger.ExchangeToken(ctx, bearerToken, roleArn, sessionName, stsCtx.sessionDuration)
	if err != nil {
		return fmt.Errorf("STS token exchange failed: %w", err)
	}

	if err := stsCtx.signer.SignRequest(ctx, req, creds); err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	return nil
}

// selectRole uses the provided role mapper to return the IAM role ARN for the given claims.
func selectRole(roleMapper *awssts.RoleMapper, claims map[string]any) (string, error) {
	roleArn, err := roleMapper.SelectRole(claims)
	if err != nil {
		return "", fmt.Errorf("failed to select IAM role: %w", err)
	}
	return roleArn, nil
}

// resolveSessionName extracts and validates the STS session name from JWT claims.
func resolveSessionName(cfg *awssts.Config, claims map[string]any) (string, error) {
	claimKey := cfg.SessionNameClaim
	if claimKey == "" {
		claimKey = "sub"
	}
	sessionName, err := awssts.ExtractSessionName(claims, claimKey)
	if err != nil {
		return "", fmt.Errorf("failed to extract session name: %w", err)
	}
	if err := awssts.ValidateSessionName(sessionName); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	return sessionName, nil
}

// Validate checks if the required strategy configuration fields are present and valid,
// and warms the per-config cache entry for this backend.
//
// This method verifies that:
//   - The AwsSts configuration block is present
//   - Region is non-empty (required for STS endpoint and SigV4 signing)
//   - The configuration is structurally valid (delegates to awssts.ValidateConfig)
func (s *AwsStsStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
	if strategy == nil || strategy.AwsSts == nil {
		return fmt.Errorf("aws_sts configuration required")
	}

	if strategy.AwsSts.Region == "" {
		return fmt.Errorf("region required in aws_sts configuration")
	}

	cfg := toAwsStsConfig(strategy.AwsSts)
	if err := awssts.ValidateConfig(cfg); err != nil {
		return err
	}

	_, err := s.getOrCreateContext(context.Background(), cfg)
	return err
}

// getOrCreateContext retrieves or creates a cached awsStsContext for the given config.
//
// Thread-safe: uses double-checked locking so that concurrent callers with the
// same config key build the roleMapper/exchanger/signer only once.
// ValidateConfig is called on cache miss to ensure structurally invalid configs
// are rejected even when Authenticate is called without prior Validate.
func (s *AwsStsStrategy) getOrCreateContext(ctx context.Context, cfg *awssts.Config) (*awsStsContext, error) {
	cacheKey := buildAwsStsCacheKey(cfg)

	// Fast path: read lock.
	s.mu.RLock()
	if cached, exists := s.cached[cacheKey]; exists {
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	// Slow path: write lock.
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check in case another goroutine created it.
	if cached, exists := s.cached[cacheKey]; exists {
		return cached, nil
	}

	if err := awssts.ValidateConfig(cfg); err != nil {
		return nil, err
	}

	roleMapper, err := awssts.NewRoleMapper(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build role mapper: %w", err)
	}

	exchanger, err := awssts.NewExchanger(ctx, cfg.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to build STS exchanger: %w", err)
	}

	signer, err := awssts.NewRequestSigner(cfg.Region, cfg.GetService())
	if err != nil {
		return nil, fmt.Errorf("failed to build request signer: %w", err)
	}

	entry := &awsStsContext{
		roleMapper:      roleMapper,
		exchanger:       exchanger,
		signer:          signer,
		sessionDuration: cfg.GetSessionDuration(),
	}
	s.cached[cacheKey] = entry
	return entry, nil
}

// buildAwsStsCacheKey computes a SHA-256 hash over all fields that differentiate
// backend configurations: Region, Service, FallbackRoleArn, every RoleMapping's
// Claim/Matcher/RoleArn/Priority (sorted by RoleArn for stability), RoleClaim,
// SessionNameClaim, SubjectProviderName, and the resolved SessionDuration.
// SessionDuration is included because it is baked into the cached awsStsContext;
// omitting it would let two backends differing only in session duration share a
// cache entry and silently use the wrong value. Using a hash avoids structural
// ambiguity from colons embedded in ARN strings and ensures configs that share
// role ARNs but differ in matching logic (Claim, Matcher) produce distinct keys.
func buildAwsStsCacheKey(cfg *awssts.Config) string {
	// Sort role mappings by RoleArn for a stable ordering. Use a stable sort so
	// that mappings sharing a RoleArn but differing in Claim or Matcher keep
	// their input order — otherwise logically identical configs could hash to
	// different keys across calls and cause spurious cache misses.
	mappings := make([]awssts.RoleMapping, len(cfg.RoleMappings))
	copy(mappings, cfg.RoleMappings)
	slices.SortStableFunc(mappings, func(a, b awssts.RoleMapping) int {
		return cmp.Compare(a.RoleArn, b.RoleArn)
	})

	var sb strings.Builder
	sb.WriteString(cfg.Region)
	sb.WriteByte(0)
	sb.WriteString(cfg.Service)
	sb.WriteByte(0)
	sb.WriteString(cfg.FallbackRoleArn)
	sb.WriteByte(0)
	sb.WriteString(cfg.RoleClaim)
	sb.WriteByte(0)
	sb.WriteString(cfg.SessionNameClaim)
	sb.WriteByte(0)
	sb.WriteString(cfg.SubjectProviderName)
	sb.WriteByte(0)
	fmt.Fprintf(&sb, "%d", cfg.GetSessionDuration())
	sb.WriteByte(0)
	for _, rm := range mappings {
		sb.WriteString(rm.RoleArn)
		sb.WriteByte(0)
		sb.WriteString(rm.Claim)
		sb.WriteByte(0)
		sb.WriteString(rm.Matcher)
		sb.WriteByte(0)
		if rm.Priority != nil {
			fmt.Fprintf(&sb, "%d", *rm.Priority)
		}
		sb.WriteByte(0)
	}

	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// toAwsStsConfig converts an authtypes.AwsStsConfig to an awssts.Config.
// The two types mirror each other; this function bridges the vmcp types
// package (which must remain a leaf with no awssts dependency) to the
// awssts implementation package.
func toAwsStsConfig(in *authtypes.AwsStsConfig) *awssts.Config {
	cfg := &awssts.Config{
		Region:              in.Region,
		Service:             in.Service,
		FallbackRoleArn:     in.FallbackRoleArn,
		RoleClaim:           in.RoleClaim,
		SessionNameClaim:    in.SessionNameClaim,
		SubjectProviderName: in.SubjectProviderName,
	}

	if in.SessionDuration != nil {
		cfg.SessionDuration = *in.SessionDuration
	}

	if len(in.RoleMappings) > 0 {
		cfg.RoleMappings = make([]awssts.RoleMapping, len(in.RoleMappings))
		for i, rm := range in.RoleMappings {
			cfg.RoleMappings[i] = awssts.RoleMapping{
				Claim:    rm.Claim,
				Matcher:  rm.Matcher,
				RoleArn:  rm.RoleArn,
				Priority: rm.Priority,
			}
		}
	}

	return cfg
}
