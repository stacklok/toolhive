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

// XAAConverter converts MCPExternalAuthConfig with xaa type to the runtime XAAConfig.
// XAA implements draft-ietf-oauth-identity-assertion-authz-grant (ID-JAG), an IETF draft
// for cross-application access using Identity Assertion JWT Authorization Grants.
type XAAConverter struct{}

// StrategyType returns the vMCP strategy type identifier for XAA auth.
func (*XAAConverter) StrategyType() string {
	return authtypes.StrategyTypeXAA
}

// ConvertToStrategy converts an MCPExternalAuthConfig with type "xaa" to a BackendAuthStrategy.
// Secret references are not resolved here; they are resolved by ResolveSecrets.
func (*XAAConverter) ConvertToStrategy(
	externalAuth *mcpv1beta1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	xaa := externalAuth.Spec.XAA
	if xaa == nil {
		return nil, fmt.Errorf("xaa config is nil")
	}

	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeXAA,
		XAA: &authtypes.XAAConfig{
			IDPTokenURL:            xaa.IDPTokenURL,
			IDPClientID:            xaa.IDPClientID,
			TargetTokenURL:         xaa.TargetTokenURL,
			InsecureTargetTokenURL: xaa.InsecureTargetTokenURL,
			TargetClientID:         xaa.TargetClientID,
			TargetAudience:         xaa.TargetAudience,
			TargetResource:         xaa.TargetResource,
			Scopes:                 xaa.Scopes,
			SubjectProviderName:    xaa.SubjectProviderName,
			SubjectTokenType:       xaa.SubjectTokenType,
		},
	}, nil
}

// ResolveSecrets fetches the IdP and target client secrets from Kubernetes and sets them
// in the strategy. This is used in discovered auth mode where secrets cannot be mounted
// as environment variables because the vMCP pod does not know about backend auth configs
// at pod creation time.
func (*XAAConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1beta1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	if strategy == nil || strategy.XAA == nil {
		return nil, fmt.Errorf("xaa strategy is nil")
	}

	xaa := externalAuth.Spec.XAA
	if xaa == nil {
		return nil, fmt.Errorf("xaa config is nil")
	}

	// Resolve IdP client secret
	if xaa.IDPClientSecretRef != nil {
		secretValue, err := resolveSecretKeyRef(ctx, k8sClient, namespace, xaa.IDPClientSecretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve IdP client secret: %w", err)
		}
		strategy.XAA.IDPClientSecret = secretValue
	}

	// Resolve target client secret
	if xaa.TargetClientSecretRef != nil {
		secretValue, err := resolveSecretKeyRef(ctx, k8sClient, namespace, xaa.TargetClientSecretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve target client secret: %w", err)
		}
		strategy.XAA.TargetClientSecret = secretValue
	}

	return strategy, nil
}
