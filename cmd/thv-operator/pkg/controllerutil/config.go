// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

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

// CompareWorkloadRefs compares two WorkloadReference values by Kind then Name.
// Suitable for use with slices.SortFunc.
func CompareWorkloadRefs(a, b mcpv1alpha1.WorkloadReference) int {
	if a.Kind != b.Kind {
		return strings.Compare(a.Kind, b.Kind)
	}
	return strings.Compare(a.Name, b.Name)
}

// SortWorkloadRefs sorts a WorkloadReference slice by Kind then Name for deterministic ordering.
// This prevents unnecessary API server writes when the same set of workloads is discovered
// in a different list order across reconcile runs.
func SortWorkloadRefs(refs []mcpv1alpha1.WorkloadReference) {
	slices.SortFunc(refs, CompareWorkloadRefs)
}

// WorkloadRefsEqual reports whether two WorkloadReference slices contain the same entries.
// Both slices must already be sorted (use SortWorkloadRefs) for correct results.
func WorkloadRefsEqual(a, b []mcpv1alpha1.WorkloadReference) bool {
	return slices.EqualFunc(a, b, func(x, y mcpv1alpha1.WorkloadReference) bool {
		return x.Kind == y.Kind && x.Name == y.Name
	})
}

// FindWorkloadRefsFromMCPServers returns a sorted list of WorkloadReference for MCPServers
// in the given namespace that reference a config identified by configName.
// The refExtractor determines which spec field contains the config reference name.
func FindWorkloadRefsFromMCPServers(
	ctx context.Context,
	c client.Client,
	namespace string,
	configName string,
	refExtractor func(*mcpv1alpha1.MCPServer) *string,
) ([]mcpv1alpha1.WorkloadReference, error) {
	servers, err := FindReferencingMCPServers(ctx, c, namespace, configName, refExtractor)
	if err != nil {
		return nil, err
	}
	refs := make([]mcpv1alpha1.WorkloadReference, 0, len(servers))
	for _, server := range servers {
		refs = append(refs, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: server.Name})
	}
	SortWorkloadRefs(refs)
	return refs, nil
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
