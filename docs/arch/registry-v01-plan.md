# Registry v2 Refactor Plan

## Goal

Replace the Provider system (4,252 lines, 20 files, 7-method interface, 4 concrete providers, dual type systems) with a simple Store + Source design. ServerJSON becomes the single canonical type. Add v0.1 API endpoints. Support multiple registries. Break config/CLI.

Issue: [#4199](https://github.com/stacklok/toolhive/issues/4199)

## What Exists Today

- Skills v0.1 endpoint shipped (PR #4753): `GET /registry/{name}/v0.1/x/dev.toolhive/skills`
- Old Provider system: 4 concrete providers, factory singleton, dual types
- Studio: all calls go to `/api/v1beta/registry/*`
- Config: flat fields (`registry_url`, `registry_api_url`, `local_registry_path`)

## PR Split

Independent PRs that can merge in any order (no dependencies):

| PR | What | Size | Breaking? |
|---|---|---|---|
| **A** | Drop groups from registry data | Small | No (groups unused) |
| **B** | Remove legacy format support | Small | No (legacy was deprecated) |
| **C** | v0.1 server endpoints | Small | No (additive) |
| **D** | Registry-aware workload creation | Small | No (additive fields) |

Sequential PRs (each depends on previous):

| PR | What | Size | Breaking? | Depends on |
|---|---|---|---|---|
| **E** | Store/Source refactor (internal) | Large | No (internal) | A, B landed |
| **F** | Config rework | Medium | **Yes** | E |
| **G** | Multi-registry + type:server discovery | Medium | No | E + F |
| **H** | Delete old API endpoints | Small | **Yes** | C |
| **I** | CLI rework | Medium | **Yes** | E |

## PR Details

### PR A: Drop Groups

- `pkg/registry/upstream_parser.go` — remove group conversion
- `pkg/runner/retriever/retriever.go` — `handleGroupLookup` returns error
- `cmd/thv/app/group.go` — `groupRunCmdFunc` returns "no longer supported"
- Remove group-related types from provider output

### PR B: Remove Legacy Format

- Delete `parseRegistryData()` from `provider_local.go`
- Delete `isUpstreamFormat()`, `parseRegistryAutoDetect()` from `upstream_parser.go`
- Simplify `provider_local.go` and `provider_remote.go` to call `parseUpstreamRegistry` directly
- Remove legacy format validation from `pkg/config/registry.go`
- Update tests

### PR C: v0.1 Server Endpoints

Same pattern as the merged skills endpoint:
- `pkg/api/v1/registry_v01_servers.go` — list + get handlers
- `pkg/api/v1/registry_v01.go` — combined router + shared helpers
- Refactor skills into shared router
- Proxy support: `proxyIfNeeded` for API registries
- Mount at `/registry` in server.go

### PR D: Registry-Aware Workload Creation

- `pkg/api/v1/workload_types.go` — add `registry` + `server` to createRequest
- `pkg/api/v1/workload_service.go` — resolve from Provider, fill defaults, user overrides win
- `pkg/api/v1/skills.go` — add optional `registry` to install request

### PR E: Store/Source Refactor

The big internal change (zero external behavior change):
- `pkg/registry/source.go` — Source interface + EmbeddedSource, FileSource, URLSource
- `pkg/registry/store.go` — Store with local/proxied, lazy loading (sync.Once per entry), singleton
- `pkg/registry/convert.go` — ConvertServerJSON at execution boundary
- Migrate all callers to Store
- Delete: provider.go, provider_base.go, provider_local.go, provider_remote.go, provider_api.go, provider_cached.go, factory.go, upstream_parser.go, mocks/mock_provider.go

### PR F: Config Rework (breaking)

- Replace flat fields with `Registries []RegistrySource` + `DefaultRegistry`
- Add `RegistrySourceType`: file, url, api, server
- Deprecated YAML fields auto-migrate on load + persist
- Update Store to read from new config
- Update Configurator

### PR G: Multi-Registry + Server Discovery

- `type: server` — call `GET /v1/registries` on upstream, create proxied entries per discovered registry
- `GET /v1/registries` + `GET /v1/registries/{name}` admin endpoints
- Validate discovered registry names (prevent path injection)

### PR H: Delete Old API Endpoints

- Remove GET /api/v1beta/registry (list/get registries, list/get servers)
- Keep PUT, DELETE, auth login/logout

### PR I: CLI Rework (breaking)

```
thv registry list                        # list registries
thv registry servers [--registry R]      # list servers
thv registry server <name> [--registry R]  # server details
thv registry skills [--registry R]       # list skills
thv registry search <query> [--registry R] # search
thv registry set-default <name>          # set default
thv run <server> [--registry R]          # run from specific registry
```

## Design

### Two Kinds of Registries

**Local** — loaded into memory (embedded, file, URL).
**Proxied** — thv forwards requests to upstream API.

```
Client → thv API → Store
                     ├── "embedded"     → in-memory []*ServerJSON
                     ├── "my-local"     → in-memory []*ServerJSON
                     └── "cloud/default" → proxy to upstream
```

### Core Types

```go
type Source interface {
    Load(ctx context.Context) (*LoadResult, error)
}

type LoadResult struct {
    Servers []*v0.ServerJSON
    Skills  []types.Skill
}

type Store struct {
    local       map[string]*localRegistry   // sync.Once per entry
    proxied     map[string]*proxiedRegistry
    defaultName string
}
```

### Execution Boundary

```
Store.GetServer("", "foo") → *v0.ServerJSON
    → ConvertServerJSON() → types.ImageMetadata
    → runner.NewRunConfigBuilder(...)
```

## Studio Impact

Studio changes tracked separately. Key files:
- `common/api/registry-v01.ts` — new v0.1 API functions
- `renderer/src/features/registry-servers/lib/server-json-adapter.ts` — ServerJSON adapter
- Browse UI, run-from-registry, groups removal, settings, types cleanup

Testing: `THV_PORT=8181 pnpm start` with custom thv serve.

## Lessons Learned

- sync.Once per localRegistry (not manual bool) to avoid race conditions
- Validate discovered registry names (path injection prevention)
- Size-limit HTTP response bodies
- v0.1 handlers must extract {registryName} param
- Proxy response format may differ from local (upstream wraps in {"server": ...})
- Backward compat migration for old config fields
