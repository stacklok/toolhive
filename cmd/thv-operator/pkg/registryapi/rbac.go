// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

import (
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/rbac"
)

// registryAPIRBACRules defines the RBAC policy rules for the registry API server.
// These rules allow the registry API to:
// - Read MCP resources for registry discovery
// - Read Services for HTTPRoute traversal and endpoint resolution
// - Read Gateway API resources for ingress configuration
// - Perform leader election using configmaps and leases
//
// Note: Using namespace-scoped Role limits visibility to resources within the same namespace.
// If cross-namespace discovery is needed, consider using ClusterRole instead.
var registryAPIRBACRules = []rbacv1.PolicyRule{
	// MCP resource discovery
	{
		APIGroups: []string{"toolhive.stacklok.dev"},
		Resources: []string{"mcpservers", "mcpremoteproxies", "virtualmcpservers"},
		Verbs:     []string{"get", "list", "watch"},
	},
	// Service discovery for endpoint resolution
	{
		APIGroups: []string{""},
		Resources: []string{"services"},
		Verbs:     []string{"get", "list", "watch"},
	},
	// Gateway API for ingress configuration
	{
		APIGroups: []string{"gateway.networking.k8s.io"},
		Resources: []string{"httproutes", "gateways"},
		Verbs:     []string{"get", "list", "watch"},
	},
	// Leader election using ConfigMaps
	{
		APIGroups: []string{""},
		Resources: []string{"configmaps"},
		Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
	},
	// Leader election using Leases (preferred method)
	{
		APIGroups: []string{"coordination.k8s.io"},
		Resources: []string{"leases"},
		Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
	},
	// Event creation for leader election status
	{
		APIGroups: []string{""},
		Resources: []string{"events"},
		Verbs:     []string{"create", "patch"},
	},
}

// ensureRBACResources ensures that the RBAC resources (ServiceAccount, Role, RoleBinding)
// are in place for the registry API server.
//
// All resources are namespace-scoped and use owner references for automatic cleanup
// when the MCPRegistry is deleted.
func (m *manager) ensureRBACResources(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
) error {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)
	ctxLogger.Info("Ensuring RBAC resources for registry API")

	rbacClient := rbac.NewClient(m.client, m.scheme)
	resourceName := GetServiceAccountName(mcpRegistry)
	labels := labelsForRegistryAPI(mcpRegistry, resourceName)

	if err := rbacClient.EnsureRBACResources(ctx, rbac.EnsureRBACResourcesParams{
		Name:      resourceName,
		Namespace: mcpRegistry.Namespace,
		Rules:     registryAPIRBACRules,
		Owner:     mcpRegistry,
		Labels:    labels,
	}); err != nil {
		return err
	}

	ctxLogger.Info("Successfully ensured RBAC resources for registry API")
	return nil
}
