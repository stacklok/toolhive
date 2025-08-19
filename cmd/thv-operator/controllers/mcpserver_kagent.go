package controllers

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// kagentToolServerGVK defines the GroupVersionKind for kagent ToolServer
var kagentToolServerGVK = schema.GroupVersionKind{
	Group:   "kagent.dev",
	Version: "v1alpha1",
	Kind:    "ToolServer",
}

// Constants for kagent config types
const (
	kagentConfigTypeSSE            = "sse"
	kagentConfigTypeStreamableHTTP = "streamableHttp"
)

// isKagentIntegrationEnabled checks if kagent integration is enabled via environment variable
func isKagentIntegrationEnabled() bool {
	enabled := os.Getenv("KAGENT_INTEGRATION_ENABLED")
	if enabled == "" {
		return false
	}
	result, err := strconv.ParseBool(enabled)
	if err != nil {
		return false
	}
	return result
}

// ensureKagentToolServer ensures a kagent ToolServer resource exists for the ToolHive MCPServer
func (r *MCPServerReconciler) ensureKagentToolServer(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	logger := log.FromContext(ctx)

	// Check if kagent integration is enabled
	if !isKagentIntegrationEnabled() {
		// If not enabled, ensure any existing kagent ToolServer is deleted
		return r.deleteKagentToolServer(ctx, mcpServer)
	}

	// Create the kagent ToolServer object
	kagentToolServer := r.createKagentToolServerObject(mcpServer)

	// Check if the kagent ToolServer already exists
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(kagentToolServerGVK)
	err := r.Get(ctx, types.NamespacedName{
		Name:      kagentToolServer.GetName(),
		Namespace: kagentToolServer.GetNamespace(),
	}, existing)

	if errors.IsNotFound(err) {
		// Create the kagent ToolServer
		logger.Info("Creating kagent ToolServer",
			"name", kagentToolServer.GetName(),
			"namespace", kagentToolServer.GetNamespace())
		if err := r.Create(ctx, kagentToolServer); err != nil {
			return fmt.Errorf("failed to create kagent ToolServer: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get kagent ToolServer: %w", err)
	}

	// Update the kagent ToolServer if needed
	existingSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	desiredSpec, _, _ := unstructured.NestedMap(kagentToolServer.Object, "spec")

	if !equality.Semantic.DeepEqual(existingSpec, desiredSpec) {
		logger.Info("Updating kagent ToolServer",
			"name", kagentToolServer.GetName(),
			"namespace", kagentToolServer.GetNamespace())
		existing.Object["spec"] = kagentToolServer.Object["spec"]
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update kagent ToolServer: %w", err)
		}
	}

	return nil
}

// deleteKagentToolServer deletes the kagent ToolServer if it exists
func (r *MCPServerReconciler) deleteKagentToolServer(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	logger := log.FromContext(ctx)

	kagentToolServer := &unstructured.Unstructured{}
	kagentToolServer.SetGroupVersionKind(kagentToolServerGVK)
	kagentToolServer.SetName(fmt.Sprintf("toolhive-%s", mcpServer.Name))
	kagentToolServer.SetNamespace(mcpServer.Namespace)

	err := r.Get(ctx, types.NamespacedName{
		Name:      kagentToolServer.GetName(),
		Namespace: kagentToolServer.GetNamespace(),
	}, kagentToolServer)

	if errors.IsNotFound(err) {
		// Already deleted
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get kagent ToolServer for deletion: %w", err)
	}

	logger.Info("Deleting kagent ToolServer",
		"name", kagentToolServer.GetName(),
		"namespace", kagentToolServer.GetNamespace())
	if err := r.Delete(ctx, kagentToolServer); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete kagent ToolServer: %w", err)
	}

	return nil
}

// createKagentToolServerObject creates an unstructured kagent ToolServer object
func (*MCPServerReconciler) createKagentToolServerObject(mcpServer *mcpv1alpha1.MCPServer) *unstructured.Unstructured {
	kagentToolServer := &unstructured.Unstructured{}
	kagentToolServer.SetGroupVersionKind(kagentToolServerGVK)
	kagentToolServer.SetName(fmt.Sprintf("toolhive-%s", mcpServer.Name))
	kagentToolServer.SetNamespace(mcpServer.Namespace)

	// Build the service URL for the ToolHive MCP server
	serviceName := createServiceName(mcpServer.Name)
	serviceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
		serviceName, mcpServer.Namespace, mcpServer.Spec.Port)

	// Determine the config type based on ToolHive transport
	var configType string
	var config map[string]interface{}

	switch mcpServer.Spec.Transport {
	case "sse":
		configType = kagentConfigTypeSSE
		config = map[string]interface{}{
			kagentConfigTypeSSE: map[string]interface{}{
				"url": serviceURL,
			},
		}
	case "streamable-http":
		configType = kagentConfigTypeStreamableHTTP
		config = map[string]interface{}{
			kagentConfigTypeStreamableHTTP: map[string]interface{}{
				"url": serviceURL,
			},
		}
	default:
		// For stdio or any other transport, default to SSE
		// since ToolHive exposes everything via HTTP
		configType = kagentConfigTypeSSE
		config = map[string]interface{}{
			kagentConfigTypeSSE: map[string]interface{}{
				"url": serviceURL,
			},
		}
	}

	config["type"] = configType

	// Build the spec
	spec := map[string]interface{}{
		"description": fmt.Sprintf("ToolHive MCP Server: %s", mcpServer.Name),
		"config":      config,
	}

	kagentToolServer.Object = map[string]interface{}{
		"apiVersion": "kagent.dev/v1alpha1",
		"kind":       "ToolServer",
		"metadata": map[string]interface{}{
			"name":      kagentToolServer.GetName(),
			"namespace": kagentToolServer.GetNamespace(),
			"labels": map[string]interface{}{
				"toolhive.stacklok.dev/managed-by": "toolhive-operator",
				"toolhive.stacklok.dev/mcpserver":  mcpServer.Name,
			},
			"ownerReferences": []interface{}{
				map[string]interface{}{
					"apiVersion":         "toolhive.stacklok.dev/v1alpha1",
					"kind":               "MCPServer",
					"name":               mcpServer.Name,
					"uid":                string(mcpServer.UID),
					"controller":         true,
					"blockOwnerDeletion": true,
				},
			},
		},
		"spec": spec,
	}

	return kagentToolServer
}
