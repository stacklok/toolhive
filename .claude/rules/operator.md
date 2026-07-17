---
paths:
  - "cmd/thv-operator/**"
  - "test/e2e/chainsaw/**"
---

# Operator Rules

Applies to Kubernetes operator code and CRD definitions.

## The governing principle

**A reconciler is an idempotent, level-triggered function from observed state to
desired state; everything else is plumbing in service of that.**

`Reconcile` must produce the correct result regardless of how it was triggered,
how many events were missed, or how many times it runs. Most operator bugs are a
violation of that sentence wearing a different costume. The rules below are
defaults with known failure modes, not laws — the two places people most often
*correctly* break them are bypassing the cache via `APIReader` for a genuine
read-your-write need, and skipping `observedGeneration` on a CR with no
generation semantics. Knowing *why* each rule exists is what tells you when
you're in the exception.

> **On the file references below.** Rules cite specific files as exemplars
> ("the pattern to copy") and as counter-examples ("do not copy", "being
> migrated", "fix first"). The *pattern* is authoritative; the file pointer is an
> illustration accurate as of this rule's last edit. If a cited counter-example
> has since been fixed, that is success, not a contradiction — apply the
> structural rule and update or drop the stale pointer. Never treat a "do not
> copy" pointer as license to assume the named file is still broken without
> checking it.

## Reconciliation core

- **Recompute, don't diff on the event.** Read the world, compute desired state,
  and converge — never branch on *which* event triggered the run or on
  old-vs-new event payloads. Diffing your freshly-built desired object against
  the live actual object is correct and expected (that *is* recompute); see
  `deploymentNeedsUpdate`/`serviceNeedsUpdate` in `mcpserver_controller.go`. When
  a feature is conceptually an event (a restart request, a one-shot migration),
  represent it as durable state and compare against it — see
  `handleRestartAnnotation` comparing `restartedAt` against a persisted
  `lastProcessedRestart` annotation. If cache lag could corrupt the recompute,
  read uncached via `APIReader` (see `storageversionmigrator_controller.go`).

- **Idempotent always.** Reconciling twice with no intervening change must be a
  no-op. Prove it in a unit test: reconcile to steady state, capture
  `ResourceVersion`, reconcile again, and assert `RequeueAfter == 0` *and*
  `ResourceVersion` is unchanged — an unchanged RV proves no spurious write
  occurred. See the steady-state assertions in `mcptelemetryconfig_controller_test.go`,
  `mcpoidcconfig_controller_test.go`, and `mcpauthzconfig_controller_test.go`.
  Guard every write behind a desired-vs-actual comparison.

- **One object per request.** The workqueue carries only a `NamespacedName` —
  *what* to reconcile, never *why*. Cross-resource watches map the triggering
  object to the set of owner `reconcile.Request`s and discard all other context;
  the mapper may List/filter to find affected objects (see the `mapXToY`
  functions in `virtualmcpserver_controller.go`), but `Reconcile` must re-derive
  every relationship from the named object alone. Never attach event details to
  the request or to controller fields to "remember" why a reconcile was
  scheduled.

- **Never sleep, block, or spawn goroutines that outlive the call.** To retry,
  return the error (controller-runtime backs off exponentially). To come back
  later on purpose, return `RequeueAfter`. Use `RequeueAfter` — never a tight
  `Requeue: true` loop — when polling for external readiness (see the 30s
  readiness requeue in `mcpregistry_controller.go`). `Requeue: true` (or the
  preferred `RequeueAfter: 0`) is acceptable only to immediately re-run after a
  write so the next pass sees fresh state. Critically: for an *expected*
  not-ready/contention condition, return `nil` error with `RequeueAfter`, not an
  error — returning an error applies exponential backoff and records a phantom
  failure (see the rationale comment in `storageversionmigrator_controller.go`).

## Errors and requeue

- **Return error → requeue with backoff. That is the retry mechanism.** Never
  build your own retry loop or sleep-and-retry. Reserve returned errors for
  genuine transient failures; for *waiting on external convergence* return
  `ctrl.Result{RequeueAfter: D}, nil`, not a returned error.

- **Separate terminal from transient errors.** A malformed or invalid spec (bad
  URL, malformed policy, schema violation) is NOT retryable: surface it via a
  `Valid=False` condition with `ObservedGeneration` set, optionally emit a
  one-shot Warning on the false-transition, and `return ctrl.Result{}, nil`.
  Returning the error instead requeues forever with backoff and buries the
  signal. Transient errors (apiserver conflicts, dependency `Get` failures,
  network) must be returned so controller-runtime retries. Reference patterns:
  `MCPOIDCConfigReconciler.handleValidationFailure` and `VirtualMCPServer`'s
  typed `SpecValidationError` peeled off with `errors.As` in
  `handleSpecValidationError`. Counter-example — do NOT copy:
  `MCPRemoteProxyReconciler.validateSpec` currently returns terminal spec errors
  (`RemoteURLInvalid`, bad Cedar syntax) and requeues forever.

- **No log-and-swallow.** A reconcile that logs an error then reports success
  leaves the cluster diverged with no signal. Either return the error (transient
  → backoff) or record it as a condition (terminal → `return nil`). Two patterns
  are explicitly *not* swallows: (1) a best-effort status update inside an error
  branch may be logged-and-skipped as long as the operative error is still
  returned; (2) an advisory enrichment whose failure does not block the
  reconcile's objective may log-and-continue *with a comment saying why*.
  Cleanup errors during finalization must be returned so the finalizer is
  retained and retried (see `finalizeMCPServer`). Advisory validators that
  swallow errors must still not paint a *transient* API error as a *terminal*
  reason. Use controller-runtime's `log.FromContext(ctx)` for reconcile logging,
  not `pkg/logger`.

## Status and conditions

(See also **Status Condition Parity** and **Status Writes** below for the
write-path mechanics.)

- **Spec is desired; status is observed and must be reconstructable.** Status
  must be fully derivable from spec plus the observed cluster on every reconcile —
  never treat your own status as the authoritative source of intent. Reading
  prior status back is allowed only for change-detection and idempotency (e.g.
  comparing a freshly recomputed `Status.ConfigHash`, or short-circuiting a side
  effect on `ObservedGeneration`), where the compared value is re-derived each
  reconcile, not carried forward as truth. When hashing for this purpose,
  canonicalize the spec first (see `mcpauthzconfig_controller.go`) so a no-op
  `kubectl apply` round-trip does not flip the hash and cause spurious writes.

- **Use `metav1.Condition` + `meta.SetStatusCondition`.** Express discrete state
  facts as conditions with `+listType=map`/`+listMapKey=type` markers. Truly
  shared condition types (`Valid`, `DeletionBlocked`) live in
  `api/v1beta1/conditions.go`; per-CRD condition types are defined alongside their
  own status struct in the respective `*_types.go` file — follow that convention,
  don't dump CRD-specific types into the shared file. Do not
  invent a bespoke status string for something a condition already covers. A
  coarse `Phase` enum (validated with `+kubebuilder:validation:Enum`, shown via
  `+kubebuilder:printcolumn`) is acceptable *in addition to* conditions as a
  single human-facing lifecycle summary — the Kubernetes phase+conditions
  convention — but conditions remain the machine-readable source of detail.

- **Set `observedGeneration`.** Every CRD status type carries
  `ObservedGeneration int64` and the controller stamps it to `<obj>.Generation`
  on each successful status write, so consumers can tell whether status reflects
  the current spec. The config-controller family additionally stamps
  per-condition `ObservedGeneration` via `metav1.Condition.ObservedGeneration` —
  prefer that richer form for new condition writers. The theoretical exception
  for a CR with no spec→status reconciliation is not used anywhere in toolhive;
  omitting it requires explicit justification.

- **Write status through the `/status` subresource.** Every CRD carries
  `+kubebuilder:subresource:status`, so status writes are separate API calls and
  cannot zero spec fields. Route every status write through
  `controllerutil.MutateAndPatchStatus` (see **Status Writes** for the
  authoritative rules on fresh-`Get`, sole array ownership, and scalar
  co-ownership) — never write spec and status in the same call. New controllers
  MUST use the helper; the legacy `r.Status().Update` call sites in
  `mcpserver_controller.go`, `mcpremoteproxy_controller.go`, and
  `mcpregistry_controller.go` predate this rule and are being migrated — do not
  copy them.

## Ownership and GC

- **Owner-reference everything you create** so Kubernetes garbage-collects it on
  cascade delete — use `controllerutil.SetControllerReference`, or the upsert
  helpers in `cmd/thv-operator/pkg/kubernetes/{configmaps,secrets,rbac}` and
  `cmd/thv-operator/pkg/registryapi` that wire it in centrally. Do not write manual deletion logic in a finalizer
  for same-namespace, same-or-narrower-scope children; GC already reclaims them,
  and the manual delete only adds RBAC surface and a finalizer failure path.
  Owner refs do not work cross-namespace or namespaced→cluster-scoped — for those
  you need a finalizer or a label-based sweep. Note: `finalizeMCPServer`
  currently deletes owned StatefulSet/Service/ConfigMap children manually; treat
  that as legacy, not a pattern to copy.

## Finalizers

- Add a finalizer **only for cleanup GC cannot do**: external-system teardown,
  cross-namespace deletion, or cross-resource reference bookkeeping (e.g.
  updating or blocking on resources that *reference* this one, as the config
  controllers and `MCPGroup` do). Do not add a finalizer solely to delete
  same-namespace owned children — owner references already handle that.
- Add and remove finalizers through `ctrlutil.MutateAndPatchSpec`, never bare
  `r.Update` — see **Spec / metadata patching** below for why (that section is the
  authoritative home for the merge-patch finalizer-protection guarantee). The
  `mcpgroup` and `mcpregistry` controllers still use raw `r.Update` and should be
  migrated.
- The handler MUST be idempotent and tolerate the target already being gone (use
  a NotFound-swallowing delete like `deleteIfExists`), and every code path MUST
  eventually remove the finalizer on success — a handler that can return without
  removing it makes the object permanently undeletable. A deletion-block that
  withholds removal while references still exist is acceptable only if it
  requeues and is guaranteed to clear once the references disappear.

## API hygiene and concurrency

(See also **Spec / metadata patching** and **Status Writes** below.)

- **Optimistic concurrency, always — never force a write past a conflict.** On a
  409 `Conflict`, return the error and let controller-runtime requeue with a
  fresh `Get` — exactly what `MutateAndPatchSpec` relies on. Never call
  `obj.SetResourceVersion("")` (or otherwise drop the precondition) to make a
  write "go through": that silently overwrites whatever a concurrent writer just
  committed. A 409 is the guard doing its job; treat it as routine.

- **Cache reads, API-server writes, no read-after-write.** Reconcilers read
  through the manager's cached client and write to the API server. Do not `Get`
  an object you just wrote and assert on your own change within the same
  reconcile — the informer cache is eventually consistent and will lag; rely on
  the resulting watch event to re-enqueue you. The one sanctioned exception is
  `APIReader` (`mgr.GetAPIReader()`), used today only by the StorageVersionMigrator
  for a genuine read-true-stored-state need. Do not reach for `APIReader` on a
  hot path — it hits the apiserver directly and does not scale.

- **Own your fields; don't blind-PUT.** When more than one writer (another
  controller, a user, kube-apiserver) may touch the same object, only write the
  fields you own — use the merge-patch helpers (`MutateAndPatchSpec` for
  spec/metadata, `MutateAndPatchStatus` for status), which send only the fields
  the mutate closure changed so co-owned fields are never clobbered (see **Spec /
  metadata patching** and **Status Writes** below for the mechanics). The
  repo-specific rules: do NOT introduce server-side apply (`client.Apply`,
  `FieldOwner`) as a one-off — toolhive has standardized on the merge-patch
  helpers, and mixing the two field-ownership models invites confusion. A
  full-PUT `r.Update` on a CR's spec or metadata is the anti-pattern this
  replaces. Selective-field `r.Update` on operator-exclusive child objects
  (Deployments, Services) is tolerated because the operator is the sole writer of
  the touched fields and the preceding `Get` supplies the resourceVersion
  precondition.

