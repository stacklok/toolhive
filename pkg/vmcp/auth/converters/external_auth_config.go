// Package converters provides functions to convert external authentication configurations
// to vMCP auth strategy metadata.
package converters

import (
	"fmt"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
)

// ExternalAuthConfigToStrategyMetadata converts an MCPExternalAuthConfig to vMCP auth strategy metadata.
// This function is used at runtime by vMCP when discovering backend authentication configurations.
//
// Currently supported external auth types:
//   - tokenExchange: Converts to token_exchange strategy with appropriate metadata
//
// Returns the strategy type and metadata map that can be used to configure vMCP auth strategies.
func ExternalAuthConfigToStrategyMetadata(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (strategyType string, metadata map[string]any, err error) {
	if externalAuth == nil {
		return "", nil, fmt.Errorf("external auth config is nil")
	}

	switch externalAuth.Spec.Type {
	case mcpv1alpha1.ExternalAuthTypeTokenExchange:
		return convertTokenExchangeConfig(externalAuth.Spec.TokenExchange)
	default:
		return "", nil, fmt.Errorf("unsupported external auth type: %s", externalAuth.Spec.Type)
	}
}

// convertTokenExchangeConfig converts TokenExchangeConfig to token_exchange strategy metadata
func convertTokenExchangeConfig(
	tokenExchange *mcpv1alpha1.TokenExchangeConfig,
) (strategyType string, metadata map[string]any, err error) {
	if tokenExchange == nil {
		return "", nil, fmt.Errorf("token exchange config is nil")
	}

	metadata = make(map[string]any)
	metadata["token_url"] = tokenExchange.TokenURL

	if tokenExchange.ClientID != "" {
		metadata["client_id"] = tokenExchange.ClientID
	}

	// Handle client secret - use environment variable reference for security
	// The secret will be mounted as an environment variable by the operator
	if tokenExchange.ClientSecretRef != nil {
		metadata["client_secret_env"] = "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET"
	}

	if tokenExchange.Audience != "" {
		metadata["audience"] = tokenExchange.Audience
	}

	if len(tokenExchange.Scopes) > 0 {
		metadata["scopes"] = tokenExchange.Scopes
	}

	// Normalize subject token type
	if tokenExchange.SubjectTokenType != "" {
		normalized, err := tokenexchange.NormalizeTokenType(tokenExchange.SubjectTokenType)
		if err != nil {
			return "", nil, fmt.Errorf("invalid subject token type: %w", err)
		}
		metadata["subject_token_type"] = normalized
	}

	return "token_exchange", metadata, nil
}
