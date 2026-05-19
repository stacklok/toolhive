// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// OBOConverter is the default StrategyConverter for ExternalAuthTypeOBO.
// Every method returns an error wrapping obo.ErrEnterpriseRequired. An
// out-of-tree build replaces it by calling DefaultRegistry().Register(...)
// once during init().
type OBOConverter struct{}

// StrategyType returns the vMCP strategy identifier for OBO.
func (*OBOConverter) StrategyType() string { return authtypes.StrategyTypeOBO }

// ConvertToStrategy returns an error wrapping obo.ErrEnterpriseRequired.
func (*OBOConverter) ConvertToStrategy(
	_ *mcpv1beta1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	return nil, fmt.Errorf("vMCP OBO converter: %w", obo.ErrEnterpriseRequired)
}

// ResolveSecrets returns an error wrapping obo.ErrEnterpriseRequired.
func (*OBOConverter) ResolveSecrets(
	_ context.Context,
	_ *mcpv1beta1.MCPExternalAuthConfig,
	_ client.Client,
	_ string,
	_ *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	return nil, fmt.Errorf("vMCP OBO converter: %w", obo.ErrEnterpriseRequired)
}
