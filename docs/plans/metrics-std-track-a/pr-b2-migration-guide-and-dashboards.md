# PR B2 — Migration guide, deprecation schedule, and dashboards

- **Depends on**: [B1](pr-b1-rename-with-dual-emission.md) — **hard**. Names must be final before documenting them.
- **Blocks**: nothing. Last PR in the sequence.
- **Size**: ~500 lines, docs + JSON only. No production Go code.
- **Type of change**: Documentation.
- **Source**: `docs/telemetry-migration-guide.md`, `docs/observability.md`, `examples/otel/**` on `gautam/metrics-std-toolhive`, plus commits `6cb43cb76` and `4860adcbc`.
- **RFC**: §3.7 (RED/USE for customers), D13 (migration guide as the mitigation).

## Why

The migration guide **is** the mitigation. RFC D13 and both reviewers accept the
rename specifically because a one-page old→new mapping ships with it. If the
guide is wrong, the mitigation does not exist.

The version on the source branch has four defects a reviewer verified, each of
which makes it unfollowable. Fixing them is the substance of this PR.

Docs-only, so the line count is acceptable per CLAUDE.md.

## Defect 1 — Tables mix Prometheus and OTel names (blocking)

The left column holds real Prometheus names; the right column holds OTel dotted
names. **Anyone pasting a value from the right column into PromQL gets zero
results.** The guide cannot be followed as written.

Every right-column value needs the exporter's transformation applied:

- dots → underscores
- counters gain `_total`
- histograms gain the unit suffix (`_seconds`) plus `_bucket`/`_sum`/`_count`

Rows needing `_total`: the branch's lines 286, 287, 289, 290, 294, 298, 300.
Rows needing `_seconds` + `_bucket`/`_sum`/`_count`: 288, 291, 292, 295, 299, 315,
316. Re-verify against the final B1 names rather than trusting these numbers.

**Fix — do both:**

1. Add a **Prometheus name** column so each row shows dotted source *and* exported
   form. Lines 324–330 of the branch version already do this correctly for the two
   HTTP/MCP metrics, which makes the omission elsewhere read as deliberate.
2. Add one sentence above the tables explaining dots→underscores, `_total`, and
   unit suffixes.

The single highest-value check on this PR: **paste every PromQL-shaped string from
the guide into a live Prometheus and confirm it returns data.**

## Defect 2 — `token_savings` name coincidence unstated

`stacklok.vmcp.optimizer.token_savings` carries unit `%`, so it exports as
`stacklok_vmcp_optimizer_token_savings_percent` — which matches the **old**
Prometheus name exactly. It is the one row where old and new coincide, and a
reader who assumes every row changed will "migrate" a query that already worked.
Say so explicitly.

## Defect 3 — `_backend_requests_duration` listed twice, contradictorily

`toolhive_vmcp_backend_requests_duration` appears both as a rename **and** in the
deleted table, whose preamble says there is no renamed successor. Its two siblings
(`_backend_requests`, `_backend_errors`) appear only in the deleted table.

**Fix**: drop the rename row. It belongs in the deleted table with its siblings.

## Defect 4 — "What Is New" table incomplete

It lists only `mcp.server.operation.duration` and `mcp.client.operation.duration`,
contradicting the "What Changed" section above it. Missing:

- `http.server.request.duration` ([A2](pr-a2-proxy-observability-hardening.md))
- `stacklok.toolhive.proxy.sse_connection.duration` ([A2](pr-a2-proxy-observability-hardening.md))
- `stacklok.build_info` ([A2](pr-a2-proxy-observability-hardening.md)) — use the
  **actual exported name** resolved there, including `_ratio` if the upstream unit
  fix has not landed
- `stacklok.vmcp.mcp_server.health` ([A3](pr-a3-vmcp-backend-health-gauge.md)) —
  semantically new, **not** a rename of `toolhive_vmcp_backends_discovered`
- the `outcome` label vocabulary

This table is what a reader scans to learn what they *gain* in exchange for
rewriting queries.

## Reframe as a deprecation schedule

The guide currently reads as a one-shot cutover. With dual emission it must read
as a schedule:

1. **This release** — both names emitted; `--otel-use-legacy-metrics` defaults
   `true`. Migrate queries at your own pace, running old and new side by side.
2. **Next release (state the version)** — default flips to `false`. Legacy names
   available only by opting in.
3. **Later minor (state the version)** — flag and legacy names removed.

State **literal versions**, not "a future release." A schedule without dates is
not a schedule, and this is the text users plan against. Match the removal release
named in B1's startup warning — if the two disagree, the deprecation story is
incoherent.

Also document:

- How to set the flag: CLI (`--otel-use-legacy-metrics=false`), config
  (`use-legacy-metrics`), and CRD (`spec.otel.useLegacyMetrics`).
- The **series-count cost** of dual emission: roughly double for the overlap
  window, with histograms the expensive case. Operators sizing Prometheus need
  this.

## Things dual emission does *not* cover

Call these out — a user who trusts side-by-side comparison will otherwise be
misled:

- **Bucket boundaries are not dual-emitted.** A `histogram_quantile()` panel
  spanning the upgrade mixes layouts regardless of which name it queries. List
  every histogram whose buckets changed in Track A
  [A2](pr-a2-proxy-observability-hardening.md) and B1.
- **`error.type` is set only for status ≥ 500** (A2). Any error-rate
  query built on `error.type` presence stops counting 4xx — including auth
  denials. This is the headline behavioral change; give it its own callout.
- **Merged counters may shift attempts→completions** (B1). Whatever B1 decided,
  reflect it here.
