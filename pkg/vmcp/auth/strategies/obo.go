// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/auth/obo/cache"
	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// oboAuditComponent identifies the vMCP OBO strategy as the source component
// of its audit records.
const oboAuditComponent = "toolhive-obo-vmcp"

// OBOStrategy is the vMCP outgoing-auth strategy for Microsoft Entra
// on-behalf-of (OBO). At request time it takes the OBO subject (assertion)
// token from the authenticated Identity the vMCP incoming-auth layer placed in
// the request context, exchanges it through a cached Entra Exchanger for a
// backend-scoped token, and sets Authorization: Bearer <backend token> on the
// outgoing request. The subject is the inbound token the client presented
// (Identity.Token, the default) or, when OBOConfig.SubjectTokenProviderName is
// set, that upstream provider's token (Identity.UpstreamTokens[name]).
//
// It is the vMCP counterpart of the proxy middleware in pkg/auth/obo: both
// derive the exchange target with exchange.Target, so identical config sends
// Entra the same scope and partitions the token cache the same way. On a
// conditional-access or claims challenge it fails fast: it records an audit
// event and returns an error without injecting a token.
//
// It caches one Entra Exchanger (fronted by the per-user token cache) per backend
// configuration, so the token cache survives across requests; per-user token
// caching is handled inside that cache. Safe for concurrent use.
type OBOStrategy struct {
	envReader   env.Reader
	auditLogger *slog.Logger

	// buildExchanger constructs the cached Exchanger for a config. It is a field
	// so tests can inject a stub Exchanger instead of a real Entra HTTP client;
	// production uses defaultBuildOBOExchanger.
	buildExchanger func(cfg *authtypes.OBOConfig, clientSecret string) (exchange.Exchanger, error)

	// mu guards exchangers.
	mu sync.RWMutex

	// exchangers caches the per-backend Exchanger keyed by oboExchangerCacheKey.
	// Each is shared across all users of that backend (the cache partitions per
	// user internally).
	exchangers map[string]exchange.Exchanger
}

// NewOBOStrategy returns a Strategy using the default Entra+cache Exchanger builder
// and the default JSON audit logger. envReader resolves a client secret supplied
// via ClientSecretEnv (the operator-managed config path); the discovered-auth
// path supplies the resolved secret on the config directly.
func NewOBOStrategy(envReader env.Reader) *OBOStrategy {
	return newOBOStrategy(envReader, nil, nil)
}

// newOBOStrategy is the test-friendly constructor. A nil auditLogger gets the
// default JSON audit logger; a nil build gets defaultBuildOBOExchanger.
func newOBOStrategy(
	envReader env.Reader,
	auditLogger *slog.Logger,
	build func(cfg *authtypes.OBOConfig, clientSecret string) (exchange.Exchanger, error),
) *OBOStrategy {
	if auditLogger == nil {
		auditLogger = audit.NewAuditLogger(nil)
	}
	if build == nil {
		build = defaultBuildOBOExchanger
	}
	return &OBOStrategy{
		envReader:      envReader,
		auditLogger:    auditLogger,
		buildExchanger: build,
		exchangers:     make(map[string]exchange.Exchanger),
	}
}

// Name returns the strategy identifier ("obo"), matching the dispatch key the
// converter and the registry use.
func (*OBOStrategy) Name() string { return authtypes.StrategyTypeOBO }

// Authenticate exchanges the request's user token for a backend token and injects
// it. For a health-check probe (no user identity) it skips authentication. It
// returns an error — injecting nothing — when the config is invalid, no identity
// or subject token is present, or the exchange fails; a conditional-access /
// claims challenge additionally emits an audit record.
func (s *OBOStrategy) Authenticate(
	ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
	// The config is validated before the health-check skip, as the token-exchange
	// strategy does: a misconfigured backend fails its health probe instead of
	// being probed unauthenticated and reported healthy.
	cfg, clientSecret, err := s.resolveConfig(strategy)
	if err != nil {
		return fmt.Errorf("obo: invalid strategy configuration: %w", err)
	}

	// Health probes carry no user identity, and OBO has no user to act on behalf
	// of, so skip authentication and let the backend be probed unauthenticated —
	// mirroring the token-exchange strategy's no-credentials health path.
	if healthcontext.IsHealthCheck(ctx) {
		return nil
	}

	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("obo: no identity found in context")
	}

	userToken, err := oboSubjectToken(identity, cfg.SubjectTokenProviderName)
	if err != nil {
		return err
	}

	exchanger, err := s.getOrCreateExchanger(cfg, clientSecret)
	if err != nil {
		return fmt.Errorf("obo: build exchanger: %w", err)
	}

	target := oboExchangeTarget(cfg)
	backendToken, err := exchanger.Exchange(ctx, userToken, target)
	if err != nil {
		return s.mapExchangeError(ctx, identity, target, err)
	}

	// Overwrite Authorization with the backend-scoped token so the raw subject
	// token never reaches the backend.
	req.Header.Set("Authorization", "Bearer "+backendToken)
	return nil
}

