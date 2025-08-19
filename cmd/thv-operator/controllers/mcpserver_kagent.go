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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	kagentAPIVersionV1Alpha1 = "v1alpha1"
	kagentAPIVersionV1Alpha2 = "v1alpha2"
)

// kagentToolServerGVK defines the GroupVersionKind for kagent v1alpha1 ToolServer
var kagentToolServerGVK = schema.GroupVersionKind{
	Group:   "kagent.dev",
	Version: kagentAPIVersionV1Alpha1,
	Kind:    "ToolServer",
}

// kagentRemoteMCPServerGVK defines the GroupVersionKind for kagent v1alpha2 RemoteMCPServer
var kagentRemoteMCPServerGVK = schema.GroupVersionKind{
	Group:   "kagent.dev",
	Version: kagentAPIVersionV1Alpha2,
	Kind:    "RemoteMCPServer",
}

// Constants for kagent config types
const (
	// v1alpha1 config types
	kagentConfigTypeSSE            = "sse"
	kagentConfigTypeStreamableHTTP = "streamableHttp"

	// v1alpha2 protocol types
	kagentProtocolSSE            = "SSE"
	kagentProtocolStreamableHTTP = "STREAMABLE_HTTP"

	// Environment variable for kagent API version preference
	kagentAPIVersionEnv = "KAGENT_API_VERSION"
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

// getPreferredKagentAPIVersion returns the preferred kagent API version
// Defaults to v1alpha1 for backward compatibility, but can be overridden
// via KAGENT_API_VERSION environment variable
func getPreferredKagentAPIVersion() string {
	version := os.Getenv(kagentAPIVersionEnv)
	if version == "v1alpha2" {
		return "v1alpha2"
	}
	// Default to v1alpha1 for backward compatibility
	return "v1alpha1"
}

// detectKagentAPIVersion detects which kagent API version is available in the cluster
func (r *MCPServerReconciler) detectKagentAPIVersion(ctx context.Context) string {
	// First check if user has a preference
	preferred := getPreferredKagentAPIVersion()

	// Try to list resources of the preferred version to see if it's available
	if preferred == kagentAPIVersionV1Alpha2 {
		// Try v1alpha2 RemoteMCPServer
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "kagent.dev",
			Version: kagentAPIVersionV1Alpha2,
			Kind:    "RemoteMCPServerList",
		})

		// We just want to check if the API exists, limit to 1 item
		if err := r.List(ctx, list, &client.ListOptions{Limit: 1}); err == nil {
			return kagentAPIVersionV1Alpha2
		}
	}

	// Try v1alpha1 ToolServer
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kagent.dev",
		Version: kagentAPIVersionV1Alpha1,
		Kind:    "ToolServerList",
	})

	if err := r.List(ctx, list, &client.ListOptions{Limit: 1}); err == nil {
		return kagentAPIVersionV1Alpha1
	}

	// If neither works, return the preferred version anyway
	// The actual resource creation will fail with a clear error
	return preferred
}

// ensureKagentToolServer ensures a kagent resource exists for the ToolHive MCPServer
// It automatically detects and uses the appropriate kagent API version
func (r *MCPServerReconciler) ensureKagentToolServer(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	logger := log.FromContext(ctx)

	// Check if kagent integration is enabled
	if !isKagentIntegrationEnabled() {
		// If not enabled, ensure any existing kagent resources are deleted
		return r.deleteKagentToolServer(ctx, mcpServer)
	}

	// Detect which kagent API version to use
	apiVersion := r.detectKagentAPIVersion(ctx)
	logger.V(1).Info("Using kagent API version", "version", apiVersion)

	// Create the appropriate kagent resource based on API version
	var kagentResource *unstructured.Unstructured
	var gvk schema.GroupVersionKind

	if apiVersion == kagentAPIVersionV1Alpha2 {
		kagentResource = r.createKagentRemoteMCPServerObject(mcpServer)
		gvk = kagentRemoteMCPServerGVK
	} else {
		kagentResource = r.createKagentToolServerObject(mcpServer)
		gvk = kagentToolServerGVK
	}

	// Check if the kagent resource already exists
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)
	err := r.Get(ctx, types.NamespacedName{
		Name:      kagentResource.GetName(),
		Namespace: kagentResource.GetNamespace(),
	}, existing)

	if errors.IsNotFound(err) {
		// Create the kagent resource
		logger.Info("Creating kagent resource",
			"kind", gvk.Kind,
			"version", gvk.Version,
			"name", kagentResource.GetName(),
			"namespace", kagentResource.GetNamespace())
		if err := r.Create(ctx, kagentResource); err != nil {
			return fmt.Errorf("failed to create kagent %s: %w", gvk.Kind, err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get kagent %s: %w", gvk.Kind, err)
	}

	// Update the kagent resource if needed
	existingSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	desiredSpec, _, _ := unstructured.NestedMap(kagentResource.Object, "spec")

	if !equality.Semantic.DeepEqual(existingSpec, desiredSpec) {
		logger.Info("Updating kagent resource",
			"kind", gvk.Kind,
			"version", gvk.Version,
			"name", kagentResource.GetName(),
			"namespace", kagentResource.GetNamespace())
		existing.Object["spec"] = kagentResource.Object["spec"]
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update kagent %s: %w", gvk.Kind, err)
		}
	}

	return nil
}

