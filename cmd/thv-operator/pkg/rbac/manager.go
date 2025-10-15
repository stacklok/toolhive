package rbac

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// Manager defines the interface for managing RBAC resources for MCPServers
type Manager interface {
	// EnsureRBACResources ensures all required RBAC resources exist for the MCPServer
	EnsureRBACResources(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error

	// GetProxyRunnerServiceAccountName returns the service account name for the proxy runner
	GetProxyRunnerServiceAccountName(mcpServerName string) string

	// GetMCPServerServiceAccountName returns the service account name for the MCP server
	GetMCPServerServiceAccountName(mcpServerName string) string
}

// ResourceFactory is a function type for creating Kubernetes resources
type ResourceFactory func() client.Object

// Config holds configuration for the RBAC manager
type Config struct {
	// DefaultRBACRules defines the default RBAC rules for proxy runners and MCP servers
	DefaultRBACRules []rbacv1.PolicyRule

	// Client is the Kubernetes client used for RBAC operations
	Client client.Client

	// Scheme is the runtime scheme for setting owner references
	Scheme *runtime.Scheme
}

// GetDefaultRBACRules returns the default RBAC rules for ToolHive ProxyRunner and MCP servers
func GetDefaultRBACRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{"apps"},
			Resources: []string{"statefulsets"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete", "apply"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"services"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete", "apply"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods/log"},
			Verbs:     []string{"get"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods/attach"},
			Verbs:     []string{"create", "get"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list", "watch"},
		},
	}
}

// ResourceType represents the type of RBAC resource
type ResourceType string

const (
	// ResourceTypeRole represents a Kubernetes Role
	ResourceTypeRole ResourceType = "Role"
	// ResourceTypeServiceAccount represents a Kubernetes ServiceAccount
	ResourceTypeServiceAccount ResourceType = "ServiceAccount"
	// ResourceTypeRoleBinding represents a Kubernetes RoleBinding
	ResourceTypeRoleBinding ResourceType = "RoleBinding"
)

// manager implements the Manager interface
type manager struct {
	client           client.Client
	scheme           *runtime.Scheme
	defaultRBACRules []rbacv1.PolicyRule
}

// NewManager creates a new RBAC manager
func NewManager(cfg Config) Manager {
	rules := cfg.DefaultRBACRules
	if rules == nil {
		rules = GetDefaultRBACRules()
	}

	return &manager{
		client:           cfg.Client,
		scheme:           cfg.Scheme,
		defaultRBACRules: rules,
	}
}

// EnsureRBACResources ensures all required RBAC resources exist for the MCPServer
func (m *manager) EnsureRBACResources(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	proxyRunnerSAName := m.GetProxyRunnerServiceAccountName(mcpServer.Name)

	// Ensure Role for proxy runner
	if err := m.ensureResource(ctx, mcpServer, ResourceTypeRole, func() client.Object {
		return &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerSAName,
				Namespace: mcpServer.Namespace,
			},
			Rules: m.defaultRBACRules,
		}
	}); err != nil {
		return fmt.Errorf("failed to ensure proxy runner Role: %w", err)
	}

	// Ensure ServiceAccount for proxy runner
	if err := m.ensureResource(ctx, mcpServer, ResourceTypeServiceAccount, func() client.Object {
		return &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerSAName,
				Namespace: mcpServer.Namespace,
			},
		}
	}); err != nil {
		return fmt.Errorf("failed to ensure proxy runner ServiceAccount: %w", err)
	}

	// Ensure RoleBinding for proxy runner
	if err := m.ensureResource(ctx, mcpServer, ResourceTypeRoleBinding, func() client.Object {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerSAName,
				Namespace: mcpServer.Namespace,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     proxyRunnerSAName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      proxyRunnerSAName,
					Namespace: mcpServer.Namespace,
				},
			},
		}
	}); err != nil {
		return fmt.Errorf("failed to ensure proxy runner RoleBinding: %w", err)
	}

	// If a service account is specified in the spec, we don't need to create one
	if mcpServer.Spec.ServiceAccount != nil {
		return nil
	}

	// Otherwise, create a service account for the MCP server itself
	mcpServerSAName := m.GetMCPServerServiceAccountName(mcpServer.Name)
	if err := m.ensureResource(ctx, mcpServer, ResourceTypeServiceAccount, func() client.Object {
		return &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerSAName,
				Namespace: mcpServer.Namespace,
			},
		}
	}); err != nil {
		return fmt.Errorf("failed to ensure MCP server ServiceAccount: %w", err)
	}

	return nil
}

