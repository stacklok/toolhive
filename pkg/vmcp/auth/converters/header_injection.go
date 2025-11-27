// Package converters provides strategy-specific converters for external authentication configurations.
package converters

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
)

// HeaderInjectionConverter converts MCPExternalAuthConfig HeaderInjection to vMCP header_injection strategy metadata.
type HeaderInjectionConverter struct{}

// StrategyType returns the vMCP strategy type for header injection.
func (*HeaderInjectionConverter) StrategyType() string {
	return "header_injection"
}

// ConvertToMetadata converts HeaderInjectionConfig to header_injection strategy metadata (without secrets resolved).
// The secret value will be added by ResolveSecrets.
func (*HeaderInjectionConverter) ConvertToMetadata(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (map[string]any, error) {
	headerInjection := externalAuth.Spec.HeaderInjection
	if headerInjection == nil {
		return nil, fmt.Errorf("header injection config is nil")
	}

	metadata := make(map[string]any)
	metadata[strategies.MetadataHeaderName] = headerInjection.HeaderName

	return metadata, nil
}

// ResolveSecrets fetches the header value secret from Kubernetes and adds it to the metadata.
// Unlike token exchange which can use environment variables in non-discovered mode, header
// injection always requires dynamic secret resolution because backends can be added or modified
// at runtime, even in non-discovered mode. The vMCP pod cannot know all backend auth configs
// at pod creation time.
func (*HeaderInjectionConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	metadata map[string]any,
) (map[string]any, error) {
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

	// Add the resolved secret value to metadata
	metadata[strategies.MetadataHeaderValue] = string(secretValue)

	return metadata, nil
}
