# Controller Design

This document describes the enterprise authorization controller — the
reconciliation loops, watch setup, SSA patching, ConfigMap lifecycle, and
conflict avoidance with the OSS operator.

Refer to [00-invariants.md](00-invariants.md) for design constraints and
[02-cedar-compilation.md](02-cedar-compilation.md) for the Cedar compilation
algorithm this controller drives.

**Prerequisite**: The OSS changes in [04-oss-changes.md](04-oss-changes.md)
(at minimum change #1: `GroupClaim` field in `ConfigOptions`, and change #2:
`serverName` on `Authorizer`) must be merged before the enterprise controller
is deployed. Without them, the `group_claim` and server-scoped entity
features in the generated ConfigMap are silently ignored by the OSS Cedar
authorizer.

## 1. Controller Architecture

### Primary resource: MCPServer (fan-in pattern)

The controller is keyed on **MCPServer** as the primary resource. All CRD
changes (ToolhiveAuthorizationPolicy, ToolhiveRoleBinding, ToolhivePlatformRole)
fan in to MCPServer reconciliation via `MapFunc`s.

```
ToolhiveAuthorizationPolicy ──┐
ToolhiveRoleBinding ──────────┤ MapFunc → enqueue MCPServer(s)
ToolhivePlatformRole ─────────┤
MCPServer (create/delete) ────┘
                                   │
                                   ▼
                    EnterpriseAuthzReconciler.Reconcile(MCPServer)
                                   │
                                   ├─ Collect policies targeting this server
                                   ├─ Resolve roles and bindings
                                   ├─ Compile Cedar (02-cedar-compilation.md)
                                   ├─ Write/update ConfigMap
                                   ├─ SSA-patch MCPServer.spec.authzConfig
                                   └─ Update policy status conditions
```

**Why MCPServer as primary (not ToolhiveAuthorizationPolicy)**:

1. **Output-aligned**: The controller produces one ConfigMap per MCPServer,
   aggregating all policies targeting that server. When the reconcile key is
   the MCPServer, the reconciler naturally has the right scope — all inputs
   for one output.

2. **Multi-policy fan-in is natural**: When a ToolhiveRoleBinding changes, it
   may affect multiple MCPServers (via multiple policies). A policy-keyed
   reconciler would need to enumerate affected servers itself. With MCPServer
   as primary, each affected server gets its own reconcile event.

3. **Consistent with existing patterns**: The VirtualMCPServer controller in
   `controllers/virtualmcpserver_controller.go` uses the same fan-in pattern
   — it watches MCPGroup, MCPServer, MCPExternalAuthConfig, etc. and fans
   them into VirtualMCPServer reconciliation via MapFuncs.

4. **Simpler cleanup**: When an MCPServer is deleted, the reconciler clears
   the ConfigMap via owner references. No finalizer needed.

**Alternative considered**: Keying on ToolhiveAuthorizationPolicy as primary.
This is simpler when one policy targets one server, but breaks down with
multi-policy aggregation (section 6 of 02-cedar-compilation.md). Each policy
reconcile would need to re-aggregate with other policies for the same server,
creating redundant work and race conditions between concurrent policy
reconciles.

### Reconciler struct

```go
type EnterpriseAuthzReconciler struct {
    client.Client
    Scheme   *runtime.Scheme
    Recorder record.EventRecorder
}
```

The reconciler is intentionally minimal. It has no caches or state beyond
what controller-runtime provides. All inputs are read from the API server
during reconciliation.

All three enterprise CRDs (`ToolhiveAuthorizationPolicy`, `ToolhiveRoleBinding`,
`ToolhivePlatformRole`) are **namespace-scoped**. A policy can only target an
MCPServer in the same namespace, and a binding only affects policies in the
same namespace. Cross-namespace targeting is out of scope for MVP.

The controller requires **leader election** (the default for
`ctrl.NewControllerManagedBy`). Only one instance should be actively
reconciling at a time. If the enterprise controller runs as a separate binary
from the OSS operator, it must use a distinct leader election lock name.

### RBAC requirements

```go
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=toolhiveauthorizationpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=toolhiveauthorizationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=toolhiverolebindings,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=toolhiveplatformroles,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
```

Note: `mcpservers` needs `patch` (for SSA), not `update`. The enterprise
controller never does a full update on MCPServer — it only SSA-patches the
`spec.authzConfig` field.

## 2. Watch Setup

### SetupWithManager

