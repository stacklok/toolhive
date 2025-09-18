# Proposal: Use the MCP Registry via ToolHive configuration (run by name, no per-run server.json)

Enable ToolHive to run MCP servers published in the upstream MCP Registry by configuring that registry in ToolHive’s config. Users will keep invoking servers “by name” with `thv run`, and ToolHive will resolve those names through the configured registry. We will not accept server.json files as inputs to `thv run`.

## User experience

A single configuration step selects the registry (URL or local file). After that, users keep invoking `thv run <name>` exactly as today. When the configured registry is the upstream MCP Registry, the name is resolved against it and the server runs with the same ergonomics.

Accepted inputs for `thv run` are unchanged:
- by name (resolved via the configured registry)
- by image reference
- by protocol scheme (`uvx://`, `npx://`, `go://`)
- by remote MCP URL

## Configuration surface (reuse what we already have)

We keep the current configuration model. The application config type is [`config.Config`](pkg/config/config.go:25) and the provider interface is [`config.Provider`](pkg/config/interface.go:7).

Setting the active registry:
- URL registry via [`config.SetRegistryURL()`](pkg/config/interface.go:45) (backed by [`config.setRegistryURL()`](pkg/config/registry.go:37)).
- Local file registry via [`config.SetRegistryFile()`](pkg/config/interface.go:50) (backed by [`config.setRegistryFile()`](pkg/config/registry.go:80)).

Inspecting the current selection:
- [`config.GetRegistryConfig()`](pkg/config/interface.go:60) (backed by [`config.getRegistryConfig()`](pkg/config/registry.go:136)).

The fields `registry_url`, `local_registry_path`, and `allow_private_registry_ip` already exist on [`config.Config`](pkg/config/config.go:25). To use the upstream MCP Registry, set `registry_url` to the service’s base URL. To use a local file registry, set `local_registry_path`.

## Name resolution flow (what changes, what doesn’t)

The entrypoint and builder/runner pipeline are unchanged: we still assemble run state in [`app.BuildRunnerConfig()`](cmd/thv/app/run_flags.go:225), parse protocol/image/remote inputs via [`runner.ParseProtocolScheme()`](pkg/runner/protocol.go:73), construct the configuration with [`runner.NewRunConfigBuilder()`](pkg/runner/config_builder.go:629) and [`runner.RunConfig`](pkg/runner/config.go:34), and execute with [`runner.Runner.Run()`](pkg/runner/runner.go:91).

For “by name” runs, we add a registry-kind check before resolution. If [`config.GetRegistryConfig()`](pkg/config/interface.go:60) indicates a file registry, we read locally as before. If it indicates a URL registry pointing at the upstream MCP Registry, we query it and receive a server.json document. We then convert that document to ToolHive metadata through our upstream model and converter—see [`registry.UpstreamServerDetail`](pkg/registry/upstream.go:6) and [`registry.ConvertUpstreamToToolhive()`](pkg/registry/upstream_conversion.go:38)—so the rest of the flow proceeds identically: metadata plus flags go through the builder into a [`runner.RunConfig`](pkg/runner/config.go:34), then to runtime. Transport, ports, permissions, middleware (OIDC/authz/audit/telemetry), and environment/secret handling remain the same.

## Upstream package types (docker, npm, pypi) and flow integration

This section documents how we will read package data from the upstream MCP Registry (`server.json`) and map it into ToolHive’s existing execution paths. It uses the current upstream schema fields and keeps the CLI and builder flow unchanged; the only code change needed is a small adjustment in the registry retrieval path to enable protocol builds when package data originates from the registry.

### Schema fields we will read

We use the upstream JSON Schema at https://static.modelcontextprotocol.io/schemas/2025-07-09/server.schema.json. From `ServerDetail.packages[]`, we consume: `registry_type` (e.g., npm, pypi, oci), `identifier` (package name or image coordinates), `version` (a concrete version), and `transport` (stdio, streamable-http, or sse). Our current internal “upstream” model used older field names; we will update it and the converter to the official names (`registry_type`, `identifier`, `version`, `transport`).

### How ToolHive will map supported types

We will initially support Docker (OCI), npm, and PyPI. Other ecosystems (e.g., nuget, mcpb) are out of scope for now and can be added incrementally.

- OCI (“docker” in our docs, “oci” in upstream):
  - Mapping: Build an image reference from identifier and version. If identifier already includes tag or digest, we respect it; otherwise, we form repo/name:version.
  - Execution: Run as a normal container image via the existing path (no runtime changes).
  - Transport: We map the upstream transport into ToolHive’s transport enum when building `RunConfig`.

- npm:
  - Mapping: Convert the package into a protocol-encoded form using npx, e.g., npx://@scope/pkg@version (exact string assembled by the converter).
  - Execution: Same protocol build pathway ToolHive already uses for CLI protocol inputs; see “Flow integration” below.

- PyPI:
  - Mapping: Convert the package into a protocol-encoded form using uvx, e.g., uvx://package==version (the pin syntax follows uv’s conventions; final string assembly happens in the converter).
  - Execution: Same protocol build pathway as npm.

### Flow integration (what changes, what stays)

The CLI continues to accept the same inputs and, after resolution, we still construct a `RunConfig` with the existing builder and execute with the current transport stack. The only change is in the registry resolution path: if the converted `ImageMetadata.Image` is a protocol-encoded string (`npx://` or `uvx://`), the retriever re-detects the protocol and synthesizes a temporary image just as the CLI path does. Practically, after receiving `ImageMetadata` from conversion, [`retriever.GetMCPServer`](pkg/runner/retriever/retriever.go:41) checks the string with [`runner.IsImageProtocolScheme()`](pkg/runner/protocol.go:402) and, when true, builds via [`runner.HandleProtocolScheme()`](pkg/runner/protocol.go:29), then proceeds with verification, pulling, and normal execution. This keeps the CLI and builder/runner behavior intact while making the registry-driven path feature-parity with direct protocol inputs.


In short, we consume registry_type, identifier, version, and transport from server.json; support oci (direct image), npm (npx protocol build), and pypi (uvx protocol build) initially; update our upstream model/converter to the official field names; and add a small retriever hook so protocol strings originating from the registry take the same build path as CLI protocol inputs. Additional ecosystems can be added incrementally following the same convert‑then‑run pattern.

## Security and validation

- Registry URL validation and private-IP policies already live in [`config.setRegistryURL()`](pkg/config/registry.go:37); we keep enforcing them.
- No new `thv run` inputs means no new shell-injection surface. Protocol and image execution paths remain unchanged.
- Optional: for future integrity features (e.g., file hashes in MCPB flows), we can extend the conversion or pull/verify step without changing the UX.

## Documentation changes (concise)

- “Config → Registry” docs: show how to set `registry_url` to the upstream MCP Registry and how to switch back to a local file registry.
- “Run” docs: clarify that “by name” always resolves through the configured registry; no additional input forms are needed.

## What we are deliberately not doing

- Not adding `thv run ./server.json` or `thv run https://…/server.json`.
- Not introducing new `mcp-reg://…` run forms.
- Not changing the `thv run` flag surface.

## Outcome

- A single place to configure the source of truth (the registry), which can be the upstream MCP Registry or a local file registry.
- The `thv run <name>` path remains simple and predictable while gaining upstream ecosystem coverage via conversion inside the resolver.
- Minimal change to user workflows; maximal interoperability.