## Scale and event plumbing

- **Index every field you `List`-filter by.** Register a field index with
  `mgr.GetFieldIndexer().IndexField` and query with `client.MatchingFields`
  rather than listing everything and filtering in memory — the latter turns a
  single config change into an O(all-CRs-in-namespace) scan on every event. Any
  `List`-filter qualifies, but the dominant case is reverse-reference ("find
  every X that references this Y") lookups; see **Reverse References** below for
  the worked pattern and exemplars. The config-fan-out controllers (`mcpserver`,
  `mcpoidcconfig`, `mcptelemetryconfig`, `mcpauthzconfig`,
  `mcpexternalauthconfig`) currently list-and-filter and are the place to fix
  first.

- **Watch and map, don't poll; filter watches with predicates.** Use
  `Owns`/`Watches` + `handler.EnqueueRequestsFromMapFunc` for related objects.
  Add `predicate.GenerationChangedPredicate{}` to a `Watches` of a spec-driven
  CRD so status-only churn doesn't trigger a full fan-out reconcile (see
  `mcpwebhookconfig_controller.go`). Do NOT use a generation predicate on objects
  whose generation is meaningless or whose status changes you must react to
  (ConfigMaps, an MCPGroup readiness flip) — write a field-specific predicate
  instead (see `configMapDataChangedPredicate`). Do NOT add a
  `GenerationChangedPredicate` to the primary `For()` type to "avoid self-reconcile
  loops": status writes go through `MutateAndPatchStatus` and don't bump
  `metadata.generation`, so the loop it guards against can't happen and the
  predicate would only suppress legitimate spec reconciles.

