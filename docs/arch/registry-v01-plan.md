# Plan: Simplified Registry System

## Context

The current registry system in `pkg/registry/` has excessive abstraction: a 9-method `Provider` interface, 4 concrete providers, a `BaseProvider` composition pattern, a `CachedAPIRegistryProvider` wrapper, a factory singleton, a `Manager` interface on top, and two parallel type systems (`ServerJSON` vs `ImageMetadata`/`RemoteServerMetadata`). All of this to do: "load a list of servers from somewhere and serve them."

This plan replaces the entire provider system with a simpler `Store` + `Source` design where `ServerJSON` is the single canonical type, adds v0.1 API endpoints, and cleans up config/CLI.

Issue: [#4199](https://github.com/stacklok/toolhive/issues/4199)

**Decisions:**
- `ServerJSON` is the single canonical storage type
- API registries are **proxied**, static sources (embedded, file, URL) loaded into memory
- Groups **dropped** from registry data (decision on future support deferred)
- Config and CLI changes are **breaking**
- `Provider` interface **deleted** — all callers use Store directly
- Old API data endpoints **deleted** (not kept temporarily)
- Everything on **one thv branch**, then Studio migrates in a separate branch

## Design

### Two Kinds of Registries

**Local** — loaded into memory from static sources (embedded, file, URL).
**Proxied** — thv forwards requests to an upstream API.

```
Client → thv API → Store
                     ├── "embedded"          → in-memory []*ServerJSON
                     ├── "my-local"          → in-memory []*ServerJSON
                     └── "cloud/default"     → proxy to upstream URL
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
    mu          sync.RWMutex
    local       map[string]*localRegistry
    proxied     map[string]*proxiedRegistry
    defaultName string
}
```

### Execution Boundary

```
Store.GetServer("", "foo") → *v0.ServerJSON
    → converters.ServerJSONToImageMetadata() → *types.ImageMetadata
    → runner.NewRunConfigBuilder(...)
    → runner.Run()
```

Conversion at point of use only (retriever + workload service).

### API (final state)

**New v0.1 endpoints:**
```
GET /registry/{name}/v0.1/servers
GET /registry/{name}/v0.1/servers/{serverName}/versions/latest
GET /registry/{name}/v0.1/x/dev.toolhive/skills              ← already shipped
GET /registry/{name}/v0.1/x/dev.toolhive/skills/{ns}/{name}  ← already shipped
```

**New admin endpoints:**
```
GET  /v1/registries
GET  /v1/registries/{name}
```

**Updated workload/skill creation:**
```
POST /api/v1beta/workloads  ← new optional: {"registry": "...", "server": "..."}
POST /api/v1beta/skills     ← new optional: {"registry": "..."}
```

**Kept:**
```
PUT  /api/v1beta/registry/{name}       ← config management (writes to Registries list)
POST /api/v1beta/registry/auth/login
POST /api/v1beta/registry/auth/logout
```

**Deleted:**
```
GET  /api/v1beta/registry
GET  /api/v1beta/registry/{name}
GET  /api/v1beta/registry/{name}/servers
GET  /api/v1beta/registry/{name}/servers/{serverName}
POST /api/v1beta/registry
DELETE /api/v1beta/registry/{name}
```

### Config (final state)

```yaml
registries:
  - name: my-local
    type: file
    location: /path/to/registry.json
  - name: cloud
    type: api
    location: "http://registry.example.com/registry/default"
default_registry: cloud
```

Old flat fields removed.

### CLI (final state)

```
thv registry list                        # list registries
thv registry servers [--registry R]      # list servers
thv registry server <name> [--registry R]  # server details
thv registry skills [--registry R]       # list skills
thv registry search <query> [--registry R] # search
thv registry set-default <name>          # set default registry

thv run <server> [--registry R]          # run from specific registry
thv skill install <name> [--registry R]  # install from specific registry
```

## Implementation

### Step 1: thv — everything on one branch

Build on one branch off main. Split into PRs for review after validation.

#### 1a. Store + Source (new core)
- `pkg/registry/source.go` — Source interface + EmbeddedSource, FileSource, URLSource
- `pkg/registry/store.go` — Store struct, local + proxied entries, query methods, global singleton
- `pkg/registry/convert.go` — ConvertServerJSON wrapper (for execution boundary)
- `pkg/registry/proxy.go` — reverse proxy helper for API registries

#### 1b. Delete old registry internals
- Delete: `provider.go`, `provider_base.go`, `provider_local.go`, `provider_remote.go`, `provider_api.go`, `provider_cached.go`, `factory.go`, `manager.go`, `manager_multi.go`, `upstream_parser.go`, `deprecation.go`
- Keep: `service.go` (updated), `errors.go`, `policy_gate.go`, `auth_manager.go`
- Keep: `api/` (HTTP client), `auth/` (OAuth)

#### 1c. v0.1 server endpoints
- `pkg/api/v1/registry_v01_servers.go` — list + get servers from Store (ServerJSON directly, no reverse conversion)
- Refactor `registry_v01_skills.go` into combined `RegistryV01Router`
- For proxied registries: handler checks `store.IsProxied()` and reverse-proxies

#### 1d. Registry admin endpoints
- `GET /v1/registries` + `GET /v1/registries/{name}`

#### 1e. Registry-aware workload/skill creation
- Add `Registry` + `Server` fields to `createRequest`
- Resolve from Store → ConvertServerJSON → build RunConfig
- User-provided fields override registry defaults

#### 1f. Config rework
- Replace flat fields with `Registries []RegistrySource` + `DefaultRegistry`
- Remove legacy format validation
- Update Configurator / `PUT /api/v1beta/registry/{name}` to write to `Registries` list

#### 1g. CLI rework
- New command structure under `thv registry`
- `--registry` flag on `run`, `skill install`, `search`

#### 1h. Delete old API endpoints
- Remove old `GET /api/v1beta/registry/*` data-serving endpoints
- Keep `PUT`, auth login/logout

#### 1i. Migrate all callers
- `pkg/runner/retriever/retriever.go` → use Store
- `cmd/thv/app/` → use Store
- `pkg/api/v1/registry.go` → remove deleted handlers
- `pkg/api/v1/workload_service.go` → use Store
- Rewrite tests

#### 1j. Validate
- `task build` + `task test` + `task lint`
- Manual testing with embedded catalog + registry server

### Step 2: Studio migration

After thv changes land on main.

#### 2a. Update workload creation
- `use-run-from-registry.tsx` → `POST /api/v1beta/workloads { registry, server }` + user overrides only
- Remove client-side field extraction (`prepareCreateWorkloadData` simplification)
- Same for remote server flow

#### 2b. Update browse UI
- Replace `GET /api/v1beta/registry/*/servers` with `GET /registry/{name}/v0.1/servers`
- New `server-json-adapter.ts` — flatten ServerJSON for UI components
- Update route files and components

#### 2c. Drop groups
- Remove group cards from browse page
- Delete group detail page (`registry-group_.$name.tsx`)
- Delete `InstallGroupButton`

#### 2d. Update registry list
- Replace `GET /api/v1beta/registry` with `GET /v1/registries`

#### 2e. Clean up types
- Delete `registry-types.ts` custom types
- Regenerate API client from updated OpenAPI spec

## Files

### New
| File | What |
|---|---|
| `store.go` | Store, query methods, singleton |
| `source.go` | Source interface + 3 implementations |
| `convert.go` | ServerJSON → ToolHive type converters |
| `proxy.go` | Reverse proxy for API registries |
| `registry_v01_servers.go` | v0.1 server handlers |

### Deleted
| File | Replaced by |
|---|---|
| `provider.go` | Store |
| `provider_base.go` | Store query methods |
| `provider_local.go` | EmbeddedSource + FileSource |
| `provider_remote.go` | URLSource |
| `provider_api.go` | proxy.go + convert.go |
| `provider_cached.go` | Store / proxy |
| `factory.go` | Store singleton |
| `manager.go` | Store |
| `manager_multi.go` | Store |
| `upstream_parser.go` | Inlined into sources |
| `deprecation.go` | Deleted |

### Kept
- `pkg/registry/api/` — HTTP client
- `pkg/registry/auth/` — OAuth
- `auth_manager.go`, `errors.go`, `policy_gate.go`, `service.go`

## PR Split (after validation on one branch)

| PR | Scope | Breaking? |
|---|---|---|
| **A** | Store/Source + delete old providers | Internal |
| **B** | v0.1 server endpoints + registry admin endpoints | Additive |
| **C** | Registry-aware workload/skill creation | Additive |
| **D** | Config rework | **Yes** |
| **E** | CLI rework | **Yes** |
| **F** | Delete old API endpoints | **Yes** |
| **G** | Studio migration (separate repo) | FE |

## What Remains After All PRs

- **Registry server discovery** — `type: server` calling `GET /v1/registries` on upstream to discover registries
- **E2E tests** — for v0.1 endpoints, multi-registry, registry-aware workload creation
- **Operator alignment** — operator has own `registryapi` package (not blocking)
- **Groups** — decision deferred on whether to reintroduce as config/API-based feature

## Verification

1. `task build` + `task test` + `task lint`
2. `POST /api/v1beta/workloads {"registry":"default","server":"..."}` → workload starts
3. `GET /registry/default/v0.1/servers` → ServerJSON list
4. `GET /registry/default/v0.1/x/dev.toolhive/skills` → skills ✅ (shipped)
5. `GET /v1/registries` → registry list
6. `thv registry list` → shows registries
7. `thv registry servers` → shows servers
8. `thv run <server> --registry default` → runs from registry
9. Old `GET /api/v1beta/registry/*` → 404
10. Studio: browse servers via v0.1 → display correctly
11. Studio: click "Run" → single POST with registry reference → workload starts