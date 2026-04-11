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
- Status updates require a separate client patch (`r.Status().Update()`)

## Status Condition Parity

When adding a status condition to one CRD type, check all parallel types (e.g., `MCPServer` and `VirtualMCPServer`) for the same condition. Conditions that warn about misconfiguration or unsupported states should be consistent across types that share the same feature set — a gap means one type silently accepts invalid config that the other rejects.

## Key Operator Commands

```bash
task operator-install-crds    # Install CRDs
task operator-generate        # Generate deepcopy, client code
task operator-manifests       # Generate CRD YAML, RBAC
task operator-test            # Run unit tests
task operator-e2e-test        # Run e2e tests
task crdref-gen              # Generate CRD API docs (run from cmd/thv-operator/)
```
