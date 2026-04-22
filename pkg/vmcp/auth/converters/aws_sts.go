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

// AwsStsConverter converts MCPExternalAuthConfig AWSSts to vMCP aws_sts strategy.
type AwsStsConverter struct{}

// StrategyType returns the vMCP strategy type identifier for AWS STS auth.
func (*AwsStsConverter) StrategyType() string {
	return authtypes.StrategyTypeAwsSts
}

// ConvertToStrategy converts an MCPExternalAuthConfig with type "awsSts" to a BackendAuthStrategy.
func (*AwsStsConverter) ConvertToStrategy(
	externalAuth *mcpv1beta1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	if externalAuth.Spec.AWSSts == nil {
		return nil, fmt.Errorf("aws sts config is nil")
	}

	src := externalAuth.Spec.AWSSts

	roleMappings := make([]authtypes.RoleMapping, len(src.RoleMappings))
	for i, m := range src.RoleMappings {
		roleMappings[i] = authtypes.RoleMapping{
			Claim:    m.Claim,
			Matcher:  m.Matcher,
			RoleArn:  m.RoleArn,
			Priority: m.Priority,
		}
	}

	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeAwsSts,
		AwsSts: &authtypes.AwsStsConfig{
			Region:            src.Region,
			Service:           src.Service,
			FallbackRoleArn:   src.FallbackRoleArn,
			RoleMappings:      roleMappings,
			RoleClaim:         src.RoleClaim,
			SessionDuration:   src.SessionDuration,
			SessionNameClaim:  src.SessionNameClaim,
			TokenProviderName: src.TokenProviderName,
		},
	}, nil
}

// ResolveSecrets is a no-op for AWS STS strategy since credentials are obtained
// at runtime via the pod's IAM role (IRSA or instance profile); no K8s secrets are needed.
func (*AwsStsConverter) ResolveSecrets(
	_ context.Context,
	_ *mcpv1beta1.MCPExternalAuthConfig,
	_ client.Client,
	_ string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	return strategy, nil
}
