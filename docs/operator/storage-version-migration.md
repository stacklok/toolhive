# Storage Version Migration

The ToolHive operator ships a `StorageVersionMigrator` controller that keeps every ToolHive CRD's `status.storedVersions` list clean, so a future operator release can drop deprecated API versions (e.g. `v1alpha1`) without orphaning objects in etcd.

The controller is **enabled by default** via the `operator.features.storageVersionMigrator` chart value (or the `TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR` environment variable if you inject it directly via `operator.env`). Set the value to `false` to opt out.

## Why this exists

When a CRD graduates from, say, `v1alpha1` to `v1beta1` with both versions served and `v1beta1` as the storage version, existing objects continue to work — they are transparently converted on read/write. But the API server records every version that has ever been used for storage in `CustomResourceDefinition.status.storedVersions`. Until that list is trimmed, the API server refuses to let you remove a version from `spec.versions`, because doing so would orphan any etcd-stored objects encoded at that version.

The cleanup is not automatic. Someone has to re-store every existing object at the current storage version, then explicitly patch `status.storedVersions` to drop the old entry. The `StorageVersionMigrator` controller does this for you, on every opted-in ToolHive CRD, continuously. See [upstream Kubernetes documentation](https://kubernetes.io/docs/tasks/manage-kubernetes-objects/storage-version-migration/) for the underlying mechanism.

## How it works

For each opted-in CRD the controller does:

1. Live-reads the CRD via `APIReader` (bypassing the informer cache, so it sees the current `storedVersions`).
2. Reads `spec.versions` to find the entry with `storage: true`.
3. If `status.storedVersions` already equals `[<currentStorageVersion>]`, exits — nothing to do.
4. Otherwise, paginates through every Custom Resource of the kind and issues a plain `Get` + `Update` against the main resource. The API server re-encodes the request body at the current storage version and compares the resulting bytes to what's in etcd. If they differ, etcd is rewritten at the new storage version. If they match (already migrated), the API server elides the write.
5. Once every CR has been processed without errors, patches `CRD.status.storedVersions` to `[<currentStorageVersion>]` using an optimistic-lock merge — so concurrent API-server writes cause a clean retry rather than a silent overwrite.

### Concurrent-write safety

The migrator handles conflicts conservatively. If a per-CR `Update` returns `IsConflict` (another writer raced), the controller retries the per-CR Get + Update up to three attempts. If every retry conflicts, the migration pass returns a sentinel error and the controller requeues itself after 30 seconds without bumping the exponential backoff. `status.storedVersions` is only trimmed when every CR in a pass was successfully re-stored.

This is the upstream `kube-storage-version-migrator` semantics — a Conflict means another writer succeeded, which itself re-encoded the CR at the storage version, so the migration is effectively done for that CR even if our own Update didn't land.

### Admission webhook interaction

Each per-CR `Update` goes through the API server's admission chain (mutating then validating webhooks) before reaching the storage-encoder elision check. **Only the etcd write and watch fanout are elided** when the encoded bytes match what's already stored — admission webhooks fire on every Update regardless.

For ToolHive's own webhooks this is fine; they only reject changes that break spec invariants, and a same-spec round-trip Update cannot trigger those rejections. **If you run a cluster-wide admission policy engine** like Kyverno, Gatekeeper, or OPA, check that your policies don't reject same-spec round-trip Updates of ToolHive CRs before enabling the migrator. A policy that requires a `lastUpdatedBy` annotation, for example, would reject every migrator Update and the controller would never converge.

## The opt-in label

A CRD participates in migration only if it carries:

```yaml
metadata:
  labels:
    toolhive.stacklok.dev/auto-migrate-storage-version: "true"
```

The label is set at CRD-generation time via a kubebuilder marker on each Go root type:

```go
//+kubebuilder:object:root=true
//+kubebuilder:storageversion
//+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
type MCPServer struct { ... }
```

`task operator-manifests` bakes the label into the generated CRD YAML. Every ToolHive CRD that carries `+kubebuilder:storageversion` ships with the marker today.

A CI test (`TestStorageVersionRootMarkerCoverage` in `cmd/thv-operator/controllers/marker_coverage_test.go`) fails the build if a root type is added without the migrate marker. There is no self-serve "exclude" marker — every storage-version root type must opt in. If a future CRD legitimately should not be auto-migrated, the path is to add an entry to the `excludedRootTypes` allowlist in the test file via PR review, not via a self-serve marker.

### Adding a new CRD

Add the marker to the type alongside the existing kubebuilder markers:

```go
//+kubebuilder:object:root=true
//+kubebuilder:storageversion
//+kubebuilder:subresource:status
//+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
type NewShinyThing struct { ... }
```

Then `task operator-manifests` to regenerate the CRD YAML. CI verifies the marker is present.

## Enabling the controller

The controller is enabled by default — `operator.features.storageVersionMigrator` defaults to `true`, which sets `TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR=true` on the operator Deployment and registers the reconciler with the manager. No action is required to use it.

To opt out, set the Helm feature flag to `false` at install or upgrade time:

```yaml
operator:
  features:
    storageVersionMigrator: false
```

Once enabled, the controller is dormant on CRDs whose `storedVersions` already equals `[<currentStorageVersion>]` — most of the time, most CRDs. It only does meaningful work when a CRD's stored-versions list is dirty (typically right after a graduation release).

### Requires cluster-scoped RBAC

The migrator only works when the operator runs cluster-scoped (`operator.rbac.scope=cluster`, the default). It watches cluster-scoped `CustomResourceDefinition` objects and re-stores custom resources across **all** namespaces — neither is possible for a namespace-scoped operator, which gets only per-namespace `RoleBindings` and a namespace-restricted manager cache. To prevent a silently wedged operator, the chart **fails the render** if `storageVersionMigrator: true` is combined with `operator.rbac.scope=namespace`:

```
operator.features.storageVersionMigrator requires operator.rbac.scope=cluster: the
StorageVersionMigrator controller watches cluster-scoped CustomResourceDefinitions and
re-stores resources across all namespaces, which a namespace-scoped operator cannot do.
Set operator.features.storageVersionMigrator=false for namespace-scoped installs.
```

Namespace-scoped installs must set `storageVersionMigrator: false` and handle storage-version cleanup by other means — e.g. running the standalone [`kube-storage-version-migrator`](https://github.com/kubernetes-sigs/kube-storage-version-migrator), which is a cluster-scoped component with its own identity, before any version-removal release.

## Per-CRD emergency escape hatch

Removing the label on a live cluster excludes that single CRD from migration immediately:

```bash
kubectl label crd/mcpservers.toolhive.stacklok.dev \
  toolhive.stacklok.dev/auto-migrate-storage-version-
```

Intended for incident response only. If you deploy the operator via GitOps (Argo CD, Flux) or `helm upgrade`, the chart will re-apply the chart-set label within seconds. For a long-term per-cluster opt-out, leave `storageVersionMigrator: false` and accept that you will need to handle storage-version cleanup yourself before any version-removal release.

## Interaction with version-removal releases

The `StorageVersionMigrator` must have run against your cluster *before* an operator release that drops a deprecated CRD version ships. The typical sequence is:

1. **Release N**: both versions served, newer version is storage, `StorageVersionMigrator` enabled by default. Operators running the migrator have their `storedVersions` trimmed during this deprecation window.
2. **Release N+1+**: the deprecated version is removed from `spec.versions`. Because every cluster that enabled the migrator already has clean `storedVersions`, the CRD update applies.

> **⚠ Skip-a-version upgrade trap.** If your cluster upgrades directly from a pre-migrator release to the version-removal release without ever running an intermediate release that runs the migrator, the helm upgrade will **fail** when the API server refuses to remove the deprecated version from `spec.versions`. To recover: deploy [kube-storage-version-migrator](https://github.com/kubernetes-sigs/kube-storage-version-migrator) once to clean `storedVersions`, then retry the upgrade. To avoid the trap entirely, install each release in sequence, and keep `storageVersionMigrator` enabled (the default) for at least one release before any version-removal release.

## Verification

For any ToolHive CRD in a cluster where the controller has run successfully:

```bash
kubectl get crd mcpservers.toolhive.stacklok.dev \
  -o jsonpath='{.status.storedVersions}'
# ["v1beta1"]
```

If the list contains more than one entry, the controller has not yet finished migrating — check operator logs for reconcile errors and the `StorageVersionMigrationFailed` event on the CRD. The controller will also INFO-log `storage version migration not converging — sustained concurrent writes` after five consecutive conflict-only passes against the same CRD, which is the signal to investigate sibling reconcilers or admission policies that may be racing with the migrator.

## RBAC

The operator ServiceAccount carries (generated from kubebuilder markers, applied by the operator Helm chart):

- `customresourcedefinitions.apiextensions.k8s.io`: `get`, `list`, `watch`
- `customresourcedefinitions/status.apiextensions.k8s.io`: `update`, `patch`
- `*.toolhive.stacklok.dev`: `get`, `list`, `update`

The wildcard on `toolhive.stacklok.dev` resources is intentional: the set of opted-in CRDs is a runtime label decision, not a codegen-time enumeration. The runtime gate (`isManagedCRD` requiring the opt-in label) ensures the controller only writes to CRDs that explicitly opted in, even though the RBAC bound is wider.

## Related

- Issue: [stacklok/toolhive#4969](https://github.com/stacklok/toolhive/issues/4969)
- PR-A — controller: [stacklok/toolhive#5362](https://github.com/stacklok/toolhive/pull/5362)
- PR-B — opt-in labels + marker-coverage CI: [stacklok/toolhive#5391](https://github.com/stacklok/toolhive/pull/5391)
- Kubernetes CRD versioning: [official docs](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/)
- Upstream reference: [`kube-storage-version-migrator`](https://github.com/kubernetes-sigs/kube-storage-version-migrator)
