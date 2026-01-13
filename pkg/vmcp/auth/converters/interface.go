// Package converters provides a registry for converting external authentication configurations
// to vMCP auth strategy metadata.
package converters

import (
	"context"
	"fmt"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// StrategyConverter defines the interface for converting external auth configs to BackendAuthStrategy.
// Each auth type (e.g., token exchange, header injection) implements this interface.
type StrategyConverter interface {
	// StrategyType returns the vMCP strategy type identifier (e.g., "token_exchange", "header_injection")
	StrategyType() string

	// ConvertToStrategy converts an MCPExternalAuthConfig to a BackendAuthStrategy with typed fields.
	// Secret references should be represented as environment variable names (e.g., "TOOLHIVE_*")
	// that will be resolved later by ResolveSecrets or at runtime.
	ConvertToStrategy(externalAuth *mcpv1alpha1.MCPExternalAuthConfig) (*authtypes.BackendAuthStrategy, error)

	// ResolveSecrets fetches secrets from Kubernetes and replaces environment variable references
	// with actual secret values in the strategy configuration. This is used in discovered auth mode where
	// secrets cannot be mounted as environment variables because the vMCP pod doesn't know
	// about backend auth configs at pod creation time.
	//
	// For non-discovered mode (where secrets are mounted as env vars), this is typically a no-op.
	ResolveSecrets(
		ctx context.Context,
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
		k8sClient client.Client,
		namespace string,
		strategy *authtypes.BackendAuthStrategy,
	) (*authtypes.BackendAuthStrategy, error)
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
	r.Register(mcpv1alpha1.ExternalAuthTypeUnauthenticated, &UnauthenticatedConverter{})

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

// ConvertToStrategy is a convenience function that uses the default registry to convert
// an external auth config to a BackendAuthStrategy with typed fields.
// This is the main entry point for converting auth configs at runtime.
func ConvertToStrategy(
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	if externalAuth == nil {
		return nil, fmt.Errorf("external auth config is nil")
	}

	registry := DefaultRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	if err != nil {
		return nil, err
	}

	strategy, err := converter.ConvertToStrategy(externalAuth)
	if err != nil {
		return nil, err
	}

	return strategy, nil
}

// ResolveSecretsForStrategy is a convenience function that uses the default registry to resolve
// secrets for a given strategy.
func ResolveSecretsForStrategy(
	ctx context.Context,
	externalAuth *mcpv1alpha1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	if externalAuth == nil {
		return nil, fmt.Errorf("external auth config is nil")
	}

	registry := DefaultRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	if err != nil {
		return nil, err
	}

	return converter.ResolveSecrets(ctx, externalAuth, k8sClient, namespace, strategy)
}

// DiscoverAndResolveAuth discovers authentication configuration from an MCPServer's
// ExternalAuthConfigRef and resolves it to a BackendAuthStrategy with typed fields.
// This is the main entry point for auth discovery from Kubernetes.
//
// Returns:
//   - strategy: The resolved BackendAuthStrategy with typed fields and secrets fetched from Kubernetes
//   - error: Any error that occurred during discovery or resolution
//
// Returns nil strategy and nil error if externalAuthConfigRef is nil (no auth configured).
func DiscoverAndResolveAuth(
	ctx context.Context,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	namespace string,
	k8sClient client.Client,
) (*authtypes.BackendAuthStrategy, error) {
	// Check if there's an ExternalAuthConfigRef
	if externalAuthConfigRef == nil {
		// No auth config to discover
		return nil, nil
	}

	// Fetch the MCPExternalAuthConfig
	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{}
	key := client.ObjectKey{
		Name:      externalAuthConfigRef.Name,
		Namespace: namespace,
	}

	if err := k8sClient.Get(ctx, key, externalAuth); err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s: %w", externalAuthConfigRef.Name, err)
	}

	// Get the converter registry
	registry := DefaultRegistry()

	// Get the converter for this auth type
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	if err != nil {
		return nil, fmt.Errorf("failed to get converter for auth type %s: %w", externalAuth.Spec.Type, err)
	}

	// Convert to strategy (without secrets resolved)
	strategy, err := converter.ConvertToStrategy(externalAuth)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to strategy: %w", err)
	}

	// Resolve secrets from Kubernetes
	strategy, err = converter.ResolveSecrets(ctx, externalAuth, k8sClient, namespace, strategy)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve secrets: %w", err)
	}

	return strategy, nil
}
