// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/auth/upstreamswap"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/bodylimit"
	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/oauthproto/tokenexchange"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/recovery"
	"github.com/stacklok/toolhive/pkg/telemetry"
	headerfwd "github.com/stacklok/toolhive/pkg/transport/middleware"
	"github.com/stacklok/toolhive/pkg/transport/middleware/origin"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/usagemetrics"
	"github.com/stacklok/toolhive/pkg/webhook/mutating"
	"github.com/stacklok/toolhive/pkg/webhook/validating"
)

// GetSupportedMiddlewareFactories returns a map of supported middleware types to their factory functions
func GetSupportedMiddlewareFactories() map[string]types.MiddlewareFactory {
	return map[string]types.MiddlewareFactory{
		auth.MiddlewareType:                   auth.CreateMiddleware,
		tokenexchange.MiddlewareType:          tokenexchange.CreateMiddleware,
		upstreamswap.MiddlewareType:           upstreamswap.CreateMiddleware,
		awssts.MiddlewareType:                 awssts.CreateMiddleware,
		obo.MiddlewareType:                    obo.CreateMiddleware,
		bodylimit.MiddlewareType:              bodylimit.CreateMiddleware,
		mcp.ParserMiddlewareType:              mcp.CreateParserMiddleware,
		mcp.ToolFilterMiddlewareType:          mcp.CreateToolFilterMiddleware,
		mcp.ToolCallFilterMiddlewareType:      mcp.CreateToolCallFilterMiddleware,
		ratelimit.MiddlewareType:              ratelimit.CreateMiddleware,
		usagemetrics.MiddlewareType:           usagemetrics.CreateMiddleware,
		telemetry.MiddlewareType:              telemetry.CreateMiddleware,
		authz.MiddlewareType:                  authz.CreateMiddleware,
		audit.MiddlewareType:                  audit.CreateMiddleware,
		recovery.MiddlewareType:               recovery.CreateMiddleware,
		headerfwd.HeaderForwardMiddlewareName: headerfwd.CreateMiddleware,
		headerfwd.StripAuthMiddlewareName:     headerfwd.CreateStripAuthMiddleware,
		origin.MiddlewareType:                 origin.CreateMiddleware,
		validating.MiddlewareType:             validating.CreateMiddleware,
		mutating.MiddlewareType:               mutating.CreateMiddleware,
	}
}