```go
func (r *EnterpriseAuthzReconciler) SetupWithManager(mgr ctrl.Manager) error {
    // Index ToolhiveAuthorizationPolicy by spec.targetRef.name
    // for efficient lookup during reconciliation
    if err := mgr.GetFieldIndexer().IndexField(
        context.Background(),
        &enterprisev1alpha1.ToolhiveAuthorizationPolicy{},
        ".spec.targetRef.name",
        func(obj client.Object) []string {
            policy := obj.(*enterprisev1alpha1.ToolhiveAuthorizationPolicy)
            if policy.Spec.TargetRef.Name == "" {
                return nil
            }
            return []string{policy.Spec.TargetRef.Name}
        },
    ); err != nil {
        return fmt.Errorf("failed to create index: %w", err)
    }

    return ctrl.NewControllerManagedBy(mgr).
        Named("enterprise-authz").
        For(&mcpv1alpha1.MCPServer{},
            builder.WithPredicates(mcpServerPredicate())).
        Watches(&enterprisev1alpha1.ToolhiveAuthorizationPolicy{},
            handler.EnqueueRequestsFromMapFunc(r.mapPolicyToMCPServer)).
        Watches(&enterprisev1alpha1.ToolhiveRoleBinding{},
            handler.EnqueueRequestsFromMapFunc(r.mapRoleBindingToMCPServers)).
        Watches(&enterprisev1alpha1.ToolhivePlatformRole{},
            handler.EnqueueRequestsFromMapFunc(r.mapPlatformRoleToMCPServers)).
        Complete(r)
}
```

### MCPServer predicate

The enterprise controller reacts to MCPServer creates, deletes, label
changes, and `spec.authzConfig` drift. It must **not** react to status
updates or other spec changes made by the OSS operator.

```go
func mcpServerPredicate() predicate.Predicate {
    return predicate.Funcs{
        CreateFunc: func(e event.CreateEvent) bool {
            return true // new server — check if policies target it
        },
        DeleteFunc: func(e event.DeleteEvent) bool {
            return true // server gone — clean up ConfigMap
        },
        UpdateFunc: func(e event.UpdateEvent) bool {
            oldServer := e.ObjectOld.(*mcpv1alpha1.MCPServer)
            newServer := e.ObjectNew.(*mcpv1alpha1.MCPServer)

            // React to label changes (future targetSelector support)
            if !maps.Equal(oldServer.Labels, newServer.Labels) {
                return true
            }

            // React to authzConfig drift (admin override detection).
            // If someone kubectl-edits spec.authzConfig, the enterprise
            // controller must reconcile to self-heal via SSA ForceOwnership.
            // This follows the cert-manager pattern for caBundle injection.
            if !reflect.DeepEqual(
                oldServer.Spec.AuthzConfig,
                newServer.Spec.AuthzConfig,
            ) {
                return true
            }

            return false
        },
        GenericFunc: func(e event.GenericEvent) bool {
            return false
        },
    }
}
```

**Self-heal on drift**: The enterprise controller is the authoritative owner
of `spec.authzConfig` when policies target the server. If an admin manually
edits the field, the predicate detects the change, the reconciler fires, and
SSA with `ForceOwnership` overwrites the manual edit. This follows the
cert-manager pattern (cainjector self-heals on manual `caBundle` edits).

The self-heal also emits a warning event for observability:

```go
r.Recorder.Eventf(mcpServer, corev1.EventTypeWarning,
    "AuthzConfigDrift",
    "spec.authzConfig was manually modified; reverting to controller-managed state")
```

**Why not a reconcile loop**: The enterprise controller's own SSA write to
`spec.authzConfig` also triggers this predicate. However, the subsequent
reconcile is a no-op — the ConfigMap content hasn't changed and the SSA patch
writes the same value. The extra reconcile is cheap and exits quickly. This
is the same trade-off cert-manager makes.

### MapFunc implementations

#### Policy → MCPServer (direct mapping)

```go
func (r *EnterpriseAuthzReconciler) mapPolicyToMCPServer(
    ctx context.Context,
    obj client.Object,
) []reconcile.Request {
    policy := obj.(*enterprisev1alpha1.ToolhiveAuthorizationPolicy)
    if policy.Spec.TargetRef.Name == "" {
        return nil
    }
    return []reconcile.Request{{
        NamespacedName: types.NamespacedName{
            Name:      policy.Spec.TargetRef.Name,
            Namespace: policy.Namespace,
        },
    }}
}
```

#### RoleBinding → MCPServer (two-hop mapping)

A RoleBinding change can affect any MCPServer targeted by a policy that
references one of the binding's platform roles.

```go
func (r *EnterpriseAuthzReconciler) mapRoleBindingToMCPServers(
    ctx context.Context,
    obj client.Object,
) []reconcile.Request {
    binding := obj.(*enterprisev1alpha1.ToolhiveRoleBinding)

    // Collect all role names from this binding
    roleNames := make(map[string]struct{})
    for _, b := range binding.Spec.Bindings {
        roleNames[b.PlatformRole] = struct{}{}
    }

    // Find all policies that reference any of these roles
    var policyList enterprisev1alpha1.ToolhiveAuthorizationPolicyList
    if err := r.List(ctx, &policyList,
        client.InNamespace(binding.Namespace)); err != nil {
        return nil
    }

    // Deduplicate target MCPServers
    targets := make(map[types.NamespacedName]struct{})
    for i := range policyList.Items {
        policy := &policyList.Items[i]
        for _, b := range policy.Spec.Bindings {
            if _, ok := roleNames[b.PlatformRole]; ok {
                targets[types.NamespacedName{
                    Name:      policy.Spec.TargetRef.Name,
                    Namespace: policy.Namespace,
                }] = struct{}{}
                break
            }
        }
    }

    requests := make([]reconcile.Request, 0, len(targets))
    for nn := range targets {
        requests = append(requests, reconcile.Request{NamespacedName: nn})
    }
    return requests
}
```