- **`gen_ai.tool.name`/`gen_ai.prompt.name` remain unbounded** (A2),
  with the `metric_relabel_config` mitigation.

## Dashboards

`examples/otel/grafana-dashboards/` — four files:

| File |
|---|
| `toolhive-cli-mcp-grafana-dashboard-otel-scrape.json` |
| `toolhive-mcp-grafana-dashboard-otel-remotewrite.json` |
| `toolhive-mcp-grafana-dashboard-otel-scrape.json` |
| `toolhive-mcp-otel-semconv-dashboard.json` |

Update every panel query to the new **exported Prometheus** names and label keys
(`server` → `mcp_server`). These four shipped dashboards are also the concrete
evidence that scraped consumers exist — the point that reopened D13 — so leaving
them broken would undercut the argument the rename rests on.

Also check `examples/otel/README.md` and
`examples/otel/prometheus-stack-values.yaml` for stale names.

**Verify by loading them**, not by reading the JSON. A typo in a PromQL expression
inside a JSON string is invisible to review and to CI. Use the `deploy-otel` skill
(`.claude/skills/deploy-otel/`) to stand up Prometheus + Grafana, import each
dashboard, and confirm panels render data.

## Files to change

| File | Change |
|---|---|
| `docs/telemetry-migration-guide.md` | Defects 1–4; deprecation schedule; caveats |
| `docs/observability.md` | Reconcile with final names; D8 section; cardinality warnings |
| `docs/arch/10-virtual-mcp-architecture.md` | Stale metric references |
| `docs/operator/virtualmcpserver-observability.md` | Names + `useLegacyMetrics` field |
| `examples/otel/grafana-dashboards/*.json` (4) | Panel queries |
| `examples/otel/README.md`, `prometheus-stack-values.yaml` | Stale names |

## Verification

```bash
task lint-fix   # markdown/JSON formatting
task test
```

The checks that actually matter here are manual:

```bash
# 1. Stand up the stack (deploy-otel skill), run a workload, then:
#    paste EVERY PromQL string from the guide into Prometheus.
#    Each must return data. This is the acceptance test for Defect 1.

# 2. Confirm no dotted OTel name is presented as a PromQL-pasteable value:
grep -nE '^\|.*stacklok\.[a-z_.]+.*\|' docs/telemetry-migration-guide.md
#    Every hit must be in a "dotted source name" column, never a Prometheus one.

# 3. Import all four dashboards into Grafana; confirm panels render.

# 4. Cross-check the guide's name list against a live scrape:
curl -s localhost:<port>/metrics | grep -oE '^[a-z_]+' | sort -u > /tmp/actual.txt
#    Every new name in the guide must appear in /tmp/actual.txt.
```

## Acceptance criteria

- [ ] Every table row shows the **exported Prometheus name**, with `_total` /
      `_seconds` / `_bucket`/`_sum`/`_count` applied; a Prometheus-name column
      and/or an explanatory sentence is present.
- [ ] Every PromQL string in the guide has been pasted into a live Prometheus and
      returns data.
- [ ] `token_savings`'s old/new Prometheus name coincidence is stated.
- [ ] `toolhive_vmcp_backend_requests_duration` appears **once**, in the deleted
      table with its siblings.
- [ ] "What Is New" lists all five new instruments plus the `outcome` vocabulary,
      using `build_info`'s real exported name.
- [ ] Guide reads as a deprecation schedule with **literal version numbers**,
      matching B1's startup warning.
- [ ] Flag documented for CLI, config file, and CRD; dual-emit series-count cost
      stated.
- [ ] Bucket changes, the `error.type >= 500` change, any attempts→completions
      shift, and the unbounded `gen_ai.*` attributes are each called out as **not**
      covered by dual emission.
- [ ] All four dashboards updated **and loaded in Grafana** with panels rendering.
- [ ] No production Go code in the diff.

## PR description skeleton

```markdown
## Summary

The migration guide is the mitigation the rename rests on — RFC D13 and both
reviewers accept the breakage specifically because a one-page old→new mapping
ships with it. The existing guide had four defects that made it unfollowable:

- **Tables mixed Prometheus and OTel names.** The left column held real Prometheus
  names, the right held dotted OTel names, so anyone pasting from the right column
  into PromQL got zero results. Every row now shows the exported form, with
  `_total`/`_seconds` and bucket suffixes applied.
- **`token_savings` exports as `..._token_savings_percent`** (unit `%`), which
  matches the *old* name exactly — the one row where old and new coincide, now
  stated so nobody "migrates" a query that already worked.
- **`toolhive_vmcp_backend_requests_duration` was listed twice**, as both renamed
  and deleted, contradicting the deleted table's own preamble.
- **"What Is New" omitted five new instruments**, contradicting the section above
  it. That table is what tells a reader what they gain for the rewrite.

Reframed from a one-shot cutover to a deprecation schedule with literal version
numbers, matching the startup warning, and documents the series-count cost of the
overlap window.

Also documents what dual emission does *not* cover, since a user trusting
side-by-side comparison would otherwise be misled: bucket boundaries aren't
dual-emitted, `error.type` is now set only for status ≥ 500 (so error-rate queries
stop counting 4xx, including auth denials), and `gen_ai.*` attributes remain
unbounded.

All four shipped Grafana dashboards updated and verified by loading them into
Grafana — a PromQL typo inside a JSON string is invisible to review.

## Type of change
- [x] Documentation update

## Test plan
- [x] Manual verification: pasted every PromQL string from the guide into a live
      Prometheus and confirmed each returns data; imported all four dashboards
      into Grafana and confirmed panels render; cross-checked the guide's name
      list against a live `/metrics` scrape.
```
