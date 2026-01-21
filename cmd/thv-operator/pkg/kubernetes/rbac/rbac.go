// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package rbac

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// RBACAPIGroup is the Kubernetes API group for RBAC resources
	RBACAPIGroup = "rbac.authorization.k8s.io"
)

// OperationResult is an alias for controllerutil.OperationResult for convenience.
type OperationResult = controllerutil.OperationResult

// Client provides convenience methods for working with Kubernetes RBAC resources.
type Client struct {
	client client.Client
	scheme *runtime.Scheme
}

// NewClient creates a new rbac Client instance.
// The scheme is required for operations that need to set owner references.
func NewClient(c client.Client, scheme *runtime.Scheme) *Client {
	return &Client{
		client: c,
		scheme: scheme,
	}
}

// GetServiceAccount retrieves a Kubernetes ServiceAccount by name and namespace.
// Returns the service account if found, or an error if not found or on failure.
func (c *Client) GetServiceAccount(ctx context.Context, name, namespace string) (*corev1.ServiceAccount, error) {
	serviceAccount := &corev1.ServiceAccount{}
	err := c.client.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, serviceAccount)

	if err != nil {
		return nil, fmt.Errorf("failed to get service account %s in namespace %s: %w", name, namespace, err)
	}

	return serviceAccount, nil
}

// UpsertServiceAccountWithOwnerReference creates or updates a Kubernetes ServiceAccount with an owner reference.
// The owner reference ensures the service account is garbage collected when the owner is deleted.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
// Callers should return errors to let the controller work queue handle retries.
func (c *Client) UpsertServiceAccountWithOwnerReference(
	ctx context.Context,
	serviceAccount *corev1.ServiceAccount,
	owner client.Object,
) (OperationResult, error) {
	return c.upsertServiceAccount(ctx, serviceAccount, owner)
}

// UpsertServiceAccount creates or updates a Kubernetes ServiceAccount without an owner reference.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
// Callers should return errors to let the controller work queue handle retries.
func (c *Client) UpsertServiceAccount(ctx context.Context, serviceAccount *corev1.ServiceAccount) (OperationResult, error) {
	return c.upsertServiceAccount(ctx, serviceAccount, nil)
}

