package controllerutil

import (
	"context"
	"fmt"
	"hash/fnv"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/dump"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// CalculateConfigHash calculates a hash of any configuration spec using Kubernetes utilities.
// This function uses k8s.io/apimachinery/pkg/util/dump.ForHash which is designed for
// generating consistent string representations for hashing in Kubernetes.
// It then applies FNV-1a hash which is commonly used in Kubernetes for fast hashing.
// See: https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/controller_utils.go
func CalculateConfigHash[T any](spec T) string {
	// Use k8s.io/apimachinery/pkg/util/dump.ForHash which is designed for
	// generating consistent string representations for hashing in Kubernetes
	hashString := dump.ForHash(spec)

	// Use FNV-1a hash which is commonly used in Kubernetes for fast hashing
	// See: https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/controller_utils.go
	hasher := fnv.New32a()
	// Write returns an error only if the underlying writer returns an error,
	// which never happens for hash.Hash implementations
	//nolint:errcheck
	_, _ = hasher.Write([]byte(hashString))
	return fmt.Sprintf("%x", hasher.Sum32())
}

// FindReferencingMCPServers finds MCPServers in the given namespace that reference a config resource.
// The refExtractor function should return the config name from an MCPServer if it references the config,
// or nil if it doesn't reference any config of this type.
//
// Example usage for ToolConfig:
//
//	servers, err := FindReferencingMCPServers(ctx, client, namespace, configName,
//	    func(server *mcpv1alpha1.MCPServer) *string {
//	        if server.Spec.ToolConfigRef != nil {
//	            return &server.Spec.ToolConfigRef.Name
//	        }
//	        return nil
//	    })
func FindReferencingMCPServers(
	ctx context.Context,
	c client.Client,
	namespace string,
	configName string,
	refExtractor func(*mcpv1alpha1.MCPServer) *string,
) ([]mcpv1alpha1.MCPServer, error) {
	// List all MCPServers in the same namespace
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	if err := c.List(ctx, mcpServerList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}

	// Filter MCPServers that reference this config
	var referencingServers []mcpv1alpha1.MCPServer
	for _, server := range mcpServerList.Items {
		if refName := refExtractor(&server); refName != nil && *refName == configName {
			referencingServers = append(referencingServers, server)
		}
	}

	return referencingServers, nil
}

// GetToolConfigForMCPRemoteProxy fetches MCPToolConfig referenced by MCPRemoteProxy
func GetToolConfigForMCPRemoteProxy(
	ctx context.Context,
	c client.Client,
	proxy *mcpv1alpha1.MCPRemoteProxy,
) (*mcpv1alpha1.MCPToolConfig, error) {
	if proxy.Spec.ToolConfigRef == nil {
		return nil, fmt.Errorf("MCPRemoteProxy %s does not reference a MCPToolConfig", proxy.Name)
	}

	toolConfig := &mcpv1alpha1.MCPToolConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      proxy.Spec.ToolConfigRef.Name,
		Namespace: proxy.Namespace,
	}, toolConfig)

	if err != nil {
		return nil, fmt.Errorf("failed to get MCPToolConfig %s: %w", proxy.Spec.ToolConfigRef.Name, err)
	}

	return toolConfig, nil
}

// GetExternalAuthConfigForMCPRemoteProxy fetches MCPExternalAuthConfig referenced by MCPRemoteProxy
func GetExternalAuthConfigForMCPRemoteProxy(
	ctx context.Context,
	c client.Client,
	proxy *mcpv1alpha1.MCPRemoteProxy,
) (*mcpv1alpha1.MCPExternalAuthConfig, error) {
	if proxy.Spec.ExternalAuthConfigRef == nil {
		return nil, fmt.Errorf("MCPRemoteProxy %s does not reference a MCPExternalAuthConfig", proxy.Name)
	}

	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      proxy.Spec.ExternalAuthConfigRef.Name,
		Namespace: proxy.Namespace,
	}, externalAuthConfig)

	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s: %w", proxy.Spec.ExternalAuthConfigRef.Name, err)
	}

	return externalAuthConfig, nil
}

// GetExternalAuthConfigByName is a generic helper for fetching MCPExternalAuthConfig by name
func GetExternalAuthConfigByName(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
) (*mcpv1alpha1.MCPExternalAuthConfig, error) {
	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, externalAuthConfig)

	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s: %w", name, err)
	}

	return externalAuthConfig, nil
}
