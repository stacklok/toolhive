# vMCP Library Embedding

## Overview

The `pkg/vmcp/` packages provide a stable Go library for embedding vMCP functionality into downstream projects. The library is designed for import — not just for internal use — and `github.com/stacklok/brood-box` is the reference production embedder.

## Why a Stability Table

Downstream consumers like `brood-box` need predictability across ToolHive releases. Without explicit stability guarantees, any refactor in `pkg/vmcp/` could silently break embedders. The stability table below formalises the contract: **Stable** packages have semver-aligned compatibility guarantees; **Experimental** packages may change before stabilising; **Internal** packages are not for external use.

## Library Embedding Pattern

### Importing `pkg/vmcp/`

```go
import (
    vmcpcore "github.com/stacklok/toolhive/pkg/vmcp/core"
    vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
    "github.com/stacklok/toolhive/pkg/vmcp/server"
    "github.com/stacklok/toolhive/pkg/vmcp/aggregator"
    "github.com/stacklok/toolhive/pkg/vmcp/router"
)
```

The `pkg/vmcp/` root package (`github.com/stacklok/toolhive/pkg/vmcp`) contains only shared domain types (`types.go`, `errors.go`) and is always safe to import. Embedders that need the split public API import `pkg/vmcp/core` for the domain object and `pkg/vmcp/server` for the HTTP/MCP transport.

### Reference Implementation: brood-box

