# Plugins System

## Why This Exists

Plugins are the Claude Plugin manifest format — an OCI artifact containing a
`.claude-plugin/plugin.json` manifest plus component directories (`commands/`,
`agents/`, `skills/`, `hooks/`). A plugin bundles multiple component types into
a single installable unit, where a skill is a single component.

The plugins system mirrors `pkg/skills` structurally — the scoping model
(user vs. project), install-status lifecycle, storage shape, and OCI artifact
layout are identical — but diverges at the materialization seam: a skill
installs into a single directory, while a plugin must be materialized into
each target client's directory layout (Claude Code's plugins dir + settings.json
marketplace, Codex's `.agents/plugins` marketplace), and different clients load
different component subsets.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    pluginsvc.service                         │
│  Install (dispatch: git → OCI → registry name)              │
│  Uninstall · List · Info                                     │
│  per-(scope,name,projectRoot) pluginLock                     │
└───────────────┬─────────────────────────────────────────────┘
                │  MaterializationAdapter interface
                │  (Materialize / Dematerialize / EnsureRegistered /
                │   Health / SupportedComponents / ScopeSupport)
       ┌────────┴────────┐
       ▼                 ▼
┌─────────────┐   ┌──────────────┐
│ ClaudeCode  │   │    Codex     │
│ Adapter     │   │   Adapter    │
│ FS + JSON   │   │ FS + JSON    │
│ ~/.claude/  │   │ ~/.agents/   │
│ plugins/    │   │ plugins/     │
│ +settings   │   │ marketplace  │
│ .json       │   │ .json        │
└─────────────┘   └──────────────┘
```

### Phasing

- **Phase 2** (shipped): build/validate/push/list-builds/delete-build/get-content
  + SQLite storage + migration 002.
- **Phase 3** (this doc): install/list/info/uninstall + MaterializationAdapter
  (Claude Code + Codex) + groups integration.
- **Phase 4** (shipped, #5528): REST API + `thv ai-plugin` CLI + app wiring.
- **Phase 5b** (shipped, #5529): registry-name install wired via `lazyPluginLookup`;
  plain-name resolution through the registry lookup chain.

## Core Concepts

### Plugin Manifest

`.claude-plugin/plugin.json` — kebab-case name, version, description, author,
license, keywords. Parsed by `plugins.ParsePluginManifest` (`pkg/plugins/parser.go`).

### Component Inventory

A plugin declares component directories. The OCI config carries a
`ComponentInventory` (map of component-type → count): `commands`, `agents`,
`skills`, `hooks`, `mcpServers`, `lspServers`. Not all clients load all types.

### Installation Scopes

Identical to skills: `user` (user-wide) or `project` (project-local, must be a
git repository). Storage keys on `(scope, project_root)`.

### Multi-Client Materialization

Unlike skills (single directory per client), plugins materialize differently
per client:

| Client | Layout | Config mutation | Components loaded | Scope degradation |
|---|---|---|---|---|
| Claude Code | `~/.claude/plugins/<name>/` | `~/.claude/settings.json`: `enabledPlugins["<name>@toolhive"]` + `extraKnownMarketplaces.toolhive` (`directory` source pointing at the plugins root); shared `~/.claude/plugins/.claude-plugin/marketplace.json` (top-level `name`/`owner`/`plugins[]`, each `source: "./<name>"`) | commands, agents, skills, hooks | none |
| Codex | `~/.agents/plugins/toolhive/<name>/` (user) or `<root>/.agents/plugins/toolhive/<name>/` (project) | shared `.agents/plugins/marketplace.json` (top-level `name` + `plugins[]`, each with a `local` `source.path` = `./toolhive/<name>` relative to the marketplace root, plus `policy`/`category`). No `config.toml` mutation — see "Codex load step" below. | skills, mcpServers, hooks | none |

The `MaterializationAdapter` interface (`pkg/plugins/adapter.go`) owns this:
each adapter extracts the plugin tree and (optionally) mutates client config,
reporting which component types it deliberately dropped.

## Plugin Lifecycle

### 1. Discovery

OCI reference (`ghcr.io/org/plugin:v1`), git reference
(`git://host/owner/repo[@ref][#subdir]`), or plain name (registry lookup —
wired via `lazyPluginLookup`; `thv ai-plugin install <plain-name>`
resolves through the registry's `SearchPlugins` → first hit's OCI package).

### 2. Building

`pluginsvc.Build` packages a local plugin directory into an OCI artifact via
`ociplugins.PluginPackager`. The layer is a single tar.gz of the whole plugin
tree.

### 3. Installation

`pluginsvc.Install` dispatches by reference type:

