# Reviewers (natural-language ownership)

This file is the **source of truth for reviewer routing**. It is consumed by an
automated GitHub Action that, on every `pull_request`, feeds the PR diff plus the
ownership rules below to an LLM. The LLM returns a structured decision describing
who to request as reviewers and who to merely notify.

A few things to understand before editing:

- **This routing is ADVISORY.** Reviewers picked here are *requested* (and can be
  re-requested), but they do not by themselves block merge. They are a best-effort,
  context-aware replacement for coarse path-glob CODEOWNERS.
- **The hard merge GATE is deterministic and lives in `.github/CODEOWNERS`.** That
  slim file lists only the genuinely sensitive paths where a missing review must
  block merge regardless of what the Action decides. If a path is gated *and* routed
  here, both apply.
- **Two tiers per area:**
  - **Required** — people who should be added as *requested reviewers* for a matching change.
  - **Notify** — people who want *awareness only*. They are @-mentioned in a PR comment,
    NOT requested as reviewers. Use this for "I like seeing this part of the system change"
    without burdening folks who don't want the review load.
- **The CONDITION sentences are the whole point.** They let the LLM be more precise than a
  glob: scope reviewers to a sub-feature, or drop/skip reviewers for low-risk churn
  (generated code, doc regen, comment- or test-only edits). Prefer adding conditions over
  adding people.
- **Edits to this file are themselves gated** by `.github/CODEOWNERS` (it sits under
  `.github/`), so routing rules cannot be changed without a gating review.

Handles are case-sensitive and must match GitHub exactly (including the leading `@`).
The repo does not use GitHub teams, so all owners are individuals.

---

## Default

Covers everything not matched by a more specific section below.

- **Required:** @JAORMX

Always assign the default owner when no other section applies. If a more specific section
matches, prefer that section and drop the default unless the change is genuinely
cross-cutting (touching three or more areas), in which case keep @JAORMX as a tie-breaker.

---

## AI Agent Configuration

Paths: `CLAUDE.md`, `.claude/` (skills, agents, rules), `.github/REVIEWERS.md`

- **Required:** @JAORMX @jhrozek @rdimitrov @jerm-dro

Changes here alter what AI agents are allowed to do in CI, so always require the full set —
do not narrow this even for "small" wording tweaks, since prompt/rule wording is the security
surface. Edits to this `REVIEWERS.md` routing doc itself belong to this area as well.
Pure typo fixes inside a single rule's prose may skip the wider set and require only @JAORMX,
but anything touching agent permissions, tool allow-lists, or CI-invoked skills keeps the full list.

---

## CI / GitHub Actions / Supply Chain

Paths: `.github/workflows/`, `.github/actions/`, `.github/CODEOWNERS`, `.github/` (other)

- **Required:** @JAORMX @jhrozek @rdimitrov

Workflow and action changes are a supply-chain surface (they run with repo credentials), so
require careful review. If a workflow change adds or modifies a third-party action reference,
changes permissions/`secrets:` usage, or alters what gets published, also add @ChrisJBurns.
For release-related workflows (`releaser.yml`, `create-release-*.yml`, `helm-publish.yml`,
`image-build-and-publish.yml`, `skills-build-and-publish.yml`, `release-notes.yml`) see the
Release & Publishing section and add those owners on top. Editing `.github/CODEOWNERS` itself
always requires this set plus @JAORMX.

---

## Release & Publishing

Paths: `.github/workflows/releaser.yml`, `.github/workflows/create-release-pr.yml`,
`.github/workflows/create-release-tag.yml`, `.github/workflows/helm-publish.yml`,
`.github/workflows/image-build-and-publish.yml`, `.github/workflows/skills-build-and-publish.yml`,
`.github/workflows/release-notes.yml`

- **Required:** @JAORMX @rdimitrov
- **Notify:** @ChrisJBurns

Release plumbing controls what artifacts ship and how they are signed/published. Always require
the listed owners. Do not auto-skip these even for "version bump only" PRs. If the change touches
image signing, attestation, or registry push targets, treat it as higher risk and also pull in
@jhrozek.

---

## CLI (thv)

Paths: `cmd/thv/`, `cmd/help/`, `pkg/cli/`, `pkg/tui/`, `pkg/desktop/`, `docs/cli/`, `test/e2e/`

- **Required:** @JAORMX @ChrisJBurns @amirejaz @lujunsan @rdimitrov @jhrozek @reyortiz3 @aponcedeleonch

This is a broad, heavily-shared area. Lean on conditions to keep review load sane:
- If the PR is **only** regenerated CLI docs under `docs/cli/` (the output of `task docs`) with
  no change to command code, require just @JAORMX @ChrisJBurns and skip the rest.
- For **TUI-only** changes (`pkg/tui/`), require @ChrisJBurns @amirejaz and notify @lujunsan;
  the wider CLI list is not needed.
