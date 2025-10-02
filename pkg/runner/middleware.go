package runner

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// GetSupportedMiddlewareFactories returns a map of supported middleware types to their factory functions
func GetSupportedMiddlewareFactories() map[string]types.MiddlewareFactory {
	return map[string]types.MiddlewareFactory{
		auth.MiddlewareType:              auth.CreateMiddleware,
		mcp.ParserMiddlewareType:         mcp.CreateParserMiddleware,
		mcp.ToolFilterMiddlewareType:     mcp.CreateToolFilterMiddleware,
		mcp.ToolCallFilterMiddlewareType: mcp.CreateToolCallFilterMiddleware,
		telemetry.MiddlewareType:         telemetry.CreateMiddleware,
		authz.MiddlewareType:             authz.CreateMiddleware,
		audit.MiddlewareType:             audit.CreateMiddleware,
	}
}

// PopulateMiddlewareConfigs populates the MiddlewareConfigs slice based on the RunConfig settings
// This function serves as a bridge between the old configuration style and the new generic middleware system
func PopulateMiddlewareConfigs(config *RunConfig) error {
	var middlewareConfigs []types.MiddlewareConfig

	// Authentication middleware (always present)
	authParams := auth.MiddlewareParams{
		OIDCConfig: config.OIDCConfig,
	}
	authConfig, err := types.NewMiddlewareConfig(auth.MiddlewareType, authParams)
	if err != nil {
		return fmt.Errorf("failed to create auth middleware config: %w", err)
	}
	middlewareConfigs = append(middlewareConfigs, *authConfig)

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

	// Authorization middleware (if enabled)
	if config.AuthzConfig != nil {
		authzParams := authz.FactoryMiddlewareParams{
			ConfigPath: config.AuthzConfigPath, // Keep for backwards compatibility
			ConfigData: config.AuthzConfig,     // Use the loaded config data
		}
		authzConfig, err := types.NewMiddlewareConfig(authz.MiddlewareType, authzParams)
		if err != nil {
			return fmt.Errorf("failed to create authorization middleware config: %w", err)
		}
		middlewareConfigs = append(middlewareConfigs, *authzConfig)
	}

	// Audit middleware (if enabled)
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

	// Set the populated middleware configs
	config.MiddlewareConfigs = middlewareConfigs
	return nil
}
