package controllerutil

import (
	"context"
	"fmt"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetToolConfigForMCPServer retrieves the MCPToolConfig referenced by an MCPServer
func GetToolConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	mcpServer *mcpv1alpha1.MCPServer,
) (*mcpv1alpha1.MCPToolConfig, error) {
	if mcpServer.Spec.ToolConfigRef == nil {
		// We throw an error because in this case you assume there is a ToolConfig
		// but there isn't one referenced.
		return nil, fmt.Errorf("MCPServer %s does not reference a MCPToolConfig", mcpServer.Name)
	}

	toolConfig := &mcpv1alpha1.MCPToolConfig{}
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