- For **desktop integration** changes (`pkg/desktop/`), require @ChrisJBurns @JAORMX.
- For new top-level commands or changes to flag parsing / global config wiring, require the full set.
- Comment-only or test-only edits under `test/e2e/` can require just @JAORMX @ChrisJBurns.

---

## HTTP API

Paths: `pkg/api/`, `pkg/server/`, `docs/server/`

- **Required:** @JAORMX @amirejaz @rdimitrov @reyortiz3 @aponcedeleonch

`pkg/server/discovery/` (client discovery / health) lives here too. If the PR is **only**
regenerated server/API docs under `docs/server/` (no handler change), require just
@JAORMX @amirejaz. If the change adds or modifies an authenticated endpoint or touches auth
middleware wiring, also add @jhrozek (see Security & Policy). New routes or changes to request
parsing/validation require the full set.

---

## Kubernetes (operator, proxyrunner, charts)

Paths: `cmd/thv-operator/`, `cmd/thv-proxyrunner/`, `pkg/operator/`, `deploy/charts/operator/`,
`deploy/charts/operator-crds/`, `config/webhook/`, `pkg/webhook/`, `pkg/k8s/`, `pkg/export/`,
`test/e2e/chainsaw/operator/`, `test/e2e/thv-operator/`, `docs/operator/`

- **Required:** @ChrisJBurns @JAORMX @jerm-dro @jhrozek @tgrunnagle @rdimitrov @reyortiz3 @blkt

Conditions to reduce noise on this large area:
- If the PR is **only** regenerated CRD reference docs (`docs/operator/`, output of
  `task crdref-gen`) or regenerated CRD manifests / deepcopy with no controller-logic change,
  require just @ChrisJBurns @jerm-dro and skip the rest.
- For **chart-only** changes under `deploy/charts/` (templates/values, no Go), require
  @ChrisJBurns @jerm-dro @blkt. Do NOT request review on `Chart.yaml` version bumps — the
  release process owns those; if a PR's only chart change is a version bump, skip charts review.
- For **webhook** changes (`config/webhook/`, `pkg/webhook/`) that affect admission/validation
  behavior, keep @ChrisJBurns @jhrozek @tgrunnagle.
- Controller / reconcile-logic changes (controllers under `cmd/thv-operator/`, `pkg/operator/`)
  **must always include @ChrisJBurns** as a required reviewer. CRD-schema (API) changes require the full set.
- Comment-only or generated-mock-only (`task gen`) edits can drop to @ChrisJBurns @JAORMX.

---

## Virtual MCP Server (vMCP)

Paths: `cmd/vmcp/`, `pkg/vmcp/`, `test/integration/vmcp/`

- **Required:** @JAORMX @jhrozek @jerm-dro @amirejaz @ChrisJBurns @tgrunnagle

vMCP has two auth boundaries (incoming OIDC for clients, outgoing RFC 8693 token exchange to
backends). If the change touches **outgoing auth / token exchange / per-backend credential
handling**, always include @jhrozek and @tgrunnagle (security-sensitive) — do not skip them
even for "small" edits there. If the change is to backend **aggregation/routing or composite
tool** logic only, @jhrozek may be Notify rather than Required. Comment-only or test-only edits
under `test/integration/vmcp/` can require just @jerm-dro @amirejaz.

---

## Core Runtime & Lifecycle

Paths: `pkg/workloads/`, `pkg/runner/`, `pkg/runtime/`, `pkg/state/`, `pkg/config/`,
`pkg/migration/`, `pkg/groups/`, `pkg/client/`, `pkg/core/`, `pkg/environment/`,
`pkg/lockfile/`, `pkg/updates/`, `pkg/versions/`, `pkg/healthcheck/`, `pkg/foreach/`,
`pkg/syncutil/`, `pkg/storage/`, `pkg/cache/`

- **Required:** @JAORMX @amirejaz @lujunsan

`pkg/core/` is the shared domain model — changes there ripple widely, so for `pkg/core/`
schema/type changes also Notify the vMCP and Kubernetes owners (@jerm-dro @ChrisJBurns).
Changes to `pkg/config/` or `pkg/migration/` that alter on-disk config format or run a data
migration are higher risk: require the full set and Notify @ChrisJBurns. Pure utility tweaks
in `pkg/foreach/`, `pkg/syncutil/`, `pkg/cache/`, or `pkg/lockfile/` (concurrency/util helpers)
can require just @amirejaz. `pkg/updates/` and `pkg/versions/` (self-update / version reporting)
can require @JAORMX @lujunsan.

---

## Infrastructure Abstractions

Paths: `pkg/container/`, `pkg/transport/`, `pkg/mcp/`, `pkg/networking/`, `pkg/labels/`,
`pkg/process/`, `pkg/server/discovery/` (transport adjacency), `pkg/templates/`,
`pkg/fileutils/`, `pkg/git/`

- **Required:** @JAORMX @jhrozek @blkt @amirejaz @ChrisJBurns @rdimitrov