1. **Git** (`gitresolver.IsGitReference`): clone via `pkg/git.Client`, read
   `.claude-plugin/plugin.json`, collect all files, build an in-memory tar.gz,
   then materialize.
2. **OCI**: pull via registry client, extract config + layer. **Name/repo
   consistency check**: the declared plugin name must match the OCI reference's
   last path component (422 on mismatch — prevents accidental clobbering; not
   publisher authenticity).
3. **Plain name**: local store → registry lookup → 404 with install hint.

After resolving the artifact, `installWithExtraction` resolves target clients,
acquires the per-plugin lock, calls each client's `MaterializationAdapter`,
builds the `InstalledPlugin` record, persists it (Create/Update with
upgrade-digest/same-digest-new-clients branches), and registers the plugin in
its group. When a `ClientManager` is configured, target trees are snapshotted
(files plus registration state via `Health`) before materialization; any later
failure — extraction, DB persist, group registration, or lock write — restores
the snapshot exactly (files, and `EnsureRegistered` only when the tree was
registered before) and joins every compensation error with the trigger.
Without a `ClientManager` (embedded/test services), compensation degrades to
dematerializing what this call wrote.

### 4. Uninstallation

`pluginsvc.Uninstall` is scope-dependent:

- **Unmanaged / user scope**: `Dematerialize` per client (best-effort,
  `errors.Join`), remove group memberships, then delete the store record.
  Group removal snapshots memberships first and restores them if an update
  midway or the DB delete fails, keeping the operation retryable. Idempotent.
- **Lock-managed project scope**: fails closed. Every stored client must have
  a materializer, the lock entry is removed after snapshotting client trees,
  and a later dematerialize/group/DB failure restores the pin, the trees, and
  adapter registration so the plugin is never left half-removed or
  installed-but-untracked.

### 5. Info

`pluginsvc.Info` returns metadata + the install record + two computed fields:
- `UnmaterializedComponents` — per client, component types the plugin declares
  that the adapter does NOT load (static diff of `Components` vs
  `SupportedComponents`).
- `ProjectScopeDegradedClients` — clients for which a project-scope install
  degraded (recomputed from scope + `DegradesOnProjectScope`).

## Git-Based Plugin Resolution

The git resolver reuses `pkg/skills/gitresolver`'s skill-agnostic helpers
(`ParseGitReference`, `IsGitReference`, `ResolveAuth`, `WriteFiles`,
`CloneConfigForRef`, `ClientForURL`) but does NOT call `Resolver.Resolve` —
that method is skill-specific (reads `SKILL.md`). Instead,
`pluginsvc.installFromGit` clones directly, reads the plugin manifest, and
collects the whole subtree (a plugin is multi-component, not a single file).

A **name/repo consistency check** (mirroring the OCI check) enforces that the
declared manifest name matches the name implied by the git reference — the
subdir's last segment when `#subdir` is present, else the repo's last segment
(422 on mismatch). When collecting files, the executable bit committed in the
repo is **preserved** (rather than forcing 0644) so hook scripts keep `+x`.

## Storage

`pkg/storage/sqlite/plugin_store.go` — `installed_plugins` +
`plugin_dependencies` tables (migration 002). Reuses the `entries` table's
`UNIQUE(entry_type, name)` with `entry_type = "plugin"`. The
`PluginStore` interface has Create/Get/List/Update/Delete + dependency methods.

## Group Integration

`groups.Group` carries a `Plugins []string` field (mirrors `Skills`).
`groups.AddPluginToGroup` / `groups.RemovePluginFromAllGroups` are line-for-line
ports of the skills analogues.

## Security Model

### Trust Model

Project-scoped plugin installs are verified against Sigstore signatures, and the project's `toolhive.lock.yaml` — the same file skills use, under its own `plugins:` key — records the trust decisions those verifications produce. A plugin contributes hooks, agents, commands, and MCP server declarations to whichever client loads it, so this is a strictly larger blast radius than a skill: the trust decision is never implicit.

On first install of a signed plugin the observed signer identity is recorded (trust on first use) as the entry's `provenance:` block and **displayed to the user** — by `thv ai-plugin install` when it completes, and by `thv ai-plugin info` on every later read. Each subsequent install, sync, and upgrade enforces that identity *inside* the Sigstore verification policy: OCI artifacts through their attached signature bundles, git commits through gitsign signature-and-chain verification (recorded `provisional: true` — the transparency-log proof of signing time is not yet validated, so the replay window is unbounded until that lands, and the marker is rendered wherever the identity is). `thv ai-plugin sync` additionally re-verifies each entry's stored signature bundle offline — embedded trust root, no network — before counting it current, so a tampered-with stored bundle is caught in CI rather than at the next install. `thv ai-plugin upgrade` refuses to move to an artifact signed by a different identity, or to an unsigned one, without an explicit `--allow-signer-change`.

