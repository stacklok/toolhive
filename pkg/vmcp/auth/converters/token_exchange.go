// Package converters provides strategy-specific converters for external authentication configurations.
package converters

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TokenExchangeConverter converts MCPExternalAuthConfig TokenExchange to vMCP token_exchange strategy metadata.
type TokenExchangeConverter struct{}

// StrategyType returns the vMCP strategy type for token exchange.
func (*TokenExchangeConverter) StrategyType() string {
	return "token_exchange"
}

// ConvertToMetadata converts TokenExchangeConfig to token_exchange strategy metadata (without secrets resolved).
// Secret references are represented as environment variable names that will be resolved at runtime.
func (*TokenExchangeConverter) ConvertToMetadata(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (map[string]any, error) {
	tokenExchange := externalAuth.Spec.TokenExchange
	if tokenExchange == nil {
		return nil, fmt.Errorf("token exchange config is nil")
	}

	metadata := make(map[string]any)
	metadata["token_url"] = tokenExchange.TokenURL

	// Add optional fields if present
	if tokenExchange.ClientID != "" {
		metadata["client_id"] = tokenExchange.ClientID
	}

	if tokenExchange.ClientSecretRef != nil {
		// Reference to environment variable that will be mounted from secret
		metadata["client_secret_env"] = "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET"
	}

	if tokenExchange.Audience != "" {
		metadata["audience"] = tokenExchange.Audience
	}

	if len(tokenExchange.Scopes) > 0 {
		metadata["scopes"] = tokenExchange.Scopes
	}

	if tokenExchange.SubjectTokenType != "" {
		// Convert short form to full URN if needed
		subjectTokenType := tokenExchange.SubjectTokenType
		switch subjectTokenType {
		case "access_token":
			subjectTokenType = "urn:ietf:params:oauth:token-type:access_token" // #nosec G101 - not a credential
		case "id_token":
			subjectTokenType = "urn:ietf:params:oauth:token-type:id_token" // #nosec G101 - not a credential
		case "jwt":
			subjectTokenType = "urn:ietf:params:oauth:token-type:jwt" // #nosec G101 - not a credential
		}
		metadata["subject_token_type"] = subjectTokenType
	}

	if tokenExchange.ExternalTokenHeaderName != "" {
		metadata["external_token_header_name"] = tokenExchange.ExternalTokenHeaderName
	}

	return metadata, nil
}

// ResolveSecrets for token exchange is typically a no-op because secrets are mounted as
// environment variables in the vMCP pod. The client_secret_env reference points to an
// environment variable that Kubernetes will populate from the secret at runtime.
//
// This method is provided for interface compliance but doesn't fetch secrets from Kubernetes
// because token exchange secrets are mounted at pod creation time, unlike discovered auth mode
// where secrets must be resolved dynamically.
func (*TokenExchangeConverter) ResolveSecrets(
	_ context.Context,
	_ *mcpv1alpha1.MCPExternalAuthConfig,
	_ client.Client,
	_ string,
	metadata map[string]any,
) (map[string]any, error) {
	// Token exchange secrets are mounted as environment variables at pod creation time
	// No runtime secret resolution needed
	return metadata, nil
}
