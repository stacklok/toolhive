# PR A3 — Convert vMCP backend health to a live gauge

- **Depends on**: nothing. Fully independent of A1 and A2 (lives entirely in `pkg/vmcp/`). Can start immediately.
- **Soft-conflicts**: [B1](pr-b1-rename-with-dual-emission.md) also rewrites `backendtelemetry.go`. Landing A3 first keeps the health-gauge fix out of the rename diff.
- **Size**: ~376 prod lines + ~460 test lines, 4 files. Over the 400-line guideline at the margin — see [Size](#size-and-splitting).
- **Type of change**: Bug fix (metric reports stale data) + New feature (live health signal).
- **Source**: commit `4408b1ee2` on `gautam/metrics-std-toolhive`.
- **RFC**: §3.6 coverage gaps; Phase 1; Acceptance AT #5.

## Why

RFC §3.6 names this explicitly as a coverage gap:

> vMCP backend health as a live gauge (`backends_discovered` fires once at
> startup and never updates)

On `main`, `MonitorBackends` records `toolhive_vmcp_backends_discovered` exactly
once, from a snapshot slice:

```go
// pkg/vmcp/internal/backendtelemetry/backendtelemetry.go (main)
backendCount.Record(ctx, int64(len(backends)))
```

So the gauge is a startup constant dressed up as a gauge. It cannot answer the
only question worth asking — *is this backend up right now?* If a backend goes
down, the metric keeps reporting the original count indefinitely. Any alert built
on it is dead weight.

RFC Acceptance **AT #5**: *vMCP backend-health gauge reflects a backend
transitioning to unhealthy within one collection interval.*

This is a **behavioral fix**, not a rename, which is why it belongs in Track A.
It ships the new instrument under its RFC-correct name because the metric it
replaces has fundamentally different semantics (a count vs. per-backend state) —
there is no meaningful rename mapping between them.

## Design

Replace the fire-once `Int64Gauge` with an `Int64ObservableGauge` whose callback
runs on every collection and reads live state.

| | `main` | After |
|---|---|---|
| Metric | `toolhive_vmcp_backends_discovered` | `stacklok.vmcp.mcp_server.health` |
| Type | `Int64Gauge`, recorded once | `Int64ObservableGauge` + callback |
| Reports | Backend count at startup | Per-backend health, per collection |
| Membership | Snapshot `[]vmcp.Backend` | Live `vmcp.BackendRegistry` |
| Labels | none | `mcp_server`, `state` |

### Shape: one point per `(mcp_server, state)` pair

For each live backend the callback emits a point for **every** possible health
state — `1` for the state the backend is currently in, `0` for the others:

```
stacklok_vmcp_mcp_server_health{mcp_server="github",state="healthy"} 1
stacklok_vmcp_mcp_server_health{mcp_server="github",state="unhealthy"} 0
stacklok_vmcp_mcp_server_health{mcp_server="github",state="degraded"} 0
...
```

Emitting the zeros is deliberate: a dashboard can rely on the series existing
even for a state a backend has never entered, so a panel or alert on
`state="unhealthy"` does not break on a missing series. Cardinality stays
bounded — `backends × states`, both small and bounded sets.

### Taking the registry, not a slice

The callback must call `registry.List(ctx)` on each collection rather than close
over a startup slice. This is what makes a `list_changed` removal drop the
series instead of orphaning a stale one at its last value.

Signature change (exactly one caller — `pkg/vmcp/core/core_vmcp.go`):

```go
func MonitorBackends(
	_ context.Context,
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	registry vmcp.BackendRegistry,      // was: backends []vmcp.Backend
	backendClient vmcp.BackendClient,
) (vmcp.BackendClient, *HealthProviderSetter, func() error, error)
```

`ctx` becomes unused (name it `_`) — the callback receives its own per-collection
context, and using the construction-time one would be a lifetime bug.

The returned `func() error` **unregisters the callback** and must be called on
`Close()` and on any post-registration construction failure. Leaking it means the
callback keeps firing against a dead registry.

### Health precedence

`currentHealthStatus(backend, recorded, provider)` resolves in this order:

1. Live `health.StatusProvider`, if set and tracking this backend — the
   authoritative current state.
2. The `recorded` map, written by request outcomes.
3. The registry's own `HealthStatus` (a health monitor's discovery-time
   assessment).
