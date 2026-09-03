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

On first install of a signed plugin the observed signer identity is recorded (trust on first use) as the entry's `provenance:` block and **displayed to the user** — by `thv ai-plugin install` when it completes, and by `thv ai-plugin info` on every later read. Each subsequent install, sync, and upgrade enforces that identity *inside* the Sigstore verification policy: OCI artifacts through their attached signature bundles, git commits through gitsign signature-and-chain verification (recorded `provisional: true` — the transparency-log proof of signing time is not yet validated, so the replay window is unbounded until that lands, and the marker is rendered wherever the identity is). `thv ai-plugin sync` additionally re-verifies each **OCI** entry's stored signature bundle offline — embedded trust root, no network — before counting it current, so a tampered-with stored bundle is caught in CI rather than at the next install. Git entries are not re-verified this way: a git install stores no bundle by design, because its signature lives on the commit rather than beside an artifact, so sync has nothing to re-check offline and an unchanged git plugin passes as current on its digest and content hash alone. A git entry's recorded identity is enforced when its content is next re-resolved — on drift, upgrade, or reinstall — not on every sync. `thv ai-plugin upgrade` refuses to move to an artifact signed by a different identity, or to an unsigned one, without an explicit `--allow-signer-change`.

Publishing is signed by default, and **keyless only**. `thv ai-plugin push` requires either an OIDC identity token (`--identity-token`, or acquired automatically from a GitHub Actions OIDC token — the only ambient provider implemented — or an interactive browser sign-in) or an explicit `--no-sign`. Signing attaches the signature manifest next to the pushed artifact, where install-time verification finds it.

A signed push is **staged and then promoted**: the content is published under its immutable digest, the signature is attached to that digest, and only then is the requested tag pushed. The ordering carries the guarantee. Publishing the tag first and signing afterwards would mean that any Fulcio, Rekor, or attachment failure leaves the tag already live and resolving to unsigned content — consumable by user-scoped installs with no verification at all — and returning an error does not retract it. Staged-then-promoted leaves the blobs uploaded but nothing tagged, so a consumer resolving the tag finds nothing. The staged manifest is untagged and garbage-collectable by the registry; ToolHive does not attempt to delete it, since the registry client exposes no delete and registries commonly forbid manifest deletion. An unsigned (`--no-sign`) push has no ordering constraint and publishes the requested reference directly.

The identity token itself is withheld from unprotected transports: the CLI refuses to send it to a `http://` ToolHive API on a non-loopback host, because it is a bearer credential redeemable at Fulcio for a certificate in the caller's name. HTTPS, loopback HTTP, and the Unix socket / named pipe transports are all accepted. A token-bearing push additionally **refuses to follow redirects** — clearing the base URL says nothing about where a 307 or 308 from that URL would replay the body, and those status codes preserve both method and body, so following one would hand the credential to whatever `Location` names. The ToolHive API does not redirect its own endpoints, so there is no legitimate case being given up.

There is deliberately **no `--key` flag**, and key signing is unrepresentable rather than rejected: `plugins.PushOptions` does not alias `skills.PushOptions` precisely so that it carries no `Key` field, the push DTO has none, and the push endpoint rejects unknown JSON fields so a `key` from an older client is a 400 naming the field instead of a silent unsigned publish. Making the request impossible to construct is the point — while the field existed, the in-process service answered a key with a 400 while the HTTP client dropped it and published unsigned, so the same `PluginService.Push` call meant different things depending on which implementation was wired in.

The reason is that install-time verification is keyless-only (`verifier.VerifyOCI` checks a Fulcio certificate chain), and neither the lock schema nor the install API carries a public key, so a key-signed artifact fails verification as `verifier.ErrKeySigned` — a verdict distinct from both `ErrUnsigned` and `ErrSignatureInvalid`, which means `--allow-unsigned` cannot override it and a project-scoped install is refused outright. Offering key signing would only let publishers produce plugins nobody can install. `thv skill push --key` still accepts one — carrying a caveat on the flag and in the [skills lock-file model](12-skills-system.md#project-lock-file) — and produces exactly this outcome. The flag returns here once install, sync, and the lock can carry a public key, tracked in [#6442](https://github.com/stacklok/toolhive/issues/6442).

**Migrating entries written before verification.** A lock entry recording neither a `provenance:` block nor `unsigned: true` predates trust recording — it was written while verification was gated off, or hand-edited. The lock schema permits that shape (validation enforces only that the two are mutually exclusive, not that one is present), so `pluginsvc.verifyStoredSignature` is the layer that rejects it: such an entry is reported as **drift**, never as current. `thv ai-plugin sync --check` surfaces it as drifted, `thv ai-plugin info` renders it as `(trust unrecorded)` rather than leaving it indistinguishable from an untracked install, and `thv ai-plugin sync` repairs it by reinstalling from the pinned reference, which runs install-time verification.

The repair completes on its own **only when a signature actually verifies**. Unsigned content fails closed and needs `thv ai-plugin sync --allow-unsigned` to proceed. This is the whole point of the migration rather than an inconvenience in it: repairing an unsigned legacy entry automatically would rewrite "no trust decision" into `unsigned: true` with nobody having chosen it, converting an entry that is visibly ambiguous into a standing exception that looks deliberate. Recording an unsigned exception is a trust decision, and nothing makes it on the user's behalf — so a lock-driven restore gets no implicit allowance (`isAllowedUnsigned` grants only the explicit flag), and the failure names `sync --allow-unsigned` as the remedy because `install --allow-unsigned` is not the operation that owns the lock entry.

An entry that already records `unsigned: true` is a different case and is honored without re-asking: restoring a decision the lock file states is not the same as inventing one.

Reinstalling is the intended migration, not `--adopt`: adoption records whatever the machine already has, whereas a reinstall re-fetches and verifies against the registry. A same-digest reinstall is also the repair path for a stored bundle that has gone stale or corrupt — the freshly verified bundle replaces it rather than being discarded.

**A lock entry only ever describes content the install that wrote it materialized.** An entry asserts a `contentDigest` hashed from the source just fetched, never read back from disk, so `dispatchExtraction` rematerializes whenever an install is the one bringing a record under lock tracking for the first time — even at an unchanged digest with every client already present, where it would otherwise short-circuit. Without that, a legacy tree modified since it was installed would stay active behind a brand-new entry naming a real signer and a pristine digest, and only a later `sync --check` would notice. The condition is the unmanaged-to-managed transition rather than "a bundle was freshly verified", which would miss two shapes that need it just as much: a git install produces no bundle at all (`VerifyGit` returns none — its signature lives on the commit, as above), and an `--allow-unsigned` reinstall produces no provenance, yet both write a `contentDigest`.

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
