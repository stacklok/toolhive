// Package converters provides strategy-specific converters for external authentication configurations.
package converters

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TokenExchangeConverter converts MCPExternalAuthConfig TokenExchange to vMCP token_exchange strategy.
type TokenExchangeConverter struct{}

// StrategyType returns the vMCP strategy type for token exchange.
func (*TokenExchangeConverter) StrategyType() string {
	return authtypes.StrategyTypeTokenExchange
}

// Convert converts TokenExchangeConfig to a typed BackendAuthStrategy (without secrets resolved).
// Secret references are represented as environment variable names that will be resolved at runtime.
func (*TokenExchangeConverter) Convert(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	tokenExchange := externalAuth.Spec.TokenExchange
	if tokenExchange == nil {
		return nil, fmt.Errorf("token exchange config is nil")
	}

	tokenConfig := &authtypes.TokenExchangeConfig{
		TokenURL: tokenExchange.TokenURL,
	}

	// Add optional fields if present
	if tokenExchange.ClientID != "" {
		tokenConfig.ClientID = tokenExchange.ClientID
	}

	if tokenExchange.ClientSecretRef != nil {
		// Reference to environment variable that will be mounted from secret
		tokenConfig.ClientSecretEnv = "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET"
	}

	if tokenExchange.Audience != "" {
		tokenConfig.Audience = tokenExchange.Audience
	}

	if len(tokenExchange.Scopes) > 0 {
		tokenConfig.Scopes = tokenExchange.Scopes
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
		tokenConfig.SubjectTokenType = subjectTokenType
	}

	return &authtypes.BackendAuthStrategy{
		Type:          authtypes.StrategyTypeTokenExchange,
		TokenExchange: tokenConfig,
	}, nil
}

// ResolveSecrets fetches the client secret from Kubernetes and replaces the env var reference
// with the actual secret value. Unlike non-discovered mode where secrets can be mounted as
// environment variables at pod creation time, discovered mode requires dynamic secret resolution
// because the vMCP pod doesn't know about backend auth configs at pod creation time.
//
// This method:
//  1. Checks if ClientSecretEnv is present in the strategy
//  2. Fetches the referenced Kubernetes secret
//  3. Replaces ClientSecretEnv with ClientSecret containing the actual value
//
// If ClientSecretEnv is not present (or ClientSecret is already set), strategy is returned unchanged.
func (*TokenExchangeConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	tokenExchange := externalAuth.Spec.TokenExchange
	if tokenExchange == nil {
		return nil, fmt.Errorf("token exchange config is nil")
	}

	// If no ClientSecretEnv is present, nothing to resolve
	if strategy.TokenExchange == nil || strategy.TokenExchange.ClientSecretEnv == "" {
		return strategy, nil
	}

	// If ClientSecretRef is not configured, we cannot resolve
	if tokenExchange.ClientSecretRef == nil {
		return nil, fmt.Errorf("clientSecretRef is nil")
	}

	// Fetch and resolve the secret
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      tokenExchange.ClientSecretRef.Name,
		Namespace: namespace,
	}

	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w",
			namespace, tokenExchange.ClientSecretRef.Name, err)
	}

	secretValue, ok := secret.Data[tokenExchange.ClientSecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not contain key %s",
			namespace, tokenExchange.ClientSecretRef.Name, tokenExchange.ClientSecretRef.Key)
	}

	// Replace the env var reference with actual secret value
	strategy.TokenExchange.ClientSecretEnv = ""
	strategy.TokenExchange.ClientSecret = string(secretValue)

	return strategy, nil
}