4. Empty `HealthStatus` normalizes to `BackendHealthy`.

Only the registry/provider can report the richer states (`degraded`, `unknown`);
request outcomes can only produce healthy/unhealthy/unauthenticated. Keep that
asymmetry documented in the code — it is not obvious.

### Classifying request outcomes

```go
// healthStatusForError classifies a failed backend call into the health state the
// gauge should report. The bool reports whether the gauge should be updated at all.
func healthStatusForError(err error) (vmcp.BackendHealthStatus, bool)
```

- `context.Canceled` / `context.DeadlineExceeded` → `(_, false)`. **Leave the
  gauge untouched.** A caller hanging up says nothing about backend health;
  treating client cancellation as a backend failure would produce false unhealthy
  alerts under normal load-shedding.
- `vmcp.ErrAuthenticationFailed` / `ErrAuthorizationFailed` →
  `BackendUnauthenticated`. Distinct from unhealthy: the backend is up and
  answering, our credentials are wrong. Different fix, different alert.
- Anything else → `BackendUnhealthy`.

### Bounding the map

`backendHealth` is keyed by **workload ID**, not name — the same identity space
as the registry and `health.StatusProvider`, so a backend rename cannot orphan or
duplicate an entry.

`set()` adds an entry per distinct workload ID and nothing removes one, so the
map would grow with the process. The gauge callback calls `retain(live)` with the
registry's live ID set on every collection, pruning absent keys. **This is a real
unbounded-growth fix, not tidiness** — call it out in the PR body.

Guard the map with a `sync.RWMutex`: the callback reads (`snapshot()`) on the
collection goroutine while request paths write concurrently. Adding a
mutex-bearing field forces `telemetryBackendClient` from a value to a **pointer**
receiver — update all six methods and the interface assertion:

```go
var _ vmcp.BackendClient = (*telemetryBackendClient)(nil)
```

### `HealthProviderSetter` — consider deleting it

The branch uses a mutex-guarded two-phase setter because `MonitorBackends` runs
before `buildHealthMonitor`, so the provider does not exist yet.

**A reviewer showed the cycle it works around does not exist.** `buildHealthMonitor`
(`core_vmcp.go`) takes only `cfg` and deliberately uses the *undecorated*
`cfg.BackendClient` — its own comment says "Use the undecorated client so health
checks do not emit backend-call telemetry."

So if `buildHealthMonitor` moves **above** the `MonitorBackends` call, the
provider can be passed by value and `HealthProviderSetter` disappears entirely —
along with its mutex, `Set`/`get`, the four-value return, and the
`if healthProviderSetter != nil` block in `core_vmcp.go`.

**Preferred: reorder and delete the setter.** Verify the reordering is safe
(nothing between the two call sites depends on the decorated client), and if it
is, take the simpler design. If reordering turns out to be unsafe, keep the setter
and **document the actual reason** — not the construction-order cycle, which is
not real.

### Construction-failure cleanup

Once the callback is registered, every later error path in `core.New` must
unregister it. Use a single `constructed bool` plus one deferred cleanup that
discharges both the unregister and `stopStore`, replacing the scattered
per-branch `stopStore()` calls:

```go
constructed := false
defer func() {
    if !constructed {
        unregisterHealthLogged(unregisterBackendHealth)
        stopStore()
    }
}()
// ... on success, at the end:
constructed = true
```

`unregisterHealthLogged` should **log** rather than swallow an unregister error.

## Size and splitting

~376 production lines across 4 files — at the margin of the 400-line budget, and
almost all of it in `backendtelemetry.go`. State the size in the PR description.
If review is slow, the natural seam is:

1. **The gauge** — `backendtelemetry.go` + `core_vmcp.go` wiring + tests.
2. **The cleanup refactor** — `constructed`/deferred-cleanup consolidation.

Prefer shipping as one PR: the cleanup exists *because* the callback is a
resource needing release, so splitting leaks it in between. Only split if a
reviewer asks.

**Do not** include in this PR: the `useLegacyMetrics` parameter, the
`legacyRequests`/`legacyErrors`/`legacyDurations` fields, or the
`toolhive_vmcp_backend_revision_reclassifications` rename. All belong to B1. Keep
`requestsTotal`/`errorsTotal`/`requestsDuration` exactly as they are on `main`.

