// Package converters provides strategy-specific converters for external authentication configurations.
package converters

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
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

// ResolveSecrets fetches the client secret from Kubernetes and replaces the env var reference
// with the actual secret value. Unlike non-discovered mode where secrets can be mounted as
// environment variables at pod creation time, discovered mode requires dynamic secret resolution
// because the vMCP pod doesn't know about backend auth configs at pod creation time.
//
// This method:
//  1. Checks if client_secret_env is present in metadata
//  2. Fetches the referenced Kubernetes secret
//  3. Replaces client_secret_env with client_secret containing the actual value
//
// If client_secret_env is not present (or client_secret is already set), metadata is returned unchanged.
func (*TokenExchangeConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	metadata map[string]any,
) (map[string]any, error) {
	tokenExchange := externalAuth.Spec.TokenExchange
	if tokenExchange == nil {
		return nil, fmt.Errorf("token exchange config is nil")
	}

	// If no client_secret_env is present, nothing to resolve
	if _, hasEnvRef := metadata["client_secret_env"]; !hasEnvRef {
		return metadata, nil
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
	delete(metadata, "client_secret_env")
	metadata["client_secret"] = string(secretValue)

	return metadata, nil
}