// Validate checks the strategy's OBO configuration shape and that a client secret
// is resolvable, failing fast at client-creation time rather than per request.
func (s *OBOStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
	_, _, err := s.resolveConfig(strategy)
	return err
}

// resolveConfig validates the OBO config and resolves the client secret. The
// secret comes from the resolved ClientSecret (discovered-auth path) or, failing
// that, from the environment variable named by ClientSecretEnv (operator-managed
// path). Mirrors the token-exchange strategy's parse step.
func (s *OBOStrategy) resolveConfig(strategy *authtypes.BackendAuthStrategy) (*authtypes.OBOConfig, string, error) {
	cfg, err := parseOBOConfig(strategy)
	if err != nil {
		return nil, "", err
	}
	secret, err := s.resolveClientSecret(cfg)
	if err != nil {
		return nil, "", err
	}
	return cfg, secret, nil
}

// resolveClientSecret returns the client secret from the config or its named
// environment variable. ClientSecret takes precedence; the mutual-exclusion of
// ClientSecret and ClientSecretEnv is enforced in parseOBOConfig.
func (s *OBOStrategy) resolveClientSecret(cfg *authtypes.OBOConfig) (string, error) {
	if cfg.ClientSecret != "" {
		return cfg.ClientSecret, nil
	}
	if cfg.ClientSecretEnv != "" {
		secret := s.envReader.Getenv(cfg.ClientSecretEnv)
		if secret == "" {
			return "", fmt.Errorf("environment variable %s is not set or empty", cfg.ClientSecretEnv)
		}
		return secret, nil
	}
	return "", fmt.Errorf("a client secret is required (clientSecret or clientSecretEnv)")
}

// getOrCreateExchanger returns the cached Exchanger for cfg, building it on first
// use under a double-checked lock so the per-user token cache is shared across
// requests to the same backend.
func (s *OBOStrategy) getOrCreateExchanger(cfg *authtypes.OBOConfig, clientSecret string) (exchange.Exchanger, error) {
	key := oboExchangerCacheKey(cfg, clientSecret)

	s.mu.RLock()
	if ex, ok := s.exchangers[key]; ok {
		s.mu.RUnlock()
		return ex, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if ex, ok := s.exchangers[key]; ok {
		return ex, nil
	}

	ex, err := s.buildExchanger(cfg, clientSecret)
	if err != nil {
		return nil, err
	}
	s.exchangers[key] = ex
	return ex, nil
}

// mapExchangeError translates an exchange failure into the error the Strategy
// returns. The returned error wraps only the matching entra sentinel (so
// errors.Is keeps working) behind a generic message: it reaches MCP clients via
// the tool error, so it must not carry Entra's error_description or any other
// provider detail. That detail is recorded server-side instead — in the audit
// record for a conditional-access / claims challenge, and in the diagnostic log
// otherwise. A cancelled context propagates as-is so it is not misreported as a
// backend failure.
func (s *OBOStrategy) mapExchangeError(
	ctx context.Context, identity *auth.Identity, target string, err error,
) error {
	switch {
	case errors.Is(err, entra.ErrInteractionRequired):
		// The audit record is built from the original error so it keeps the
		// ExchangeError's safe-to-log fields.
		s.auditInteractionRequired(ctx, identity, target, err)
		return fmt.Errorf("obo: token exchange denied: %w", entra.ErrInteractionRequired)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, entra.ErrInvalidGrant):
		// The end-user token was rejected (expired, malformed, or not consented):
		// a per-caller condition, so it is logged at DEBUG only.
		logExchangeFailure(ctx, slog.LevelDebug, err)
		return fmt.Errorf("obo: token exchange failed: %w", entra.ErrInvalidGrant)
	default:
		// invalid_client, a transient IdP failure, or an unclassified error.
		logExchangeFailure(ctx, slog.LevelWarn, err)
		for _, sentinel := range []error{entra.ErrInvalidClient, entra.ErrTransient} {
			if errors.Is(err, sentinel) {
				return fmt.Errorf("obo: token exchange failed: %w", sentinel)
			}
		}
		return errors.New("obo: token exchange failed")
	}
}

// logExchangeFailure records an exchange failure in the diagnostic log. Only
// *entra.ExchangeError detail is logged: its Error() composes solely
// non-sensitive fields. Any other error is logged without its message so no
// Exchanger implementation can leak credential material here, matching the
// proxy middleware.
func logExchangeFailure(ctx context.Context, level slog.Level, err error) {
	var exErr *entra.ExchangeError
	if errors.As(err, &exErr) {
		slog.Log(ctx, level, "obo: token exchange failed", "error", exErr.Error())
		return
	}
	slog.Log(ctx, level, "obo: token exchange failed with an unclassified error")
}