// PopulateMiddlewareConfigs populates the MiddlewareConfigs slice based on the RunConfig settings
// This function serves as a bridge between the old configuration style and the new generic middleware system
//
//nolint:gocyclo // Function complexity is acceptable for middleware configuration
func PopulateMiddlewareConfigs(config *RunConfig) error {
	var middlewareConfigs []types.MiddlewareConfig
	// TODO: Consider extracting other middleware setup into helper functions like addUsageMetricsMiddleware
	//
	// NOTE: Origin-validation middleware is intentionally NOT added here. It is
	// wired centrally in runner.Run (via prependOriginMiddleware) for both the
	// operator/proxyrunner path (this function) and the CLI path
	// (WithMiddlewareFromFlags), because that is the only place where the
	// effective Host/Port/AllowedOrigins are fully resolved.

	// Body size limit middleware (always present, outermost). See addBodyLimitMiddleware.
	middlewareConfigs, err := addBodyLimitMiddleware(middlewareConfigs)
	if err != nil {
		return err
	}

	// Authentication middleware (always present)
	authParams := auth.MiddlewareParams{
		OIDCConfig: config.OIDCConfig,
	}
	authConfig, authErr := types.NewMiddlewareConfig(auth.MiddlewareType, authParams)
	if authErr != nil {
		return fmt.Errorf("failed to create auth middleware config: %w", authErr)
	}
	middlewareConfigs = append(middlewareConfigs, *authConfig)

	// Upstream swap middleware (if embedded auth server is configured)
	// This exchanges ToolHive JWTs for upstream IdP tokens when embedded auth server is used.
	// IMPORTANT: Must run BEFORE token exchange middleware so it can read the `tsid` claim
	// from the original ToolHive JWT before any token modification occurs.
	middlewareConfigs, err = addUpstreamSwapMiddleware(middlewareConfigs, config)
	if err != nil {
		return err
	}

	// Token exchange middleware (if configured)
	// Runs after upstream swap so that if both are configured, upstream swap can first
	// inject the upstream IdP token, then token exchange can further transform it if needed.
	middlewareConfigs, err = addTokenExchangeMiddleware(middlewareConfigs, config.TokenExchangeConfig)
	if err != nil {
		return err
	}

	// Tools filter and override middleware (if enabled)
	if len(config.ToolsFilter) > 0 || len(config.ToolsOverride) > 0 {
		// Prepare overrides map (convert runner.ToolOverride -> mcp.ToolOverride)
		overrides := make(map[string]mcp.ToolOverride)
		for actualName, tool := range config.ToolsOverride {
			overrides[actualName] = mcp.ToolOverride{
				Name:        tool.Name,
				Description: tool.Description,
			}
		}

		// Add tool filter middleware with both filter and overrides
		toolFilterParams := mcp.ToolFilterMiddlewareParams{
			FilterTools:   config.ToolsFilter,
			ToolsOverride: overrides,
		}
		toolFilterConfig, err := types.NewMiddlewareConfig(mcp.ToolFilterMiddlewareType, toolFilterParams)
		if err != nil {
			return fmt.Errorf("failed to create tool filter middleware config: %w", err)
		}
		middlewareConfigs = append(middlewareConfigs, *toolFilterConfig)

		// Add tool call filter middleware with same params
		toolCallFilterConfig, err := types.NewMiddlewareConfig(mcp.ToolCallFilterMiddlewareType, toolFilterParams)
		if err != nil {
			return fmt.Errorf("failed to create tool call filter middleware config: %w", err)
		}
		middlewareConfigs = append(middlewareConfigs, *toolCallFilterConfig)
	}

	// MCP Parser middleware (always present)
	mcpParserParams := mcp.ParserMiddlewareParams{}
	mcpParserConfig, err := types.NewMiddlewareConfig(mcp.ParserMiddlewareType, mcpParserParams)
	if err != nil {
		return fmt.Errorf("failed to create MCP parser middleware config: %w", err)
	}
	middlewareConfigs = append(middlewareConfigs, *mcpParserConfig)

	// Rate limit middleware (if configured)
	// Positioned after MCP parser (needs tool name from context).
	// Will also need user identity from auth when per-user limits are added (#4550).
	middlewareConfigs, err = addRateLimitMiddleware(middlewareConfigs, config)
	if err != nil {
		return err
	}

	// Mutating Webhooks middleware (if configured).
	// Must run BEFORE validating webhooks:
	// MCP Parser -> [Mutating Webhooks] -> [Validating Webhooks] -> Audit -> Authz
	middlewareConfigs, err = addMutatingWebhookMiddleware(middlewareConfigs, config)
	if err != nil {
		return err
	}

	// Validating Webhooks middleware (if configured)
	middlewareConfigs, err = addValidatingWebhookMiddleware(middlewareConfigs, config)
	if err != nil {
		return err
	}

	// Load application config for global settings
	configProvider := cfg.NewDefaultProvider()
	appConfig := configProvider.GetConfig()

	// Usage metrics middleware (if enabled)
	middlewareConfigs, err = addUsageMetricsMiddleware(middlewareConfigs, appConfig.DisableUsageMetrics)
	if err != nil {
		return err
	}

	// Telemetry middleware (if enabled)
	if config.TelemetryConfig != nil {
		telemetryParams := telemetry.FactoryMiddlewareParams{
			Config:     config.TelemetryConfig,
			ServerName: config.Name,
			Transport:  config.Transport.String(),
		}
		telemetryConfig, err := types.NewMiddlewareConfig(telemetry.MiddlewareType, telemetryParams)
		if err != nil {
			return fmt.Errorf("failed to create telemetry middleware config: %w", err)
		}
		middlewareConfigs = append(middlewareConfigs, *telemetryConfig)
	}

	// Audit middleware (if enabled)
	// Added BEFORE authorization so it wraps it at request time: authorization
	// denials (403) must still produce an audit event with outcome "denied".
	// If audit ran inside authz, a deny would short-circuit before the auditor
	// ever saw the request.
	if config.AuditConfig != nil {
		auditParams := audit.MiddlewareParams{
			ConfigPath:    config.AuditConfigPath, // Keep for backwards compatibility
			ConfigData:    config.AuditConfig,     // Use the loaded config data
			Component:     config.AuditConfig.Component,
			TransportType: config.Transport.String(), // Pass the actual transport type
		}
		auditConfig, err := types.NewMiddlewareConfig(audit.MiddlewareType, auditParams)
		if err != nil {
			return fmt.Errorf("failed to create audit middleware config: %w", err)
		}
		middlewareConfigs = append(middlewareConfigs, *auditConfig)
	}

	// Authorization middleware (if enabled)
	if config.AuthzConfig != nil {
		authzCfgData, err := injectUpstreamProviderIfNeeded(config.AuthzConfig, config.EmbeddedAuthServerConfig)
		if err != nil {
			return fmt.Errorf("failed to inject upstream provider into authorization config: %w", err)
		}
		authzParams := authz.FactoryMiddlewareParams{
			ConfigPath: config.AuthzConfigPath, // Keep for backwards compatibility
			ConfigData: authzCfgData,           // Use the (possibly-enriched) config data
		}
		authzConfig, err := types.NewMiddlewareConfig(authz.MiddlewareType, authzParams)
		if err != nil {
			return fmt.Errorf("failed to create authorization middleware config: %w", err)
		}
		middlewareConfigs = append(middlewareConfigs, *authzConfig)
	}

	// AWS STS middleware (if configured)
	// Placed after audit/authz so that authorization is checked before exchanging
	// credentials, and close to the backend so SigV4 signing happens as late as
	// possible — minimizing the chance of subsequent middleware invalidating the signature.
	middlewareConfigs, err = addAWSStsMiddleware(middlewareConfigs, config)
	if err != nil {
		return err
	}

	// Header forward middleware (if configured for remote servers).
	// Added near the end so it executes closest to the backend handler (innermost).
	// By this point, WithSecrets() has resolved any secret-backed headers
	// into resolvedHeaders, so we pass the merged map to the factory.
	middlewareConfigs, err = addHeaderForwardMiddleware(middlewareConfigs, config)
	if err != nil {
		return err
	}

	// Additional middleware configs injected by external-auth handlers via
	// WithAdditionalMiddlewareConfigs (e.g. the enterprise OBO handler). Upstream
	// carries these pre-built configs verbatim and does not interpret their
	// parameters. They are spliced into the backend-egress group here: after auth
	// (so the authenticated identity is available) and after the awssts /
	// header-forward middleware, and before recovery — giving an injected egress
	// middleware the final say on the outbound request to the backend. Appending an
	// empty slice is a no-op, so this is harmless when nothing was injected.
	//
	// config.AdditionalMiddlewareConfigs is intentionally NOT cleared after the
	// splice. Two reasons: (1) this overwrite-based build is idempotent — re-running
	// PopulateMiddlewareConfigs rebuilds the local slice from scratch and re-reads
	// the carrier, so the injected entry appears exactly once each time rather than
	// accumulating; and (2) the carrier stays serialized as a fallback, so a config
	// persisted before population (empty MiddlewareConfigs) still gets the entry when
	// the proxyrunner re-populates via the len(MiddlewareConfigs)==0 guard. The cost
	// is that a fully-populated ConfigMap carries the entry in both slices; the
	// duplicate is inert because the proxyrunner reads MiddlewareConfigs.
	middlewareConfigs = append(middlewareConfigs, config.AdditionalMiddlewareConfigs...)

	// Recovery middleware (always present, added last). The proxy transports
	// apply this slice in reverse order (see applyMiddlewares), so the
	// last-appended entry becomes the INNERMOST wrapper and executes closest to
	// the handler. Recovery therefore catches panics from the handler and the
	// inner middleware; panics raised in middleware that wrap it (earlier
	// entries, such as body-limit and auth) are not caught here.
	recoveryConfig, err := types.NewMiddlewareConfig(recovery.MiddlewareType, nil)
	if err != nil {
		return fmt.Errorf("failed to create recovery middleware config: %w", err)
	}
	middlewareConfigs = append(middlewareConfigs, *recoveryConfig)

	// Set the populated middleware configs
	config.MiddlewareConfigs = middlewareConfigs
	return nil
}

