package converters

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// UnauthenticatedConverter converts unauthenticated external auth configs to BackendAuthStrategy.
// This converter handles the case where no authentication is required for a backend.
type UnauthenticatedConverter struct{}

// StrategyType returns the vMCP strategy type identifier for unauthenticated auth.
func (*UnauthenticatedConverter) StrategyType() string {
	return authtypes.StrategyTypeUnauthenticated
}

// ConvertToStrategy converts an MCPExternalAuthConfig with type "unauthenticated" to a BackendAuthStrategy.
// Since unauthenticated requires no configuration, this simply returns a strategy with the correct type.
func (*UnauthenticatedConverter) ConvertToStrategy(
	_ *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeUnauthenticated,
		// No additional fields needed for unauthenticated
	}, nil
}

// ResolveSecrets is a no-op for unauthenticated strategy since there are no secrets to resolve.
func (*UnauthenticatedConverter) ResolveSecrets(
	_ context.Context,
	_ *mcpv1alpha1.MCPExternalAuthConfig,
	_ client.Client,
	_ string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	// No secrets to resolve for unauthenticated strategy
	return strategy, nil
}
