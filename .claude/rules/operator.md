---
paths:
  - "cmd/thv-operator/**"
  - "test/e2e/chainsaw/**"
---

# Operator Rules

Applies to Kubernetes operator code and CRD definitions.

## CRD vs PodTemplateSpec

**Rule of thumb**: If it affects how the operator behaves or how the MCP server operates, it's a **CRD attribute**. If it affects where/how pods run, it's **PodTemplateSpec**.

**CRD Attributes** — use for business logic:
- Authentication methods
- Authorization policies
- MCP-specific configuration
- Application behavior

**PodTemplateSpec** — use for infrastructure:
- Node selection (nodeSelector, affinity)
- Resource requests/limits
- Volume mounts
- Security context, tolerations

See `cmd/thv-operator/DESIGN.md` for detailed decision guidelines.

## CRD Type Conventions

- Use `metav1.Duration` for duration fields in CRD types, not `string` or
  integer seconds. It serializes as Go duration strings (`"1m0s"`, `"30s"`),
  has built-in OpenAPI schema support, and is the standard Kubernetes convention.

## Development Workflow

- Always run `task operator-generate` after modifying CRD types
- Always run `task operator-manifests` after adding kubebuilder markers
- Always run `task crdref-gen` from `cmd/thv-operator/` after CRD changes to regenerate API docs (uses relative paths)
- Use `envtest` for integration testing, not real clusters
- Chainsaw tests require a real Kubernetes cluster
- Status writes must go through `controllerutil.MutateAndPatchStatus` — see the Status Writes section below

## Status Condition Parity

When adding a status condition to one CRD type, check all parallel types (e.g., `MCPServer` and `VirtualMCPServer`) for the same condition. Conditions that warn about misconfiguration or unsupported states should be consistent across types that share the same feature set — a gap means one type silently accepts invalid config that the other rejects.

## Status Writes

Use `controllerutil.MutateAndPatchStatus` for every status write — not `r.Status().Update` or inline `client.Status().Patch` (see #4633). The helper's doc comment is the authoritative spec.

When adding a status-write call site, check three things:

1. **Caller holds a freshly-`Get`ted object.** Reconciler-start writers do; writers that iterate `List` results (e.g., deletion-path fan-out in `MCPGroupReconciler`) do not and need a fresh `Get` before calling the helper.
2. **Caller is the sole owner of the entire `Status.Conditions` array.** Per-condition-type ownership is NOT enough. JSON merge-patch replaces the array wholesale for CRDs (the `+listType=map` marker is only honored by strategic-merge-patch), so any concurrent writer whose Patch lands between this caller's Get and Patch — on any condition type, not just the ones this caller touches — will be erased. A fresh `Get` narrows the TOCTOU window but does not eliminate it. If two code paths must write conditions on the same CRD (e.g., operator reconciler + in-pod `K8sReporter`), fix at the design level: consolidate to a single owner, or move one writer to a dedicated status field outside the array.
3. **Scalar fields the writer touches are not co-owned.** A stale-computed value different from the caller's snapshot will overwrite the live value — the helper cannot defend against this.

Do not use `MutateAndPatchStatus` for spec or metadata writes — those require optimistic locking (`client.MergeFromWithOptions(..., MergeFromWithOptimisticLock{})`). See #4767.

## Key Operator Commands

```bash
task operator-install-crds    # Install CRDs
task operator-generate        # Generate deepcopy, client code
task operator-manifests       # Generate CRD YAML, RBAC
task operator-test            # Run unit tests
task operator-e2e-test        # Run e2e tests
task crdref-gen              # Generate CRD API docs (run from cmd/thv-operator/)
```
