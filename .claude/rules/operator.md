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

## Development Workflow

- Always run `task operator-generate` after modifying CRD types
- Always run `task operator-manifests` after adding kubebuilder markers
- Always run `task crdref-gen` from `cmd/thv-operator/` after CRD changes to regenerate API docs (uses relative paths)
- Use `envtest` for integration testing, not real clusters
- Chainsaw tests require a real Kubernetes cluster
- Status updates require a separate client patch (`r.Status().Update()`)

## Key Operator Commands

```bash
task operator-install-crds    # Install CRDs
task operator-generate        # Generate deepcopy, client code
task operator-manifests       # Generate CRD YAML, RBAC
task operator-test            # Run unit tests
task operator-e2e-test        # Run e2e tests
task crdref-gen              # Generate CRD API docs (run from cmd/thv-operator/)
```