[`github.com/stacklok/brood-box`](https://github.com/stacklok/brood-box) embeds `pkg/vmcp/` under `internal/infra/mcp/`. It demonstrates the recommended pattern:

1. Load a `vmcpconfig.Config` from YAML or programmatically.
2. Instantiate an aggregator, router, backend client, backend registry, session factory, and any workflow definitions.
3. Use `server.New(ctx, cfg, rt, backendClient, backendRegistry, workflowDefs)` when you want the stable composition root to assemble `core.New(...)` and `server.Serve(...)` for you.
4. Call `server.Start(ctx)` and `server.Stop(ctx)` for lifecycle management.

This is the same path used by `pkg/vmcp/cli/serve.go` in the `thv vmcp serve` command; the library has no CLI-specific coupling.

### New/Serve Split

The public API now separates the vMCP domain object from the transport:

```go
coreVMCP, err := vmcpcore.New(coreCfg)
srv, err := server.Serve(ctx, coreVMCP, serverCfg)
```

`core.New` builds the identity-parameterized `core.VMCP` domain object. It owns capability aggregation, routing, composite workflows, admission checks, and backend health. Its methods use `pkg/vmcp` domain types and an explicit `*auth.Identity`; no `mcp-go` types cross this boundary.

`server.Serve` wraps an already-constructed `core.VMCP` with the MCP/HTTP transport. It owns the mcp-go server, session hooks, session storage, handler construction, lifecycle wiring, status reporting, metrics routes, and optional embedded auth-server routes.

`server.New` remains the stable convenience path for existing embedders. It derives the core and transport configs, calls `core.New(...)`, optionally wraps the core with configured decorators, then calls `server.Serve(...)`. It is retained, not deprecated.

### Decorator Extension Model

Extension at the domain layer is done by wrapping an inner `core.VMCP`. A decorator should hold only `inner core.VMCP`, filter `List*` results, and refuse matching `CallTool`, `ReadResource`, or `GetPrompt` operations before delegating. This makes decorators subtract-only: they can hide or deny capabilities, but they cannot widen backend reachability because they have no backend access except through `inner`.

When extending the transport instead, use `(*server.Server).Handler(ctx)` as the supported outermost-wrapping seam. An embedder can mount the fully composed vMCP handler in its own mux, apply its own middleware outside ToolHive's internal chain, and serve sibling routes without reordering vMCP authentication, parsing, telemetry, or session handling.

## `pkg/vmcp/` Stability Table

The table below maps every sub-package to its stability level per RFC THV-0059. Verify against the merged RFC if there is a discrepancy.

| Package | Stability | Notes |
|---------|-----------|-------|
| `pkg/vmcp` (root) | Stable | Shared domain types (`BackendTarget`, `Tool`, etc.) and errors; safe for public import |
| `pkg/vmcp/config` | Stable | Config structs and YAML loader; `Config`, `BackendConfig`, `OptimizerConfig` |
| `pkg/vmcp/core` | Stable | Identity-explicit domain API; `VMCP`, `Config`, `New` |
| `pkg/vmcp/aggregator` | Stable | Backend discovery and capability merge; `Aggregator` interface |
| `pkg/vmcp/router` | Stable | Request routing and tool name translation; `Router` interface |
| `pkg/vmcp/server` | Stable | Transport constructor and lifecycle; `Serve`, stable wrapper `New`, `Handler`, `Start`, `Stop` |
| `pkg/vmcp/session` | Stable | Session factory and per-session routing table |
| `pkg/vmcp/auth` | Stable | Incoming/outgoing auth interfaces; `IncomingAuthenticator`, `OutgoingAuthRegistry` |
| `pkg/vmcp/client` | Stable | Backend HTTP client; used for all backend MCP calls |
| `pkg/vmcp/health` | Stable | Health monitor; `HealthMonitor` interface and implementations |
| `pkg/vmcp/status` | Stable | `StatusReporter` interface; CLI and K8s reporter implementations |
| `pkg/vmcp/optimizer` | Experimental | Optimizer interface and TEI integration; tier API may evolve |
| `pkg/vmcp/cli` | Experimental | New in Phase 4; `Serve`, `Init`, `Validate` entry points may change before stabilisation |
| `pkg/vmcp/composer` | Experimental | Composite tool DAG executor; workflow API not yet stable |
| `pkg/vmcp/cache` | Internal | Token cache; not intended for external use |
| `pkg/vmcp/conversion` | Internal | CRD-to-config conversion; K8s-specific, not for local embedding |
| `pkg/vmcp/discovery` | Internal | Discovery middleware; use via aggregator, not directly |
| `pkg/vmcp/headerforward` | Internal | Per-backend HTTP header-forwarding round-tripper; the root `headerforward` package only; `pkg/vmcp/headerforward/wirefmt` is a separate wire-format helper used by aggregator/workloads/cli/operator |
| `pkg/vmcp/k8s` | Internal | Kubernetes-specific discovery; not for local embedding |
| `pkg/vmcp/workloads` | Internal | Backend workload helpers for K8s mode; not for local embedding |
| `pkg/vmcp/schema` | Internal | MCP schema parsing; subject to change |

## Stability Declaration Convention

The `pkg/vmcp/` sub-packages do not currently carry in-source stability annotations. The stability levels in the table above are derived from RFC THV-0059 and are documented here as the authoritative reference for downstream consumers. Reviewers should consult this table (and the RFC) when evaluating whether a proposed change to a `pkg/vmcp/` package constitutes a breaking change.

## Compatibility Guarantees for Stable Packages

For packages marked **Stable**:

- **No breaking API changes** between patch and minor releases.
- **No import-path renames** without a compatibility shim and deprecation notice.
- **Deprecation policy**: a package or function is deprecated with a `// Deprecated:` comment for at least one minor release before removal.
- **Semver alignment**: breaking changes (if ever necessary) are reserved for major version bumps.

For packages marked **Experimental**:

- The API may change in any minor release.
- Changes will be noted in the release changelog.
- Callers should pin to a specific minor version until the package stabilises.

For packages marked **Internal**:

- No compatibility guarantees of any kind.
- These packages may be reorganised, merged, or removed at any time.

## Guidance for Downstream Embedders

### Pinning

Pin to a specific ToolHive minor version in your `go.mod`:

```
require github.com/stacklok/toolhive v0.Y.Z
```

Watch the [ToolHive changelog](https://github.com/stacklok/toolhive/releases) for Experimental package changes before upgrading.

### Upgrading

1. Check the release notes for any changes to packages you import.
2. Run `go mod tidy` after updating the version.
3. Ensure your tests cover the vMCP integration paths so breaking changes are caught early.

### What ToolHive Does Not Provide for Embedders

- Goroutine leak protection in Experimental/Internal packages — test your shutdown paths.
- Guarantees about the behaviour of K8s-internal packages (`k8s`, `workloads`, `conversion`) outside a Kubernetes environment.

## Related Documentation

- [Local vMCP CLI Mode](vmcp-local.md) — `thv vmcp` CLI surface and optimizer lifecycle
- [Virtual MCP Server Architecture](10-virtual-mcp-architecture.md) — Kubernetes-side vMCP (CRD, operator)
- [Groups](07-groups.md) — ToolHive groups used as vMCP backend source