Note: `pkg/container/verifier/` is NOT here — it belongs to Security & Policy (image signature
verification). For changes scoped to that subpath, route to Security instead.
- Changes to `pkg/networking/` that affect network isolation / egress-gateway behavior are
  security-relevant: always include @jhrozek and Notify @tgrunnagle.
- `pkg/transport/` changes affecting the stdio↔HTTP bridge or MCP message handling should
  include @jhrozek @amirejaz; pure label/process util tweaks (`pkg/labels/`, `pkg/process/`,
  `pkg/fileutils/`) can require just @amirejaz @blkt.
- `pkg/git/` and `pkg/templates/` (used by registry/skills tooling) can require @JAORMX @rdimitrov.

---

## Registry & Distribution

Paths: `pkg/registry/`, `pkg/skills/`, `pkg/script/`, `.github/workflows/update-registry.yml`

- **Required:** @JAORMX @rdimitrov @reyortiz3

`pkg/skills/` (ToolHive skills client/installer/resolver) and `pkg/script/` (Starlark execution
engine for skills/registry tooling) are folded in here. If a change to `pkg/script/` alters what
the Starlark sandbox can execute or its resource/syscall surface, treat it as security-sensitive
and also require @jhrozek. Routine registry data updates via the `update-registry.yml` automation
can require just @rdimitrov @reyortiz3.

---

## Security & Policy

Paths: `pkg/auth/`, `pkg/authz/`, `pkg/oauth/`, `pkg/oauthproto/`, `pkg/oidc/`,
`pkg/authserver/`, `pkg/secrets/`, `pkg/permissions/`, `pkg/container/verifier/`,
`pkg/audit/`, `pkg/certs/`, `pkg/security/`

- **Required:** @jhrozek @JAORMX @ChrisJBurns @tgrunnagle @rdimitrov

This area is the deterministic gate floor too (see `CODEOWNERS`), but routing still applies for
the broader awareness set. Conditions:
- **Token-exchange (RFC 8693)** changes in `pkg/oauth/`/`pkg/oauthproto/`/`pkg/authserver/`:
  always require @jhrozek @tgrunnagle; do NOT skip even for comment/test-only edits in those files,
  because the test fixtures encode the security contract.
- **Cedar policy / authz** changes (`pkg/authz/`): require @jhrozek @tgrunnagle.
- **Secret backend / handling** changes (`pkg/secrets/`): require @jhrozek @JAORMX.
- **Image verification** (`pkg/container/verifier/`, `pkg/certs/`): require @jhrozek @rdimitrov.
- For OIDC/OAuth changes that only adjust **provider URL validation or in-cluster host
  allow-listing**, keep @jhrozek but the rest may be Notify.
- Pure comment-only edits outside token-exchange/policy files may require just @jhrozek @JAORMX.

---

## Observability

Paths: `pkg/telemetry/`, `pkg/usagemetrics/`, `pkg/logger/`, `pkg/recovery/`, `pkg/sentry/`,
`pkg/ratelimit/`, `pkg/bodylimit/`

- **Required:** @ChrisJBurns @JAORMX @jerm-dro

`pkg/sentry/` (error reporting), `pkg/ratelimit/` and `pkg/bodylimit/` (protective middleware)
are grouped here. If a change to `pkg/ratelimit/` or `pkg/bodylimit/` changes a default limit or
makes a limit configurable in a way that weakens a protection, also Notify @jhrozek (DoS surface).
Pure log-message wording or added telemetry attributes can require just @ChrisJBurns. Changes to
`pkg/usagemetrics/` that alter what data is collected/emitted are privacy-relevant: require the
full set and Notify @JAORMX.

---

## Architecture Docs

Paths: `docs/arch/`

- **Required:** @JAORMX @amirejaz @rdimitrov @ChrisJBurns @jhrozek @tgrunnagle

Architecture decision records carry design intent. Require the full set for new ADRs or changes
to existing decisions. Typo/formatting-only fixes can require just @JAORMX. When an arch doc
change accompanies a code change in another area, prefer routing by the code area and add the
arch owners as Notify.

---

## Uncovered / fallthrough utilities

Paths: `pkg/llm/`, `pkg/llmgateway/`, `pkg/ignore/`, `pkg/json/`

- **Required:** @JAORMX

`pkg/llm/` and `pkg/llmgateway/` (the `thv llm` command bridging AI tools to OIDC-protected LLM
gateways — the "AI gateway") touch auth: **always require @ChrisJBurns for AI-gateway changes
(`pkg/llmgateway/`, `pkg/llm/`)**; for changes to their OIDC/token handling also require @jhrozek,
and Notify @amirejaz (CLI surface). `pkg/ignore/` (file-ignore globbing) and `pkg/json/` (encoding helpers)
are low-risk utilities — @JAORMX alone is fine. If any of these grow into a larger feature,
promote them into a dedicated section rather than leaving them here.
