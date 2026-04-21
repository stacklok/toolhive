// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// GetToolConfigForMCPServer retrieves the MCPToolConfig referenced by an MCPServer
func GetToolConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	mcpServer *mcpv1beta1.MCPServer,
) (*mcpv1beta1.MCPToolConfig, error) {
	if mcpServer.Spec.ToolConfigRef == nil {
		// We throw an error because in this case you assume there is a ToolConfig
		// but there isn't one referenced.
		return nil, fmt.Errorf("MCPServer %s does not reference a MCPToolConfig", mcpServer.Name)
	}

	toolConfig := &mcpv1beta1.MCPToolConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      mcpServer.Spec.ToolConfigRef.Name,
		Namespace: mcpServer.Namespace, // Same namespace as MCPServer
	}, toolConfig)

	if err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("MCPToolConfig %s not found in namespace %s",
				mcpServer.Spec.ToolConfigRef.Name, mcpServer.Namespace)
		}
		return nil, fmt.Errorf("failed to get MCPToolConfig: %w", err)
	}

	return toolConfig, nil
}
