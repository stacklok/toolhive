# PR A2 — Harden and complete proxy telemetry

- **Depends on**: nothing. Start immediately.
- **Soft-conflicts**: [B1](pr-b1-rename-with-dual-emission.md) also touches `middleware.go` (~32 lines). Whichever lands second rebases.
- **Size**: ~490 prod lines + ~500 test lines, 7 files. **Over the 400-line guideline — see [Size](#size).**
- **Type of change**: Bug fix + New feature (see per-part breakdown).
- **Source**: commits `09b576fdf`, `46864d8ee`, `13d69bc4b` on `gautam/metrics-std-toolhive`.
- **RFC**: §3.3 (cardinality, buckets), §3.4 (`build_info`), §3.5 (semconv prerequisite), D2, D8.

## Why

One theme: **make the proxy's telemetry correct and complete before renaming it.**

Six changes, each independently justified, all landing in
`pkg/telemetry/middleware.go` and its provider package. Three are live correctness
bugs on already-shipped code; three close coverage gaps the RFC requires before the
rename can proceed.

Grouped as one PR because they share one file. Split six ways, each would conflict
textually with the next in `middleware.go` for no review benefit.

## Size

~490 prod lines / 7 files, over the repo's 400-line/10-file budget. State this in
the PR description with the reason above.

**Pre-agreed fallback split** if a reviewer objects:

- **A2a** — Parts 1–3 (cardinality, buckets, D8 labels): the correctness fixes.
- **A2b** — Parts 4–6 (`http.server.request.duration`, SSE split, `build_info`):
  the new instruments.

Offer this proactively rather than arguing for the single PR.

## Recommended commit structure

Order the branch so the diff reads in stages, and say so in the PR body:

1. Bound metric cardinality (Part 1)
2. Pin histogram buckets per metric class (Part 2)
3. Add D8 ownership labels (Part 3)
4. Add `http.server.request.duration` (Part 4)
5. Split SSE into its own histogram (Part 5)
6. Register `stacklok_build_info` (Part 6)

Parts 4 and 5 must stay in that order — 5 fixes a limitation 4 introduces.

---

## Part 1 — Bound `mcp.method.name` cardinality

**Type**: Bug fix (resource exhaustion). **Files**: `pkg/mcp/parser.go`,
`middleware.go`.

`mcp.method.name` is recorded **verbatim from the client's JSON-RPC body**
(`middleware.go:743` on `main`), never validated against methods the server
recognizes. The MCP port is unauthenticated by default, so any reachable client can
mint a permanently-retained series per distinct method string — multiplied across
~14 histogram buckets, in both the SDK's in-memory aggregation and Prometheus. A
loop sending `{"method":"aaa1"}`, `{"method":"aaa2"}`, … is an unbounded
memory-growth primitive, and Prometheus keeps the series after the client stops.

RFC §3.3: *never use an unbounded value as a label on a per-request metric.*
Semconv mandates the `_OTHER` sentinel, with the raw value on spans only.

**Implement:**

```go
// pkg/mcp/parser.go
//
// IsKnownMethod reports whether method is a recognized MCP method. It is the
// union of the methods the parser has handlers for and those with static
// resource IDs, so it covers parameterless methods like "ping" too.
//
// Telemetry uses this to bound label cardinality: the JSON-RPC method arrives
// verbatim from the request body, so recording it unvalidated lets a client
// mint one time series per distinct string.
func IsKnownMethod(method string) bool {
	if _, exists := methodHandlers[method]; exists {
		return true
	}
	_, exists := staticResourceIDs[method]
	return exists
}
```

The **union of both maps** matters: `methodHandlers` alone misses parameterless
methods like `ping`, which would then be mislabelled `_OTHER`.

```go
// pkg/telemetry/middleware.go
const attrValueOther = "_OTHER"

// mcp.method.name arrives verbatim from the JSON-RPC body, so it is bounded
// against the parser's own method set; the raw value stays on the span.
if !mcpparser.IsKnownMethod(mcpMethod) {
    mcpMethod = attrValueOther
}
```

Bound the value **before** it reaches `attribute.String(...)`. Do not bound it at
the span site — spans intentionally keep the raw value.

Add `normalizeHTTPMethod` for the same class of problem, used by Part 4:

```go
// knownHTTPMethods is the set semconv treats as recording-safe for
// http.request.method. Anything else must be recorded as attrValueOther.
var knownHTTPMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {},
	http.MethodPut: {}, http.MethodPatch: {}, http.MethodDelete: {},
	http.MethodConnect: {}, http.MethodOptions: {}, http.MethodTrace: {},
}

// normalizeHTTPMethod bounds http.request.method's cardinality. Go's net/http
// accepts any RFC 7230 token as a method, so an unauthenticated caller can
// otherwise mint a permanently-retained series per distinct method string.
// Semconv mandates recording unrecognized methods as _OTHER, keeping the
// original on spans only (as http.request.method_original).
func normalizeHTTPMethod(method string) string {
	if _, ok := knownHTTPMethods[method]; ok {
		return method
	}
	return attrValueOther
}
```

**Out of scope — document, do not fix**: `gen_ai.tool.name` and
`gen_ai.prompt.name` (`middleware.go:768`/`772`) are also unbounded — they come
from `params.name` and cannot be validated without the resolved tool set, which the
middleware lacks. Add a cardinality warning to `docs/observability.md` with the
`metric_relabel_config` mitigation, and file a follow-up issue.

---

## Part 2 — Pin histogram buckets per metric class

**Type**: Bug fix (silent wrong numbers). **Files**: `middleware.go`,
`pkg/ratelimit/observability.go`, `pkg/vmcp/core/core_telemetry.go`.

Changing bucket boundaries on a shipped metric is silent data corruption:
`histogram_quantile()` over a range spanning the upgrade mixes two layouts and
returns plausible-but-wrong numbers, with no error and no gap in the graph.

`main` uses one package-level var for three measurement classes:

```go
var MCPHistogramBuckets = metrics.BucketsMCPSemconv()
```

That is the root cause — one edit moves buckets on unrelated metrics. Delete it and
select per call site.

**Presets** (`toolhive-core@v0.0.37`):

| Preset | Boundaries (s) |
|---|---|
| `BucketsFastHTTP()` | `0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10` |
| `BucketsMCPSemconv()` | `0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300` |
| `BucketsMCPProxy()` | `0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300` |
| `BucketsLongRunning()` | `0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 180, 300` |

**Assignment:**

| Metric | Preset | Reason |
|---|---|---|
| `mcp.server.operation.duration` | `BucketsMCPSemconv()` | Semconv name → semconv buckets (D2). Preserves [#6144](https://github.com/stacklok/toolhive/pull/6144). |
| `mcp.client.operation.duration` | `BucketsMCPSemconv()` | Same |
| `http.server.request.duration` | `BucketsFastHTTP()` | Part 4 |
| SSE connection duration | `BucketsMCPProxy()` | Part 5 |
| Rate-limit check latency | `BucketsMCPProxy()` | Stacklok-authored proxy op |
| Composite-tool duration | `BucketsLongRunning()` | Core names composite tools as this preset's case |

`MCPSemconv` and `MCPProxy` differ only at `0.02/0.2/2` vs `0.25/2.5` — that
closeness is why the substitution slipped past review, and why this needs a test
rather than a comment.

⚠️ Rate-limit and composite-tool bucket edges **change** relative to `main`. That
is deliberate; list it in the PR body so it is not a silent change. If a reviewer
prefers deferring those two to B1 (where the metrics are renamed anyway), that is
reasonable — raise it rather than deciding silently.

---

## Part 3 — Add D8 ownership labels

**Type**: New feature (additive). **Files**: `providers.go`,
`providers_strategy.go`, `prometheus/prometheus.go`.

RFC **D8** requires `stacklok.component` / `stacklok.product` so one Prometheus
selector covers both our `stacklok.*` metrics and the unprefixed semconv ones —
the precondition for any cross-component dashboard. None of this exists on `main`.

**The trap.** Two label-name-shaped things, not interchangeable:

| | Form | Belongs in |
|---|---|---|
| OTel resource attribute key | `stacklok.component` (dotted) | `resource.WithAttributes`, `WithResourceAsConstantLabels` filter |
| Prometheus label name | `stacklok_component` (underscore) | `promclient.WrapRegistererWith` |

The dotted form *appears* to work in the Prometheus slot because
`model.NameEscapingScheme` defaults to `UnderscoreEscaping`. But escaping is
**content-negotiated per scrape**: a scraper negotiating `escaping=allow-utf-8`
(Prometheus 3.x) gets `stacklok.component` verbatim on `go_*`/`process_*` while
OTel-native series keep the underscore form. One ownership label splits into two
incompatible families and a dashboard filtering `stacklok_component` silently drops
all runtime metrics.

**Implement:**

```go
// providers.go
const ComponentName = "toolhive"   // exported; Part 6 consumes it

// Applied LAST so they beat CustomAttributes and OTEL_RESOURCE_ATTRIBUTES —
// ownership labels must not be user-overridable.
ownershipAttrs := []attribute.KeyValue{
    attribute.String(coremetrics.AttrStacklokComponent, ComponentName),
    attribute.String(coremetrics.AttrStacklokProduct, coremetrics.ProductStacklokPlatform),
}
res, err := resource.New(ctx,
    resource.WithAttributes(baseAttrs...),
    resource.WithFromEnv(),
    resource.WithHost(),
    resource.WithAttributes(ownershipAttrs...),
)
```

Add `warnIfOwnershipAttrsOverridden` so a discarded user value is observable rather
than silent. Also check `OTEL_RESOURCE_ATTRIBUTES`, which `WithFromEnv` reads
separately and so is not in `baseAttrs`.

```go
// prometheus.go — dotted OTel keys here; this filter matches resource attributes.
prometheus.WithResourceAsConstantLabels(attribute.NewAllowKeysFilter(
    coremetrics.AttrStacklokComponent, coremetrics.AttrStacklokProduct,
))

// Go/process collectors bypass the OTel exporter, so they need the labels applied
// directly or they drop out of any ownership-filtered query.
if config.IncludeRuntimeMetrics {
    runtimeRegisterer := promclient.WrapRegistererWith(config.OwnershipLabels, registry)
    runtimeRegisterer.MustRegister(collectors.NewGoCollector())
    runtimeRegisterer.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}
```

```go
// providers_strategy.go — underscore keys, with the comment explaining why.
// Prometheus label names, NOT OTel attribute keys — coremetrics.AttrStacklokComponent/
// AttrStacklokProduct are dotted OTel resource-attribute names and must not be used
// here. This map goes into promclient.WrapRegistererWith, and Prometheus's
// name-escaping scheme is content-negotiated per scrape.
OwnershipLabels: map[string]string{
    "stacklok_component": ComponentName,
    "stacklok_product":   coremetrics.ProductStacklokPlatform,
},
```

Product value is frozen at `stacklok-platform` so it survives the
`toolhive-enterprise` → `toolhive-platform` rename.

---

## Part 4 — Add `http.server.request.duration`

**Type**: New feature. **File**: `middleware.go`.

RFC §3.5 makes this the **prerequisite** for ever deleting the six legacy twins:
`mcp.server.operation.duration` only fires for parseable MCP requests, so
transport-level traffic (SSE opens, session deletes, pre-parse rejections) is today
covered only by the legacy `toolhive_mcp_requests` counter.

| Property | Value |
|---|---|
| Name | `http.server.request.duration` (semconv — **no** prefix, D2) |
| Unit / buckets | `s` / `BucketsFastHTTP()` |
| Exported | `http_server_request_duration_seconds{,_bucket,_sum,_count}` |

**Attributes:**

| Attribute | Condition | Notes |
|---|---|---|
| `http.request.method` | Always | **Must** use `normalizeHTTPMethod` (Part 1) |
| `url.scheme` | Always | semconv **Required** |
| `http.response.status_code` | Always | |
| `mcp_server` | Always | `coremetrics.LabelMCPServer` |
| `network.protocol.version` | If known | |
| `error.type` | **Only** `>= 500` | Status code as string |

Two decisions to understand, not copy blindly:

**`mcp_server` is a deliberate non-semconv addition.** The twins this replaces both
carried an equivalent `server` label; without it one Prometheus scraping several
proxies cannot split per backend without a join.

**`error.type` at `>= 500`, not `>= 400`.** A 4xx means the client sent something
invalid; counting auth denials as server errors makes any error-rate SLO
meaningless. Real contract — needs the explicit 4xx test.

Add `requestScheme` (server-side URLs are typically scheme-less, so fall back to
`r.TLS != nil`). Follow the existing `noop.Float64Histogram{}` fallback convention:
a telemetry instrument must never prevent startup.

⚠️ **Record exactly once per request.** The middleware has multiple exit paths; if
two branches record, request volume double-counts. This is the metric the guide
will tell users to build "Total RPS" from.

---

## Part 5 — Split SSE into its own histogram

**Type**: Bug fix. **File**: `middleware.go`. **Depends on Part 4.**

Part 4 puts every request on `BucketsFastHTTP()`, capped at 10s. SSE connections
live for minutes or hours, so **every SSE observation lands in `+Inf`**:
`histogram_quantile()` returns a flat line pinned to the top bucket — a panel that
looks like it works and shows terrible latency, rather than one obviously broken —
while `_sum` is dominated by connection lifetimes and masks real regressions.

Two measurement classes were sharing one instrument. Split them:

```go
// SSE connections live for minutes to hours, not milliseconds, so they get
// their own histogram on BucketsMCPProxy() (tops out at 300s) instead of
// sharing http.server.request.duration's 10s-capped buckets.
sseConnectionDuration, err := meter.Float64Histogram(
    "stacklok.toolhive.proxy.sse_connection.duration",
    metric.WithDescription("Duration of SSE connections, recorded once the connection closes"),
    metric.WithUnit("s"),
    metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...),
)
```

Record on close via `defer`, then **`return`** so the request never reaches Part 4's
recording call:

```go
sseStart := time.Now()
rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
defer func() {
    m.sseConnectionDuration.Record(ctx, time.Since(sseStart).Seconds(), metric.WithAttributes(
        attribute.String(coremetrics.LabelMCPServer, m.serverName),
    ))
}()
next.ServeHTTP(rw, r)
return
```

⚠️ **The `return` is the load-bearing half.** Without it the request records into
*both* histograms, reintroducing the `+Inf` pollution and double-counting volume.

Attributes: `mcp_server` only. Per-connection detail belongs in traces (§3.3).

**Why a `stacklok.*` name in Group A**: no semconv metric covers "duration of a
long-lived server-push connection," so per §3.2 an invented name takes the
`stacklok.<service>.<subsystem>.<name>` form. It is **net-new**, not a rename, so no
existing dashboard can break. Note the unit is `s` with no `_seconds` in the name —
the exporter appends it.

---

## Part 6 — Register `stacklok_build_info`

**Type**: New feature. **File**: `middleware.go`. **Uses Part 3's `ComponentName`.**

RFC §3.4 / AT #2. Without it you can see a latency change but cannot attribute it
to a deploy.

`build_info` is **process identity**, not per-request or per-server.
`NewHTTPMiddleware` runs once per proxied server, and `RegisterBuildInfo` attaches
an observable-gauge callback firing on every collection — so guard with
`sync.Once`:

```go
var buildInfoOnce sync.Once

func registerBuildInfo(meter metric.Meter) {
	buildInfoOnce.Do(func() { registerBuildInfoNow(meter) })
}

// registerBuildInfoNow bypasses buildInfoOnce so a test can register against its
// own meter regardless of whether another test already consumed the Once.
func registerBuildInfoNow(meter metric.Meter) {
	info := versions.GetVersionInfo()
	if err := coremetrics.RegisterBuildInfo(meter, providers.ComponentName, info.Version, info.Commit); err != nil {
		slog.Debug("failed to register build info metric", "error", err)
	}
}
```

Document the tradeoff in the comment: a `sync.Once` binds registration to the
*first* meter, so a second provider built later gets no `build_info`. Acceptable for
the proxy; state it so the next reader is not surprised.

### ⚠️ The exported name is not `stacklok_build_info`

`RegisterBuildInfo` sets `metric.WithUnit("1")`, and the Prometheus translator
appends `_ratio` for a dimensionless gauge:

```
stacklok_build_info_ratio{commit="abc123",component="toolhive",version="v1.2.3"} 1
```

Two consequences: any dashboard joining on `stacklok_build_info` returns **empty**;
and `require.Contains(body, "stacklok_build_info")` **passes** on the suffixed name
by substring, so a naive test goes green while the documented name matches nothing.
`_ratio` is also semantically wrong for an identity gauge.

**Pick one and state it in the PR body:**

- **Preferred** — drop `WithUnit("1")` upstream in `toolhive-core`, bump the pin,
  and pin the test to `^stacklok_build_info\{`. Needs a core release.
- **Interim** — ship against `_ratio`, document the **exact** exported name, file
  the upstream issue and link it.

Do not document `stacklok_build_info` while emitting `stacklok_build_info_ratio`.

The value is always `1` — **the identity is entirely in the labels**, so a test
checking only the value asserts nothing.

---

## Files to change

| File | Parts |
|---|---|
| `pkg/telemetry/middleware.go` | 1, 2, 4, 5, 6 |
| `pkg/mcp/parser.go` | 1 |
| `pkg/telemetry/providers/providers.go` | 3, 6 |
| `pkg/telemetry/providers/providers_strategy.go` | 3 |
| `pkg/telemetry/providers/prometheus/prometheus.go` | 3 |
| `pkg/ratelimit/observability.go` | 2 |
| `pkg/vmcp/core/core_telemetry.go` | 2 |
| `pkg/mcp/parser_test.go`, `pkg/telemetry/*_test.go`, `providers/*_test.go` | tests |
| `docs/observability.md` | all |

## Tests

Every assertion must be capable of failing — see the
[README checklist](README.md#assertions-must-be-capable-of-failing).

**Part 1**: `IsKnownMethod` table (known-with-params, parameterless `ping`,
unknown, `""`); unknown method records `_OTHER`; **known method preserved
verbatim** (without this, an always-`false` `IsKnownMethod` passes); **50 distinct
unknown methods collapse to exactly ONE data point** — assert the count, which is
what distinguishes "bounded" from "renamed"; `normalizeHTTPMethod` table including
lowercase `"get"` → `_OTHER`.

**Part 2**: assert bucket **boundaries** per metric equal the expected preset.
Demonstrate failure by substituting `BucketsMCPProxy()` — given how close the
presets are, an assertion that does not discriminate is worse than none.

**Part 3**: labels on an OTel-native series; labels on a `go_*` series (catches the
`WrapRegistererWith` regression); **no dotted `stacklok.component` anywhere** via
`require.NotRegexp` — a presence-only assertion passes even with dotted keys
because escaping rewrites them; user override is discarded and warned.

**Part 4**: recorded for a non-MCP request (assert
`mcp.server.operation.duration` has **no** point); attribute correctness; **4xx
row mandatory** (`403` → no `error.type`; `200` → none; `502` → `"502"`); method
bounded; `dp.Count == 1` (not `require.Len(dps, 1)`, which cannot detect
double-recording since points are keyed by attribute set); `url.scheme` reflects
TLS.

**Part 5**: SSE records into the SSE histogram; SSE produces **no**
`http.server.request.duration` point; non-SSE records into the fast histogram and
**not** the SSE one (both directions — either alone passes against a
record-everything middleware); `dp.Count == 1`; SSE buckets pinned.

**Part 6**: name anchored at line start (`(?m)^stacklok_build_info(_ratio)?\{`);
all three labels asserted; **registered once** — call twice, count matching lines;
value is `1`.

## Scope guard

```bash
git diff main...HEAD | grep -nE 'UseLegacyMetrics|Legacy(Int64|Float64)|stacklok\.vmcp\.|stacklok\.toolhive\.ratelimit\.' \
  && echo "SCOPE VIOLATION" || echo "scope OK"
```

Permitted `stacklok.*` strings: `stacklok.build_info`,
`stacklok.toolhive.proxy.sse_connection.duration`, `stacklok.component`,
`stacklok.product`. **Do not** rename `toolhive_mcp_active_connections` or delete
any `toolhive_mcp_*` instrument — B1.

## Verification

```bash
task lint-fix
task test
task build
```

```bash
./bin/thv run --otel-enable-prometheus-metrics-path fetch

# Part 1 — bogus methods must collapse
for i in 1 2 3 4 5; do
  curl -s -X POST localhost:<mcp>/messages -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":$i,\"method\":\"bogus-$i\"}" >/dev/null
done
curl -s localhost:<m>/metrics | grep mcp_server_operation_duration | grep -c 'bogus-'   # 0
curl -s localhost:<m>/metrics | grep mcp_server_operation_duration | grep -c '_OTHER'   # >0

# Part 2 — semconv edges, not proxy edges
curl -s localhost:<m>/metrics | grep 'mcp_server_operation_duration_seconds_bucket' | grep -E 'le="(0.02|0.2|2)"'

# Part 3 — both label families, no dotted keys
curl -s localhost:<m>/metrics | grep -E 'stacklok_(component|product)' | head
curl -s localhost:<m>/metrics | grep 'stacklok\.component'    # NO output

# Part 4 — AT #4: non-MCP requests recorded
curl -s -X DELETE localhost:<mcp>/session/abc
curl -s localhost:<m>/metrics | grep http_server_request_duration_seconds_count

# Part 5 — a 15s connection lands above le="10"
timeout 15 curl -sN localhost:<mcp>/sse >/dev/null
curl -s localhost:<m>/metrics | grep sse_connection_duration_seconds_bucket

# Part 6 — record the EXACT name; docs must match
curl -s localhost:<m>/metrics | grep '^stacklok_build_info'
curl -s localhost:<m>/metrics | grep -c '^stacklok_build_info'   # exactly 1
```

## Acceptance criteria

- [ ] **P1** Unknown `mcp.method.name` → `_OTHER`; known methods (incl. `ping`)
      verbatim; N distinct unknowns → exactly one series, asserted by count; spans
      keep raw values; `gen_ai.*` exposure documented + issue filed.
- [ ] **P2** `mcp.*.operation.duration` on `BucketsMCPSemconv()` (#6144 preserved);
      `MCPHistogramBuckets` var deleted with both consumers updated; boundaries
      pinned by a test proven to fail on substitution; changed edges listed in the
      PR body.
- [ ] **P3** Both labels on OTel-native **and** `go_*` series; underscore keys with
      a `NotRegexp` guard against dotted form; ownership attrs applied last and
      un-overridable, with a warning; `ComponentName` exported.
- [ ] **P4** Emitted for non-MCP requests (AT #4) with the attribute set above;
      `error.type` only `>= 500` with a 4xx test; `dp.Count == 1`.
- [ ] **P5** SSE on `BucketsMCPProxy()`, recorded on close, carrying `mcp_server`;
      SSE absent from the fast histogram and vice versa; buckets pinned.
- [ ] **P6** Present with all three labels; registered once (counted); test anchored
      at line start; `_ratio` resolved and docs match a live scrape.
- [ ] No metric renamed; no `toolhive_*` instrument deleted.
- [ ] PR body states the over-budget size, the reason, and the A2a/A2b fallback.
- [ ] `task lint-fix`, `task test`, `task build` pass.

## PR description skeleton

```markdown
## Summary

Makes the proxy's telemetry correct and complete before the `stacklok.*` rename
touches it. Three live correctness bugs on shipped code, and three coverage gaps
the RFC requires closing before the rename can proceed.

**Correctness:**
- `mcp.method.name` was recorded verbatim from the client's JSON-RPC body and never
  validated. The MCP port is unauthenticated by default, so any reachable client
  could mint a permanently-retained series per distinct method string, multiplied
  across the histogram buckets — unbounded memory growth that Prometheus retains
  after the client stops. Now bounded to the parser's method set via the
  semconv-mandated `_OTHER` sentinel; spans keep the raw value.
- Removes the single `MCPHistogramBuckets` var that made one preset serve three
  measurement classes, and pins the preset per metric. A semconv-named metric keeps
  semconv buckets (D2), which also preserves #6144. Bucket changes on shipped
  metrics are listed below rather than left silent.
- D8 ownership labels use Prometheus underscore names for the runtime registerer,
  not dotted OTel keys. The dotted form only appears to work because
  `UnderscoreEscaping` is today's default; a scraper negotiating
  `escaping=allow-utf-8` would get dotted keys on `go_*` series and underscore keys
  everywhere else, splitting one label into two families.

**Coverage:**
- Adds `http.server.request.duration`. `mcp.server.operation.duration` only fires
  for parseable MCP requests, so SSE opens, session deletes, and pre-parse
  rejections were covered only by the legacy `toolhive_mcp_requests` counter. RFC
  §3.5 makes this the prerequisite for ever removing those twins.
- Gives SSE connections their own histogram. On the fast-HTTP preset (10s cap)
  every SSE observation landed in `+Inf`, so quantiles returned a flat line pinned
  to the top bucket — a panel that looks like it works — while `_sum` masked real
  request-latency regressions.
- Registers `stacklok_build_info` behind `sync.Once`, so metrics can be correlated
  with a release.

Note: `RegisterBuildInfo` sets `WithUnit("1")`, so the gauge actually exports as
`stacklok_build_info_ratio`. `_ratio` is wrong for an identity gauge, and a
`Contains` assertion passes on the suffixed name by substring, so the test anchors
at line start. <Chosen resolution: upstream unit fix / documented interim + issue
#NNN.>

**Size**: ~490 lines / 7 files, over the 400-line guideline. The six parts share
`pkg/telemetry/middleware.go`, so splitting them creates serial conflicts in one
file for no review benefit. Commits are ordered one-per-part to read in stages.
Happy to split into A2a (correctness) / A2b (new instruments) on request.

## Type of change
- [x] Bug fix (non-breaking change which fixes an issue)

## Test plan
- [x] Unit tests pass (`task test`)
- [x] Manual verification: bogus JSON-RPC methods collapse to one `_OTHER` series;
      semconv bucket edges present; ownership labels on both OTel-native and `go_*`
      series with no dotted keys; non-MCP requests recorded (AT #4); a 15s SSE
      connection lands above `le="10"`; exactly one `build_info` series whose exact
      name matches the docs.
```