#### PlatformRole → MCPServer (two-hop mapping)

Same pattern: find policies that reference this role, enqueue their target
MCPServers.

```go
func (r *EnterpriseAuthzReconciler) mapPlatformRoleToMCPServers(
    ctx context.Context,
    obj client.Object,
) []reconcile.Request {
    role := obj.(*enterprisev1alpha1.ToolhivePlatformRole)

    var policyList enterprisev1alpha1.ToolhiveAuthorizationPolicyList
    if err := r.List(ctx, &policyList,
        client.InNamespace(role.Namespace)); err != nil {
        return nil
    }

    targets := make(map[types.NamespacedName]struct{})
    for i := range policyList.Items {
        policy := &policyList.Items[i]
        for _, b := range policy.Spec.Bindings {
            if b.PlatformRole == role.Name {
                targets[types.NamespacedName{
                    Name:      policy.Spec.TargetRef.Name,
                    Namespace: policy.Namespace,
                }] = struct{}{}
                break
            }
        }
    }

    requests := make([]reconcile.Request, 0, len(targets))
    for nn := range targets {
        requests = append(requests, reconcile.Request{NamespacedName: nn})
    }
    return requests
}
```

### Field indexing

The field index on `spec.targetRef.name` enables efficient lookup of policies
targeting a specific MCPServer during reconciliation:

```go
var policyList enterprisev1alpha1.ToolhiveAuthorizationPolicyList
if err := r.List(ctx, &policyList,
    client.InNamespace(mcpServer.Namespace),
    client.MatchingFields{".spec.targetRef.name": mcpServer.Name},
); err != nil {
    return ctrl.Result{}, err
}
```

Without the index, the reconciler would list all policies in the namespace
and filter client-side.

## 3. Reconciliation Loop

### Main reconcile method

```go
func (r *EnterpriseAuthzReconciler) Reconcile(
    ctx context.Context,
    req ctrl.Request,
) (ctrl.Result, error) {
    logger := log.FromContext(ctx)

    // 1. Fetch the MCPServer
    var mcpServer mcpv1alpha1.MCPServer
    if err := r.Get(ctx, req.NamespacedName, &mcpServer); err != nil {
        if errors.IsNotFound(err) {
            // MCPServer deleted — ConfigMap is cleaned up via owner reference.
            // Known limitation: if a ToolhiveAuthorizationPolicy targets an
            // MCPServer that does not exist yet, the policy has no status
            // conditions (no feedback to the admin). When the MCPServer is
            // created, the create event triggers reconciliation and the
            // policy takes effect. A validating webhook on policy creation
            // can be added later to reject invalid targetRefs at admission
            // time.
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, err
    }

    // 2. Find all policies targeting this server
    var policyList enterprisev1alpha1.ToolhiveAuthorizationPolicyList
    if err := r.List(ctx, &policyList,
        client.InNamespace(mcpServer.Namespace),
        client.MatchingFields{".spec.targetRef.name": mcpServer.Name},
    ); err != nil {
        return ctrl.Result{}, fmt.Errorf("listing policies: %w", err)
    }

    // 3. No policies targeting this server — clean up
    if len(policyList.Items) == 0 {
        return r.cleanupAuthz(ctx, &mcpServer)
    }

    // 4. Resolve all role bindings and platform roles
    roleBindings, err := r.resolveRoleBindings(ctx, mcpServer.Namespace)
    if err != nil {
        return ctrl.Result{}, fmt.Errorf("resolving role bindings: %w", err)
    }

    platformRoles, err := r.resolvePlatformRoles(ctx, mcpServer.Namespace)
    if err != nil {
        return ctrl.Result{}, fmt.Errorf("resolving platform roles: %w", err)
    }

    // 5. Compile Cedar policies and entities
    result, err := r.compileCedar(
        ctx, &mcpServer, policyList.Items, roleBindings, platformRoles)
    if err != nil {
        // Surface the error via status conditions and events — these are
        // the primary feedback channels for administrators.
        r.setCompilationStatus(ctx, policyList.Items, false, err.Error())
        r.Recorder.Eventf(&mcpServer, corev1.EventTypeWarning,
            "CompilationFailed",
            "Failed to compile Cedar policies: %v", err)
        logger.Error(err, "Cedar compilation failed, will retry in 1 minute")

        // Return nil error with fixed RequeueAfter. Compilation failures are
        // typically config errors requiring human intervention — exponential
        // backoff (from returning err) would be counterproductive. The error
        // is already surfaced via the Compiled=False condition and event.
        return ctrl.Result{RequeueAfter: time.Minute}, nil
    }

    // 6. Write ConfigMap
    if err := r.ensureConfigMap(ctx, &mcpServer, result); err != nil {
        return ctrl.Result{}, fmt.Errorf("ensuring ConfigMap: %w", err)
    }

    // 7. SSA-patch MCPServer.spec.authzConfig + policy hash annotation
    if err := r.patchAuthzConfig(ctx, &mcpServer, result.ContentHash); err != nil {
        return ctrl.Result{}, fmt.Errorf("patching authzConfig: %w", err)
    }

    // 8. Update policy status conditions
    if err := r.setCompilationStatus(ctx, policyList.Items, true, ""); err != nil {
        return ctrl.Result{}, fmt.Errorf("updating policy status: %w", err)
    }

    // 9. Check for access broadening and emit warnings
    r.checkAccessBroadening(ctx, &mcpServer, policyList.Items, result)

    logger.Info("reconciled enterprise authorization",
        "server", mcpServer.Name,
        "policyCount", result.PolicyCount,
        "denyCount", result.DenyCount)

    return ctrl.Result{}, nil
}
```

