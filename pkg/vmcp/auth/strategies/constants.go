// Package strategies provides authentication strategy implementations for Virtual MCP Server.
package strategies

import authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"

// Re-export strategy type constants from authtypes for backward compatibility.
// New code should import these directly from authtypes.
const (
	// StrategyTypeUnauthenticated identifies the unauthenticated strategy.
	// Deprecated: Use authtypes.StrategyTypeUnauthenticated instead.
	StrategyTypeUnauthenticated = authtypes.StrategyTypeUnauthenticated

	// StrategyTypeHeaderInjection identifies the header injection strategy.
	// Deprecated: Use authtypes.StrategyTypeHeaderInjection instead.
	StrategyTypeHeaderInjection = authtypes.StrategyTypeHeaderInjection

	// StrategyTypeTokenExchange identifies the token exchange strategy.
	// Deprecated: Use authtypes.StrategyTypeTokenExchange instead.
	StrategyTypeTokenExchange = authtypes.StrategyTypeTokenExchange
)