// addMutatingWebhookMiddleware configures the mutating webhook middleware if any webhooks are defined.
// It must be called before addValidatingWebhookMiddleware to preserve the RFC-specified ordering.
func addMutatingWebhookMiddleware(configs []types.MiddlewareConfig, runConfig *RunConfig) ([]types.MiddlewareConfig, error) {
	if len(runConfig.MutatingWebhooks) == 0 {
		return configs, nil
	}

	params := mutating.FactoryMiddlewareParams{
		MiddlewareParams: mutating.MiddlewareParams{
			Webhooks: runConfig.MutatingWebhooks,
		},
		ServerName: runConfig.Name,
		Transport:  runConfig.Transport.String(),
	}

	config, err := types.NewMiddlewareConfig(mutating.MiddlewareType, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create mutating webhook middleware config: %w", err)
	}

	return append(configs, *config), nil
}

// addValidatingWebhookMiddleware configures the validating webhook middleware if any webhooks are defined
func addValidatingWebhookMiddleware(configs []types.MiddlewareConfig, runConfig *RunConfig) ([]types.MiddlewareConfig, error) {
	if len(runConfig.ValidatingWebhooks) == 0 {
		return configs, nil
	}

	params := validating.FactoryMiddlewareParams{
		MiddlewareParams: validating.MiddlewareParams{
			Webhooks: runConfig.ValidatingWebhooks,
		},
		ServerName: runConfig.Name,
		Transport:  runConfig.Transport.String(),
	}

	config, err := types.NewMiddlewareConfig(validating.MiddlewareType, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create validating webhook middleware config: %w", err)
	}

	return append(configs, *config), nil
}