### Error handling strategy

| Error type | Response | Requeue? |
|------------|----------|----------|
| MCPServer not found | Return nil (deleted; owner ref cleans ConfigMap) | No |
| Policy list failed | Return error (transient) | Yes (exponential backoff) |
| Role/binding resolution failed | Return error + set status | Yes (backoff) |
| Cedar compilation failed | Set `Compiled: False` on policies, emit event, log error, do NOT update ConfigMap | Yes (fixed 1 min, not backoff) |
| ConfigMap write failed | Return error | Yes (backoff) |
| SSA patch failed | Return error | Yes (backoff) |
| Status update failed | Return error (admin feedback channel) | Yes (backoff) |

The most important behavior: **compilation failure does not update the
ConfigMap**. The previous valid version stays in place, preserving the last
known-good policy at runtime (see invariant 3.5).

## 4. SSA Patching

### Patching spec.authzConfig and policy hash annotation

The enterprise controller uses Server-Side Apply to patch the MCPServer with
two concerns:

1. **`spec.authzConfig`** — the ConfigMap reference (changes only on first
   injection or cleanup)
2. **`enterprise.toolhive.stacklok.io/policy-hash` annotation** — a SHA-256
   hash of the compiled ConfigMap content (changes on every recompilation)

The annotation is critical: when a RoleBinding or PlatformRole changes, the
enterprise controller recompiles Cedar and updates the ConfigMap contents. But
`spec.authzConfig` still points to the same ConfigMap name — nothing in the
MCPServer spec changes. Without the annotation, the OSS operator would never
reconcile and the MCP server would keep running stale policy. The annotation
change bumps the MCPServer generation, triggering the OSS operator to
reconcile, re-read the ConfigMap, recompute the RunConfig checksum, and roll
out new pods.

```go
const enterpriseFieldManager = "enterprise-authz-controller"

func (r *EnterpriseAuthzReconciler) patchAuthzConfig(
    ctx context.Context,
    mcpServer *mcpv1alpha1.MCPServer,
    policyHash string,
) error {
    configMapName := fmt.Sprintf("%s-enterprise-authz", mcpServer.Name)

    patch := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "toolhive.stacklok.dev/v1alpha1",
            "kind":       "MCPServer",
            "metadata": map[string]interface{}{
                "name":      mcpServer.Name,
                "namespace": mcpServer.Namespace,
                "annotations": map[string]interface{}{
                    "enterprise.toolhive.stacklok.io/policy-hash": policyHash,
                },
            },
            "spec": map[string]interface{}{
                "authzConfig": map[string]interface{}{
                    "type": "configMap",
                    "configMap": map[string]interface{}{
                        "name": configMapName,
                        "key":  "authz.json",
                    },
                },
            },
        },
    }

    return r.Patch(ctx, patch, client.Apply,
        client.FieldOwner(enterpriseFieldManager),
        client.ForceOwnership,
    )
}
```

**Why unstructured**: Using `unstructured.Unstructured` for the SSA patch
avoids importing the full MCPServer type into the enterprise controller. The
patch only needs the exact fields being set. This also avoids accidentally
setting zero values on other fields.

**Why ForceOwnership**: The enterprise controller must be the sole owner of
`spec.authzConfig`. If an admin manually edits this field via kubectl, the
next reconcile overwrites it. This is the same pattern used by cert-manager
for CA bundle injection.

### Clearing authzConfig

When no policies target a server, the enterprise controller must clear the
`authzConfig` reference and policy-hash annotation. SSA cannot remove a field
by omitting it — the field manager simply stops owning it. To clear fields,
use an explicit `MergePatch`:

```go
func (r *EnterpriseAuthzReconciler) clearAuthzConfig(
    ctx context.Context,
    mcpServer *mcpv1alpha1.MCPServer,
) error {
    patch := client.RawPatch(
        types.MergePatchType,
        []byte(`{`+
            `"metadata":{"annotations":{"enterprise.toolhive.stacklok.io/policy-hash":null}},`+
            `"spec":{"authzConfig":null}`+
            `}`),
    )
    return r.Patch(ctx, mcpServer, patch)
}
```