## Implementation

```bash
git show gautam/metrics-std-toolhive:pkg/vmcp/internal/backendtelemetry/backendtelemetry.go
git show gautam/metrics-std-toolhive:pkg/vmcp/core/core_vmcp.go
git show 33e06f4f5:pkg/vmcp/internal/backendtelemetry/backendtelemetry.go   # before
```

**Tasks**

1. Add `healthStateLabel = "state"` and the `healthStates` slice covering every
   `vmcp.BackendHealthStatus`.
2. Add `backendHealth` (mutex + `map[string]vmcp.BackendHealthStatus`) with
   `set`, `snapshot`, `retain`.
3. Add `healthStatusForError` and `currentHealthStatus`.
4. Replace the `Int64Gauge` with `Int64ObservableGauge` +
   `meter.RegisterCallback`; call `retain(live)` at the end of the callback.
5. Change the signature to take `vmcp.BackendRegistry`; return the unregister
   func.
6. Switch `telemetryBackendClient` to a pointer receiver; update all six methods
   and the interface assertion.
7. Update `record()` to classify outcomes through `healthStatusForError`.
8. Wire `core_vmcp.go`: store the unregister func, add `unregisterHealthLogged`,
   consolidate cleanup behind `constructed`, call unregister on `Close()`.
9. Attempt the `buildHealthMonitor` reordering to delete `HealthProviderSetter`.

## Tests

**Files**: `pkg/vmcp/internal/backendtelemetry/backendtelemetry_test.go`,
`pkg/vmcp/core/core_vmcp_test.go`

1. **AT #5 — transitions to unhealthy** — a failing request flips the backend's
   `state="unhealthy"` point to `1` and `state="healthy"` to `0` on the next
   collection.
2. **Registry removal drops the series** — a backend removed from the registry
   produces no points, proving `List` is consulted per collection.
3. **Precedence** — live provider beats recorded; recorded beats registry;
   registry supplies `degraded`/`unknown`.
4. **Empty status normalizes to healthy.**
5. **`healthStatusForError` table** — `context.Canceled` and `DeadlineExceeded`
   return `false` (gauge untouched); auth/authz errors →
   `BackendUnauthenticated`; generic error → `BackendUnhealthy`.
6. **`retain` prunes absent keys** — the unbounded-growth fix, asserted directly
   on map length.
7. **Concurrent `set`/`snapshot`** — run under `-race`.
8. **Unregister on later construction failure** — table over the error branches
   in `core.New`; after each, scrape and assert the gauge is **gone**.

   ⚠️ **This assertion is easy to write so that it cannot fail.** The callback
   emits one point per backend from `registry.List`, so a mock returning an empty
   list produces zero series **whether or not the callback was unregistered** —
   `assert.NotContains(body, "stacklok_vmcp_mcp_server_health")` then passes
   vacuously. Two requirements:
   - The mock registry **must return a non-empty backend slice**.
   - Include a **positive control**: assert the gauge **is** present after a
     successful `New`, proving the negative assertion can fail at all.

9. **`Close()` unregisters** — a telemetry-enabled core stops emitting after
   `Close()`, and a double `Close()` is safe. Existing tests build from a config
   with no `TelemetryProvider`, so the unregister path is a no-op in all of them
   — this needs a telemetry-enabled fixture or it proves nothing.
10. **Every error branch is exercised, or the comment is trimmed.** If a doc
    comment lists five failure branches, the table must cover five. Do not leave
    a comment claiming coverage the table does not provide.

## Files to change

| File | Change |
|---|---|
| `pkg/vmcp/internal/backendtelemetry/backendtelemetry.go` | Observable gauge, `backendHealth`, `healthStatusForError`, pointer receiver, signature |
| `pkg/vmcp/core/core_vmcp.go` | Registry arg, unregister plumbing, `constructed` cleanup, `Close()` |
| `pkg/vmcp/internal/backendtelemetry/backendtelemetry_test.go` | Tests 1–7 |
| `pkg/vmcp/core/core_vmcp_test.go` | Tests 8–10 |

## Scope guard

```bash
git diff main...HEAD | grep -nE 'UseLegacyMetrics|Legacy(Int64|Float64)|stacklok\.toolhive\.|revision_reclassifications' \
  && echo "SCOPE VIOLATION" || echo "scope OK"
```