Publishing is signed by default. `thv ai-plugin push` requires `--key` (a cosign private key), an OIDC identity token for keyless signing (`--identity-token`, or acquired automatically from an ambient CI token or an interactive browser sign-in), or an explicit `--no-sign`. Either signing path attaches the signature manifest next to the pushed artifact, where install-time verification finds it. A failed signing fails the push: an artifact published as if it were signed — which consumers then have to install with `--allow-unsigned` — is worse than a push that visibly did not complete.

Keyless signing is the only path ToolHive can currently verify end to end. Install-time verification is keyless-only (`verifier.VerifyOCI` checks a Fulcio certificate chain), and neither the lock schema nor the install API carries a public key, so a key-signed artifact fails verification as `ErrSignatureInvalid` — not `ErrUnsigned`, which means `--allow-unsigned` does not override it and a project-scoped install of one is refused outright. `--key` therefore publishes artifacts that are verifiable by cosign but not installable by ToolHive. This applies identically to `thv skill push --key`; closing it means threading a public key through the lock entry, the install options, and offline sync re-verification for both surfaces.

What is still trusted on faith, deliberately and visibly:

- **Unsigned plugins** install only with an explicit `--allow-unsigned`, recorded as `unsigned: true` in the lock entry. That entry is a standing exception: lock-driven operations (sync restores, upgrade re-pins) honor it without re-asking, and `thv ai-plugin info` renders it as `(unsigned — explicit exception)`.
- **The lock file itself** remains a repository-editable policy document. A diff converting a `provenance:` block to `unsigned: true` is a trust downgrade that sync will honor — it cannot happen without a lock file edit, which is exactly what lock-file review must catch. Reviewing `provenance`, `unsigned`, `digest`, and `resolvedReference` changes carries the same weight as reviewing the plugin content itself.
- **First use** anchors trust to whatever identity signed the artifact at that moment; verify the printed identity is the publisher you expect.
- **Declared-but-unmanaged components** (`mcpServers`, `lspServers`) are recorded in the inventory and shown by `info`, but ToolHive neither materializes nor lifecycle-manages them — their content is not covered by anything above beyond the artifact digest.

Plugins do not yet consume catalog-declared provenance the way skills do (`registry.Skill.Provenance`), so a plugin's first project-scoped install is always ordinary trust on first use.

