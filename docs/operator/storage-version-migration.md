# Storage Version Migration

The ToolHive operator ships a `StorageVersionMigrator` controller that keeps every ToolHive CRD's `status.storedVersions` list clean, so a future operator release can drop deprecated API versions (e.g. `v1alpha1`) without orphaning objects in etcd.

## Why this exists

When a CRD graduates from, say, `v1alpha1` to `v1beta1` with both versions served and `v1beta1` as the storage version, existing objects continue to work — they are transparently converted on read/write. But the API server records every version that has ever been used for storage in `CustomResourceDefinition.status.storedVersions`. Until that list is trimmed, the Kubernetes API server refuses to let you remove a version from `spec.versions`, because doing so would orphan any etcd-stored objects encoded at that version.

The cleanup is not automatic. Someone has to re-store every existing object at the current storage version, then explicitly patch `status.storedVersions` to drop the old entry. The `StorageVersionMigrator` controller does this for you, on every opted-in ToolHive CRD, continuously. See [upstream Kubernetes documentation](https://kubernetes.io/docs/tasks/manage-kubernetes-objects/storage-version-migration/) for the mechanism.

## What the controller does

For each opted-in CRD:

1. Reads `spec.versions` to find the entry with `storage: true`.
2. If `status.storedVersions` already equals `[<currentStorageVersion>]` and only one version is served, nothing to do.
3. Otherwise, lists every Custom Resource of that kind and issues a metadata-only Server-Side Apply against the `/status` subresource with field manager `thv-storage-version-migrator`. This forces the API server to re-encode each object at the current storage version without triggering admission webhooks (SSA on `/status` typically bypasses webhooks registered on the main resource, and the empty apply owns no fields so it doesn't fight other controllers).
4. Once every object has been re-stored, patches `CRD.status.storedVersions` to `[<currentStorageVersion>]` using an optimistic-lock merge — so concurrent API-server writes cause a clean retry rather than a silent overwrite.

CRDs without a `/status` subresource fall back to main-resource SSA.

## The opt-in label

A CRD participates in migration only if it carries:

```yaml
metadata:
  labels:
    toolhive.stacklok.dev/auto-migrate-storage-version: "true"
```

The label is set at CRD-generation time via a kubebuilder marker on each Go type in `cmd/thv-operator/api/v1beta1/`:

```go
// +kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
type MCPServer struct { ... }
```

`task operator-manifests` bakes the label into the generated CRD YAML. All current ToolHive root types ship with the marker. A CI test (`TestV1beta1TypesMarkerCoverage`) fails the build if a root type is added without either this marker or an explicit `// +thv:storage-version-migrator:exclude` sibling marker — so the migrator cannot silently forget a new CRD.

Adding a new CRD that should be migrated:

```go
// +kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
type NewShinyThing struct { ... }
```

Adding a new CRD that deliberately should NOT be migrated (e.g. an experimental kind that is still stabilising its schema):

```go
// +thv:storage-version-migrator:exclude
type ExperimentalThing struct { ... }
```

## Disabling the controller

Set the Helm feature flag:

```yaml
operator:
  features:
    storageVersionMigrator: false   # default: true
```

This sets `ENABLE_STORAGE_VERSION_MIGRATOR=false` on the operator Deployment, and the reconciler is not registered with the manager.

Disable only if you are running an external migrator such as [kube-storage-version-migrator](https://github.com/kubernetes-sigs/kube-storage-version-migrator). Disabling without a replacement is a footgun: the next ToolHive release that removes a deprecated API version will refuse to apply its CRD update until `storedVersions` is cleaned, and you will have to clean it yourself.

## Per-CRD emergency escape hatch

Removing the label on a live cluster excludes that single CRD from migration immediately:

```bash
kubectl label crd/mcpservers.toolhive.stacklok.dev \
  toolhive.stacklok.dev/auto-migrate-storage-version-
```

Intended for incident response only. If you deploy the operator via GitOps (Argo CD, Flux) or `helm upgrade`, the chart will re-apply the chart-set label within seconds. Use the `storageVersionMigrator` feature flag for long-term opt-out.

## Interaction with version removal releases

The `StorageVersionMigrator` must have had time to run against your cluster *before* an operator release that drops a deprecated CRD version ships. The typical sequence is:

1. **Release N**: both versions served, newer version is storage, `StorageVersionMigrator` enabled. The controller quietly re-stores all objects and trims `storedVersions` on every cluster during this deprecation window.
2. **Release N+1+**: the deprecated version is removed from `spec.versions`. Because every cluster's `storedVersions` was already cleaned in the previous release, the CRD update applies cleanly.

If your cluster upgraded directly from a pre-migrator release to the version-removal release without ever running release N, you must clean `storedVersions` manually (or deploy `kube-storage-version-migrator` once) before the upgrade can succeed.

## Verification

For any ToolHive CRD in a cluster where the controller has run:

```bash
kubectl get crd mcpservers.toolhive.stacklok.dev \
  -o jsonpath='{.status.storedVersions}'
# ["v1beta1"]
```

If the list contains more than one entry, the controller has not yet finished migrating — check operator logs for reconcile errors and the `StorageVersionMigrationFailed` event on the CRD.

## RBAC

The controller requires (generated from kubebuilder markers, applied by the operator Helm chart):

- `customresourcedefinitions.apiextensions.k8s.io`: `get`, `list`, `watch`
- `customresourcedefinitions/status.apiextensions.k8s.io`: `update`, `patch`
- `*.toolhive.stacklok.dev`: `get`, `list`, `patch`
- `*/status.toolhive.stacklok.dev`: `patch`

## Related

- Issue: [stacklok/toolhive#4969](https://github.com/stacklok/toolhive/issues/4969)
- Kubernetes CRD versioning: [official docs](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/)
- Reference implementation: [kubernetes-sigs/cluster-api `crdmigrator`](https://github.com/kubernetes-sigs/cluster-api/tree/main/controllers/crdmigrator)
