// Package converters provides functions to convert external authentication configurations
// to vMCP auth strategy metadata.
package converters

import (
	"fmt"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// ExternalAuthConfigToStrategyMetadata converts an MCPExternalAuthConfig to vMCP auth strategy metadata.
// This function is used at runtime by vMCP when discovering backend authentication configurations.
//
// Deprecated: This function is maintained for backward compatibility. New code should use
// the registry-based approach with StrategyConverter interface. This function will delegate
// to the new registry system.
//
// Currently supported external auth types:
//   - tokenExchange: Converts to token_exchange strategy with appropriate metadata
//   - headerInjection: Converts to header_injection strategy with appropriate metadata
//
// Returns the strategy type and metadata map that can be used to configure vMCP auth strategies.
func ExternalAuthConfigToStrategyMetadata(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (strategyType string, metadata map[string]any, err error) {
	if externalAuth == nil {
		return "", nil, fmt.Errorf("external auth config is nil")
	}

	// Delegate to the new registry-based system
	registry := NewRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	if err != nil {
		return "", nil, err
	}

	metadata, err = converter.ConvertToMetadata(externalAuth)
	if err != nil {
		return "", nil, err
	}

	return converter.StrategyType(), metadata, nil
}
