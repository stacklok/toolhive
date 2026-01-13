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

// HeaderInjectionConverter converts MCPExternalAuthConfig HeaderInjection to vMCP header_injection strategy.
type HeaderInjectionConverter struct{}

// StrategyType returns the vMCP strategy type for header injection.
func (*HeaderInjectionConverter) StrategyType() string {
	return authtypes.StrategyTypeHeaderInjection
}

// ConvertToStrategy converts HeaderInjectionConfig to a BackendAuthStrategy with typed fields.
// Sets HeaderValueEnv when ValueSecretRef is present, similar to token exchange.
// Secrets are mounted as environment variables, not resolved into ConfigMap.
func (*HeaderInjectionConverter) ConvertToStrategy(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	headerInjection := externalAuth.Spec.HeaderInjection
	if headerInjection == nil {
		return nil, fmt.Errorf("header injection config is nil")
	}

	strategy := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeHeaderInjection,
		HeaderInjection: &authtypes.HeaderInjectionConfig{
			HeaderName: headerInjection.HeaderName,
		},
	}

	return strategy, nil
}

// ResolveSecrets fetches the header value secret from Kubernetes and sets it in the strategy.
// This is used for runtime discovery in the vmcp binary where secrets cannot be mounted as
// environment variables because backends are discovered dynamically at runtime.
// For operator-managed ConfigMaps (inline mode), secrets are mounted as env vars instead
// (see ConvertToStrategy).
func (*HeaderInjectionConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	if strategy == nil || strategy.HeaderInjection == nil {
		return nil, fmt.Errorf("header injection strategy is nil")
	}

	headerInjection := externalAuth.Spec.HeaderInjection
	if headerInjection == nil {
		return nil, fmt.Errorf("header injection config is nil")
	}

	if headerInjection.ValueSecretRef == nil {
		return nil, fmt.Errorf("valueSecretRef is nil")
	}

	// Fetch and resolve the secret
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      headerInjection.ValueSecretRef.Name,
		Namespace: namespace,
	}

	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w",
			namespace, headerInjection.ValueSecretRef.Name, err)
	}

	secretValue, ok := secret.Data[headerInjection.ValueSecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not contain key %s",
			namespace, headerInjection.ValueSecretRef.Name, headerInjection.ValueSecretRef.Key)
	}

	// Set the resolved secret value in the strategy
	strategy.HeaderInjection.HeaderValue = string(secretValue)

	return strategy, nil
}