**Stale resourceVersion**: The MergePatch uses `mcpServer`'s
`resourceVersion` from the `Get` at the start of reconciliation. If the
object was modified between the `Get` and this patch, the API server returns
`409 Conflict` and the workqueue retries. This is the correct behavior.

**Stale SSA field manager metadata**: After the MergePatch, the
`enterprise-authz-controller` entry may persist in `managedFields` as an
empty-owned set. This is harmless metadata — it does not affect field
ownership or conflict detection. It clears on the next SSA apply from any
field manager. An admin can set their own authzConfig without conflicts.

**Important**: After `clearAuthzConfig` succeeds, the in-memory `mcpServer`
has a stale `resourceVersion` (the API server incremented it). The
`cleanupAuthz` method must not perform any further patches on the MCPServer
in the same reconcile cycle — it should return immediately after cleanup.

## 5. ConfigMap Lifecycle

### ConfigMap naming and structure

One ConfigMap per MCPServer, named `{mcpserver-name}-enterprise-authz`.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: github-enterprise-authz
  namespace: default
  labels:
    toolhive.stacklok.io/component: enterprise-authz
    toolhive.stacklok.io/mcp-server: github
    toolhive.stacklok.io/managed-by: enterprise-authz-controller
  ownerReferences:
    - apiVersion: toolhive.stacklok.dev/v1alpha1
      kind: MCPServer
      name: github
      uid: <mcpserver-uid>
data:
  authz.json: |
    {
      "version": "1.0",
      "type": "cedarv1",
      "cedar": {
        "policies": ["permit(...);", ...],
        "entities_json": "[...]",
        "group_claim": "groups"
      }
    }