`stacklok.vmcp.mcp_server.health` is the **only** permitted `stacklok.*` string —
it is a net-new instrument replacing a metric with different semantics. Every
other `toolhive_vmcp_*` name stays untouched.

## Verification

```bash
task lint-fix
task test
task test -- -race ./pkg/vmcp/...   # the map is concurrently accessed
```

Manual — AT #5, the whole point of the PR. Use the `deploying-vmcp-locally` skill
(`.claude/skills/deploying-vmcp-locally/`) for a local setup:

```bash
# With a vMCP running against >=2 backends and Prometheus metrics enabled:
curl -s localhost:<port>/metrics | grep stacklok_vmcp_mcp_server_health
# Expect one point per (mcp_server, state); exactly one state == 1 per backend.

# Stop one backend, wait one collection interval, re-scrape:
# that backend's state="unhealthy" must now be 1 and state="healthy" 0.

# Remove a backend from the registry entirely:
# its series must disappear rather than freeze at its last value.
```

## Acceptance criteria

- [ ] `toolhive_vmcp_backends_discovered` is gone; `stacklok.vmcp.mcp_server.health`
      is an observable gauge whose callback runs per collection.
- [ ] One point per `(mcp_server, state)` for every state, `1` for the current
      one and `0` otherwise.
- [ ] A backend transitioning to unhealthy is reflected within one collection
      interval (AT #5), verified manually.
- [ ] Membership comes from `registry.List(ctx)` per collection; a removed
      backend's series disappears.
- [ ] `context.Canceled`/`DeadlineExceeded` leave the gauge unchanged.
- [ ] Auth/authz failures report `unauthenticated`, distinct from `unhealthy`.
- [ ] `retain()` prunes the map to the live set, asserted on map length.
- [ ] `-race` passes for concurrent `set`/`snapshot`.
- [ ] The callback is unregistered on `Close()` and on every post-registration
      construction failure; double `Close()` is safe.
- [ ] The unregister tests use a **non-empty** mock backend list and include a
      positive control, so the negative assertions are capable of failing.
- [ ] `HealthProviderSetter` is deleted via reordering, or retained with the real
      reason documented.
- [ ] No `useLegacyMetrics` plumbing, no other metric renamed.
- [ ] `task lint-fix` and `task test` pass.

## PR description skeleton

```markdown
## Summary

`toolhive_vmcp_backends_discovered` recorded the backend count exactly once at
startup, from a snapshot slice — a startup constant shaped like a gauge. It
cannot answer the only question worth asking of it, "is this backend up right
now?", and if a backend goes down it reports the original count forever, so any
alert on it is dead weight. RFC §3.6 lists this as a coverage gap.

- Replaces it with `stacklok.vmcp.mcp_server.health`, an observable gauge whose
  callback reads live state on every collection. Emits one point per
  `(mcp_server, state)` including zeros, so a panel on `state="unhealthy"`
  doesn't break on a missing series. Cardinality stays bounded at
  backends × states.
- Reads the live `BackendRegistry` instead of a startup slice, so a backend
  removed via `list_changed` drops its series rather than freezing at its last
  value.
- Distinguishes `unauthenticated` from `unhealthy` (the backend is up, our
  credentials are wrong — different fix, different alert), and leaves the gauge
  untouched on client cancellation, which says nothing about backend health.
- Bounds the recorded-health map: `set()` added an entry per workload ID and
  nothing removed one, so it grew with the process. The callback now prunes it to
  the live registry set.
- Consolidates construction cleanup so the gauge callback is always unregistered
  on a later failure or on `Close()`, rather than firing against a dead registry.

Ships under the RFC name because the replacement has different semantics
(per-backend state vs. a count), so there is no meaningful rename mapping. No
other metric is renamed.

## Type of change
- [x] Bug fix (non-breaking change which fixes an issue)

## Test plan
- [x] Unit tests pass (`task test`)
- [x] Race detector passes (`task test -- -race ./pkg/vmcp/...`)
- [x] Manual verification: stopped a backend and confirmed the gauge flipped to
      `state="unhealthy"` within one collection interval (RFC AT #5); removed a
      backend and confirmed its series disappeared.
```