- **Bound concurrency explicitly.** Controller-runtime defaults to
  `MaxConcurrentReconciles: 1` and a bucketed exponential rate limiter — safe but
  serial. Leave the default unless a specific CRD's reconcile throughput is
  demonstrably the bottleneck; then raise `MaxConcurrentReconciles` via
  `WithOptions(controller.Options{...})` and keep the default rate limiter. Never
  spawn your own unbounded reconcile goroutines. No toolhive controller sets this
  today; raise it only *after* the indexes above are in place, since concurrency
  multiplies the cost of any list-and-filter still present.

- **Periodic `RequeueAfter` as a correctness backstop** — only when the
  controller has a genuine missed-event failure mode (a dependency it cannot
  watch, external readiness it polls, deletes-during-downtime). Events drive
  promptness; the timer guarantees eventual convergence. Tie the requeue to the
  unconverged condition and drop it once satisfied (see `mcpregistry_controller.go`
  requeuing only while not-ready, and the EmbeddingServer "safety net" comment in
  `virtualmcpserver_controller.go`). Do NOT add an unconditional periodic requeue
  to a controller whose inputs are all watched. Short (sub-second) requeues after
  a finalizer/metadata patch are a separate "re-run with a fresh Get" pattern, not
  a backstop; keep them gated on a condition that will clear.

