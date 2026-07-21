# vMCP Library Embedding

## Overview

The `pkg/vmcp/` packages provide a stable Go library for embedding vMCP functionality into downstream projects. The library is designed for import — not just for internal use — and `github.com/stacklok/brood-box` is the reference production embedder.

## Why a Stability Table

Downstream consumers like `brood-box` need predictability across ToolHive releases. Without explicit stability guarantees, any refactor in `pkg/vmcp/` could silently break embedders. The stability table below formalises the contract: **Stable** packages have semver-aligned compatibility guarantees; **Experimental** packages may change before stabilising; **Internal** packages are not for external use.

## Library Embedding Pattern

### Recommended: assemble via `app.Builder`

The `pkg/vmcp/app` package is the recommended embedding surface: hand over a
`vmcpconfig.Config`, optionally decorate the core, and receive a ready-to-serve
`*server.Server`. The builder owns "construct once / wire once" — it builds the shared
collaborators (telemetry, the backend registry, elicitation) a single time and collapses
cleanup — so embedders no longer replicate the wiring that `pkg/vmcp/cli/serve.go` uses.

```go
import (
    "github.com/stacklok/toolhive/pkg/vmcp/app"
    vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
    "github.com/stacklok/toolhive/pkg/vmcp/core"
)

srv, _, cleanup, err := app.NewBuilder(ctx, cfg,
    app.WithVersion(version),
    app.WithHost(host, port),
    // Override any collaborator via interface-typed options, e.g. a custom registry:
    // app.WithBackendRegistry(myRegistry, myWatcher)
).
    Decorate(func(inner core.VMCP) core.VMCP { return myDecorator(inner) }). // optional (RFC THV-0076 seam)
    Finish()
if err != nil { /* handle */ }
defer cleanup()
if err := srv.Start(ctx); err != nil { /* handle */ }
```

`app.BuildCore` and `app.BuildServerConfig` are the lower-level primitives the builder is
built on; use them directly only for advanced composition (e.g. inspecting the derived
`*server.ServerConfig` before serving, or reusing the assembled `core.VMCP` elsewhere).

The `pkg/vmcp/` root package (`github.com/stacklok/toolhive/pkg/vmcp`) contains only shared
domain types (`types.go`, `errors.go`) and is always safe to import.

### Low-level: `server.New` (legacy composition root)

`server.New(ctx, cfg, router, backendClient, backendRegistry, workflowDefs)` is the
dependency-injection composition root that predates `app.Builder`. It requires the caller
to construct the router, backend client, and registry themselves. The reference embedder
[`github.com/stacklok/brood-box`](https://github.com/stacklok/brood-box) currently uses
this path (under `internal/infra/mcp/`); new embedders should prefer `app.Builder`, which
encapsulates exactly this wiring. Both remain supported.

## `pkg/vmcp/` Stability Table

The table below maps every sub-package to its stability level per RFC THV-0059. Verify against the merged RFC if there is a discrepancy.

| Package | Stability | Notes |
|---------|-----------|-------|
| `pkg/vmcp` (root) | Stable | Shared domain types (`BackendTarget`, `Tool`, etc.) and errors; public API |
| `pkg/vmcp/config` | Stable | Config structs and YAML loader; `Config`, `BackendConfig`, `OptimizerConfig` |
| `pkg/vmcp/aggregator` | Stable | Backend discovery and capability merge; `Aggregator` interface |
| `pkg/vmcp/router` | Stable | Request routing and tool name translation; `Router` interface |
| `pkg/vmcp/app` | Experimental | Config-driven assembly API for embedders. `Builder` (`NewBuilder`/`Decorate`/`Finish`) is the primary, supported entry point. `BuildCore`/`BuildServerConfig`/`Option` are **speculative** lower-level primitives kept public for advanced composition; they may be reshaped or unexported once the `Builder` covers the observed embedder needs. Prefer the `Builder` unless you specifically need to compose the primitives directly |
| `pkg/vmcp/server` | Stable | Server constructor and lifecycle; `New`, `Start`, `Stop`. The newly exported `LateBoundElicitationRequester` (and its `Bind`) is Experimental — see its doc comment |
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
