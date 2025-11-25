// Package converters provides functions to convert external authentication configurations
// to vMCP auth strategy metadata with extensible strategy-specific converters.
package converters

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// StrategyConverter converts MCPExternalAuthConfig to vMCP strategy metadata
// and handles secret resolution from Kubernetes.
//
// Each authentication strategy implements this interface to provide its own
// conversion logic and secret resolution, making the system extensible without
// requiring changes to the workload discovery code.
type StrategyConverter interface {
	// StrategyType returns the vMCP strategy type (e.g., "token_exchange", "header_injection")
	StrategyType() string

	// ConvertToMetadata converts the CRD to strategy metadata (without secrets resolved).
	// This produces metadata with secret references (e.g., client_secret_env).
	ConvertToMetadata(externalAuth *mcpv1alpha1.MCPExternalAuthConfig) (map[string]any, error)

	// ResolveSecrets fetches actual secret values from Kubernetes and updates metadata.
	// This replaces secret references with actual values for discovered auth mode.
	// Returns the updated metadata map with secrets resolved.
	ResolveSecrets(
		ctx context.Context,
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
		k8sClient client.Client,
		namespace string,
		metadata map[string]any,
	) (map[string]any, error)
}

// Registry holds all registered strategy converters and provides lookup by auth type.
type Registry struct {
	converters map[mcpv1alpha1.ExternalAuthType]StrategyConverter
}

// NewRegistry creates a new converter registry with all built-in converters registered.
func NewRegistry() *Registry {
	return &Registry{
		converters: map[mcpv1alpha1.ExternalAuthType]StrategyConverter{
			mcpv1alpha1.ExternalAuthTypeTokenExchange:   &TokenExchangeConverter{},
			mcpv1alpha1.ExternalAuthTypeHeaderInjection: &HeaderInjectionConverter{},
		},
	}
}

// GetConverter returns the converter for the specified auth type.
// Returns an error if the auth type is not supported.
func (r *Registry) GetConverter(authType mcpv1alpha1.ExternalAuthType) (StrategyConverter, error) {
	converter, ok := r.converters[authType]
	if !ok {
		return nil, fmt.Errorf("unsupported auth type: %s", authType)
	}
	return converter, nil
}

// DiscoverAndResolveAuth discovers authentication configuration from an MCPServer's
// ExternalAuthConfigRef and resolves it to a strategy type and metadata.
// This is the main entry point for auth discovery from Kubernetes.
//
// Returns:
//   - strategyType: The auth strategy type (e.g., "token_exchange", "header_injection")
//   - metadata: The resolved auth metadata with secrets fetched from Kubernetes
//   - error: Any error that occurred during discovery or resolution
//
// Returns empty string and nil if externalAuthConfigRef is nil (no auth configured).
func DiscoverAndResolveAuth(
	ctx context.Context,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	namespace string,
	k8sClient client.Client,
) (strategyType string, metadata map[string]any, err error) {
	// Check if there's an ExternalAuthConfigRef
	if externalAuthConfigRef == nil {
		// No auth config to discover
		return "", nil, nil
	}

	// Fetch the MCPExternalAuthConfig
	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{}
	key := client.ObjectKey{
		Name:      externalAuthConfigRef.Name,
		Namespace: namespace,
	}

	if err := k8sClient.Get(ctx, key, externalAuth); err != nil {
		return "", nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s: %w", externalAuthConfigRef.Name, err)
	}

	// Get the converter registry
	registry := NewRegistry()

	// Get the converter for this auth type
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get converter for auth type %s: %w", externalAuth.Spec.Type, err)
	}

	// Convert to metadata (without secrets resolved)
	metadata, err = converter.ConvertToMetadata(externalAuth)
	if err != nil {
		return "", nil, fmt.Errorf("failed to convert to metadata: %w", err)
	}

	// Resolve secrets from Kubernetes
	metadata, err = converter.ResolveSecrets(ctx, externalAuth, k8sClient, namespace, metadata)
	if err != nil {
		return "", nil, fmt.Errorf("failed to resolve secrets: %w", err)
	}

	return converter.StrategyType(), metadata, nil
}