```

**Version string**: The enterprise controller must use `"version": "1.0"`
(matching the existing `EnsureAuthzConfigMap` in `controllerutil/authz.go`).
Note that `NewCedarAuthorizer` factory code uses `"v1"` internally, but
the OSS `authz.Config.Validate()` only checks `Version != ""` — both
formats are accepted. Use `"1.0"` for consistency with the inline path.

### Owner references

The ConfigMap has an **owner reference** to the MCPServer (not to the
ToolhiveAuthorizationPolicy). This gives automatic garbage collection: when
the MCPServer is deleted, Kubernetes cascade-deletes the ConfigMap.

```go
func (r *EnterpriseAuthzReconciler) ensureConfigMap(
    ctx context.Context,
    mcpServer *mcpv1alpha1.MCPServer,
    result *CompilationResult,
) error {
    configMapName := fmt.Sprintf("%s-enterprise-authz", mcpServer.Name)

    // Capture desired state before CreateOrUpdate — CreateOrUpdate overwrites
    // the in-memory object with the fetched version before calling the mutate
    // function, so any fields set beforehand are lost on the update path.
    desiredData := map[string]string{
        "authz.json": result.ConfigMapJSON,
    }
    desiredLabels := map[string]string{
        "toolhive.stacklok.io/component":  "enterprise-authz",
        "toolhive.stacklok.io/mcp-server": mcpServer.Name,
        "toolhive.stacklok.io/managed-by": "enterprise-authz-controller",
    }

    configMap := &corev1.ConfigMap{}
    configMap.Name = configMapName
    configMap.Namespace = mcpServer.Namespace

    _, err := controllerutil.CreateOrUpdate(ctx, r.Client, configMap,
        func() error {
            configMap.Data = desiredData
            configMap.Labels = desiredLabels
            return controllerutil.SetOwnerReference(
                mcpServer, configMap, r.Scheme)
        })
    return err
}
```

This follows the established pattern in `pkg/kubernetes/configmaps/configmaps.go`:
capture desired state, pass a minimal key object to `CreateOrUpdate`, and set
all desired fields (data, labels, owner reference) inside the mutate function
where they operate on the post-fetch object.

**Why `SetOwnerReference` not `SetControllerReference`**: The ConfigMap is
owned by MCPServer for garbage collection, but the MCPServer is not
"controlled" by the enterprise controller (the OSS operator is the controller
of MCPServer). `SetControllerReference` would fail because MCPServer already
has a controller-owner. `SetOwnerReference` adds a non-controller owner
reference, which still triggers cascade delete (the GC processes all owner
references in the same namespace regardless of the `controller` field).
Note that `SetOwnerReference` does not set `blockOwnerDeletion: true`, so the
ConfigMap may be garbage-collected asynchronously after MCPServer deletion.
The reconciler handles this gracefully — it only creates ConfigMaps when
policies exist for the server.

### ConfigMap naming collision avoidance

The existing OSS operator uses these ConfigMap names for inline authz:
`{resource-name}-authz-inline` (see `controllerutil/authz.go` line 128).

The enterprise controller uses `{resource-name}-enterprise-authz`. These
naming conventions do not collide. Additionally, the enterprise controller
sets `spec.authzConfig.configMap.name` via SSA, which overrides any inline
config — the OSS operator reads whichever ConfigMap name is in the spec.

### Cleanup flow

When all policies are removed from an MCPServer, `cleanupAuthz` handles the
full teardown:

```go
func (r *EnterpriseAuthzReconciler) cleanupAuthz(
    ctx context.Context,
    mcpServer *mcpv1alpha1.MCPServer,
) (ctrl.Result, error) {
    // Step 1: Clear spec.authzConfig and policy-hash annotation FIRST.
    // This prevents the OSS operator from seeing a dangling ConfigMap
    // reference. The OSS operator sees authzConfig: null, removes the
    // volume mount, and redeploys without authorization.
    if err := r.clearAuthzConfig(ctx, mcpServer); err != nil {
        // If the field is already nil (idempotent), that's fine.
        // Otherwise return error to retry.
        if !errors.IsNotFound(err) {
            return ctrl.Result{}, fmt.Errorf("clearing authzConfig: %w", err)
        }
    }

    // Step 2: Delete the ConfigMap. After step 1, no resource references it.
    configMapName := fmt.Sprintf("%s-enterprise-authz", mcpServer.Name)
    cm := &corev1.ConfigMap{}
    if err := r.Get(ctx, types.NamespacedName{
        Name:      configMapName,
        Namespace: mcpServer.Namespace,
    }, cm); err != nil {
        if errors.IsNotFound(err) {
            // Already gone (or never existed) — nothing to do
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, fmt.Errorf("getting ConfigMap for cleanup: %w", err)
    }
    if err := r.Delete(ctx, cm); err != nil && !errors.IsNotFound(err) {
        return ctrl.Result{}, fmt.Errorf("deleting ConfigMap: %w", err)
    }

    r.Recorder.Event(mcpServer, corev1.EventTypeNormal,
        "AuthzConfigCleared",
        "All authorization policies removed; authzConfig cleared")

    // Do NOT patch mcpServer again — resourceVersion is stale after step 1.
    return ctrl.Result{}, nil
}
```

**Ordering is critical**: Clear `spec.authzConfig` **before** deleting the
ConfigMap. If the ConfigMap is deleted first, the OSS operator sees a dangling
reference and fails reconciliation. With ConfigMap deleted second, the OSS
operator sees `authzConfig: null` first and stops looking for the ConfigMap.

**Idempotency**: Both steps handle the already-done case gracefully. Clearing
an already-nil authzConfig is a no-op MergePatch. Deleting a non-existent
ConfigMap returns `NotFound`, which is treated as success. This means the
cleanup is safe to retry on partial failure (e.g., step 1 succeeds but step 2
fails — next reconcile skips step 1 and retries step 2).

**MCPServer deletion case**: When the MCPServer itself is deleted, the
reconciler returns early at step 1 (`Get` returns `NotFound`). The ConfigMap
is cleaned up automatically via its owner reference — no explicit deletion
needed.

## 6. Conflict Avoidance with OSS Operator

### The interaction model

Both controllers watch MCPServer, but they own different concerns:

| Concern | Owner | Mechanism |
|---------|-------|-----------|
| `spec.authzConfig` + policy-hash annotation | Enterprise controller | SSA with `enterprise-authz-controller` field manager |
| `spec.*` (everything else) | Admin / GitOps | kubectl / Helm / ArgoCD |
| Deployment, Service, status | OSS operator | `controllerutil.SetControllerReference` |
| ConfigMap `*-enterprise-authz` | Enterprise controller | Owner reference to MCPServer |

### Why there is no reconcile loop

1. Enterprise controller writes ConfigMap with compiled Cedar
2. Enterprise controller SSA-patches `spec.authzConfig` + policy-hash
   annotation → MCPServer generation increments
3. OSS operator reconciles (correct — annotation changed, needs
   redeployment). It reads the ConfigMap referenced by `spec.authzConfig`,
   builds the RunConfig, checksums it, and updates the Deployment.
4. OSS operator updates MCPServer `status` → generation does NOT increment
5. Enterprise controller ignores the status update (predicate filters it out)
   → no re-reconcile

The policy-hash annotation is essential for the content-change case: when a
RoleBinding or PlatformRole changes, the enterprise controller recompiles
Cedar and updates the ConfigMap contents. But `spec.authzConfig` still
references the same ConfigMap name. Without the annotation, the OSS operator
would never notice the content change. The annotation hash changes on every
recompilation, bumping the MCPServer generation and triggering redeployment.

The OSS operator's reconciliation chain after an enterprise controller patch:
```
annotation or spec.authzConfig changed
  → GenerateAuthzVolumeConfig() creates a volume mount referencing the
    enterprise ConfigMap (mounted at /etc/toolhive/authz for the proxy
    runner to read at startup)
  → AddAuthzConfigOptions() reads ConfigMap contents, parses the authz
    config, and embeds it in the RunConfig ConfigMap
  → RunConfig checksum annotation changes on the Deployment
  → Deployment rolls out new pods with updated authz
```

Note: Kubernetes propagates ConfigMap content changes to mounted volumes
within ~1 minute, but the proxy runner reads the config only at startup — a
pod restart is required to pick up new policies. The checksum annotation
change triggers this restart automatically.

This is the intended behavior — the enterprise controller changes the policy,
and the OSS operator deploys it.

### SSA and the r.Update() race condition

The OSS MCPServer controller uses `r.Update()` (full object update) in
several places, not `r.Status().Update()`. A full `r.Update()` writes ALL
spec fields, including `spec.authzConfig`. If the OSS operator does a full
update while the enterprise controller has SSA-managed `spec.authzConfig`,
the update may overwrite the SSA-managed value.

**Known race sites** in the OSS controller:
- Finalizer addition (`r.Update()` at line 294) — runs on **every** first
  reconcile of a new MCPServer
- Finalizer removal (`r.Update()` at line 283) — runs during deletion
- Restart annotation update (`r.Update()` at line 751) — runs on restart

The finalizer addition is the highest-risk site: it runs on every newly
created MCPServer, which is exactly when the enterprise controller also fires
(MCPServer create event triggers the enterprise reconciler to check if any
policies target it and inject authzConfig).

**Race scenario**:
1. MCPServer is created
2. Enterprise controller SSA-patches `spec.authzConfig` to value B (and
   policy-hash annotation)
3. OSS operator, which read the MCPServer before step 2, calls
   `r.Update()` to add the finalizer — this writes back the **full object**
   with `spec.authzConfig` at its old value (nil/A), overwriting B

**Primary safeguard — optimistic concurrency**: The `r.Update()` call sends
the `resourceVersion` from the initial `Get`. If the SSA patch in step 2
changed the object between the OSS operator's `Get` and `Update`, the API
server returns `409 Conflict`. The OSS controller's workqueue re-enqueues the
item, the next reconcile reads the updated object (now with authzConfig=B),
and the finalizer addition succeeds without clobbering authzConfig.

**Secondary safeguard — SSA re-apply**: Even if the race hits (stale
resourceVersion accepted in an edge case), the enterprise controller
reconciles again on the next CRD change and SSA re-applies the correct value
with `ForceOwnership`.

**Observed behavior**: The race window is narrow (between OSS `Get` and
`Update`), and `409 Conflict` handles it correctly in the common case. No
authorization data is permanently lost — only temporarily delayed by one
reconcile cycle.

**Long-term fix**: The OSS operator should migrate from `r.Update()` to
`r.Status().Update()` for status-only changes, and use SSA for spec metadata
changes (finalizer, annotations) with its own field manager. This is out of
scope for the initial platform authorization work but would fully eliminate
the race.

### Field ownership summary

| Scenario | Result |
|----------|--------|
| Enterprise patches authzConfig + hash | Becomes sole owner (SSA field manager) |
| Admin kubectl-edits authzConfig | Next reconcile with ForceOwnership overwrites |
| Enterprise controller removed | Field value stays (not removed) — manual cleanup |
| No policies remain for server | Enterprise clears the field (MergePatch null) |
| OSS operator does r.Update() | 409 Conflict if SSA changed object; re-read + retry |

## 7. Status Reporting

### Policy status conditions

The enterprise controller updates status on each `ToolhiveAuthorizationPolicy`
to report compilation success/failure and the effective permission set.

```go
func (r *EnterpriseAuthzReconciler) setCompilationStatus(
    ctx context.Context,
    policies []enterprisev1alpha1.ToolhiveAuthorizationPolicy,
    success bool,
    message string,
) error {
    var errs []error
    for i := range policies {
        policy := &policies[i]

        // DeepCopy before mutation — MergeFrom computes the diff against
        // the pre-mutation snapshot, so it does not require an exact
        // resourceVersion match (unlike Status().Update()).
        patch := client.MergeFrom(policy.DeepCopy())

        condition := metav1.Condition{
            Type:               "Compiled",
            ObservedGeneration: policy.Generation,
            LastTransitionTime: metav1.Now(),
        }

        if success {
            condition.Status = metav1.ConditionTrue
            condition.Reason = "CompilationSucceeded"
            condition.Message = "Cedar policies compiled and injected"
        } else {
            condition.Status = metav1.ConditionFalse
            condition.Reason = "CompilationFailed"
            condition.Message = message
        }

        meta.SetStatusCondition(&policy.Status.Conditions, condition)
        if err := r.Status().Patch(ctx, policy, patch); err != nil {
            log.FromContext(ctx).Error(err,
                "failed to patch policy status",
                "policy", policy.Name)
            errs = append(errs, err)
        }
    }
    return goerr.Join(errs...)
}
```

**Why `Status().Patch()` not `Status().Update()`**: The policy objects come
from a `List` call at the start of reconciliation. By the time status is
updated (step 8), their `resourceVersion` may be stale if any other
controller or user modified the policy. `Status().Update()` would fail with
a `409 Conflict`. `Status().Patch()` with `MergeFrom` computes a diff from
the pre-mutation snapshot and applies it without requiring an exact
`resourceVersion` match — it tolerates concurrent changes to other status
fields.

The method returns errors so the caller can requeue on failure. The
`Compiled` condition is the primary admin feedback channel and must not be
silently dropped.

**ObservedGeneration**: Set to `policy.Generation` so consumers can detect
stale status. If `ObservedGeneration < Generation`, the status reflects a
previous version of the spec.

### Kubernetes events

The controller emits events for key lifecycle transitions:

| Event | Type | Reason | When |
|-------|------|--------|------|
| Compilation succeeded | Normal | `CompilationSucceeded` | Policies compiled and ConfigMap updated |
| Compilation failed | Warning | `CompilationFailed` | Cedar validation or role resolution error |
| Access broadened | Warning | `AccessBroadened` | New policy adds roles to a server with existing policies |
| Redundant policy | Warning | `RedundantPolicy` | Restricted grant subsumed by unrestricted grant |
| ConfigMap updated | Normal | `ConfigMapUpdated` | ConfigMap written with new policies |
| AuthzConfig cleared | Normal | `AuthzConfigCleared` | All policies removed from server |

Events are emitted on the **MCPServer** (not the policy) so administrators
monitoring server resources see the authorization changes.

## 8. Compilation Integration

### CompilationResult struct

The `compileCedar` method returns a structured result used by downstream
steps:

```go
type CompilationResult struct {
    // ConfigMapJSON is the serialized JSON for the ConfigMap data
    ConfigMapJSON string

    // ContentHash is the SHA-256 hash of ConfigMapJSON, used as the
    // policy-hash annotation on the MCPServer to trigger OSS operator
    // redeployment when ConfigMap contents change
    ContentHash string

    // PolicyCount is the number of permit policies
    PolicyCount int

    // DenyCount is the number of forbid policies
    DenyCount int

    // EffectivePermissions per role for status reporting
    EffectivePermissions []EffectivePermission

    // GroupClaim from the platform IdP configuration
    GroupClaim string
}

type EffectivePermission struct {
    Role    string
    Actions []string
    Scope   string // e.g., "MCP::github"
}
```

### Role resolution

The `compileCedar` method resolves platform roles from three sources:

1. **Built-in roles** (`reader`, `writer`) — constants in the controller code
2. **Custom roles** (`ToolhivePlatformRole` CRDs) — fetched via the API
3. **Dangling references** — a policy references a role name that does not
   exist as a built-in or CRD

Dangling role references set `Compiled: False` on the policy with a message
like `"unknown platform role: security-auditor"`. The rest of the compilation
proceeds for other bindings — a single bad binding does not block the entire
ConfigMap.

### Group claim resolution

The `group_claim` value in the ConfigMap comes from the platform's IdP
configuration. For MVP, this is a static configuration (e.g., a ConfigMap or
environment variable on the enterprise controller). The controller reads it
during reconciliation and includes it in every compiled ConfigMap.

Future work: The `MCPIdentityProvider` CRD (from the full enterprise design)
replaces this with per-IdP claim configuration.

## 9. Testing Strategy

### Unit tests

| Component | Test approach |
|-----------|--------------|
| `mapPolicyToMCPServer` | Given a policy with targetRef, returns correct MCPServer request |
| `mapRoleBindingToMCPServers` | Given a binding with roles, returns MCPServers from matching policies |
| `mapPlatformRoleToMCPServers` | Given a role name, returns MCPServers from policies referencing it |
| `mcpServerPredicate` | Create→true, Delete→true, label change→true, status update→false |
| `compileCedar` | Integration with 02-cedar-compilation.md test cases |
| `cleanupAuthz` | Clears authzConfig, deletes ConfigMap |
| `setCompilationStatus` | Sets conditions with correct ObservedGeneration |

### Integration tests (envtest)

1. **Happy path**: Create RoleBinding + PlatformRole + Policy + MCPServer →
   verify ConfigMap created with correct Cedar, MCPServer has authzConfig
2. **Multi-policy**: Two policies targeting same server → ConfigMap has
   aggregated policies
3. **Policy deletion**: Delete last policy → authzConfig cleared, ConfigMap
   deleted
4. **Role change**: Update PlatformRole actions → ConfigMap recompiled
5. **Binding change**: Add group to RoleBinding → entities.json updated
6. **Compilation failure**: Policy with unknown role → Compiled=False, no
   ConfigMap change
7. **MCPServer deletion**: Delete MCPServer → ConfigMap garbage collected

### E2E tests (Chainsaw)

End-to-end tests should be added to `test/e2e/chainsaw/operator/` covering:

1. Full lifecycle: apply CRDs → verify ConfigMap → verify MCPServer authzConfig
2. Coexistence: enterprise authz + OSS operator both reconciling without
   conflicts
3. Cleanup: remove all policies → verify clean state
