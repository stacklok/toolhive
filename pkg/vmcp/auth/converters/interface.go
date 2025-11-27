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

var (
	defaultRegistry     *Registry
	defaultRegistryOnce sync.Once
)

// DefaultRegistry returns the singleton default registry with all built-in converters registered.
// This registry is lazily initialized once and reused across all calls.
func DefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = NewRegistry()
	})
	return defaultRegistry
}

// NewRegistry creates a new converter registry with all built-in converters registered.
// For most use cases, use DefaultRegistry() instead to avoid unnecessary allocations.
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

// ConvertToStrategyMetadata is a convenience function that uses the default registry to convert
// an external auth config to strategy metadata. This is the main entry point for converting
// auth configs at runtime.
func ConvertToStrategyMetadata(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (strategyType string, metadata map[string]any, err error) {
	if externalAuth == nil {
		return "", nil, fmt.Errorf("external auth config is nil")
	}

	registry := DefaultRegistry()
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

// ResolveSecretsForStrategy is a convenience function that uses the default registry to resolve
// secrets for a given strategy.
func ResolveSecretsForStrategy(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	metadata map[string]any,
) (map[string]any, error) {
	if externalAuth == nil {
		return nil, fmt.Errorf("external auth config is nil")
	}

	registry := DefaultRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	if err != nil {
		return nil, err
	}

	return converter.ResolveSecrets(ctx, externalAuth, k8sClient, namespace, metadata)
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
	registry := DefaultRegistry()

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
