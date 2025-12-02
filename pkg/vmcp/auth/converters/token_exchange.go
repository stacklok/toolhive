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

// ConvertToStrategy converts TokenExchangeConfig to a BackendAuthStrategy with typed fields.
// Secret references are represented as environment variable names that will be resolved by ResolveSecrets.
func (*TokenExchangeConverter) ConvertToStrategy(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	tokenExchange := externalAuth.Spec.TokenExchange
	if tokenExchange == nil {
		return nil, fmt.Errorf("token exchange config is nil")
	}

	// Normalize SubjectTokenType to full URN if needed
	subjectTokenType := tokenExchange.SubjectTokenType
	if subjectTokenType != "" {
		switch subjectTokenType {
		case "access_token":
			subjectTokenType = "urn:ietf:params:oauth:token-type:access_token" // #nosec G101 - not a credential
		case "id_token":
			subjectTokenType = "urn:ietf:params:oauth:token-type:id_token" // #nosec G101 - not a credential
		case "jwt":
			subjectTokenType = "urn:ietf:params:oauth:token-type:jwt" // #nosec G101 - not a credential
		}
	}

	tokenExchangeConfig := &authtypes.TokenExchangeConfig{
		TokenURL:         tokenExchange.TokenURL,
		ClientID:         tokenExchange.ClientID,
		Audience:         tokenExchange.Audience,
		Scopes:           tokenExchange.Scopes,
		SubjectTokenType: subjectTokenType,
	}

	// Set ClientSecretEnv as a placeholder for discovered mode (will be resolved by ResolveSecrets)
	if tokenExchange.ClientSecretRef != nil {
		tokenExchangeConfig.ClientSecretEnv = "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET"
	}

	strategy := &authtypes.BackendAuthStrategy{
		Type:          authtypes.StrategyTypeTokenExchange,
		TokenExchange: tokenExchangeConfig,
	}

	return strategy, nil
}

// ResolveSecrets fetches the client secret from Kubernetes and sets it in the strategy.
// Unlike non-discovered mode where secrets can be mounted as environment variables at pod creation time,
// discovered mode requires dynamic secret resolution because the vMCP pod doesn't know about backend
// auth configs at pod creation time.
//
// This method:
//  1. Checks if ClientSecretEnv is set in the strategy
//  2. Fetches the referenced Kubernetes secret
//  3. Replaces ClientSecretEnv with ClientSecret containing the actual value
//
// If ClientSecretEnv is not set, the strategy is returned unchanged.
func (*TokenExchangeConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	if strategy == nil || strategy.TokenExchange == nil {
		return nil, fmt.Errorf("token exchange strategy is nil")
	}

	tokenExchange := externalAuth.Spec.TokenExchange
	if tokenExchange == nil {
		return nil, fmt.Errorf("token exchange config is nil")
	}

	// If no ClientSecretEnv is present, nothing to resolve
	if strategy.TokenExchange.ClientSecretEnv == "" {
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
