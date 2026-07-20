// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// UpstreamInjectConverter converts MCPExternalAuthConfig UpstreamInject to vMCP upstream_inject strategy.
// This converter handles the case where an upstream IDP token obtained by the embedded
// authorization server is injected into requests to the backend service.
type UpstreamInjectConverter struct{}

// StrategyType returns the vMCP strategy type identifier for upstream inject auth.
func (*UpstreamInjectConverter) StrategyType() string {
	return authtypes.StrategyTypeUpstreamInject
}

// ConvertToStrategy converts an MCPExternalAuthConfig with type "upstreamInject" to a BackendAuthStrategy.
// It maps the CRD's UpstreamInjectSpec.ProviderName to the runtime UpstreamInjectConfig.ProviderName.
func (*UpstreamInjectConverter) ConvertToStrategy(
	externalAuth *mcpv1beta1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	if externalAuth.Spec.UpstreamInject == nil {
		return nil, fmt.Errorf("upstream inject config is nil")
	}

	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeUpstreamInject,
		UpstreamInject: &authtypes.UpstreamInjectConfig{
			ProviderName: externalAuth.Spec.UpstreamInject.ProviderName,
		},
	}, nil
}

// ResolveSecrets is a no-op for upstream inject strategy since there are no secrets to resolve.
// The upstream IDP token is obtained at runtime by the embedded authorization server.
func (*UpstreamInjectConverter) ResolveSecrets(
	_ context.Context,
	_ *mcpv1beta1.MCPExternalAuthConfig,
	_ client.Client,
	_ string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	// No secrets to resolve for upstream inject strategy
	return strategy, nil
}