// upsertServiceAccount creates or updates a Kubernetes ServiceAccount.
// If owner is provided, sets a controller reference to establish ownership.
// This ensures the service account is garbage collected when the owner is deleted.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
func (c *Client) upsertServiceAccount(
	ctx context.Context,
	serviceAccount *corev1.ServiceAccount,
	owner client.Object,
) (OperationResult, error) {
	// Store the desired state before calling CreateOrUpdate.
	// This is necessary because CreateOrUpdate first fetches the existing object from the API server
	// and overwrites the object we pass in. Any values we set on the object (other than Name/Namespace)
	// would be lost. By storing them here, we can apply them in the mutate function after the fetch.
	// See: https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/controller/controllerutil#CreateOrUpdate
	desiredLabels := serviceAccount.Labels
	desiredAnnotations := serviceAccount.Annotations
	desiredAutomountServiceAccountToken := serviceAccount.AutomountServiceAccountToken
	desiredImagePullSecrets := serviceAccount.ImagePullSecrets
	desiredSecrets := serviceAccount.Secrets

	// Create a service account object with only Name and Namespace set.
	// CreateOrUpdate requires this minimal object - it will fetch the full object from the API server.
	existing := &corev1.ServiceAccount{}
	existing.Name = serviceAccount.Name
	existing.Namespace = serviceAccount.Namespace

	result, err := controllerutil.CreateOrUpdate(ctx, c.client, existing, func() error {
		// Set the desired state
		existing.Labels = desiredLabels
		existing.Annotations = desiredAnnotations
		existing.AutomountServiceAccountToken = desiredAutomountServiceAccountToken
		existing.ImagePullSecrets = desiredImagePullSecrets
		existing.Secrets = desiredSecrets

		// Set owner reference if provided
		if owner != nil {
			if err := controllerutil.SetControllerReference(owner, existing, c.scheme); err != nil {
				return fmt.Errorf("failed to set controller reference: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("failed to upsert service account %s in namespace %s: %w",
			serviceAccount.Name, serviceAccount.Namespace, err)
	}

	return result, nil
}

// GetRole retrieves a Kubernetes Role by name and namespace.
// Returns the role if found, or an error if not found or on failure.
func (c *Client) GetRole(ctx context.Context, name, namespace string) (*rbacv1.Role, error) {
	role := &rbacv1.Role{}
	err := c.client.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, role)

	if err != nil {
		return nil, fmt.Errorf("failed to get role %s in namespace %s: %w", name, namespace, err)
	}

	return role, nil
}

// UpsertRoleWithOwnerReference creates or updates a Kubernetes Role with an owner reference.
// The owner reference ensures the role is garbage collected when the owner is deleted.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
// Callers should return errors to let the controller work queue handle retries.
func (c *Client) UpsertRoleWithOwnerReference(
	ctx context.Context,
	role *rbacv1.Role,
	owner client.Object,
) (OperationResult, error) {
	return c.upsertRole(ctx, role, owner)
}

// UpsertRole creates or updates a Kubernetes Role without an owner reference.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
// Callers should return errors to let the controller work queue handle retries.
func (c *Client) UpsertRole(ctx context.Context, role *rbacv1.Role) (OperationResult, error) {
	return c.upsertRole(ctx, role, nil)
}

// upsertRole creates or updates a Kubernetes Role.
// If owner is provided, sets a controller reference to establish ownership.
// This ensures the role is garbage collected when the owner is deleted.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
func (c *Client) upsertRole(
	ctx context.Context,
	role *rbacv1.Role,
	owner client.Object,
) (OperationResult, error) {
	// Store the desired state before calling CreateOrUpdate.
	// This is necessary because CreateOrUpdate first fetches the existing object from the API server
	// and overwrites the object we pass in. Any values we set on the object (other than Name/Namespace)
	// would be lost. By storing them here, we can apply them in the mutate function after the fetch.
	// See: https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/controller/controllerutil#CreateOrUpdate
	desiredLabels := role.Labels
	desiredAnnotations := role.Annotations
	desiredRules := role.Rules

	// Create a role object with only Name and Namespace set.
	// CreateOrUpdate requires this minimal object - it will fetch the full object from the API server.
	existing := &rbacv1.Role{}
	existing.Name = role.Name
	existing.Namespace = role.Namespace

	result, err := controllerutil.CreateOrUpdate(ctx, c.client, existing, func() error {
		// Set the desired state
		existing.Labels = desiredLabels
		existing.Annotations = desiredAnnotations
		existing.Rules = desiredRules

		// Set owner reference if provided
		if owner != nil {
			if err := controllerutil.SetControllerReference(owner, existing, c.scheme); err != nil {
				return fmt.Errorf("failed to set controller reference: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("failed to upsert role %s in namespace %s: %w",
			role.Name, role.Namespace, err)
	}

	return result, nil
}

// GetRoleBinding retrieves a Kubernetes RoleBinding by name and namespace.
// Returns the role binding if found, or an error if not found or on failure.
func (c *Client) GetRoleBinding(ctx context.Context, name, namespace string) (*rbacv1.RoleBinding, error) {
	roleBinding := &rbacv1.RoleBinding{}
	err := c.client.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, roleBinding)

	if err != nil {
		return nil, fmt.Errorf("failed to get role binding %s in namespace %s: %w", name, namespace, err)
	}

	return roleBinding, nil
}

// UpsertRoleBindingWithOwnerReference creates or updates a Kubernetes RoleBinding with an owner reference.
// The owner reference ensures the role binding is garbage collected when the owner is deleted.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
// Callers should return errors to let the controller work queue handle retries.
func (c *Client) UpsertRoleBindingWithOwnerReference(
	ctx context.Context,
	roleBinding *rbacv1.RoleBinding,
	owner client.Object,
) (OperationResult, error) {
	return c.upsertRoleBinding(ctx, roleBinding, owner)
}

// UpsertRoleBinding creates or updates a Kubernetes RoleBinding without an owner reference.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
// Callers should return errors to let the controller work queue handle retries.
func (c *Client) UpsertRoleBinding(ctx context.Context, roleBinding *rbacv1.RoleBinding) (OperationResult, error) {
	return c.upsertRoleBinding(ctx, roleBinding, nil)
}

// upsertRoleBinding creates or updates a Kubernetes RoleBinding.
// If owner is provided, sets a controller reference to establish ownership.
// This ensures the role binding is garbage collected when the owner is deleted.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
//
// IMPORTANT: RoleRef is immutable after creation. It can only be set when creating a new RoleBinding.
func (c *Client) upsertRoleBinding(
	ctx context.Context,
	roleBinding *rbacv1.RoleBinding,
	owner client.Object,
) (OperationResult, error) {
	// Store the desired state before calling CreateOrUpdate.
	// This is necessary because CreateOrUpdate first fetches the existing object from the API server
	// and overwrites the object we pass in. Any values we set on the object (other than Name/Namespace)
	// would be lost. By storing them here, we can apply them in the mutate function after the fetch.
	// See: https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/controller/controllerutil#CreateOrUpdate
	desiredLabels := roleBinding.Labels
	desiredAnnotations := roleBinding.Annotations
	desiredRoleRef := roleBinding.RoleRef
	desiredSubjects := roleBinding.Subjects

	// Create a role binding object with only Name and Namespace set.
	// CreateOrUpdate requires this minimal object - it will fetch the full object from the API server.
	existing := &rbacv1.RoleBinding{}
	existing.Name = roleBinding.Name
	existing.Namespace = roleBinding.Namespace

	result, err := controllerutil.CreateOrUpdate(ctx, c.client, existing, func() error {
		// Set the desired state
		existing.Labels = desiredLabels
		existing.Annotations = desiredAnnotations
		existing.Subjects = desiredSubjects

		// RoleRef is immutable after creation - only set it when creating a new RoleBinding
		if existing.CreationTimestamp.IsZero() {
			existing.RoleRef = desiredRoleRef
		}

		// Set owner reference if provided
		if owner != nil {
			if err := controllerutil.SetControllerReference(owner, existing, c.scheme); err != nil {
				return fmt.Errorf("failed to set controller reference: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("failed to upsert role binding %s in namespace %s: %w",
			roleBinding.Name, roleBinding.Namespace, err)
	}

	return result, nil
}

// EnsureRBACResourcesParams contains the parameters for EnsureRBACResources.
type EnsureRBACResourcesParams struct {
	// Name is the name to use for all RBAC resources (ServiceAccount, Role, RoleBinding)
	Name string
	// Namespace is the namespace where the RBAC resources will be created
	Namespace string
	// Rules are the RBAC policy rules for the Role
	Rules []rbacv1.PolicyRule
	// Owner is the owner object for setting owner references
	Owner client.Object
	// Labels are optional labels to apply to all RBAC resources
	Labels map[string]string
}

// EnsureRBACResources creates or updates a complete set of RBAC resources:
// ServiceAccount, Role, and RoleBinding. All resources use the same name and
// are created in the same namespace. The RoleBinding binds the ServiceAccount
// to the Role. All resources have owner references set for automatic cleanup.
//
// This is a convenience method that consolidates the common pattern of creating
// RBAC resources for a controller. It returns an error if any operation fails.
// Callers should return errors to let the controller work queue handle retries.
//
// Non-atomic behavior: Resource creation is sequential and non-atomic. If a later
// resource fails, earlier resources will remain. This is acceptable because:
//   - Controller reconciliation will retry and complete the setup
//   - All resources have owner references for automatic cleanup
//   - Partial state is temporary and self-healing via reconciliation
func (c *Client) EnsureRBACResources(ctx context.Context, params EnsureRBACResourcesParams) error {
	// Ensure ServiceAccount
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      params.Name,
			Namespace: params.Namespace,
			Labels:    params.Labels,
		},
	}
	if _, err := c.UpsertServiceAccountWithOwnerReference(ctx, serviceAccount, params.Owner); err != nil {
		return fmt.Errorf("failed to ensure service account: %w", err)
	}

	// Ensure Role
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      params.Name,
			Namespace: params.Namespace,
			Labels:    params.Labels,
		},
		Rules: params.Rules,
	}
	if _, err := c.UpsertRoleWithOwnerReference(ctx, role, params.Owner); err != nil {
		return fmt.Errorf("failed to ensure role: %w", err)
	}

	// Ensure RoleBinding
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      params.Name,
			Namespace: params.Namespace,
			Labels:    params.Labels,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: RBACAPIGroup,
			Kind:     "Role",
			Name:     params.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      params.Name,
				Namespace: params.Namespace,
			},
		},
	}
	if _, err := c.UpsertRoleBindingWithOwnerReference(ctx, roleBinding, params.Owner); err != nil {
		return fmt.Errorf("failed to ensure role binding: %w", err)
	}

	return nil
}