// GetProxyRunnerServiceAccountName returns the service account name for the proxy runner
func (m *manager) GetProxyRunnerServiceAccountName(mcpServerName string) string {
	return fmt.Sprintf("%s-proxy-runner", mcpServerName)
}

// GetMCPServerServiceAccountName returns the service account name for the MCP server
func (m *manager) GetMCPServerServiceAccountName(mcpServerName string) string {
	return fmt.Sprintf("%s-sa", mcpServerName)
}

// ensureResource is a generic helper to ensure a Kubernetes resource exists and is up to date
func (m *manager) ensureResource(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	resourceType ResourceType,
	createResource ResourceFactory,
) error {
	current := createResource()
	objectKey := types.NamespacedName{Name: current.GetName(), Namespace: current.GetNamespace()}
	err := m.client.Get(ctx, objectKey, current)

	if errors.IsNotFound(err) {
		return m.createResource(ctx, mcpServer, resourceType, createResource)
	} else if err != nil {
		return fmt.Errorf("failed to get %s: %w", resourceType, err)
	}

	return m.updateResourceIfNeeded(ctx, mcpServer, resourceType, createResource, current)
}

// createResource creates a new RBAC resource
func (m *manager) createResource(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	resourceType ResourceType,
	createResource ResourceFactory,
) error {
	ctxLogger := log.FromContext(ctx)
	desired := createResource()

	if err := controllerutil.SetControllerReference(mcpServer, desired, m.scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference", "resourceType", resourceType)
		// Continue without owner reference
		// This will result in orphaned resources if the MCP server is deleted
		// the only way to cleanup is to delete the resources manually
		// This is a known limitation of the operator
		// TODO: Add cleanup logic to delete the resources based on labels.
		// TODO: This can be applied to other resources as well which requires a more
		// TODO: holistic approach to cleanup.
	}

	ctxLogger.Info(
		fmt.Sprintf("%s does not exist, creating", resourceType),
		"resource.Name", desired.GetName(),
		"resource.Namespace", desired.GetNamespace(),
	)

	if err := m.client.Create(ctx, desired); err != nil {
		return fmt.Errorf("failed to create %s: %w", resourceType, err)
	}

	ctxLogger.Info(
		fmt.Sprintf("%s created", resourceType),
		"resource.Name", desired.GetName(),
		"resource.Namespace", desired.GetNamespace(),
	)
	return nil
}

// updateResourceIfNeeded updates an RBAC resource if changes are detected
func (m *manager) updateResourceIfNeeded(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	resourceType ResourceType,
	createResource ResourceFactory,
	current client.Object,
) error {
	ctxLogger := log.FromContext(ctx)
	desired := createResource()

	if err := controllerutil.SetControllerReference(mcpServer, desired, m.scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference", "resourceType", resourceType)
		// Continue without owner reference
		// This will result in orphaned resources if the MCP server is deleted
		// the only way to cleanup is to delete the resources manually
		// This is a known limitation of the operator
		// TODO: Add cleanup logic to delete the resources based on labels.
		// TODO: This can be applied to other resources as well which requires a more
		// TODO: holistic approach to cleanup.

	}

	// Check if update is needed based on resource type
	needsUpdate := false
	switch resourceType {
	case ResourceTypeRole:
		currentRole := current.(*rbacv1.Role)
		desiredRole := desired.(*rbacv1.Role)
		needsUpdate = !reflect.DeepEqual(currentRole.Rules, desiredRole.Rules)
	case ResourceTypeRoleBinding:
		currentBinding := current.(*rbacv1.RoleBinding)
		desiredBinding := desired.(*rbacv1.RoleBinding)
		needsUpdate = !reflect.DeepEqual(currentBinding.RoleRef, desiredBinding.RoleRef) ||
			!reflect.DeepEqual(currentBinding.Subjects, desiredBinding.Subjects)
	case ResourceTypeServiceAccount:
		// ServiceAccounts typically don't need updates unless metadata changes
		needsUpdate = false
	}

	if needsUpdate {
		// Preserve the ResourceVersion for update
		desired.SetResourceVersion(current.GetResourceVersion())

		ctxLogger.Info(
			fmt.Sprintf("%s exists but needs update", resourceType),
			"resource.Name", desired.GetName(),
			"resource.Namespace", desired.GetNamespace(),
		)

		if err := m.client.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update %s: %w", resourceType, err)
		}

		ctxLogger.Info(
			fmt.Sprintf("%s updated", resourceType),
			"resource.Name", desired.GetName(),
			"resource.Namespace", desired.GetNamespace(),
		)
	}

	return nil
}
