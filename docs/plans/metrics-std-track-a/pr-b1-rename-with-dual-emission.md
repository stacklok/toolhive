# PR B1 — Rename metrics to `stacklok.*` with dual emission

- **Depends on**: nothing hard. Soft-conflicts with [A2](pr-a2-proxy-observability-hardening.md) and [A3](pr-a3-vmcp-backend-health-gauge.md) (shared files); land those first to keep this diff a pure rename.
- **Blocks**: [B2](pr-b2-migration-guide-and-dashboards.md).
- **Size**: ~943 prod lines + ~670 test lines, 17 files. **Well over budget — see [Size](#size-read-this-first).**
- **Type of change**: Breaking change (names change; old names retained behind a flag defaulting to on).
- **Source**: commits `cfeb8c3b1`, `580d3d51a`, `a7c2cbd3a` on `gautam/metrics-std-toolhive`.
- **RFC**: §3.2, §3.3, D1, D4, D10, D11; D13 (see [below](#on-rfc-d13)).

## Why

RFC **D1** sets one naming scheme for Stacklok-authored metrics:
`stacklok.<service>.<subsystem>.<name>`. Today ToolHive emits `toolhive_mcp_*`,
`toolhive_rate_limit_*`, and `toolhive_vmcp_*` — three of the five schemes the RFC
exists to collapse. **D11** makes vMCP its own service namespace rather than a
subsystem of `toolhive`.

Every renamed metric is **also emitted under its old name** behind
`--otel-use-legacy-metrics`, defaulting to `true`. Nothing breaks on upgrade.

## Size — read this first

~943 prod lines / 17 files, well past the 400-line/10-file guideline. This is a
conscious trade and the PR description must say so.

**Why it is one PR.** The flag mechanism and the renames are mutually useless
apart. The mechanism alone changes no behavior (nothing consumes it); the renames
alone break every dashboard (nothing preserves the old names). Splitting them ships
either dead code or a knowingly-broken release. The reviewers asked for dual
emission *as the mitigation for the rename* — they are one change.

**Why the bulk is unavoidable.** ~60% is mechanical repetition: 21 legacy aliases,
each needing a struct field, a construction call, and a record call.
`sessionmanager/factory.go` carries 9 of them plus un-merging three `outcome`
values.

**How to make it reviewable.** Structure the branch as four ordered commits and say
so in the PR body:

1. **Add the dual-emit seam + flag plumbing** — no metric touched; `/metrics`
   byte-identical
2. **Rename proxy metrics** + legacy aliases
3. **Rename rate-limit metrics** + legacy aliases
4. **Rename vMCP metrics** + legacy aliases + un-merge outcomes

Commit 1 carries all the design judgement; 2–4 apply one pattern repeatedly. A
reviewer who reads commit 1 and spot-checks 2–4 has covered it.

**If a reviewer wants it split**, offer proactively: commit 1 as its own PR, then
2–4 as one or three. Do not argue.

## On RFC D13

D13 says "no dual-emit window." Two readings make this PR consistent with the RFC
rather than an override:

1. D13 scopes itself to "the deletion of **the six legacy twins**" (§3.5's table).
   The 15 renames were never in its scope.
2. Its closing sentence pre-authorizes revisiting: *"If a scraped consumer is
   identified before the rename lands, revisit this decision for that surface."*
   The repo ships four Grafana dashboards under `examples/otel/grafana-dashboards/`.

State the chosen reading in the PR body; get `@reyortiz3` to confirm. Do not
describe this as overriding an Accepted decision.

---

## Commit 1 — Dual-emit seam and flag

Three helpers returning a working instrument when enabled and a **no-op**
otherwise, so record sites never branch:

```go
// pkg/telemetry/legacymetrics.go
//
// This file holds the dual-emission helpers for legacy (pre-stacklok.*) metric
// names. Whether legacy names are emitted is carried explicitly as the `enabled`
// parameter — sourced from Config.UseLegacyMetrics — rather than through package
// state, matching how every other --otel* setting reaches its consumers.
//
// Each helper returns a working instrument when enabled and a no-op otherwise, so
// callers record into the legacy instrument unconditionally instead of branching
// at every record site. An instrument-creation failure also yields a no-op: a
// legacy alias must never be the reason a process fails to start.

func LegacyInt64Counter(meter metric.Meter, enabled bool, legacyName string,
    opts ...metric.Int64CounterOption) metric.Int64Counter

func LegacyFloat64Histogram(meter metric.Meter, enabled bool, legacyName string,
    opts ...metric.Float64HistogramOption) metric.Float64Histogram

func LegacyInt64UpDownCounter(meter metric.Meter, enabled bool, legacyName string,
    opts ...metric.Int64UpDownCounterOption) metric.Int64UpDownCounter
```

Both no-op paths matter: no-op-when-disabled keeps 23 call sites branch-free;
no-op-on-error means a legacy alias can never prevent startup.
`legacymetrics.go` takes names as a **parameter** — it contains no metric-name
literals.

### Flag plumbing — default `true` everywhere

| # | Layer | File |
|---|---|---|
| 1 | CLI flag | `cmd/thv/app/run_flags.go` — `RunFlags.OtelUseLegacyMetrics`, `BoolVar(..., true, ...)` |
| 2 | Flag→config | `run_flags.go` — `getTelemetryFromFlags`, `createTelemetryConfig` |
| 3 | App config | `pkg/config/config.go` — `OpenTelemetryConfig.UseLegacyMetrics *bool`, yaml `use-legacy-metrics` |
| 4 | Runner | `pkg/runner/telemetry_config.go` (nil→`true`), `config_builder.go`, `middleware.go` |
| 5 | Telemetry config | `pkg/telemetry/config.go` — field, `DefaultConfig()=true`, `String()` |
| 6 | Provider accessor | `pkg/telemetry/config.go` — `(*Provider).UseLegacyMetrics()` — **vMCP reads this**, not the field |
| 7 | Rate-limit params | `pkg/ratelimit/middleware.go` — `MiddlewareParams.UseLegacyMetrics` |
| 8 | vMCP factory | `pkg/vmcp/ratelimit/factory/limiter.go` — `Config.UseLegacyMetrics` |
| 9 | vMCP serve | `pkg/vmcp/cli/serve.go` — defaults `true` when `Telemetry == nil` |
| 10 | Operator CRD | `mcptelemetryconfig_types.go` — `+kubebuilder:default=true` |
| 11 | Operator mapping | `pkg/spectoconfig/telemetry.go` (+ drift test) |

Strict order for the rate-limit chain: `MiddlewareParams` → `factory.Config` →
`serve.go`.

**Atomic signature changes** — cannot be split from their callers:
`WithTelemetryConfigFromFlags` (11th param; breaks `run_flags.go` + **7** call
sites in `pkg/runner/config_test.go`), `MaybeMakeConfig`, `getTelemetryFromFlags`
(3 test sites).

Include the `resolveOptionalBool(cmd, name, flagVal, cfgVal)` helper — it layers
config under any flag the user did not set, deduplicates the existing
tracing/metrics/legacy-attributes resolution (net −6 lines), and keeps the CLI and
`POST /api/v1/workloads` on one build path
([#5253](https://github.com/stacklok/toolhive/issues/5253)).

CRD field:

```go
// UseLegacyMetrics controls whether legacy metric names are emitted alongside
// the new stacklok.* metric names. Defaults to true for backward compatibility.
// This will change to false in a future release and eventually be removed.
// +kubebuilder:default=true
// +optional
UseLegacyMetrics bool `json:"useLegacyMetrics"`
```

Regenerate — never hand-edit: `task gen && task docs`. Touches
`deploy/charts/operator-crds/**` (both `files/` and `templates/`),
`docs/operator/crd-api.md`, `docs/server/{docs.go,swagger.json,swagger.yaml}`,
`docs/cli/thv_run.md`.

### Deprecation warning

Once per process (`sync.Once` — `NewHTTPMiddleware` runs per proxied server):

```
WARN legacy metric names are deprecated and will be removed in <release>;
     set --otel-use-legacy-metrics=false to emit only stacklok.* names
     (see docs/telemetry-migration-guide.md)
```

**Name a literal release**, not "a future release" — this is what users plan
against, and it must match the version in [B2](pr-b2-migration-guide-and-dashboards.md).
Don't enumerate all 21 names; point at the guide.

**Verify commit 1 changes nothing**: scrape `/metrics` before and after on the same
workload, with the flag on *and* off. Output must be byte-identical.

---

## Commit 2 — Proxy renames

**File**: `pkg/telemetry/middleware.go`

| Old | New | Note |
|---|---|---|
| `toolhive_mcp_active_connections` | `stacklok.toolhive.proxy.active_connections` | The **only** true proxy rename |
| `toolhive_mcp_requests` | *(deleted)* → `http.server.request.duration` `_count` | §3.5 twin |
| `toolhive_mcp_request_duration` | *(deleted)* → `mcp.server.operation.duration` | §3.5 twin |
| `toolhive_mcp_tool_calls` | *(deleted)* → `mcp.server.operation.duration{mcp.method.name="tools/call"}` | §3.5 twin |

The three deletions are **not** renames — no `stacklok.*` successor. Retain all
three as legacy aliases (reviewers asked to keep the twins one more release);
removal is a later minor.

`http.server.request.duration` and `sse_connection.duration` come from
[A2](pr-a2-proxy-observability-hardening.md) — do not add them here.

**Restore `mcp_server` on request metrics.** A reviewer found the branch dropped
per-server breakdown from request metrics entirely — only `active_connections` kept
it. All three deleted twins carried `server`, so without it one Prometheus scraping
several proxies cannot split them without a `target_info` join. Add
`coremetrics.LabelMCPServer` to **both** `mcp.server.operation.duration` and
`http.server.request.duration`.

The e2e assertion in `test/e2e/osv_authz_test.go` **passes either way** because
`active_connections` supplies the label, so add per-metric assertions.

---

## Commit 3 — Rate-limit renames

**File**: `pkg/ratelimit/observability.go` (+ `limiter.go`, `middleware.go`)

| Old | New |
|---|---|
| `toolhive_rate_limit_decisions` | `stacklok.toolhive.ratelimit.decisions` |
| `toolhive_rate_limit_redis_errors` | `stacklok.toolhive.ratelimit.redis_errors` |
| `toolhive_rate_limit_check_latency` | `stacklok.toolhive.ratelimit.check_latency` |

Note `rate_limit` → `ratelimit`: per D1 the subsystem is one lowercase token.

### ⚠️ These dashboards break twice over

Both the metric name **and** a label key change:

```
toolhive_rate_limit_decisions{server="x"}
  → stacklok_toolhive_ratelimit_decisions_total{mcp_server="x"}
```

A user who maps only names still gets zero results. This is why the aliases must
keep the old label keys.

---

## Commit 4 — vMCP renames

### Composite tool (`pkg/vmcp/core/core_telemetry.go`)

| Old | New |
|---|---|
| `toolhive_vmcp_workflow_executions` | `stacklok.vmcp.composite_tool.executions` (+ `outcome`) |
| `toolhive_vmcp_workflow_errors` | **merged** → `...executions{outcome="error"}` |
| `toolhive_vmcp_workflow_duration` | `stacklok.vmcp.composite_tool.duration` |

### Optimizer (`pkg/vmcp/server/sessionmanager/factory.go`)

| Old | New |
|---|---|
| `..._optimizer_find_tool_requests` | `stacklok.vmcp.optimizer.find_tool.requests` (+ `outcome`) |
| `..._optimizer_find_tool_errors` | **merged** → `find_tool.requests{outcome="error"}` |
| `..._optimizer_find_tool_duration` | `stacklok.vmcp.optimizer.find_tool.duration` |
| `..._optimizer_find_tool_results` | `stacklok.vmcp.optimizer.find_tool.results` |
| `..._optimizer_call_tool_requests` | `stacklok.vmcp.optimizer.call_tool.requests` (+ `outcome`) |
| `..._optimizer_call_tool_errors` | **merged** → `call_tool.requests{outcome="error"}` |
| `..._optimizer_call_tool_not_found` | **merged** → `call_tool.requests{outcome="not_found"}` |
| `..._optimizer_call_tool_duration` | `stacklok.vmcp.optimizer.call_tool.duration` |
| `..._optimizer_token_savings_percent` | `stacklok.vmcp.optimizer.token_savings` |

`call_tool.duration` **survives** per **D10** — it measures optimizer dispatch
overhead, distinct from per-backend latency in `mcp.client.operation.duration`. The
nesting is intentional; do not delete it.

**`token_savings` is a naming trap.** Unit `%`, so it exports as
`stacklok_vmcp_optimizer_token_savings_percent` — matching the **old** Prometheus
name **exactly**. Do not bake `_percent` into the source name. Flag it for B2: the
one row where old and new coincide.

### Backend (`pkg/vmcp/internal/backendtelemetry/backendtelemetry.go`)

| Old | New | Note |
|---|---|---|
| `toolhive_vmcp_backend_requests` | *(deleted)* → `mcp_client_operation_duration_count` | §3.5 twin |
| `toolhive_vmcp_backend_errors` | *(deleted)* → `..._count{error_type!=""}` | §3.5 twin |
| `toolhive_vmcp_backend_requests_duration` | *(deleted)* → `mcp.client.operation.duration` | §3.5 twin |
| `toolhive_vmcp_backend_revision_reclassifications` | `stacklok.vmcp.backend.revision_reclassifications` | Rename, **no** twin |

Add `coremetrics.LabelMCPServer` (from `target.WorkloadName`) to
`mcp.client.operation.duration` — the deleted `_requests_duration` twin carried an
equivalent label.

`stacklok.vmcp.mcp_server.health` comes from [A3](pr-a3-vmcp-backend-health-gauge.md).

### ⚠️ The counter merge must un-merge

Four counters vanish as separate instruments, folded into an `outcome` label
(**D4**: one counter with an `outcome` label, not `_succeed`/`_failed` names). So
one merged new counter fans back out to two or three legacy counters:

```go
// New: one counter, outcome label.
t.callToolRequests.Add(ctx, 1, metric.WithAttributes(
    attribute.String(coremetrics.LabelOutcome, outcome),   // success | error | not_found
))

// Legacy: fan back out to the three separate pre-rename counters.
switch outcome {
case coremetrics.OutcomeSuccess:
    t.legacyCallToolRequests.Add(ctx, 1, ...)
case coremetrics.OutcomeError:
    t.legacyCallToolErrors.Add(ctx, 1, ...)
case outcomeNotFound:
    t.legacyCallToolNotFound.Add(ctx, 1, ...)
}
```

`outcomeNotFound = "not_found"` extends `coremetrics.OutcomeSuccess`/`OutcomeError`
(§3.3 permits per-metric outcome extensions).

**Semantics shift — decide and state it.** The merged counters increment **after**
the call (completions); the originals incremented **before** (attempts). Under load
with in-flight or panicking calls these differ, so a user comparing series during
the window will see a discrepancy and reasonably file a bug. Either:

1. **Preserve legacy semantics** — increment the legacy attempt counter before the
   call, the new outcome counter after. **Preferred**: the point of an alias is to
   behave like what it replaces.
2. **Accept the shift** — document it in B2 as a behavioral change, not a rename.

Do not leave this undecided.

---

## Label renames (commits 2–4)

| Old | New | Constant |
|---|---|---|
| `server`, `target.workload_name` | `mcp_server` | `coremetrics.LabelMCPServer` |
| `workflow.name` | `composite_tool` | `coremetrics.LabelCompositeTool` |
| `tool` | `tool_name` | `coremetrics.LabelToolName` |
| `status`, `success` (bool) | `outcome` | `coremetrics.LabelOutcome` |
| `error_type` | `error_type` | `coremetrics.LabelErrorType` (same wire value) |

Boolean labels are banned (§3.3): `success=true|false` → `outcome=success|error`.

## Naming rules

Per §3.2 — get these right or exported names will be wrong:

- **Author in dotted OTel form.** The exporter converts to underscores.
- **Never bake in `_total`/`_seconds`.** Set `metric.WithUnit("s")`; the exporter
  appends. `...ratelimit.check_latency` + `s` → `..._check_latency_seconds`.
- **Counters get `_total`** from the exporter:
  `...ratelimit.decisions` → `..._decisions_total`.

## Dual-emit pattern

```go
decisions, err := meter.Int64Counter(
    "stacklok.toolhive.ratelimit.decisions",
    metric.WithDescription("Total number of rate limit bucket decisions"))
// ...
legacyDecisions: telemetry.LegacyInt64Counter(meter, useLegacyMetrics,
    "toolhive_rate_limit_decisions",
    metric.WithDescription("DEPRECATED: renamed to stacklok.toolhive.ratelimit.decisions")),
```

Record into both unconditionally — the helper no-ops when disabled:

```go
t.decisions.Add(ctx, 1, metric.WithAttributes(
    attribute.String("namespace", t.namespace),
    attribute.String(coremetrics.LabelMCPServer, t.serverName),   // NEW key
    attribute.String("decision", decision),
))
// Legacy alias: same values under the pre-rename "server" label key.
t.legacyDecisions.Add(ctx, 1, metric.WithAttributes(
    attribute.String("namespace", t.namespace),
    attribute.String("server", t.serverName),                     // OLD key
    attribute.String("decision", decision),
))
```

The two sets differ **only** in the label key. **Build them separately** — a shared
slice is how the old key silently disappears.

Legacy histograms keep their **original** bucket boundaries. If
[A2](pr-a2-proxy-observability-hardening.md) moved a preset, the alias keeps the
pre-A2 preset; otherwise it is not a faithful alias.

## Bucket assignments

| Metric | Preset |
|---|---|
| `mcp.client.operation.duration` | `BucketsMCPSemconv()` (D2) |
| `stacklok.vmcp.composite_tool.duration` | `BucketsLongRunning()` |
| Optimizer + rate-limit durations | `BucketsMCPProxy()` |
| **All legacy aliases** | Original pre-rename preset |

## ⚠️ Integration tests assert a hard cutover

`pkg/vmcp/server/telemetry_integration_test.go` and
`test/integration/vmcp/vmcp_integration_test.go` on the source branch
`NotContains` the deleted twins — asserting the **single-pass cutover**. With dual
emission defaulting to `true` they **will fail**.

Resolve deliberately: run those cases with `UseLegacyMetrics=false`, and add a
companion case with `true` asserting both families present. Silently deleting the
`NotContains` assertions loses the only end-to-end proof the flag suppresses legacy
names.

## Files to change

| File | Commit |
|---|---|
| `pkg/telemetry/legacymetrics.go` (+test) | 1 |
| `pkg/telemetry/config.go` | 1 |
| `cmd/thv/app/run_flags.go` (+test) | 1 |
| `pkg/config/config.go` | 1 |
| `pkg/runner/{telemetry_config,config_builder,middleware}.go` (+tests) | 1 |
| `cmd/thv-operator/api/v1beta1/mcptelemetryconfig_types.go` | 1 |
| `cmd/thv-operator/pkg/spectoconfig/telemetry.go` (+drift test) | 1 |
| `deploy/charts/**`, `docs/{cli,operator,server}/**` | 1 (generated) |
| `pkg/telemetry/middleware.go` (+test) | 2 |
| `test/e2e/osv_authz_test.go` | 2 |
| `pkg/ratelimit/{observability,limiter,middleware}.go` (+test) | 3 |
| `pkg/vmcp/core/core_telemetry.go` (+test) | 4 |
| `pkg/vmcp/server/sessionmanager/factory.go` (+test) | 4 |
| `pkg/vmcp/internal/backendtelemetry/backendtelemetry.go` (+test) | 4 |
| `pkg/vmcp/{cli/serve,ratelimit/factory/limiter}.go` | 4 |
| `pkg/vmcp/server/telemetry_integration_test.go`, `test/integration/vmcp/` | 4 |

## Tests

Every assertion must be capable of failing — see the
[README checklist](README.md#assertions-must-be-capable-of-failing).

1. **Helpers**: enabled → working instrument; disabled → no data point **and no
   panic when recorded into**; creation failure → usable no-op, not an error.
2. **Default `true` at each layer separately** — `DefaultConfig()`,
   `BuildTelemetryConfigFromAppConfig(nil)`, the CLI flag's registered default, the
   CRD default. A single assertion lets one layer silently default `false`.
3. **`resolveOptionalBool` table** — flag+config → flag wins; config only → config;
   neither → `true`.
4. **Commit 1 is inert** — `/metrics` byte-identical before/after, flag on and off.
5. **New names in correct Prometheus form** — assert **exported** strings
   (`stacklok_toolhive_ratelimit_decisions_total`,
   `..._check_latency_seconds`,
   `stacklok_vmcp_optimizer_token_savings_percent`). Catches a baked-in `_total` or
   missing unit.
6. **All 21 legacy names present when enabled; absent when disabled.**
7. **Aliases keep OLD label keys** — the "breaks twice over" guard:

   ```go
   require.Regexp(t, `(?m)^toolhive_rate_limit_decisions_total\{[^}]*[{,]server="x"`, body)
   require.NotRegexp(t, `(?m)^toolhive_rate_limit_decisions_total\{[^}]*mcp_server=`, body)
   ```

   Anchor `server=` on `[{,]` — unanchored, it is a substring of `mcp_server=` and
   cannot fail.
8. **Un-merge correctness** — one success, one error, one not-found through
   `CallTool`: new counter has three `outcome` series **and** each of the three
   legacy counters incremented by exactly 1.
9. **Values agree** — new and legacy report the same totals for the same traffic
   (per the attempts-vs-completions decision).
10. **`mcp_server` per metric** — anchored on
    `mcp_server_operation_duration_seconds_count`,
    `http_server_request_duration_seconds_count`, and
    `mcp_client_operation_duration_seconds_count`, so `active_connections` cannot
    satisfy them.
11. **No boolean labels** — no series carries `success="true"|"false"`.
12. **Legacy histogram buckets unchanged.**
13. **Both flag modes end-to-end** — see the integration-test note.
14. **Warning fires once** — two `NewHTTPMiddleware` calls → one warning.

Replace `counterValue(m)` with `counterValueForOutcome(m, outcome)` where a counter
gained the label; a helper summing across outcomes hides a misattributed outcome.

## Scope guard

```bash
# Track A instruments must already be on main, not added here.
git diff main...HEAD | grep -nE 'http\.server\.request\.duration|sse_connection\.duration|build_info|mcp_server\.health' \
  && echo "CHECK: Group A territory" || echo "ok"
```

Do not touch `docs/telemetry-migration-guide.md` or the dashboards — B2.

## Verification

```bash
task gen && task docs
task lint-fix
task test
task test -- -race ./pkg/vmcp/...
task build
```

```bash
./bin/thv run --otel-enable-prometheus-metrics-path fetch
curl -s localhost:<port>/metrics | grep -E '^(stacklok_|toolhive_)' | sort   # BOTH families

./bin/thv run --otel-use-legacy-metrics=false --otel-enable-prometheus-metrics-path fetch
curl -s localhost:<port>/metrics | grep -cE '^toolhive_'                     # 0

# The label-compat check that matters:
curl -s localhost:<port>/metrics | grep '^toolhive_rate_limit_decisions_total' | head -1
# Must show server="..." and NOT mcp_server="..."

# token_savings: old and new Prometheus names coincide — expect ONE series
curl -s localhost:<port>/metrics | grep -c '^stacklok_vmcp_optimizer_token_savings_percent'
```

vMCP: use the `deploying-vmcp-locally` skill
(`.claude/skills/deploying-vmcp-locally/`). Trigger a not-found `call_tool` and
confirm both the new `outcome="not_found"` series and the legacy `_not_found`
counter increment.

## Acceptance criteria

- [ ] `legacymetrics.go` exports the three helpers; no-op when disabled **and** on
      creation error; contains no metric-name literals.
- [ ] Commit 1 alone leaves `/metrics` byte-identical, flag on and off.
- [ ] `UseLegacyMetrics` defaults `true` at all four layers, each asserted
      separately; `(*Provider).UseLegacyMetrics()` exists.
- [ ] `--otel-use-legacy-metrics` in `thv run --help` and `docs/cli/thv_run.md`;
      CRD has `+kubebuilder:default=true`; charts/swagger regenerated via
      `task gen`/`task docs`; drift test covers the field.
- [ ] Once-per-process WARN names a **literal** removal release matching B2.
- [ ] 15 metrics renamed per the tables; `call_tool.duration` retained (D10);
      `token_savings` keeps unit `%` with no baked-in `_percent`.
- [ ] All 21 legacy names emitted when enabled, absent when disabled.
- [ ] Aliases carry **old** label keys, asserted with delimiter-anchored regexes.
- [ ] Merged counters fan back out correctly, proven by a three-outcome test.
- [ ] Attempts-vs-completions shift resolved and stated in the PR body.
- [ ] `mcp_server` on server, HTTP, and client duration metrics, asserted per metric.
- [ ] No boolean labels; legacy histograms keep original buckets.
- [ ] Integration tests cover **both** flag modes; no `NotContains` deleted to pass.
- [ ] **No twin deleted** — all six retained as aliases.
- [ ] PR body states the over-budget size, the reason, the four-commit structure,
      and the fallback split.
- [ ] `task lint-fix`, `task test`, `-race`, `task build` pass.

## PR description skeleton

```markdown
## Summary

RFC D1 sets one naming scheme for Stacklok-authored metrics,
`stacklok.<service>.<subsystem>.<name>`, and D11 makes vMCP its own service
namespace. ToolHive currently emits three of the five schemes the RFC exists to
collapse. Renaming them is what lets a customer write one selector across our
components instead of memorising per-component prefixes.

Every renamed metric is **also emitted under its old name** behind
`--otel-use-legacy-metrics` (default `true`), so nothing breaks on upgrade. A
rename empties a dashboard panel exactly like a deletion does, and the renames
otherwise have no overlap window — old name stops, new name starts, same release.
Span attributes already have this machinery (`--otel-use-legacy-attributes`,
retirement tracked in #6072); metrics now get the same.

- Adds the dual-emit seam: helpers returning a working instrument when enabled and
  a no-op otherwise, so 23 record sites stay branch-free. A creation failure also
  no-ops — a legacy alias must never be why a process fails to start.
- Renames proxy, rate-limit, and vMCP metrics; retains all 21 old names as aliases,
  including the six §3.5 twins (kept one more release rather than deleted alongside
  the rename).
- Aliases keep their **old label keys**. The rate-limit metrics change both name and
  label, so an alias emitting the old name with the new key would be a second
  breaking change, not a compatibility path.
- Folds four `_errors`/`_not_found` counters into an `outcome` label (D4: stable
  denominator for error ratios); the aliases fan back out to the original separate
  counters.
- Restores `mcp_server` on the request/client duration metrics. Per-server breakdown
  had been left only on `active_connections`, so one Prometheus scraping several
  proxies couldn't split them without a join — and the existing e2e assertion passed
  either way because `active_connections` supplied the label.

Two things worth a reviewer's eye: `token_savings` carries unit `%` and so exports
as `..._token_savings_percent`, matching the *old* name exactly — the only row where
old and new coincide. And the merged counters increment on completion where the
originals incremented on attempt; <chosen resolution>.

On RFC D13 ("no dual-emit window"): D13 scopes itself to the six twins in §3.5, not
the 15 renames, and its closing sentence pre-authorizes revisiting if a scraped
consumer is identified — the repo ships four Grafana dashboards. @reyortiz3 to
confirm the reading.

**Size**: ~943 lines / 17 files, over the guideline. The mechanism and the renames
are mutually useless apart — the mechanism alone changes no behavior, the renames
alone break every dashboard — so splitting them ships either dead code or a
knowingly-broken release. ~60% is mechanical alias repetition. Commits are ordered
seam → proxy → rate limit → vMCP; commit 1 carries the design judgement and 2–4
apply one pattern. Happy to split commit 1 into its own PR on request.

## Type of change
- [x] Breaking change (fix or feature that would cause existing functionality to not work as expected)

## Does this introduce a user-facing change?

Yes. Proxy, rate-limit, and vMCP metrics are renamed, and four counters are merged
into an `outcome` label. Old names and their original label keys are still emitted
by default via `--otel-use-legacy-metrics` (default `true`), so existing dashboards
keep working on upgrade. Set it to `false` to emit only the new names. Old names
will be removed in <release>. Migration table: #NNN.

## Test plan
- [x] Unit tests pass (`task test`)
- [x] Race detector passes (`task test -- -race ./pkg/vmcp/...`)
- [x] Manual verification: confirmed the seam commit alone leaves `/metrics`
      byte-identical; both name families present by default and none with the flag
      off; `toolhive_rate_limit_decisions_total` still carries `server=` not
      `mcp_server=`; a not-found call increments both the new `outcome="not_found"`
      series and the legacy `_not_found` counter.
```
