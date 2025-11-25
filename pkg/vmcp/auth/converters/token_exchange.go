package converters

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
)

// TokenExchangeConverter converts MCPExternalAuthConfig TokenExchange to vMCP token_exchange strategy metadata.
type TokenExchangeConverter struct{}

// StrategyType returns the vMCP strategy type for token exchange.
func (*TokenExchangeConverter) StrategyType() string {
	return "token_exchange"
}

// ConvertToMetadata converts TokenExchangeConfig to token_exchange strategy metadata (without secrets resolved).
// Secret references are represented as environment variable names that will be resolved later.
func (*TokenExchangeConverter) ConvertToMetadata(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (map[string]any, error) {
	tokenExchange := externalAuth.Spec.TokenExchange
	if tokenExchange == nil {
		return nil, fmt.Errorf("token exchange config is nil")
	}

	metadata := make(map[string]any)
	metadata["token_url"] = tokenExchange.TokenURL

	if tokenExchange.ClientID != "" {
		metadata["client_id"] = tokenExchange.ClientID
	}

	// Set env var reference - will be resolved later in ResolveSecrets for discovered mode
	// or mounted as an actual env var for inline mode
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
			return nil, fmt.Errorf("invalid subject token type: %w", err)
		}
		metadata["subject_token_type"] = normalized
	}

	return metadata, nil
}

// ResolveSecrets fetches the client secret from Kubernetes and replaces the env var reference
// with the actual secret value. This is used in discovered auth mode where secrets cannot be
// mounted as environment variables because the vMCP pod doesn't know about backend auth configs
// at pod creation time.
func (*TokenExchangeConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	metadata map[string]any,
) (map[string]any, error) {
	tokenExchange := externalAuth.Spec.TokenExchange
	if tokenExchange == nil || tokenExchange.ClientSecretRef == nil {
		return metadata, nil // No secret to resolve
	}

	// Fetch the secret from Kubernetes
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      tokenExchange.ClientSecretRef.Name,
		Namespace: namespace,
	}

	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get client secret %s: %w",
			tokenExchange.ClientSecretRef.Name, err)
	}

	secretValue, ok := secret.Data[tokenExchange.ClientSecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("client secret %s is missing key %s",
			tokenExchange.ClientSecretRef.Name, tokenExchange.ClientSecretRef.Key)
	}

	// Replace env var reference with actual secret value
	// This allows vMCP to use the secret without requiring it to be mounted as an env var
	delete(metadata, "client_secret_env")
	metadata["client_secret"] = string(secretValue)

	return metadata, nil
}
