// Package converters provides functions to convert external authentication configurations
// to typed vMCP BackendAuthStrategy configurations.
//
// The package provides a registry-based approach where each auth type (e.g., token exchange,
// header injection) has a dedicated converter that implements the StrategyConverter interface.
// Converters produce typed *config.BackendAuthStrategy values instead of untyped map[string]any,
// providing type safety and clean serialization.
package converters
