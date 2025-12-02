// Package resolver provides authentication resolver implementations for resolving
// external auth config references to concrete authentication strategies.
//
// This package contains:
//   - AuthResolver interface for resolving external auth config references
//   - K8SAuthResolver for Kubernetes environments (fetches MCPExternalAuthConfig CRDs)
//   - CLIAuthResolver for CLI environments (loads YAML files, resolves env vars)
package resolver

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// AuthResolver resolves external auth config references to concrete authentication strategies.
// This interface allows the backend discoverer to resolve "external_auth_config_ref" strategy types
// to their actual implementation (token_exchange or header_injection) at runtime.
//
// Implementations of this interface are responsible for:
// 1. Looking up the referenced auth config (from K8s CRDs or YAML files)
// 2. Converting it to a BackendAuthStrategy with appropriate configuration
// 3. Resolving any secret references to actual values
//
//go:generate mockgen -destination=mocks/mock_auth_resolver.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/auth/resolver AuthResolver
type AuthResolver interface {
	// ResolveExternalAuthConfig resolves an external auth config reference name
	// to a concrete BackendAuthStrategy.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - refName: The name of the external auth config to resolve
	//
	// Returns:
	//   - *authtypes.BackendAuthStrategy: The resolved auth strategy with all configuration populated
	//   - error: If the referenced resource cannot be found or resolved
	ResolveExternalAuthConfig(ctx context.Context, refName string) (*authtypes.BackendAuthStrategy, error)
}

// Ensure K8SAuthResolver implements AuthResolver
var _ AuthResolver = (*K8SAuthResolver)(nil)

// K8SAuthResolver implements AuthResolver using Kubernetes.
// It resolves external auth config references by looking up MCPExternalAuthConfig
// resources in the Kubernetes cluster.
type K8SAuthResolver struct {
	k8sClient client.Client
	namespace string
}

// NewK8SAuthResolver creates a new Kubernetes-based auth resolver.
//
// Parameters:
//   - k8sClient: The Kubernetes client for fetching MCPExternalAuthConfig resources
//   - namespace: The namespace to look up resources in
func NewK8SAuthResolver(k8sClient client.Client, namespace string) *K8SAuthResolver {
	return &K8SAuthResolver{
		k8sClient: k8sClient,
		namespace: namespace,
	}
}

// ResolveExternalAuthConfig resolves an external auth config reference name
// to a concrete BackendAuthStrategy by looking up the MCPExternalAuthConfig
// resource in Kubernetes and converting it to the appropriate strategy type.
//
// This method reuses the existing DiscoverAndResolveAuth function from the
// converters package, which handles:
// 1. Fetching the MCPExternalAuthConfig resource
// 2. Converting it to a BackendAuthStrategy
// 3. Resolving any secret references to actual values
func (r *K8SAuthResolver) ResolveExternalAuthConfig(
	ctx context.Context,
	refName string,
) (*authtypes.BackendAuthStrategy, error) {
	if refName == "" {
		return nil, fmt.Errorf("external auth config ref name is empty")
	}

	// Create an ExternalAuthConfigRef to use with the existing discovery logic
	ref := &mcpv1alpha1.ExternalAuthConfigRef{Name: refName}

	// Use the existing DiscoverAndResolveAuth function which handles:
	// - Fetching the MCPExternalAuthConfig
	// - Converting to BackendAuthStrategy
	// - Resolving secrets
	strategy, err := converters.DiscoverAndResolveAuth(
		ctx,
		ref,
		r.namespace,
		r.k8sClient,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve external auth config %s/%s: %w", r.namespace, refName, err)
	}

	if strategy == nil {
		return nil, fmt.Errorf("external auth config %s/%s resolved to nil strategy", r.namespace, refName)
	}

	return strategy, nil
}
