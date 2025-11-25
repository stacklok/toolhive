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

// HeaderInjectionConverter converts MCPExternalAuthConfig HeaderInjection to vMCP header_injection strategy metadata.
type HeaderInjectionConverter struct{}

// StrategyType returns the vMCP strategy type for header injection.
func (*HeaderInjectionConverter) StrategyType() string {
	return "header_injection"
}

// ConvertToMetadata converts HeaderInjectionConfig to header_injection strategy metadata (without secrets resolved).
// Secret references are represented as environment variable names that will be resolved later.
func (*HeaderInjectionConverter) ConvertToMetadata(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (map[string]any, error) {
	headerInjection := externalAuth.Spec.HeaderInjection
	if headerInjection == nil {
		return nil, fmt.Errorf("header injection config is nil")
	}

	metadata := make(map[string]any)
	metadata["header_name"] = headerInjection.HeaderName

	// Set env var reference if secret is used
	if headerInjection.ValueSecretRef != nil {
		metadata["header_value_env"] = "TOOLHIVE_HEADER_INJECTION_VALUE"
	} else if headerInjection.Value != "" {
		// Direct value (not recommended for sensitive data)
		metadata["header_value"] = headerInjection.Value
	} else {
		return nil, fmt.Errorf("header injection must specify either value or valueSecretRef")
	}

	return metadata, nil
}

// ResolveSecrets fetches the header value secret from Kubernetes and replaces the env var reference
// with the actual secret value. This is used in discovered auth mode where secrets cannot be
// mounted as environment variables because the vMCP pod doesn't know about backend auth configs
// at pod creation time.
func (*HeaderInjectionConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	metadata map[string]any,
) (map[string]any, error) {
	headerInjection := externalAuth.Spec.HeaderInjection
	if headerInjection == nil || headerInjection.ValueSecretRef == nil {
		return metadata, nil // No secret to resolve (using direct value)
	}

	// Fetch the secret from Kubernetes
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      headerInjection.ValueSecretRef.Name,
		Namespace: namespace,
	}

	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get header value secret %s: %w",
			headerInjection.ValueSecretRef.Name, err)
	}

	secretValue, ok := secret.Data[headerInjection.ValueSecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("header value secret %s is missing key %s",
			headerInjection.ValueSecretRef.Name, headerInjection.ValueSecretRef.Key)
	}

	// Replace env var reference with actual secret value
	delete(metadata, "header_value_env")
	metadata["header_value"] = string(secretValue)

	return metadata, nil
}