## Validation

- **Push spec validation into the API.** Express constraints as OpenAPI schema
  markers (`+kubebuilder:validation:Enum/Minimum/Maximum/Pattern/Required`) and,
  for cross-field rules, discriminated unions, and mutual exclusions, as CEL via
  `+kubebuilder:validation:XValidation`. See `mcpexternalauthconfig_types.go` and
  `mcpserver_types.go` for the established patterns. Bad specs must be rejected at
  admission, not discovered mid-reconcile. Reconcile-time Go validation is
  reserved for (a) what CEL genuinely cannot express and (b) checks on
  *derived/assembled* config (e.g. the `runner.RunConfig` built by the
  controller) that does not exist at admission time — it is not a substitute for
  schema markers on the raw spec. When a Go check intentionally mirrors a CEL rule
  for defense-in-depth, say so in a comment.

## Manager bootstrap and operation

- The manager bootstrap (`cmd/thv-operator/app/app.go`) must wire: a metrics
  endpoint (`Metrics.BindAddress`, default `:8080`), a health-probe endpoint
  (`HealthProbeBindAddress`, default `:8081`) with both `AddHealthzCheck` and
  `AddReadyzCheck`, and leader election (`LeaderElection` + `LeaderElectionID` +
  `LeaderElectionNamespace`). The binary takes a `--leader-elect` flag, which the
  Helm chart currently passes unconditionally (`deploy/charts/operator/templates/deployment.yaml`),
  so leader election is on even at the default `replicaCount: 1` — do not "fix"
  this into a `replicaCount`-gated conditional, which would disable it by default.
  The chart also owns the leases/configmaps RBAC and the probe definitions against
  the health port (see `deploy/charts/operator/tests/probes_test.yaml`).
