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
	"strings"
	"sync"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/obo"
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

	// logger receives the diagnostic log of exchange failures. nil means
	// slog.Default(), read at log time; tests set it to capture the output.
	logger *slog.Logger

	// buildExchanger constructs the cached Exchanger for a config. It is a field
	// so tests can inject a stub Exchanger instead of a real Entra HTTP client;
	// production uses defaultBuildOBOExchanger.
	buildExchanger func(cfg *authtypes.OBOConfig, clientSecret string) (exchange.Exchanger, error)

	// mu guards exchangers. Building an Exchanger does no I/O, so it is built
	// under the lock, which also keeps concurrent first requests to one build.
	mu sync.Mutex

	// exchangers caches the per-backend Exchanger keyed by oboExchangerCacheKey.
	// Each is shared across all users of that backend (the cache partitions per
	// user internally).
	exchangers map[string]oboExchangerEntry
}

// oboExchangerEntry is a cached Exchanger together with the client it belongs
// to (oboClientKey), so a rotated client secret can replace it.
type oboExchangerEntry struct {
	client    string
	exchanger exchange.Exchanger
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
		exchangers:     make(map[string]oboExchangerEntry),
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
// use under s.mu so the per-user token cache is shared across requests to the
// same backend.
func (s *OBOStrategy) getOrCreateExchanger(cfg *authtypes.OBOConfig, clientSecret string) (exchange.Exchanger, error) {
	key := oboExchangerCacheKey(cfg, clientSecret)

	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.exchangers[key]; ok {
		return entry.exchanger, nil
	}
	ex, err := s.buildExchanger(cfg, clientSecret)
	if err != nil {
		return nil, err
	}

	// A new secret for a client this strategy already serves is a rotation
	// (discovered mode re-reads the Secret): drop the Exchanger built with the
	// old secret, so it and its users' cached tokens do not stay in memory.
	// Two backends that share a client but use different secrets would replace
	// each other's Exchanger on every switch; that costs only fresh exchanges.
	client := oboClientKey(cfg)
	for k, entry := range s.exchangers {
		if entry.client == client {
			delete(s.exchangers, k)
		}
	}
	s.exchangers[key] = oboExchangerEntry{client: client, exchanger: ex}
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
		s.logExchangeFailure(ctx, slog.LevelDebug, err)
		return fmt.Errorf("obo: token exchange failed: %w", entra.ErrInvalidGrant)
	case errors.Is(err, entra.ErrInvalidClient):
		// A rejected client credential is the operator's problem, not the
		// caller's, so the message does not say so; the proxy middleware hides
		// it behind an opaque 502 for the same reason. errors.Is still matches.
		s.logExchangeFailure(ctx, slog.LevelWarn, err)
		return &opaqueExchangeError{sentinel: entra.ErrInvalidClient}
	case errors.Is(err, entra.ErrTransient):
		s.logExchangeFailure(ctx, slog.LevelWarn, err)
		return fmt.Errorf("obo: token exchange failed: %w", entra.ErrTransient)
	default:
		s.logExchangeFailure(ctx, slog.LevelWarn, err)
		return errors.New(oboExchangeFailedMessage)
	}
}

// oboExchangeFailedMessage is the generic client-facing exchange failure text.
const oboExchangeFailedMessage = "obo: token exchange failed"

// opaqueExchangeError reports a generic exchange failure to the client while
// still matching sentinel with errors.Is.
type opaqueExchangeError struct {
	sentinel error
}

func (*opaqueExchangeError) Error() string { return oboExchangeFailedMessage }

func (e *opaqueExchangeError) Unwrap() error { return e.sentinel }

// logExchangeFailure records an exchange failure in the diagnostic log. Only
// *entra.ExchangeError detail is logged: its Error() composes solely
// non-sensitive fields. Any other error is logged without its message so no
// Exchanger implementation can leak credential material here, matching the
// proxy middleware.
func (s *OBOStrategy) logExchangeFailure(ctx context.Context, level slog.Level, err error) {
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	var exErr *entra.ExchangeError
	if errors.As(err, &exErr) {
		logger.Log(ctx, level, "obo: token exchange failed", "error", exErr.Error())
		return
	}
	logger.Log(ctx, level, "obo: token exchange failed with an unclassified error")
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

// defaultBuildOBOExchanger constructs the production Exchanger through
// obo.NewCachedExchanger, the builder the proxy middleware also uses, so the
// same CRD value yields the same Exchanger and effective skew on both paths. A
// nil CacheSkewSeconds is passed as 0, which keeps the cache default.
func defaultBuildOBOExchanger(cfg *authtypes.OBOConfig, clientSecret string) (exchange.Exchanger, error) {
	skew := 0
	if cfg.CacheSkewSeconds != nil {
		skew = int(*cfg.CacheSkewSeconds)
	}
	return obo.NewCachedExchanger(cfg.TokenURL, cfg.ClientID, clientSecret, skew)
}

// parseOBOConfig validates the OBO configuration shape carried on the strategy. It
// does not resolve the client secret (see resolveClientSecret).
func parseOBOConfig(strategy *authtypes.BackendAuthStrategy) (*authtypes.OBOConfig, error) {
	if strategy == nil || strategy.OBO == nil {
		return nil, fmt.Errorf("OBO configuration is required")
	}
	cfg := strategy.OBO

	if strings.TrimSpace(cfg.TokenURL) == "" {
		return nil, fmt.Errorf("tokenUrl is required")
	}
	// The Exchanger is built on the first authenticated request, after the
	// health-check skip, so check the URL here or a relative or non-https
	// tokenUrl would leave the backend healthy while every call fails.
	if err := entra.ValidateTokenURL(cfg.TokenURL); err != nil {
		return nil, fmt.Errorf("invalid tokenUrl: %w", err)
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
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
// risk), and a rotated secret warms a fresh entry, which replaces the stale one
// (see getOrCreateExchanger). Hashing (rather than embedding the secret) mirrors how obo/cache keys the
// sensitive user token.
func oboExchangerCacheKey(cfg *authtypes.OBOConfig, clientSecret string) string {
	secretFP := sha256.Sum256([]byte(clientSecret))
	return oboClientKey(cfg) + "\x00" + hex.EncodeToString(secretFP[:])
}

// oboClientKey identifies the confidential client an Exchanger serves, without
// its secret: the token URL, client ID and cache skew. Exchangers that share it
// differ only in the secret, so a new one replaces the old.
func oboClientKey(cfg *authtypes.OBOConfig) string {
	skew := int32(0)
	if cfg.CacheSkewSeconds != nil {
		skew = *cfg.CacheSkewSeconds
	}
	return fmt.Sprintf("%s\x00%s\x00%d", cfg.TokenURL, cfg.ClientID, skew)
}