// auditInteractionRequired emits the audit record for a fail-fast
// conditional-access or claims challenge (see obo.NewInteractionRequiredAuditEvent
// for what it does and does not record).
//
// Unlike the proxy middleware, which runs on the inbound server request and
// records the client IP as a network source, this strategy runs on the
// backend-bound request, which carries no inbound client address. The source is
// therefore recorded as local to this component; the denied principal is carried
// by the subjects map.
func (s *OBOStrategy) auditInteractionRequired(
	ctx context.Context, identity *auth.Identity, target string, err error,
) {
	event := obo.NewInteractionRequiredAuditEvent(
		audit.EventSource{Type: audit.SourceTypeLocal, Value: oboAuditComponent},
		oboAuditComponent, identity, target, err)
	event.LogTo(ctx, s.auditLogger, audit.LevelAudit)
}

// defaultBuildOBOExchanger constructs the production Exchanger: a cached Entra
// on-behalf-of Exchanger built from the config's token URL, client ID, and
// resolved client secret.
//
// A skew override is applied only when strictly positive. A resolved 0 (whether
// configured as "0s" or truncated down from a sub-second value) leaves the cache's
// default skew in place — matching the proxy-runtime middleware, whose gate is
// also `> 0` and whose contract field is a non-pointer int that cannot distinguish
// an explicit 0 from "unset". Keeping the same gate here preserves cross-surface
// parity: the same CRD value yields the same effective skew on both paths.
func defaultBuildOBOExchanger(cfg *authtypes.OBOConfig, clientSecret string) (exchange.Exchanger, error) {
	exchanger, err := entra.NewFromTokenURL(cfg.TokenURL, cfg.ClientID, clientSecret, nil)
	if err != nil {
		return nil, err
	}

	var opts []cache.Option
	if cfg.CacheSkewSeconds != nil && *cfg.CacheSkewSeconds > 0 {
		opts = append(opts, cache.WithSkew(time.Duration(*cfg.CacheSkewSeconds)*time.Second))
	}
	return cache.New(exchanger, opts...), nil
}

// parseOBOConfig validates the OBO configuration shape carried on the strategy. It
// does not resolve the client secret (see resolveClientSecret).
func parseOBOConfig(strategy *authtypes.BackendAuthStrategy) (*authtypes.OBOConfig, error) {
	if strategy == nil || strategy.OBO == nil {
		return nil, fmt.Errorf("OBO configuration is required")
	}
	cfg := strategy.OBO

	if cfg.TokenURL == "" {
		return nil, fmt.Errorf("tokenUrl is required")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("clientId is required")
	}
	if cfg.ClientSecret != "" && cfg.ClientSecretEnv != "" {
		return nil, fmt.Errorf("clientSecret and clientSecretEnv are mutually exclusive")
	}
	if oboExchangeTarget(cfg) == "" {
		return nil, fmt.Errorf("one of audience or scopes is required")
	}
	if cfg.CacheSkewSeconds != nil && *cfg.CacheSkewSeconds < 0 {
		return nil, fmt.Errorf("cacheSkewSeconds must not be negative")
	}
	return cfg, nil
}

// oboExchangeTarget collapses the config's audience/scopes with
// exchange.Target, the same rule the proxy middleware uses, so identical config
// sends Entra the same scope and uses the same cache partition key.
func oboExchangeTarget(cfg *authtypes.OBOConfig) string {
	return exchange.Target(cfg.Audience, cfg.Scopes)
}

// oboSubjectToken resolves the OBO subject (assertion) token from the authenticated
// Identity. With an empty providerName it uses the inbound token the client
// presented (Identity.Token); with a providerName set it uses that upstream
// provider's token (Identity.UpstreamTokens[name]).
func oboSubjectToken(identity *auth.Identity, providerName string) (string, error) {
	if providerName != "" {
		token := identity.UpstreamTokens[providerName] // nil map read is safe
		if token == "" {
			return "", fmt.Errorf("obo: provider %q: %w", providerName, authtypes.ErrUpstreamTokenNotFound)
		}
		return token, nil
	}
	if identity.Token == "" {
		return "", fmt.Errorf("obo: identity has no token")
	}
	return identity.Token, nil
}

// oboExchangerCacheKey identifies the per-backend Exchanger by the fields that
// determine it and its cache: token URL, client ID, cache skew, and a fingerprint
// of the client secret. The secret is included as a SHA-256 hex digest — never
// raw — so two configs sharing a (URL, clientID) but differing in secret cannot
// silently share the first-cached Exchanger (a cross-config credential-confusion
// risk), and a rotated secret warms a fresh entry rather than reusing the stale
// one. Hashing (rather than embedding the secret) mirrors how obo/cache keys the
// sensitive user token.
func oboExchangerCacheKey(cfg *authtypes.OBOConfig, clientSecret string) string {
	skew := int32(0)
	if cfg.CacheSkewSeconds != nil {
		skew = *cfg.CacheSkewSeconds
	}
	secretFP := sha256.Sum256([]byte(clientSecret))
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s", cfg.TokenURL, cfg.ClientID, skew, hex.EncodeToString(secretFP[:]))
}
