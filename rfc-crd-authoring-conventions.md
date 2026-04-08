# RFC: CRD Authoring Conventions

## Status

Draft

## Context

ToolHive's Kubernetes operator manages ~8 CRD types (MCPServer, MCPRegistry, VirtualMCPServer, EmbeddingServer, MCPExternalAuthConfig, MCPToolConfig, MCPGroup, MCPRemoteProxy). Each CRD has hand-written conversion, validation, defaulting, and reference resolution code. As the operator has grown, several classes of bugs have recurred — silently dropped fields, unwired cross-CRD references, inconsistent validation — all rooted in the same underlying cause: there are no conventions governing how CRD types are authored, converted, validated, or wired together.

This RFC proposes a series of incremental improvements, each delivering standalone value, that together establish these conventions. Each phase can be reviewed, merged, and evaluated independently.

## Problem

The operator's CRD authoring has four related problems. They compound: solving any one in isolation leaves the others to cause the same classes of bugs.

### 1. Silent field drift between CRD spec and runtime config

CRD types are designed for Kubernetes UX — nested structs, grouped fields, K8s-specific annotations. Runtime config types are designed for the binary that consumes them — flat structs, CLI-compatible, no K8s dependencies. This separation is correct and intentional.

But conversion between the two is entirely hand-written, per-type, with no shared structure. `NormalizeMCPTelemetryConfig` maps nested CRD fields to flat runtime fields. `ConvertTelemetryConfig` does something similar but different. Each conversion function is bespoke — different naming, different error handling, different assumptions about which fields need defaults applied first.

**The consequence:** when a field is added to either side, there is no signal that the conversion layer needs updating. The code compiles, tests pass, and the field is silently dropped. This has caused real bugs: PR #3118, issue #3125, issue #3142.

### 2. Defaulting and validation are scattered and interleaved with conversion

There is no enforced ordering between defaulting, validation, and conversion. Some defaults are applied inside conversion functions. Some validation happens before conversion, some after, some not at all.

**The consequence:** conversion functions contain implicit defaulting logic that's invisible to reviewers. A developer adding a new field doesn't know where to put validation or defaults — the answer is different for every CRD type.

### 3. Cross-CRD references each require ~60 lines of hand-written wiring

MCPServer references MCPExternalAuthConfig, MCPToolConfig, MCPOIDCConfig, and MCPTelemetryConfig. Each requires a watch, MapFunc, resolve function, error handling, and status condition — wired independently. The VirtualMCPServer controller has 8 `Watches` calls in `SetupWithManager`, each with its own MapFunc. MCPRemoteProxy has 3.

**The consequence:** adding a new cross-CRD reference is expensive and error-prone. Missing a watch means changes to the referenced resource don't trigger re-reconciliation.

### 4. Reconcilers interleave lifecycle management with business logic

The MCPServer reconciler is ~2,300 lines. It interleaves reference resolution, validation, conversion, resource creation, and status management in a single imperative flow. VirtualMCPServer follows a different structure. MCPRemoteProxy follows yet another.

**The consequence:** a developer writing a new controller reverse-engineers an existing one and copy-pastes, introducing subtle divergences.

### How these problems relate

These form a chain: no conversion conventions → silently dropped fields. No defaulting/validation ordering → defaults hidden in converters → harder to review → more dropped fields. No reference wiring conventions → bespoke boilerplate → missed watches → stale data. No reconciler structure → inconsistent patterns → easier to miss all of the above.

## Jobs to be done

The conventions should be evaluated against four developer tasks that recur as the operator evolves.

**Job 1: Introduce a new CRD type.** Needs API types, runtime config, conversion, validation, defaulting, controller, tests. Most expensive job, most likely to introduce structural bugs.

**Job 2: Add a new cross-CRD reference.** A CRD starts referencing another CRD. Today: ~60 lines of watch/MapFunc/resolve/condition boilerplate per reference.