// GetAllRBACResources retrieves all RBAC resources (ServiceAccount, Role, RoleBinding)
// with the given name and namespace. This is useful for debugging, status reporting,
// or verification of RBAC resource state.
//
// If any resource is not found, it returns an error indicating which resource is missing.
// If all resources exist, they are returned in order: ServiceAccount, Role, RoleBinding.
func (c *Client) GetAllRBACResources(
	ctx context.Context,
	name, namespace string,
) (*corev1.ServiceAccount, *rbacv1.Role, *rbacv1.RoleBinding, error) {
	// Get ServiceAccount
	sa, err := c.GetServiceAccount(ctx, name, namespace)
	if err != nil {
		return nil, nil, nil, err // error already wrapped by GetServiceAccount
	}

	// Get Role
	role := &rbacv1.Role{}
	roleKey := client.ObjectKey{Name: name, Namespace: namespace}
	if err := c.client.Get(ctx, roleKey, role); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get role %s in namespace %s: %w",
			name, namespace, err)
	}

	// Get RoleBinding
	rb := &rbacv1.RoleBinding{}
	rbKey := client.ObjectKey{Name: name, Namespace: namespace}
	if err := c.client.Get(ctx, rbKey, rb); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get role binding %s in namespace %s: %w",
			name, namespace, err)
	}

	return sa, role, rb, nil
}