- "Reconcile metrics" are provided automatically by controller-runtime
  (`controller_runtime_reconcile_*`, workqueue metrics) once the metrics endpoint
  is enabled — do not hand-roll them. The operator currently exposes no custom
  OTEL domain metrics; its only leader-only runnable
  (`pkg/operator/telemetry`, `LeaderTelemetryRunnable`) is a usage/update-check
  worker that polls the ToolHive update API and persists an instance-id ConfigMap —
  not a metrics emitter. If you add domain metrics, register them on the
  controller-runtime metrics registry.

## CRD vs PodTemplateSpec

**Rule of thumb**: If it affects how the operator behaves or how the MCP server operates, it's a **CRD attribute**. If it affects where/how pods run, it's **PodTemplateSpec**.

**CRD Attributes** — use for business logic:
- Authentication methods
- Authorization policies
- MCP-specific configuration
- Application behavior

**PodTemplateSpec** — use for infrastructure:
- Node selection (nodeSelector, affinity)
- Resource requests/limits
- Volume mounts
- Security context, tolerations

See `cmd/thv-operator/DESIGN.md` for detailed decision guidelines.

## Phase and Conditions

**Conditions** are the primary status mechanism. Each condition represents one
aspect of readiness, validity, or external dependency state, and controllers
should set conditions with specific reason constants and actionable messages.
Use specific condition types such as `ConfigurationValid` or `RemoteAvailable`
when the condition is scoped to one CRD; reserve truly shared types for
cross-CRD conventions.

**Phase** is a human-readable lifecycle summary for `kubectl get` output. It
must be derived from the current conditions and observed state, never set as an
independent source of truth.

Rules:

- Never set `Phase` directly from a separate code path; derive it from the
  current condition states.
- `Ready` means all readiness conditions are `True`.
- `Failed` means at least one condition is `False` with a terminal reason.
- `Pending` means required conditions are not all satisfied yet, and no terminal
  failure has been observed.
- Condition `Type` names should be specific and have meaningful `Reason`
  constants.

## CRD Type Conventions

- Use `metav1.Duration` for duration fields in CRD types, not `string` or
  integer seconds. It serializes as Go duration strings (`"1m0s"`, `"30s"`),
  has built-in OpenAPI schema support, and is the standard Kubernetes convention.

## Development Workflow

- Always run `task operator-generate` after modifying CRD types
- Always run `task operator-manifests` after adding kubebuilder markers
- Always run `task crdref-gen` from the repo root after CRD changes to regenerate API docs (running it from `cmd/thv-operator/` fails — the task resolves the config path relative to the repo root)
- Use `envtest` for integration testing, not real clusters
- Chainsaw tests require a real Kubernetes cluster
- Status writes must go through `controllerutil.MutateAndPatchStatus` — see the Status Writes section below

## Status Condition Parity

When adding a status condition to one CRD type, check all parallel types (e.g., `MCPServer` and `VirtualMCPServer`) for the same condition. Conditions that warn about misconfiguration or unsupported states should be consistent across types that share the same feature set — a gap means one type silently accepts invalid config that the other rejects.

## Status Writes

