// Package converters provides a registry for converting external authentication configurations
// to vMCP auth strategy metadata.
package converters

import (
	"context"
	"fmt"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// StrategyConverter defines the interface for converting external auth configs to strategy metadata.
// Each auth type (e.g., token exchange, header injection) implements this interface.
type StrategyConverter interface {
	// StrategyType returns the vMCP strategy type identifier (e.g., "token_exchange", "header_injection")
	StrategyType() string

	// ConvertToMetadata converts an MCPExternalAuthConfig to strategy metadata.
	// Secret references should be represented as environment variable names (e.g., "TOOLHIVE_*")
	// that will be resolved later by ResolveSecrets or at runtime.
	ConvertToMetadata(externalAuth *mcpv1alpha1.MCPExternalAuthConfig) (map[string]any, error)

	// ResolveSecrets fetches secrets from Kubernetes and replaces environment variable references
	// with actual secret values in the metadata. This is used in discovered auth mode where
	// secrets cannot be mounted as environment variables because the vMCP pod doesn't know
	// about backend auth configs at pod creation time.
	//
	// For non-discovered mode (where secrets are mounted as env vars), this is typically a no-op.
	ResolveSecrets(
		ctx context.Context,
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
		k8sClient client.Client,
		namespace string,
		metadata map[string]any,
	) (map[string]any, error)
}

// Registry holds registered strategy converters
type Registry struct {
	mu         sync.RWMutex
	converters map[mcpv1alpha1.ExternalAuthType]StrategyConverter
}

// NewRegistry creates a new converter registry with all built-in converters registered
func NewRegistry() *Registry {
	r := &Registry{
		converters: make(map[mcpv1alpha1.ExternalAuthType]StrategyConverter),
	}

	// Register built-in converters
	r.Register(mcpv1alpha1.ExternalAuthTypeTokenExchange, &TokenExchangeConverter{})
	r.Register(mcpv1alpha1.ExternalAuthTypeHeaderInjection, &HeaderInjectionConverter{})

	return r
}

// Register adds a converter to the registry
func (r *Registry) Register(authType mcpv1alpha1.ExternalAuthType, converter StrategyConverter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.converters[authType] = converter
}

// GetConverter retrieves a converter by auth type
func (r *Registry) GetConverter(authType mcpv1alpha1.ExternalAuthType) (StrategyConverter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	converter, ok := r.converters[authType]
	if !ok {
		return nil, fmt.Errorf("unsupported auth type: %s", authType)
	}
	return converter, nil
}

// ConvertToStrategyMetadata is a convenience function that creates a registry and converts
// an external auth config to strategy metadata. This is the main entry point for converting
// auth configs at runtime.
func ConvertToStrategyMetadata(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (strategyType string, metadata map[string]any, err error) {
	if externalAuth == nil {
		return "", nil, fmt.Errorf("external auth config is nil")
	}

	registry := NewRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	if err != nil {
		return "", nil, err
	}

	metadata, err = converter.ConvertToMetadata(externalAuth)
	if err != nil {
		return "", nil, err
	}

	return converter.StrategyType(), metadata, nil
}

// ResolveSecretsForStrategy is a convenience function that resolves secrets for a given strategy
func ResolveSecretsForStrategy(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	metadata map[string]any,
) (map[string]any, error) {
	if externalAuth == nil {
		return metadata, fmt.Errorf("external auth config is nil")
	}

	registry := NewRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	if err != nil {
		return metadata, err
	}

	return converter.ResolveSecrets(ctx, externalAuth, k8sClient, namespace, metadata)
}