// addTokenExchangeMiddleware adds token exchange middleware if configured
func addTokenExchangeMiddleware(
	middlewares []types.MiddlewareConfig,
	tokenExchangeConfig *tokenexchange.Config,
) ([]types.MiddlewareConfig, error) {
	if tokenExchangeConfig == nil {
		return middlewares, nil
	}

	tokenExchangeParams := tokenexchange.MiddlewareParams{
		TokenExchangeConfig: tokenExchangeConfig,
	}
	tokenExchangeMwConfig, err := types.NewMiddlewareConfig(
		tokenexchange.MiddlewareType,
		tokenExchangeParams,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange middleware config: %w", err)
	}
	return append(middlewares, *tokenExchangeMwConfig), nil
}

// addBodyLimitMiddleware ensures the body-size-limit middleware is present as the
// outermost entry (index 0) of the chain. It is the symmetric counterpart to recovery
// ("always present, innermost"): body-limit is "always present, outermost", so an
// oversized request body is rejected with 413 before auth, the MCP parser, or any
// handler buffers it via io.ReadAll — regardless of which builder assembled the chain.
//
// Idempotent: if the chain already starts with body-limit, the slice is returned
// unchanged. Defaults to bodylimit.DefaultMaxRequestBodySize.
func addBodyLimitMiddleware(middlewares []types.MiddlewareConfig) ([]types.MiddlewareConfig, error) {
	if len(middlewares) > 0 && middlewares[0].Type == bodylimit.MiddlewareType {
		return middlewares, nil
	}
	bodyLimitConfig, err := types.NewMiddlewareConfig(bodylimit.MiddlewareType, bodylimit.MiddlewareParams{
		MaxBytes: bodylimit.DefaultMaxRequestBodySize,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create body limit middleware config: %w", err)
	}
	return append([]types.MiddlewareConfig{*bodyLimitConfig}, middlewares...), nil
}

// addHeaderForwardMiddleware adds header forward middleware if configured for remote servers
func addHeaderForwardMiddleware(middlewares []types.MiddlewareConfig, config *RunConfig) ([]types.MiddlewareConfig, error) {
	if config.RemoteURL == "" || !config.HeaderForward.HasHeaders() {
		return middlewares, nil
	}

	headerForwardParams := headerfwd.HeaderForwardMiddlewareParams{
		AddHeaders: config.HeaderForward.ResolvedHeaders(),
	}
	headerForwardConfig, err := types.NewMiddlewareConfig(headerfwd.HeaderForwardMiddlewareName, headerForwardParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create header forward middleware config: %w", err)
	}
	return append(middlewares, *headerForwardConfig), nil
}

// addUsageMetricsMiddleware adds usage metrics middleware if enabled
func addUsageMetricsMiddleware(middlewares []types.MiddlewareConfig, configDisabled bool) ([]types.MiddlewareConfig, error) {
	if !usagemetrics.ShouldEnableMetrics(configDisabled) {
		return middlewares, nil
	}

	usageMetricsParams := usagemetrics.MiddlewareParams{}
	usageMetricsConfig, err := types.NewMiddlewareConfig(usagemetrics.MiddlewareType, usageMetricsParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create usage metrics middleware config: %w", err)
	}
	return append(middlewares, *usageMetricsConfig), nil
}

// addUpstreamSwapMiddleware adds upstream swap middleware if the embedded auth server is configured.
// This middleware exchanges ToolHive JWTs for upstream IdP tokens.
// The middleware is only added when EmbeddedAuthServerConfig is set and
// DisableUpstreamTokenInjection is false. If UpstreamSwapConfig is nil,
// default configuration values are used.
func addUpstreamSwapMiddleware(
	middlewares []types.MiddlewareConfig,
	config *RunConfig,
) ([]types.MiddlewareConfig, error) {
	// Only add middleware if embedded auth server is configured
	if config.EmbeddedAuthServerConfig == nil {
		return middlewares, nil
	}

	// When upstream token injection is disabled, strip the client's credential
	// headers (Authorization, Cookie, Proxy-Authorization) so they never reach
	// the upstream server. Two ordering invariants apply, pinned by
	// TestPopulateMiddlewareConfigs_StripAuthOrdering:
	//   - strip-auth is appended after the auth middleware, so the client JWT
	//     is fully validated (and the identity stored in the request context
	//     for authz/audit) before the header is removed;
	//   - token-injecting middlewares (token exchange, AWS STS) run closer to
	//     the backend and would re-add an Authorization header after the
	//     strip, silently defeating the flag — that contradiction is rejected
	//     here instead.
	if config.EmbeddedAuthServerConfig.DisableUpstreamTokenInjection {
		if config.TokenExchangeConfig != nil {
			return nil, fmt.Errorf("disableUpstreamTokenInjection cannot be combined with token exchange: " +
				"token exchange would re-add an Authorization header after strip-auth removes it")
		}
		if config.AWSStsConfig != nil {
			return nil, fmt.Errorf("disableUpstreamTokenInjection cannot be combined with AWS STS: " +
				"SigV4 signing would re-add credentials after strip-auth removes them")
		}
		return addAuthHeaderStripMiddleware(middlewares)
	}

	// Use provided config or defaults
	upstreamSwapConfig := config.UpstreamSwapConfig
	if upstreamSwapConfig == nil {
		upstreamSwapConfig = &upstreamswap.Config{}
	}

	// Derive ProviderName from the upstream config if not explicitly set
	if upstreamSwapConfig.ProviderName == "" {
		var names []string
		if embeddedCfg := config.EmbeddedAuthServerConfig; embeddedCfg != nil {
			names = make([]string, len(embeddedCfg.Upstreams))
			for i, u := range embeddedCfg.Upstreams {
				names[i] = u.Name
			}
		}
		upstreamSwapConfig.ProviderName = authserver.ResolveFirstUpstreamName(names)
	}

	upstreamSwapParams := upstreamswap.MiddlewareParams{
		Config: upstreamSwapConfig,
	}
	upstreamSwapMwConfig, err := types.NewMiddlewareConfig(
		upstreamswap.MiddlewareType,
		upstreamSwapParams,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create upstream swap middleware config: %w", err)
	}
	return append(middlewares, *upstreamSwapMwConfig), nil
}

// injectUpstreamProviderIfNeeded enriches an authz.Config with the
// PrimaryUpstreamProvider derived from the embedded auth server config.
// When the embedded auth server is active, Cedar policies should evaluate
// claims from the upstream IDP token rather than the ToolHive-issued JWT.
// If embeddedCfg is nil the original authzCfg is returned unchanged.
func injectUpstreamProviderIfNeeded(
	authzCfg *authz.Config,
	embeddedCfg *authserver.RunConfig,
) (*authz.Config, error) {
	if embeddedCfg == nil {
		return authzCfg, nil
	}

	names := make([]string, len(embeddedCfg.Upstreams))
	for i, u := range embeddedCfg.Upstreams {
		names[i] = u.Name
	}
	providerName := authserver.ResolveFirstUpstreamName(names)

	return cedar.InjectUpstreamProvider(authzCfg, providerName)
}

// addAuthHeaderStripMiddleware adds the strip-auth middleware
// (pkg/transport/middleware), which removes the client's credential headers
// before forwarding to the upstream. This prevents the client's ToolHive JWT,
// cookies, and proxy credentials from leaking to upstream servers that don't
// expect them.
func addAuthHeaderStripMiddleware(
	middlewares []types.MiddlewareConfig,
) ([]types.MiddlewareConfig, error) {
	mwConfig, err := types.NewMiddlewareConfig(headerfwd.StripAuthMiddlewareName, struct{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to create strip-auth middleware config: %w", err)
	}
	return append(middlewares, *mwConfig), nil
}

// addAWSStsMiddleware adds AWS STS middleware if configured.
// Returns an error if AWSStsConfig is set but RemoteURL is empty, because
// SigV4 signing is only meaningful for remote MCP servers.
func addAWSStsMiddleware(middlewares []types.MiddlewareConfig, config *RunConfig) ([]types.MiddlewareConfig, error) {
	if config.AWSStsConfig == nil {
		return middlewares, nil
	}

	if config.RemoteURL == "" {
		return nil, fmt.Errorf("AWS STS middleware requires a remote URL: SigV4 signing is only meaningful for remote MCP servers")
	}

	awsStsParams := awssts.MiddlewareParams{
		AWSStsConfig: config.AWSStsConfig,
		TargetURL:    config.RemoteURL, // Use remote URL as the target for SigV4 signing
	}
	awsStsMwConfig, err := types.NewMiddlewareConfig(awssts.MiddlewareType, awsStsParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS STS middleware config: %w", err)
	}
	return append(middlewares, *awsStsMwConfig), nil
}

// prependOriginMiddleware prepends Origin-header validation middleware for
// DNS-rebind protection per MCP 2025-11-25 §"Security Warning". It is placed at
// the front of the chain so disallowed Origin values are rejected before
// authentication or any business logic runs. Default-derivation logic lives in
// origin.ResolveAllowedOrigins so the standalone `thv proxy` command and the
// runner path agree on behavior.
//
// This is called from runner.Run after both middleware-population paths
// (PopulateMiddlewareConfigs and WithMiddlewareFromFlags) have run, because
// that is the only point where the effective Host/Port/AllowedOrigins are
// fully resolved — the CLI builder defers port resolution to validateConfig.
//
// When the effective allowlist is empty — which happens when the operator
// binds to a non-loopback host without supplying --allowed-origins — the
// middleware is skipped entirely and a WARN is logged so the security-disabled
// state is visible in operator logs. A follow-up PR hardens the non-loopback
// path by requiring an explicit opt-in flag (see audit row 22).
func prependOriginMiddleware(middlewares []types.MiddlewareConfig, config *RunConfig) ([]types.MiddlewareConfig, error) {
	allowed := origin.ResolveAllowedOrigins(config.Host, config.Port, config.AllowedOrigins)
	if len(allowed) == 0 {
		slog.Warn("Origin validation disabled — no allowlist configured for non-loopback bind",
			"host", config.Host,
			"port", config.Port,
			"hint", "pass --allowed-origins=https://your-client.example to enable DNS-rebind protection",
		)
		return middlewares, nil
	}

	params := origin.MiddlewareParams{AllowedOrigins: allowed}
	mwCfg, err := types.NewMiddlewareConfig(origin.MiddlewareType, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create origin middleware config: %w", err)
	}
	// Prepend so Origin validation is the outermost wrapper (runs first at
	// request time). Build a new slice to avoid mutating the caller's backing
	// array.
	return append([]types.MiddlewareConfig{*mwCfg}, middlewares...), nil
}

// addRateLimitMiddleware adds rate limit middleware if configured.
func addRateLimitMiddleware(middlewares []types.MiddlewareConfig, config *RunConfig) ([]types.MiddlewareConfig, error) {
	if config.RateLimitConfig == nil {
		return middlewares, nil
	}

	if config.ScalingConfig == nil || config.ScalingConfig.SessionRedis == nil {
		return nil, fmt.Errorf("rate limiting requires sessionStorage with provider redis")
	}
	redisAddr := config.ScalingConfig.SessionRedis.Address
	redisDB := config.ScalingConfig.SessionRedis.DB

	params := ratelimit.MiddlewareParams{
		Namespace:  config.RateLimitNamespace,
		ServerName: config.Name,
		Config:     config.RateLimitConfig,
		RedisAddr:  redisAddr,
		RedisDB:    redisDB,
	}
	mwConfig, err := types.NewMiddlewareConfig(ratelimit.MiddlewareType, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create rate limit middleware config: %w", err)
	}
	return append(middlewares, *mwConfig), nil
}