**Job 3: Modify an existing type.** Add, remove, or rename a field on a CRD spec or runtime config. Today: silent drift if the conversion layer isn't updated.

**Job 4: Diagnose a dropped field.** A user reports a CRD field has no effect. Today: trace through bespoke conversion functions with inconsistent names and locations.

Each phase below improves one or more of these jobs. The "today vs proposed" comparison is given within each phase.

## Prior art

### Kubernetes core: conversion-gen + round-trip fuzz testing

Kubernetes verifies API version conversions via round-trip fuzz testing: create a random object, convert through the hub and back, assert the result matches. Dropped fields cause test failures. `conversion-gen` auto-generates conversion functions for matching field names; developers write manual overrides for the rest.

**Relevant insight:** Test the *result* of conversion (does every field survive?), not a *description* of the mapping. Our Phase 1 adopts this principle.

### Crossplane: managed reconciler framework

Crossplane's `crossplane-runtime` provides an opinionated reconciler. The developer implements an `ExternalClient` interface (Observe, Create, Update, Delete). The framework handles the reconcile loop, reference resolution, error classification, and status conditions.

**Relevant insight:** The framework owns the lifecycle; the developer owns the business logic. Crossplane's interface assumes external cloud resources (not K8s-native), but the separation principle applies. Our Phase 4 adopts this.

### Knative: reconciler hooks + duck typing

Knative's `pkg/reconciler` provides a hook-based pattern. The developer implements `ReconcileKind()`; the framework handles informer setup, status propagation, and leader/observer patterns.

**Relevant insight:** A focused developer interface (one method, not a full Reconcile loop) reduces boilerplate and enforces structure.

### reconcilerio/runtime: sub-reconciler composition

Structures reconciliation into composable sub-reconcilers, each handling one concern. Sub-reconcilers share state via a "stash" mechanism.

**Relevant insight:** Decomposing large reconcilers into independent, testable stages. Useful for Phase 4.

### Cluster API: continuous fuzz testing for conversion

Extends the Kubernetes fuzz model with `FuzzTestFunc` and continuous OSS-Fuzz integration. 20+ fuzzers have caught 4 conversion issues before production.

**Relevant insight:** Confirms that fuzz/reflection-based field coverage testing is industry-proven at scale.

### goverter: fail-by-default conversion

Generates type-safe conversion functions where unmapped fields are compile errors by default. You must explicitly map or ignore every field.

**Relevant insight:** The "fail on unmapped fields" philosophy is exactly right. goverter's codegen doesn't fit ToolHive's structurally-different types, but the principle is adopted in Phase 1's drift detection.

### CEL validation rules (Kubernetes 1.25+)

CRD validation rules using Common Expression Language run in-process on the API server. They support cross-field validation and update validation without webhooks.

**Relevant insight:** Schema-level cross-field validation should use CEL where possible; Go-level `Validator[T]` is for rules that need logic, lookups, or complex conditions. Phase 2 codifies this layering.

## Proposed solution: phased roadmap

Each phase is a standalone improvement. Later phases build on earlier ones but each delivers value independently and can be reviewed and merged on its own.

---

### Phase 1: Drift detection tests

