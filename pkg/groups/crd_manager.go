// Package groups provides functionality for managing logical groupings of MCP servers.
// This file contains the CRD-based implementation for Kubernetes environments.
package groups

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	thverrors "github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/validation"
)

// crdManager implements the Manager interface using Kubernetes CRDs
type crdManager struct {
	k8sClient client.Client
	namespace string
}

// NewCRDManager creates a new CRD-based group manager
func NewCRDManager(k8sClient client.Client, namespace string) Manager {
	return &crdManager{
		k8sClient: k8sClient,
		namespace: namespace,
	}
}

// Create creates a new group with the specified name.
func (m *crdManager) Create(ctx context.Context, name string) error {
	// Validate group name
	if err := validation.ValidateGroupName(name); err != nil {
		return thverrors.NewInvalidArgumentError(err.Error(), err)
	}

	// Check if group already exists
	exists, err := m.Exists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if exists {
		return thverrors.NewGroupAlreadyExistsError(fmt.Sprintf("group '%s' already exists", name), nil)
	}

	// Create the MCPGroup CRD
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.namespace,
		},
		Spec: mcpv1alpha1.MCPGroupSpec{},
	}

	if err := m.k8sClient.Create(ctx, mcpGroup); err != nil {
		return fmt.Errorf("failed to create MCPGroup: %w", err)
	}

	logger.Infof("Created MCPGroup '%s' in namespace '%s'", name, m.namespace)
	return nil
}

// Get retrieves a group by name.
func (m *crdManager) Get(ctx context.Context, name string) (*Group, error) {
	mcpGroup := &mcpv1alpha1.MCPGroup{}
	err := m.k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: m.namespace,
	}, mcpGroup)

	if err != nil {
		if errors.IsNotFound(err) {
			return nil, thverrors.NewGroupNotFoundError(fmt.Sprintf("group '%s' not found", name), err)
		}
		return nil, fmt.Errorf("failed to get MCPGroup: %w", err)
	}

	return mcpGroupToGroup(mcpGroup), nil
}

// List returns all groups.
func (m *crdManager) List(ctx context.Context) ([]*Group, error) {
	mcpGroupList := &mcpv1alpha1.MCPGroupList{}
	err := m.k8sClient.List(ctx, mcpGroupList, client.InNamespace(m.namespace))
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPGroups: %w", err)
	}

	groups := mcpGroupListToGroups(mcpGroupList)

	// Sort groups alphanumerically by name
	sort.Slice(groups, func(i, j int) bool {
		return strings.Compare(groups[i].Name, groups[j].Name) < 0
	})

	return groups, nil
}

// Delete removes a group by name.
func (m *crdManager) Delete(ctx context.Context, name string) error {
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.namespace,
		},
	}

	err := m.k8sClient.Delete(ctx, mcpGroup)
	if err != nil {
		if errors.IsNotFound(err) {
			return thverrors.NewGroupNotFoundError(fmt.Sprintf("group '%s' not found", name), err)
		}
		return fmt.Errorf("failed to delete MCPGroup: %w", err)
	}

	logger.Infof("Deleted MCPGroup '%s' from namespace '%s'", name, m.namespace)
	return nil
}

// Exists checks if a group with the specified name exists.
func (m *crdManager) Exists(ctx context.Context, name string) (bool, error) {
	mcpGroup := &mcpv1alpha1.MCPGroup{}
	err := m.k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: m.namespace,
	}, mcpGroup)

	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if MCPGroup exists: %w", err)
	}

	return true, nil
}

// In Kubernetes, client configuration management is not applicable, so this is a no-op.
func (*crdManager) RegisterClients(context.Context, []string, []string) error {
	return nil
}

// In Kubernetes, client configuration management is not applicable, so this is a no-op.
func (*crdManager) UnregisterClients(context.Context, []string, []string) error {
	return nil
}

// mcpGroupListToGroups converts an MCPGroupList to a slice of Groups
func mcpGroupListToGroups(mcpGroupList *mcpv1alpha1.MCPGroupList) []*Group {
	groups := make([]*Group, 0, len(mcpGroupList.Items))
	for i := range mcpGroupList.Items {
		groups = append(groups, mcpGroupToGroup(&mcpGroupList.Items[i]))
	}
	return groups
}

// mcpGroupToGroup converts an MCPGroup CRD to a Group
func mcpGroupToGroup(mcpGroup *mcpv1alpha1.MCPGroup) *Group {
	// In Kubernetes, RegisteredClients is not applicable - always return empty slice
	return &Group{
		Name:              mcpGroup.Name,
		RegisteredClients: []string{},
	}
}
