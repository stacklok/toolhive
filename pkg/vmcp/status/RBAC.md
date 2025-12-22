# StatusReporter RBAC Requirements

This document defines the Kubernetes RBAC permissions required for the StatusReporter to function in Kubernetes mode.

## Overview

The `K8SReporter` implementation requires permissions to update `VirtualMCPServer` status subresource. These permissions must be granted to the ServiceAccount that the vMCP pod runs with.

## Required RBAC Permissions

### Role/ClusterRole Rules

```yaml
rules:
  - apiGroups: ["toolhive.stacklok.io"]
    resources: ["virtualmcpservers/status"]
    verbs: ["get", "update", "patch"]
```

### Explanation

- **get**: Required to fetch the latest resource version before updating status (optimistic concurrency control)
- **update**: Required to update the status subresource
- **patch**: Required for patch-based status updates (optional, but recommended for efficiency)

## Example RBAC Manifests

### For Cluster-Scoped vMCP (ClusterRole)

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: vmcp-status-reporter
rules:
  - apiGroups: ["toolhive.stacklok.io"]
    resources: ["virtualmcpservers/status"]
    verbs: ["get", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: vmcp-status-reporter
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: vmcp-status-reporter
subjects:
  - kind: ServiceAccount
    name: vmcp-server
    namespace: toolhive-system
```

### For Namespace-Scoped vMCP (Role)

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: vmcp-status-reporter
  namespace: toolhive-system
rules:
  - apiGroups: ["toolhive.stacklok.io"]
    resources: ["virtualmcpservers/status"]
    verbs: ["get", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: vmcp-status-reporter
  namespace: toolhive-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: vmcp-status-reporter
subjects:
  - kind: ServiceAccount
    name: vmcp-server
    namespace: toolhive-system
```

## Integration with Operator

**The ToolHive operator automatically creates these RBAC resources** when deploying a VirtualMCPServer. No manual configuration is required.

The operator creates:

1. **ServiceAccount**: `<vmcp-name>-vmcp` in the same namespace
2. **Role**: With status update permissions (defined in `vmcpRBACRules`)
3. **RoleBinding**: Binds the role to the ServiceAccount
4. **Deployment**: Runs the vMCP pod with the ServiceAccount and `VMCP_NAME`/`VMCP_NAMESPACE` env vars

### Implementation Details

The RBAC rules are defined in `cmd/thv-operator/controllers/virtualmcpserver_deployment.go`:

```go
var vmcpRBACRules = []rbacv1.PolicyRule{
    {
        APIGroups: []string{""},
        Resources: []string{"configmaps", "secrets"},
        Verbs:     []string{"get", "list", "watch"},
    },
    {
        APIGroups: []string{"toolhive.stacklok.dev"},
        Resources: []string{"mcpgroups", "mcpservers", "mcpremoteproxies", "mcpexternalauthconfigs"},
        Verbs:     []string{"get", "list", "watch"},
    },
    {
        // Required for StatusReporter to update VirtualMCPServer status
        APIGroups: []string{"toolhive.stacklok.dev"},
        Resources: []string{"virtualmcpservers/status"},
        Verbs:     []string{"get", "update", "patch"},
    },
}
```

The operator calls `ensureRBACResources()` during reconciliation to create/update these resources.

### Example Operator Code (Reference)

```go
func (r *VirtualMCPServerReconciler) ensureRBAC(ctx context.Context, vmcp *mcpv1alpha1.VirtualMCPServer) error {
    // Create ServiceAccount
    sa := &corev1.ServiceAccount{
        ObjectMeta: metav1.ObjectMeta{
            Name:      vmcp.Name + "-vmcp",
            Namespace: vmcp.Namespace,
        },
    }
    if err := r.Client.Create(ctx, sa); err != nil {
        return err
    }

    // Create Role for status updates
    role := &rbacv1.Role{
        ObjectMeta: metav1.ObjectMeta{
            Name:      vmcp.Name + "-status-reporter",
            Namespace: vmcp.Namespace,
        },
        Rules: []rbacv1.PolicyRule{
            {
                APIGroups: []string{"toolhive.stacklok.io"},
                Resources: []string{"virtualmcpservers/status"},
                Verbs:     []string{"get", "update", "patch"},
            },
        },
    }
    if err := r.Client.Create(ctx, role); err != nil {
        return err
    }

    // Create RoleBinding
    roleBinding := &rbacv1.RoleBinding{
        ObjectMeta: metav1.ObjectMeta{
            Name:      vmcp.Name + "-status-reporter",
            Namespace: vmcp.Namespace,
        },
        RoleRef: rbacv1.RoleRef{
            APIGroup: "rbac.authorization.k8s.io",
            Kind:     "Role",
            Name:     role.Name,
        },
        Subjects: []rbacv1.Subject{
            {
                Kind:      "ServiceAccount",
                Name:      sa.Name,
                Namespace: vmcp.Namespace,
            },
        },
    }
    return r.Client.Create(ctx, roleBinding)
}
```

## Mode-Aware RBAC (Issue #3003)

In **dynamic mode** (`outgoingAuth.source: discovered`), the vMCP runtime needs **additional** permissions beyond status updates:

```yaml
rules:
  # Status updates (always required)
  - apiGroups: ["toolhive.stacklok.io"]
    resources: ["virtualmcpservers/status"]
    verbs: ["get", "update", "patch"]

  # Backend discovery (only in dynamic mode)
  - apiGroups: ["toolhive.stacklok.io"]
    resources: ["mcpservers", "mcpremoteproxies", "mcpexternalauthconfigs", "mcpgroups"]
    verbs: ["get", "list"]
```

In **static mode** (`outgoingAuth.source: inline`), only the status update permissions are needed.

## Security Considerations

1. **Least Privilege**: The StatusReporter only needs access to the status subresource, not the spec
2. **Namespace Isolation**: Use Role/RoleBinding for namespace-scoped vMCP servers
3. **Resource Versioning**: The reporter uses optimistic concurrency control to prevent conflicts
4. **Error Handling**: Failed status updates are logged but don't crash the vMCP server

## Testing RBAC

To verify RBAC is configured correctly:

```bash
# Check if the ServiceAccount exists
kubectl get sa -n toolhive-system vmcp-server

# Check if the Role exists
kubectl get role -n toolhive-system vmcp-status-reporter

# Check if the RoleBinding exists
kubectl get rolebinding -n toolhive-system vmcp-status-reporter

# Test permissions using kubectl auth can-i
kubectl auth can-i update virtualmcpservers/status \
  --as=system:serviceaccount:toolhive-system:vmcp-server \
  -n toolhive-system
```

Should return `yes` if RBAC is configured correctly.

## References

- Issue #2854: StatusReporter abstraction
- Issue #3003: Mode-aware ConfigMap and RBAC generation
- Issue #3004: Remove backend discovery from operator in dynamic mode