See [Project Lock File](12-skills-system.md#project-lock-file) in the skills document for the lock file's schema and the shared verification internals.

### Archive Extraction Safety

Both adapters delegate to `skills.Installer.Extract`, which enforces:
decompression bomb limits, per-file size/count caps, symlink/hardlink rejection,
pre-extraction `ValidatePathNoSymlinks`, post-extraction `CheckFilesystem`, and
prefix-containment on every written file.

### Path Construction

`ClientManager.GetPluginPath` validates the plugin name (kebab-case, no `..`)
before `filepath.Join` — neither `clientType` nor `pluginName` can escape the
home/project dir.

### Marketplace registration (Codex)

The Codex adapter follows Codex's marketplace model. Codex discovers
marketplaces at a fixed set of paths, including the personal
`~/.agents/plugins/marketplace.json` and the per-repo
`$REPO_ROOT/.agents/plugins/marketplace.json`. The adapter extracts each
plugin's source under that marketplace root, namespaced by the marketplace name
(`<root>/toolhive/<name>`), and maintains the `marketplace.json` so Codex can
discover it. The manifest has a top-level `name` and a `plugins` array; each
entry has a `local` `source.path` that is **relative to the marketplace root**
and starts with `./` (`./toolhive/<name>`), plus the required `policy` and
`category` fields. Writes use `fileutils.WithFileLock` + `AtomicWriteFile`, and
the plugin name is validated (no traversal) by `GetPluginPath` before use.
`Dematerialize` removes the plugin's source directory and its `plugins` array
entry, deleting the manifest when no plugins remain.

#### Codex load step (manual, by design)

Unlike Claude Code, ToolHive does **not** fully automate Codex plugin loading,
and this is deliberate:

- **No `config.toml` mutation.** Codex's `[plugins."<name>@<marketplace>"]`
  `enabled` key is only an on/off toggle for an *already-installed* plugin;
  setting `enabled = true` for a plugin that was never installed does not load
  it, and writing to the user's shared `config.toml` has blast radius for no
  benefit. The adapter leaves `config.toml` untouched.
- **No shelling out.** Loading a Codex plugin requires an explicit install
  (`codex plugin install <name>@toolhive`), which is a CLI action. ToolHive
  never invokes client binaries to mutate their state (every other client
  integration is file-based config editing), so the adapter does not shell out
  to `codex`.

Instead, ToolHive lays down the plugin source and a discoverable
`marketplace.json`, and the user completes loading with a one-time:

```bash
# The personal marketplace at ~/.agents/plugins is auto-discovered; if needed:
codex plugin marketplace add ~/.agents/plugins
codex plugin install <name>@toolhive
```

Automating this (e.g. a dedicated integration that drives the Codex install flow
without shelling out) is tracked as a follow-up.

### settings.json Mutation (Claude Code)

The Claude Code adapter mutates `~/.claude/settings.json` (or the project
`<root>/.claude/settings.json`) under `fileutils.WithFileLock`. It adds
`enabledPlugins["<name>@toolhive"]` and an `extraKnownMarketplaces.toolhive`
entry whose `source` is a `directory` source pointing at the **plugins root**
(the directory containing all installed plugins), not the per-plugin directory.
A single shared `marketplace.json` lives at `<root>/.claude-plugin/marketplace.json`
with the required top-level `name`/`owner` fields and a `plugins` array; each
plugin is one entry with a `source: "./<name>"` path resolved against the
marketplace root. `upsert`/`remove` keep that array in sync as plugins are
installed and uninstalled, so a non-LIFO uninstall cannot invalidate the shared
marketplace path. `Dematerialize` removes the plugin from the `plugins` array
(deleting the manifest and its `.claude-plugin` dir when empty), reverts its own
`enabledPlugins` additions, and drops `extraKnownMarketplaces.toolhive` only when
no remaining `@toolhive` plugins are enabled.

### Name/Repo Consistency

The OCI install path rejects artifacts whose declared name doesn't match the
reference's repository last segment. This is a consistency check, not publisher
authenticity — `pluginConfig.Name` is self-declared. Publisher authenticity is
established separately, by signature verification (see [Trust Model](#trust-model)).

### Git Clone Bounds

The git client wraps both worktree and storer in `LimitedFs` (100MB total,
10k files), bounding the in-memory clone.

## Dependency on toolhive-core

The OCI plugin layer (`ociplugins.Store`, `PluginPackager`, `RegistryClient`,
`PluginConfig`, `ComponentInventory`) comes from
`github.com/stacklok/toolhive-core/oci/plugins`, mirroring how skills uses
`toolhive-core/oci/skills`.

## Key Files

| File | Purpose |
|---|---|
| `pkg/plugins/adapter.go` | `MaterializationAdapter` interface |
| `pkg/plugins/types.go` | `InstalledPlugin`, `PluginMetadata`, `ComponentInventory` |
| `pkg/plugins/options.go` | `InstallOptions`/`InstallResult`/`PluginInfo` etc. |
| `pkg/plugins/service.go` | `PluginService` interface |
| `pkg/plugins/pluginsvc/service.go` | concrete `service` + `With*` options + `pluginLock` |
| `pkg/plugins/pluginsvc/install.go` | Install dispatch |
| `pkg/plugins/pluginsvc/install_oci.go` | OCI pull + name/repo check |
| `pkg/plugins/pluginsvc/install_git.go` | git clone + manifest read |
| `pkg/plugins/pluginsvc/install_extraction.go` | shared materialize + persist core |
| `pkg/plugins/pluginsvc/list.go` / `info.go` / `uninstall.go` | lifecycle methods |
| `pkg/plugins/adapters/claudecode.go` | Claude Code adapter (FS + settings.json mutation) |
| `pkg/plugins/adapters/codex.go` | Codex adapter (FS + TOML) |
| `pkg/registry/api/plugins_client.go` | MCP v0.1 registry plugin search client |
| `pkg/api/v1/registry_v01_plugins.go` | HTTP handler for plugin search endpoint |
| `pkg/storage/sqlite/plugin_store.go` | SQLite store |
| `pkg/groups/plugins.go` | group membership |
| `pkg/client/plugins.go` | client metadata + path resolution |

## Related Documentation

- [Skills System](12-skills-system.md) - Sibling distribution system (OCI artifacts, multi-client install, build/publish/install lifecycle)
- [Registry System](06-registry-system.md) - Registry-name resolution in the install dispatch chain
- [Groups](07-groups.md) - Plugin group membership (`pkg/groups/plugins.go`)
- [Core Concepts](02-core-concepts.md) - Platform terminology
