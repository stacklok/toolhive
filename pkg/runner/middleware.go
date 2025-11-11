package runner

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authz"
	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/usagemetrics"
)

// GetSupportedMiddlewareFactories returns a map of supported middleware types to their factory functions
func GetSupportedMiddlewareFactories() map[string]types.MiddlewareFactory {
	return map[string]types.MiddlewareFactory{
		auth.MiddlewareType:              auth.CreateMiddleware,
		tokenexchange.MiddlewareType:     tokenexchange.CreateMiddleware,
		mcp.ParserMiddlewareType:         mcp.CreateParserMiddleware,
		mcp.ToolFilterMiddlewareType:     mcp.CreateToolFilterMiddleware,
		mcp.ToolCallFilterMiddlewareType: mcp.CreateToolCallFilterMiddleware,
		usagemetrics.MiddlewareType:      usagemetrics.CreateMiddleware,
		telemetry.MiddlewareType:         telemetry.CreateMiddleware,
		authz.MiddlewareType:             authz.CreateMiddleware,
		audit.MiddlewareType:             audit.CreateMiddleware,
	}
}

// hasMiddlewareType checks if a middleware of the given type already exists
func hasMiddlewareType(middlewares []types.MiddlewareConfig, middlewareType string) bool {
	for _, m := range middlewares {
		if m.Type == middlewareType {
			return true
		}
	}
	return false
}

// PopulateMiddlewareConfigs populates the MiddlewareConfigs slice based on the RunConfig settings
// This function serves as a bridge between the old configuration style and the new generic middleware system
// It appends default middlewares to any existing middlewares, avoiding duplicates
func PopulateMiddlewareConfigs(config *RunConfig) error {
	// Start with existing middlewares (may already contain operator-provided middlewares)
	middlewareConfigs := config.MiddlewareConfigs
	var err error
	// TODO: Consider extracting other middleware setup into helper functions like addUsageMetricsMiddleware

	// Authentication middleware (add if not already present)
	if !hasMiddlewareType(middlewareConfigs, auth.MiddlewareType) {
		authParams := auth.MiddlewareParams{
			OIDCConfig: config.OIDCConfig,
		}
		authConfig, err := types.NewMiddlewareConfig(auth.MiddlewareType, authParams)
		if err != nil {
			return fmt.Errorf("failed to create auth middleware config: %w", err)
		}
		middlewareConfigs = append(middlewareConfigs, *authConfig)
	}

	// Tools filter and override middleware (add if enabled and not already present)
	hasToolFiltering := len(config.ToolsFilter) > 0 || len(config.ToolsOverride) > 0
	if hasToolFiltering && !hasMiddlewareType(middlewareConfigs, mcp.ToolFilterMiddlewareType) {
		middlewareConfigs = addToolFilterMiddlewares(
			middlewareConfigs,
			config.ToolsFilter,
			config.ToolsOverride,
		)
	}

	// MCP Parser middleware (add if not already present)
	if !hasMiddlewareType(middlewareConfigs, mcp.ParserMiddlewareType) {
		mcpParserParams := mcp.ParserMiddlewareParams{}
		mcpParserConfig, err := types.NewMiddlewareConfig(mcp.ParserMiddlewareType, mcpParserParams)
		if err != nil {
			return fmt.Errorf("failed to create MCP parser middleware config: %w", err)
		}
		middlewareConfigs = append(middlewareConfigs, *mcpParserConfig)
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
	middlewareConfigs = addTelemetryMiddleware(
		middlewareConfigs,
		config.TelemetryConfig,
		config.Name,
		config.Transport.String(),
	)

	// Authorization middleware (if enabled)
	middlewareConfigs = addAuthzMiddleware(middlewareConfigs, config.AuthzConfigPath)

	// Audit middleware (if enabled)
	enableAudit := config.AuditConfig != nil
	middlewareConfigs = addAuditMiddleware(
		middlewareConfigs,
		enableAudit,
		config.AuditConfigPath,
		config.Name,
		config.Transport.String(),
	)

	// Set the populated middleware configs
	config.MiddlewareConfigs = middlewareConfigs
	return nil
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
