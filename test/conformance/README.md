# MCP Conformance

Runs the [MCP conformance suite](https://github.com/modelcontextprotocol/conformance)
against a ToolHive deployment to verify that ToolHive's proxy/transport layer is
spec-transparent.

## What it does

ToolHive is a proxy in front of MCP servers, so the suite is run against the
endpoint that `thv run` exposes. `run-conformance.sh`:

1. Clones the conformance repo at a pinned version and builds its reference
   server (`examples/servers/typescript/everything-server.ts`, which implements
   the fixtures the suite requires) into a container image using `Dockerfile`.
2. Starts it through `thv run --transport streamable-http`.
3. Points `npx @modelcontextprotocol/conformance server` at the proxied
   `/mcp` endpoint, gated by `expected-failures.yaml`.

The reference server is **not** the npm `@modelcontextprotocol/server-everything`
package — that is an unrelated server that does not implement the conformance
fixtures.

## Running locally

```bash
task conformance
```

Requires Docker and Node.js. Results are written to `test/conformance/results/`.

## Version pinning

`CONFORMANCE_VERSION` (default in `run-conformance.sh`) pins **both** the npm
tool (`@modelcontextprotocol/conformance@$VERSION`) and the git server source
(`git --branch v$VERSION`). Repo tags map 1:1 to npm versions, so bump both by
changing this one value. Renovate does not track this pair — bump it manually.

## The baseline (`expected-failures.yaml`)

Lists scenarios that are known to fail and accepted. CI fails only on failures
**not** listed here. An entry that starts passing again is reported as *stale*
and also fails CI, so keep the list accurate. The CLI (`thv run`) proxy is
transparent, so the list is currently empty.

## Targets

`run-conformance.sh` supports two targets via `CONFORMANCE_TARGET`:

- **`proxy`** (default, `task conformance`) — runs the reference server through
  `thv run` and tests the transparent CLI proxy endpoint. This path is
  transport-transparent and does **not** exercise ToolHive's own MCP serving
  code, so its baseline (`expected-failures.yaml`) is empty.
- **`vmcp`** (`task conformance-vmcp`) — runs the reference server as a backend
  in a group, aggregates it through `thv vmcp serve`, and tests the vMCP
  endpoint. vMCP terminates and re-serves MCP using the migrated
  `mcpcompat`/go-sdk serving surface, so this target is what actually exercises
  that code. The generated config is patched from the `prefix` conflict strategy
  to `priority` so backend tool names are preserved (unprefixed) for the suite's
  fixtures, and the run asserts `serverInfo.name` identifies vMCP before testing.
  Its baseline is `expected-failures-vmcp.yaml`.

## Scope

Covers the CLI (`thv run`) proxy path and the vMCP aggregator path
(`thv vmcp serve`) with the `active` suite. The vmcp target is what exercises
ToolHive's `mcpcompat`/go-sdk serving surface; its baseline lives in
`expected-failures-vmcp.yaml` (sampling, completions, and resource subscribe are
expected to fail until vMCP supports them). Not covered:

- **Kubernetes operator proxy** — it rewrites the outbound `Host` header to the
  upstream Service identity, which breaks MCP servers that validate `Host` (per
  the spec's DNS-rebinding guidance). Blocked on resolving that before a k8s
  conformance gate is meaningful.
- **stdio→streamable-http bridge** — the reference server is HTTP-only; covering
  the bridge needs a stdio fixture server.
