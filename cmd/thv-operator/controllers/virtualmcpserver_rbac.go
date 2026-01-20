package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// ensureRBACResourcesForVirtualMCPServer ensures that RBAC resources (ServiceAccount, Role, RoleBinding)
// are in place for the VirtualMCPServer in dynamic mode.
//
// This implementation follows the MCPRegistry pattern using CreateOrUpdate with RetryOnConflict,
// which automatically updates RBAC rules during operator upgrades. This is an improvement over
// the previous EnsureRBACResource helper which only created resources but never updated them.
//
// All resources are namespace-scoped and use owner references for automatic cleanup
// when the VirtualMCPServer is deleted.
func (r *VirtualMCPServerReconciler) ensureRBACResourcesForVirtualMCPServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	ctxLogger := log.FromContext(ctx).WithValues("virtualmcpserver", vmcp.Name)
	ctxLogger.Info("Ensuring RBAC resources for VirtualMCPServer")

	resourceName := vmcpServiceAccountName(vmcp.Name)

	if err := r.ensureServiceAccountForVirtualMCPServer(ctx, vmcp, resourceName); err != nil {
		return fmt.Errorf("failed to ensure service account: %w", err)
	}

	if err := r.ensureRoleForVirtualMCPServer(ctx, vmcp, resourceName); err != nil {
		return fmt.Errorf("failed to ensure role: %w", err)
	}

	if err := r.ensureRoleBindingForVirtualMCPServer(ctx, vmcp, resourceName); err != nil {
		return fmt.Errorf("failed to ensure role binding: %w", err)
	}

	ctxLogger.Info("Successfully ensured RBAC resources for VirtualMCPServer")
	return nil
}

// ensureServiceAccountForVirtualMCPServer ensures the ServiceAccount exists for the VirtualMCPServer.
func (r *VirtualMCPServerReconciler) ensureServiceAccountForVirtualMCPServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	resourceName string,
) error {
	ctxLogger := log.FromContext(ctx)

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: vmcp.Namespace,
		},
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result, err := controllerutil.CreateOrUpdate(ctx, r.Client, serviceAccount, func() error {
			serviceAccount.Labels = labelsForVirtualMCPServerRBAC(vmcp, resourceName)
			return controllerutil.SetControllerReference(vmcp, serviceAccount, r.Scheme)
		})
		if err != nil {
			return err
		}
		ctxLogger.Info("ServiceAccount reconciled", "name", resourceName, "namespace", vmcp.Namespace, "result", result)
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to ensure ServiceAccount: %w", err)
	}
	return nil
}

// ensureRoleForVirtualMCPServer ensures the Role exists for the VirtualMCPServer.
func (r *VirtualMCPServerReconciler) ensureRoleForVirtualMCPServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	resourceName string,
) error {
	ctxLogger := log.FromContext(ctx)

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: vmcp.Namespace,
		},
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
			role.Labels = labelsForVirtualMCPServerRBAC(vmcp, resourceName)
			role.Rules = vmcpRBACRules
			return controllerutil.SetControllerReference(vmcp, role, r.Scheme)
		})
		if err != nil {
			return err
		}
		ctxLogger.Info("Role reconciled", "name", resourceName, "namespace", vmcp.Namespace, "result", result)
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to ensure Role: %w", err)
	}
	return nil
}

// ensureRoleBindingForVirtualMCPServer ensures the RoleBinding exists for the VirtualMCPServer.
func (r *VirtualMCPServerReconciler) ensureRoleBindingForVirtualMCPServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	resourceName string,
) error {
	ctxLogger := log.FromContext(ctx)

	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: vmcp.Namespace,
		},
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result, err := controllerutil.CreateOrUpdate(ctx, r.Client, roleBinding, func() error {
			roleBinding.Labels = labelsForVirtualMCPServerRBAC(vmcp, resourceName)
			// RoleRef is immutable after creation, but CreateOrUpdate handles this
			if roleBinding.CreationTimestamp.IsZero() {
				roleBinding.RoleRef = rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "Role",
					Name:     resourceName,
				}
			}
			roleBinding.Subjects = []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      resourceName,
					Namespace: vmcp.Namespace,
				},
			}
			return controllerutil.SetControllerReference(vmcp, roleBinding, r.Scheme)
		})
		if err != nil {
			return err
		}
		ctxLogger.Info("RoleBinding reconciled", "name", resourceName, "namespace", vmcp.Namespace, "result", result)
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to ensure RoleBinding: %w", err)
	}
	return nil
}

// labelsForVirtualMCPServerRBAC returns the labels for VirtualMCPServer RBAC resources.
func labelsForVirtualMCPServerRBAC(vmcp *mcpv1alpha1.VirtualMCPServer, resourceName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "virtualmcpserver-rbac",
		"app.kubernetes.io/instance":   resourceName,
		"app.kubernetes.io/component":  "rbac",
		"app.kubernetes.io/part-of":    "toolhive",
		"app.kubernetes.io/managed-by": "toolhive-operator",
		"toolhive.stacklok.dev/vmcp":   vmcp.Name,
	}
}