// deleteKagentToolServer deletes any kagent resources (v1alpha1 or v1alpha2) if they exist
func (r *MCPServerReconciler) deleteKagentToolServer(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	logger := log.FromContext(ctx)
	resourceName := fmt.Sprintf("toolhive-%s", mcpServer.Name)

	// Try to delete v1alpha1 ToolServer
	toolServer := &unstructured.Unstructured{}
	toolServer.SetGroupVersionKind(kagentToolServerGVK)
	toolServer.SetName(resourceName)
	toolServer.SetNamespace(mcpServer.Namespace)

	err := r.Get(ctx, types.NamespacedName{
		Name:      toolServer.GetName(),
		Namespace: toolServer.GetNamespace(),
	}, toolServer)

	if err == nil {
		logger.Info("Deleting kagent ToolServer",
			"name", toolServer.GetName(),
			"namespace", toolServer.GetNamespace())
		if err := r.Delete(ctx, toolServer); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete kagent ToolServer: %w", err)
		}
	}

	// Try to delete v1alpha2 RemoteMCPServer
	remoteMCPServer := &unstructured.Unstructured{}
	remoteMCPServer.SetGroupVersionKind(kagentRemoteMCPServerGVK)
	remoteMCPServer.SetName(resourceName)
	remoteMCPServer.SetNamespace(mcpServer.Namespace)

	err = r.Get(ctx, types.NamespacedName{
		Name:      remoteMCPServer.GetName(),
		Namespace: remoteMCPServer.GetNamespace(),
	}, remoteMCPServer)

	if err == nil {
		logger.Info("Deleting kagent RemoteMCPServer",
			"name", remoteMCPServer.GetName(),
			"namespace", remoteMCPServer.GetNamespace())
		if err := r.Delete(ctx, remoteMCPServer); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete kagent RemoteMCPServer: %w", err)
		}
	}

	return nil
}

// createKagentToolServerObject creates an unstructured kagent v1alpha1 ToolServer object
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

// createKagentRemoteMCPServerObject creates an unstructured kagent v1alpha2 RemoteMCPServer object
func (*MCPServerReconciler) createKagentRemoteMCPServerObject(mcpServer *mcpv1alpha1.MCPServer) *unstructured.Unstructured {
	remoteMCPServer := &unstructured.Unstructured{}
	remoteMCPServer.SetGroupVersionKind(kagentRemoteMCPServerGVK)
	remoteMCPServer.SetName(fmt.Sprintf("toolhive-%s", mcpServer.Name))
	remoteMCPServer.SetNamespace(mcpServer.Namespace)

	// Build the service URL for the ToolHive MCP server
	serviceName := createServiceName(mcpServer.Name)
	serviceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
		serviceName, mcpServer.Namespace, mcpServer.Spec.Port)

	// Determine the protocol based on ToolHive transport
	var protocol string
	switch mcpServer.Spec.Transport {
	case "sse":
		protocol = kagentProtocolSSE
	case "streamable-http":
		protocol = kagentProtocolStreamableHTTP
	default:
		// For stdio or any other transport, default to SSE
		// since ToolHive exposes everything via HTTP
		protocol = kagentProtocolSSE
	}

	// Build the spec for v1alpha2 RemoteMCPServer
	spec := map[string]interface{}{
		"description": fmt.Sprintf("ToolHive MCP Server: %s", mcpServer.Name),
		"url":         serviceURL,
		"protocol":    protocol,
		// terminateOnClose defaults to true which is what we want
	}

	// Add timeout if needed (optional, using default for now)
	// spec["timeout"] = "30s"

	remoteMCPServer.Object = map[string]interface{}{
		"apiVersion": "kagent.dev/v1alpha2",
		"kind":       "RemoteMCPServer",
		"metadata": map[string]interface{}{
			"name":      remoteMCPServer.GetName(),
			"namespace": remoteMCPServer.GetNamespace(),
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

	return remoteMCPServer
}