**Problem addressed:** Silent field drift (#1). Improves Job 3 (modify a type) and Job 4 (diagnose a dropped field).

**What changes:**

Add two test helpers: `FullyPopulate[T]()` which uses reflection to fill every field on a struct with a non-zero value, and `AssertFieldCoverage` which checks that every field on the converter output is non-zero after conversion.

The key insight comes from how Kubernetes core and Cluster API approach this. Kubernetes uses fuzz testing — random inputs, round-trip conversion, assert nothing is lost. Fuzzing is automatic (new fields are discovered by reflection) but fragile (random data violates constraints like enums, ranges, and URLs). Hand-written fixtures are precise but require someone to remember to add new fields — the same "remember to update" problem we're solving.

The resolution: **auto-populate via reflection, override only constrained fields.** `FullyPopulate` walks the struct and sets every field to a deterministic non-zero value (`string` → `"test-<fieldname>"`, `bool` → `true`, `*float64` → `ptr(1.0)`, pointer → allocated, slice → one element, nested struct → recurse). New fields are discovered automatically — no one needs to remember anything. The developer only provides overrides for fields with domain constraints the populator can't know about.

```go
// pkg/testutil/populate.go — reflection-based, discovers new fields automatically
func FullyPopulate[T any]() T {
    var zero T
    rv := reflect.ValueOf(&zero).Elem()
    populate(rv, "")
    return zero
}

// pkg/testutil/drift.go — checks that conversion mapped every field
func AssertFieldCoverage[Runtime any](t *testing.T, result Runtime, excluded map[string]string) {
    t.Helper()
    rv := reflect.ValueOf(result)
    rt := rv.Type()

    for i := 0; i < rt.NumField(); i++ {
        field := rt.Field(i)
        name := jsonFieldName(field)
        if name == "" || name == "-" {
            continue
        }
        if reason, ok := excluded[name]; ok {
            t.Logf("field %q excluded: %s", name, reason)
            continue
        }
        if rv.Field(i).IsZero() {
            t.Errorf("runtime field %q is zero after converting a fully-populated CRD spec — "+
                "the converter may not map this field", name)
        }
    }
}
```

Per-converter test:

```go
func TestTelemetryConverterCoverage(t *testing.T) {
    // Reflection fills every field. New fields are discovered automatically.
    full := testutil.FullyPopulate[v1alpha1.MCPTelemetryConfig]()

    // Override ONLY fields with constraints the auto-populator can't know about.
    full.OpenTelemetry.Tracing.SamplingRate = ptr(0.5) // must be 0.0–1.0

    result := NormalizeMCPTelemetryConfig(full)

    excluded := map[string]string{
        "serviceName":    "set per-server by the controller, not from CRD spec",
        "serviceVersion": "resolved at runtime from binary version",
    }

    testutil.AssertFieldCoverage(t, result, excluded)
}
```

No new interfaces. No framework. No generics on CRD types. Just two test helpers applied to existing conversion functions.

A developer adds a field to either side and doesn't touch the test. `FullyPopulate` discovers the new CRD field via reflection and fills it. If the converter doesn't map it, the output field is zero and the test fails. Forgetting a constrained-field override is also safe: the converter either handles the auto-populated value (test passes) or returns an error (test catches it, developer adds the override). Either way, drift is detected without anyone remembering to update a fixture.

**Job 3 before:** Developer modifies the struct. Code compiles, tests pass, field is silently dropped.

**Job 3 after:** Developer modifies the struct and doesn't touch the test. `FullyPopulate` discovers the new field via reflection. Drift test fails: `runtime field "useLegacyAttributes" is zero after converting a fully-populated CRD spec`.

**Scope:** ~150 lines of shared test helpers (`FullyPopulate` + `AssertFieldCoverage`) + one test per existing conversion function. Can be done in a single PR per conversion.

---

### Phase 2: Converter, Validator, and Defaulter interfaces

**Problem addressed:** Scattered defaults and validation (#2). Improves Job 1 (new CRD type), Job 3 (modify a type), and Job 4 (diagnose a dropped field).

**Depends on:** Phase 1 (drift tests validate the converters).

**What changes:**

Define three interfaces that give conversion, validation, and defaulting a predictable structure:

```go
type Converter[CRD, Runtime any] interface {
    Convert(spec CRD, ctx ResolutionContext) (Runtime, error)
}

type Validator[T any] interface {
    Validate(spec T) []FieldError
}

type Defaulter[T any] interface {
    Default(spec *T)
}
```

Add a pipeline runner that enforces ordering:

```go
func RunPipeline[CRD, Runtime any](
    d Defaulter[CRD], v Validator[CRD], c Converter[CRD, Runtime],
    spec CRD, ctx ResolutionContext,
) (Runtime, error) {
    d.Default(&spec)

    if errs := v.Validate(spec); len(errs) > 0 {
        var zero Runtime
        return zero, ValidationError{Errors: errs}
    }

    return c.Convert(spec, ctx)
}
```

Refactor existing conversion functions to implement `Converter`. Extract scattered defaults into `Defaulter` implementations, scattered validation into `Validator` implementations. This can be done one CRD type at a time — each refactor is a single PR.

**Validation layering:** Schema-level validation (required, enum, range, declarative cross-field rules) stays in kubebuilder markers and CEL. `Validator[T]` handles semantic rules requiring Go logic. This matches Kubernetes' own layering (OpenAPI schema + admission webhooks).

**Defaulting layering:** Static defaults stay in kubebuilder markers (`+kubebuilder:default=`). `Defaulter[T]` handles conditional/computed defaults ("if transport is `sse`, default port to `8080`"). The pipeline runs defaults before validation, so validation always sees fully-defaulted data.

**Job 4 before:** Trace through bespoke functions with inconsistent names. No predictable path from "field doesn't work" to root cause.

**Job 4 after:** Find the `Converter` for the CRD type (discoverable by interface). Check `Default()` — is the default overwriting the value? Check `Validate()` — is it being rejected? Check `Convert()` — is the mapping correct? Same structure for every CRD type.

**Scope:** Interface definitions + pipeline runner (~200 lines). Then one refactoring PR per CRD type to migrate existing conversion functions.

---

### Phase 3: References[T] and auto-wired cross-CRD references

**Problem addressed:** Boilerplate reference wiring (#3). Improves Job 1 (new CRD type) and Job 2 (add a reference).

**Depends on:** Nothing (can proceed in parallel with Phase 2).

**What changes:**

Introduce a generic type that encodes reference relationships in the Go type system:

```go
type References[T client.Object] struct {
    // +kubebuilder:validation:Required
    Name string `json:"name"`
}
```

Add `AutoWatchReferences`: at controller startup, reflect over the spec struct, discover all `References[T]` fields, and wire watches + MapFuncs + resolve logic + status conditions for each.

```go
func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
    builder := ctrl.NewControllerManagedBy(mgr).
        For(&v1alpha1.MCPServer{})

    refSet, err := refs.AutoWatchReferences(builder, mgr.GetClient(), &v1alpha1.MCPServer{})
    if err != nil {
        return err
    }
    r.Refs = refSet

    return builder.Complete(r)
}
```

Migrate existing reference fields one at a time. Each migration replaces ~60 lines of hand-written watch/MapFunc/resolve/condition code with one struct field change.

**Job 2 before:** Add a ref field, write a Watches call, write a MapFunc, write resolve logic, add error handling, add a status condition. ~60 lines per reference.

**Job 2 after:** Add one struct field: `TelemetryConfigRef *References[MCPTelemetryConfig]`. Run `make manifests`. Done.

**Key technical risk:** controller-gen and Go generics. `References[T]` uses Go generics; controller-gen may not fully support generic type parameters in CRD schema generation. A spike (or the standalone prototype) must confirm this works before migrating real CRDs. If controller-gen can't handle it, a non-generic workaround is possible (e.g., code generation of concrete reference types).

**Scope:** `References[T]` type + `AutoWatchReferences` helper + `RefResolver` (~500 lines). Then one migration PR per controller.

---

### Phase 4: Structured reconciler with Sync

**Problem addressed:** Interleaved lifecycle management (#4). Improves Job 1 (new CRD type).

**Depends on:** Phases 2 and 3 (the pipeline and reference resolution are what the framework orchestrates).

**What changes:**

The framework owns the reconcile lifecycle. The developer implements a `Sync` function that receives a context with everything already resolved:

```go
type Reconciler[Spec, Config any] interface {
    Converter[Spec, Config]
    Validator[Spec]
    Defaulter[Spec]
    Sync(ctx SyncContext[Spec, Config]) (ctrl.Result, error)
}

type SyncContext[Spec, Config any] struct {
    Object  client.Object
    Spec    Spec           // defaulted + validated
    Config  Config         // converted
    Status  StatusHelper
    // Ref("fieldName") returns the resolved referenced object
}
```

The framework's generic `Reconcile` handles fetch, reference resolution, pipeline execution (Default → Validate → Convert), error classification, and status management. The developer's `Sync` receives fully-resolved data and does whatever resource creation is appropriate. The framework doesn't prescribe a resource topology — one Deployment or three, one ConfigMap or two — it handles the lifecycle around it.

For escape hatches (image validation with requeue, readiness gates), an optional `Preconditions` hook runs before the pipeline.

**Job 1 before:** Reverse-engineer an existing ~2,300-line reconciler. Copy-paste, adapt. No template.

**Job 1 after:** Implement three interfaces (Converter, Validator, Defaulter) + a Sync function with the unique business logic. Reference wiring and pipeline execution are automatic. The framework enforces the same lifecycle structure across all controllers.

**Scope:** Framework reconciler (~300 lines). Then migrate controllers one at a time — each migration is a large but mechanical refactor.

---

## Prototype

Before Phase 3, build a standalone proof-of-concept to validate `References[T]` with controller-gen and the `AutoWatchReferences` reflection pattern. These are the components with genuine technical risk. Phases 1 and 2 use well-established patterns (reflection-based testing, Go interfaces) and can proceed directly.

The prototype uses a simplified domain mirroring ToolHive's CRD graph (MCPServer → MCPExternalAuthConfig, MCPToolConfig), delivered as a sequence of commits where each diff demonstrates one job-to-be-done.

### Success criteria

- `make manifests` produces valid CRD YAML with `References[T]` rendering correctly.
- Reflection discovers `References[T]` fields and auto-wires watches.
- envtest confirms: reference resolved, referenced resource update triggers re-reconcile, broken reference sets degraded condition.
- Drift detection catches a new unmapped field.

## Alternatives considered

### Drift detection only (original THV-0058 scope)

The original RFC proposed drift tests without broader conventions. Drift detection is Phase 1 of this roadmap — it delivers immediate value and is the right place to start. But it doesn't prevent the structural problems that cause drift: bespoke conversion functions, scattered defaults, inconsistent validation. The later phases address those.

### Full code generation (ACK-style)

Generate conversion, validation, and defaulting from a schema definition. ToolHive doesn't have a canonical schema to generate from, and the CRD and runtime types are structurally different enough that codegen would require heavy annotation. Convention interfaces (Phase 2) give most of the benefit without the tooling investment. Codegen can be layered on later once conventions are stable.

### Embed runtime types in CRDs directly

Eliminates the conversion layer but couples CRD schema evolution to runtime changes, leaks CLI-only fields, and prevents user-friendly nested CRD structure. The type separation is correct; the problem is the lack of conventions around conversion.

### Generic resource set / declarative resource management

Have reconcilers return a bag of desired Kubernetes objects; framework handles diffing. This is a large, separate problem (dependency ordering, garbage collection, status aggregation) — effectively rebuilding Helm. The `Sync` pattern (Phase 4) gives the developer full flexibility over resource creation while the framework handles lifecycle.

## Decision

Adopt CRD authoring conventions as a phased roadmap:

1. **Phase 1: Drift detection tests** — immediate value, no new abstractions, catches the most common bug class.
2. **Phase 2: Convention interfaces** — gives conversion, validation, and defaulting a predictable structure.
3. **Phase 3: References[T]** — eliminates cross-CRD reference boilerplate. Requires prototype to validate technical approach.
4. **Phase 4: Structured reconciler** — framework owns lifecycle, developer owns business logic.

Each phase is independently valuable and independently reviewable. A team can adopt Phase 1 this week and decide later whether to continue.