Use `controllerutil.MutateAndPatchStatus` for every status write — not `r.Status().Update` or inline `client.Status().Patch` (see #4633). The helper's doc comment is the authoritative spec.

When adding a status-write call site, check three things:

1. **Caller holds a freshly-`Get`ted object.** Reconciler-start writers do; writers that iterate `List` results (e.g., deletion-path fan-out in `MCPGroupReconciler`) do not and need a fresh `Get` before calling the helper.
2. **Caller is the sole owner of the entire `Status.Conditions` array.** Per-condition-type ownership is NOT enough. JSON merge-patch replaces the array wholesale for CRDs (the `+listType=map` marker is only honored by strategic-merge-patch), so any concurrent writer whose Patch lands between this caller's Get and Patch — on any condition type, not just the ones this caller touches — will be erased. A fresh `Get` narrows the TOCTOU window but does not eliminate it. If two code paths must write conditions on the same CRD (e.g., operator reconciler + in-pod `K8sReporter`), fix at the design level: consolidate to a single owner, or move one writer to a dedicated status field outside the array.
3. **Scalar fields the writer touches are not co-owned.** A stale-computed value different from the caller's snapshot will overwrite the live value — the helper cannot defend against this.

Do not use `MutateAndPatchStatus` for spec or metadata writes — those require optimistic locking (`client.MergeFromWithOptions(..., MergeFromWithOptimisticLock{})`). See #4767.

## Key Operator Commands

```bash
task operator-install-crds    # Install CRDs
task operator-generate        # Generate deepcopy, client code
task operator-manifests       # Generate CRD YAML, RBAC
task operator-test            # Run unit tests
task operator-e2e-test        # Run e2e tests
task crdref-gen              # Generate CRD API docs (run from the repo root)
```

## Spec / metadata patching

Never use `r.Update` on a CR spec or metadata: `Update` is a full PUT,
so any field our local copy does not track (e.g. `spec.authzConfig`
written by a separate authorization controller) gets zeroed on every
reconcile.

Use `controllerutil.MutateAndPatchSpec` instead. The helper wraps an
optimistic-lock merge patch: the body only contains fields the caller
changed, and `MergeFromWithOptimisticLock` sends `resourceVersion` as a
precondition, so if the server moved between our Get and Patch the
apiserver returns 409 and controller-runtime requeues with a fresh Get.

This is what protects `metadata.finalizers`. Merge-patch has no
array-append semantics — arrays are replaced wholesale — so when our
diff includes `finalizers` (e.g. an `AddFinalizer` call) it must have
been computed from an up-to-date snapshot. The 409 + requeue is what
guarantees that: any concurrent finalizer added by another controller
fails our precondition, and the next reconcile observes it via a fresh
Get before recomputing the diff.

```go
if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, mcpServer, func(m *mcpv1beta1.MCPServer) {
    controllerutil.AddFinalizer(m, MCPServerFinalizerName)
}); err != nil {
    return ctrl.Result{}, err
}
```

Expect 409s as routine log noise once the external controller lands —
the guard doing its job, not a bug.

Status-subresource patching uses the sibling helper
`controllerutil.MutateAndPatchStatus` (see the "Status Writes" section
above).

## Reverse References (who-references-me lookups)

When a controller tracks which other objects reference it — e.g. a config CRD
referenced by workloads through a `*Ref` spec field — follow these rules. They
are the level-triggered, indexed pattern controller-runtime is built for;
deviating re-implements (usually less correctly) what the framework already
gives you. See #5607 / #5608 for the worked example.

- **Use a field index for the reverse lookup.** To find "which objects reference
  X", register `mgr.GetFieldIndexer().IndexField(...)` on the referrer's ref
  field and query with `client.MatchingFields`. Do **not** `List` every object
  and filter in memory — that is O(all objects) on the hot path. Mirror the
  existing `setupGroupRefFieldIndexes` / `mcpserverentry_controller` indexes. The
  extractor func must return `nil` for objects with no ref so they aren't indexed
  under the empty key.

- **Don't hand-roll stale-reference detection in watch handlers.**
  `handler.EnqueueRequestsFromMapFunc` already runs the map function on **both**
  the old and new object on update events (and on the object for create/delete),
  so a map function that just returns "the object this referrer points at"
  automatically enqueues both the newly- *and* previously-referenced object — you
  do not need to scan every object's status to find the one holding a now-stale
  entry. Pair the handler with a predicate so it only fires when the ref actually
  changes (avoids status churn).

- **Reconcile is level-triggered.** Rebuild reverse-reference state from current
  cluster truth on every reconcile; never treat stored status as authoritative.
  Watch handlers are latency hints, not correctness — missed events are healed by
  periodic resync.

- **Don't store a denormalized reverse-reference list** (e.g.
  `Status.ReferencingWorkloads`) unless a concrete consumer needs it materialized
  on the object itself, such as a `kubectl` printer column. For deletion
  protection, have the finalizer recompute referrers **on demand** (the
  Kubernetes PVC-protection model: `pkg/controller/volume/pvcprotection`). A
  finalizer that re-queries live makes a stored list redundant for protection,
  and maintaining the list means watching every referrer purely to keep a count
  fresh.
