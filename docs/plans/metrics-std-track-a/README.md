# Metrics standardization — PR split plan

Splits the local work on `gautam/metrics-std-toolhive` into **PR #5956 plus one
stacked PR**, along the commit boundary that already exists in the branch history.

Implements the [Metrics Standardization RFC](https://github.com/stacklok/stacklok-enterprise-platform/blob/main/docs/rfcs/metrics-standardization.md)
(Phase 1, the Phase 2 prerequisites, and Phase 3).

**Status: nothing pushed. No commits rewritten.**

## The split

10 local commits sort into two contiguous blocks, already in the right order. No
reordering, no interactive rebase, no commit surgery.

```
fd179b4e8  Fix legacy-flag default on thv serve       ┐
a7c2cbd3a  Dual-emit vMCP metrics                     │  STACKED PR
580d3d51a  Dual-emit rate-limit metrics               │  (4 commits)
cfeb8c3b1  Add --otel-use-legacy-metrics              ┘
──────────────── split point: 4860adcbc ────────────────
4860adcbc  Correct false cardinality claims in docs   ┐
6cb43cb76  Fix dashboards and docs                    │
13d69bc4b  Register build_info once per process       │  PUSH TO #5956
70302f0a5  Make metrics assertions capable of failing │  (6 commits)
4408b1ee2  Harden vMCP backend-health gauge           │
09b576fdf  Restore semconv buckets, bound cardinality ┘
──────────────── origin/gautam/metrics-std-toolhive ────
```

| | Commits | Prod lines | Files |
|---|---|---|---|
| **#5956 gains** | 6 | +211 / −38 | 5 |
| **Stacked PR** | 4 | +510 / −76 | 20 |

The remote already contains the `stacklok.*` rename. #5956 stays a rename PR; the
stacked PR adds the dual-emission compatibility layer on top.

## Verification already performed

The split point was tested in a throwaway worktree at `4860adcbc`:

- `task build` — succeeds
- `go test ./pkg/telemetry/... ./pkg/ratelimit/... ./pkg/vmcp/core/... ./pkg/vmcp/internal/backendtelemetry/... ./pkg/vmcp/server/... ./pkg/mcp/...` — **all pass, zero failures**
- `git grep -E 'UseLegacyMetrics|LegacyInt64Counter|otel-use-legacy-metrics'` over `pkg/` and `cmd/` — **no hits**
- `pkg/telemetry/legacymetrics.go` — absent

Seven files appear in both blocks (`pkg/telemetry/middleware.go`,
`pkg/vmcp/internal/backendtelemetry/backendtelemetry.go`,
`pkg/vmcp/core/{core_vmcp,core_telemetry}.go`, `backendtelemetry_test.go`,
`docs/observability.md`, `docs/telemetry-migration-guide.md`) but the hunks do not
collide — which the passing build and tests at the split point prove.

The stacked PR needs **no rebase**: it is the current history, four commits with
single parents each.

## Documentation overlap — decided

`docs/observability.md` and `docs/telemetry-migration-guide.md` are edited in both
blocks. **Decision: leave as-is** (no commit surgery).

This is coherent rather than merely tolerable. Verified at `4860adcbc`, the guide:

- States metrics are a **hard cutover** with no legacy fallback, in an explicit
  signal-policy table (`Metric names and labels | Hard cutover, no legacy
  fallback`).
- Reserves its dual-emission language for **span attributes** behind
  `useLegacyAttributes`, which genuinely is dual-emitted at that commit.
- Contains no reference to `--otel-use-legacy-metrics` or `useLegacyMetrics`.

So #5956 ships an accurate description of its own behavior. The stacked PR then
reframes the metric half as a deprecation schedule, which is correct because that
PR is what introduces dual emission. Neither PR misrepresents what it ships.

## What each PR needs

### PR #5956 — push the 6 fix commits

Already-open PR; currently `CHANGES_REQUESTED`. These six commits address the
review feedback. Update the PR description to note what changed since the last
review, and reply to the review threads the commits resolve.

The six, and what they answer:

| Commit | Addresses |
|---|---|
| `09b576fdf` | Unbounded `mcp.method.name`/`http.request.method` cardinality; reverted #6144 semconv buckets |
| `4408b1ee2` | Backend-health gauge fired once at startup; unbounded recorded-health map |
| `70302f0a5` | Four assertions that could not fail (incl. one masking a genuinely absent metric) |
| `13d69bc4b` | `build_info` registered per-server instead of per-process |
| `6cb43cb76` | Dashboards and docs left behind by the rename |
| `4860adcbc` | False cardinality/completeness claims in docs |

**Still unresolved in #5956** — flag these in the PR description rather than
letting reviewers rediscover them:

1. **`stacklok_build_info` exports as `stacklok_build_info_ratio`.**
   `coremetrics.RegisterBuildInfo` sets `metric.WithUnit("1")`, so the Prometheus
   translator appends `_ratio`. `docs/observability.md` documents
   `stacklok.build_info`, so a fleet dashboard joining on `stacklok_build_info`
   returns empty. `_ratio` is also wrong for an identity gauge. Either drop the
   unit upstream in `toolhive-core` and bump the pin, or document the exact
   exported name and file the upstream issue.
2. **The cutover strategy itself** — which the stacked PR answers. Link it.

### Stacked PR — the 4 dual-emit commits

Base: the updated `gautam/metrics-std-toolhive`. Branch off `4860adcbc`.

Ships the dual-emission layer both reviewers asked for:
`pkg/telemetry/legacymetrics.go`, `--otel-use-legacy-metrics` (default `true`)
threaded through CLI/config/CRD, and legacy aliases for all 21 old metric names.

The four commits already read as stages, which is how to present them:

1. `cfeb8c3b1` — seam + flag plumbing + proxy aliases
2. `580d3d51a` — rate-limit aliases
3. `a7c2cbd3a` — vMCP aliases; guide reframed as a deprecation schedule
4. `fd179b4e8` — `thv serve` flag default; stale comments

Commit 1 carries the design judgement; the rest apply one pattern repeatedly.

**Points to raise in the PR description:**

- **RFC D13 says "no dual-emit window."** Two readings make this consistent with
  the RFC rather than an override: D13 scopes itself to "the deletion of **the six
  legacy twins**" (§3.5), not the 15 renames; and its closing sentence
  pre-authorizes revisiting *"if a scraped consumer is identified before the rename
  lands"* — the repo ships four Grafana dashboards. Ask `@reyortiz3` to confirm the
  reading. Do not frame it as overriding an Accepted decision.
- **Size**: ~510 prod lines / 20 files, over the 400-line/10-file budget. ~60% is
  mechanical alias repetition (21 aliases × field + construction + record call) plus
  regenerated CRDs and swagger. Offer the fallback split proactively: commit 1 as
  its own PR, then 2–4.
- **The removal release must be a literal version**, in both the startup warning and
  the guide, and they must agree.
- **Legacy aliases keep their old label keys.** The rate-limit metrics change both
  name and label (`toolhive_rate_limit_decisions{server=}` →
  `stacklok_toolhive_ratelimit_decisions_total{mcp_server=}`), so an alias emitting
  the old name with the new key would be a second breaking change, not a
  compatibility path.
- **Merged counters shift attempts → completions.** Four `_errors`/`_not_found`
  counters fold into an `outcome` label (D4); the new counters increment after the
  call where the originals incremented before. State whether the aliases preserve
  attempt semantics or the shift is accepted and documented.

## Sequencing

```
1. Push 6 fix commits to gautam/metrics-std-toolhive  → updates #5956
2. Reply to the review threads those commits resolve
3. Open the stacked PR from the 4 dual-emit commits, based on #5956
4. Link the two PRs in both descriptions
```

Do not squash. The commit boundaries are what make both PRs reviewable, and the
boundary at `4860adcbc` is the whole basis of this split.

## Verification before pushing

```bash
task lint-fix
task test
task build
```

Two tests fail on a pristine tree and are **not** caused by this work:

- `TestStandaloneSSE_ListChangedRefiltersThroughExistingMiddleware`
- `TestTelemetryProviderValidation/3,4,5`

For the stacked PR, also confirm the flag behaves:

```bash
./bin/thv run --otel-enable-prometheus-metrics-path fetch
curl -s localhost:<port>/metrics | grep -E '^(stacklok_|toolhive_)' | sort   # both families

./bin/thv run --otel-use-legacy-metrics=false --otel-enable-prometheus-metrics-path fetch
curl -s localhost:<port>/metrics | grep -cE '^toolhive_'                     # 0

# Legacy aliases must keep the OLD label key:
curl -s localhost:<port>/metrics | grep '^toolhive_rate_limit_decisions_total' | head -1
# must show server="..." NOT mcp_server="..."
```

## Reference specs

The per-PR specs in this directory predate this plan. They were written for a
hypothetical re-split of #5956 into small PRs from scratch, before it was clear the
local commits already divided cleanly.

They remain useful as **reference material** — each carries verified line
references, the reviewer-flagged defects, and test requirements:

| Spec | Maps to |
|---|---|
| [pr-a1](pr-a1-test-assertion-correctness.md) | commit `70302f0a5` |
| [pr-a2](pr-a2-proxy-observability-hardening.md) | commits `09b576fdf`, `13d69bc4b` (+ already-pushed work) |
| [pr-a3](pr-a3-vmcp-backend-health-gauge.md) | commit `4408b1ee2` |
| [pr-b1](pr-b1-rename-with-dual-emission.md) | the stacked PR (`cfeb8c3b1`…`fd179b4e8`) |
| [pr-b2](pr-b2-migration-guide-and-dashboards.md) | guide/dashboard work, split across both PRs |

They do **not** describe the plan above — the commit-boundary split supersedes them.
Use them for the detail, not the structure.
