# RFC-0023: Issue Plan — CRD v1alpha2 Optimization and Configuration Extraction

> **Parent Epic**: CRD v1alpha2 Optimization and Configuration Extraction (RFC-0023)
>
> **Scope**: All work lands as `v1alpha2` before promoting to `v1beta1`.
> CLI migration tooling (`thv migrate`) is **out of scope** for this epic.
> MCPRemoteEndpoint (RFC-0057) is assumed to exist as an approved CRD.
> Each story includes its own unit tests, integration tests, and documentation — tests are not deferred.

---

## Dependency Graph

```
STORY-01 (CEL validation)          ─── no dependencies
STORY-02 (MCPOIDCConfig CRD)       ─── no dependencies
STORY-03 (MCPTelemetryConfig CRD)  ─── no dependencies
STORY-04 (Deprecated field removal) ── no dependencies
STORY-05 (MCPRegistry status)       ── no dependencies
STORY-06 (Printer columns)          ── depends on STORY-02, STORY-03 (06-A)

STORY-07 (Workload config refs)     ── depends on STORY-02, STORY-03
```

```
  ┌──────────┐  ┌──────────┐  ┌──────────┐
  │ STORY-01 │  │ STORY-04 │  │ STORY-05 │
  │ CEL val. │  │ Deprec.  │  │ Registry │
  └──────────┘  └──────────┘  └──────────┘

  ┌──────────┐  ┌──────────┐
  │ STORY-02 │  │ STORY-03 │
  │ OIDCCfg  │  │ TelCfg   │
  └────┬─────┘  └────┬─────┘
       │    │    │    │
       │    └──┬─┘    │
       │       ▼      │
       │ ┌──────────┐ │
       │ │ STORY-06 │ │
       │ │ Printer  │ │
       │ └──────────┘ │
       │              │
       └──────┬───────┘
              ▼
        ┌──────────┐
        │ STORY-07 │
        │ Workload │
        │ refs     │
        └──────────┘
```

## Summary

| Story | Title | Size | Dependencies | Sub-Issues |
|---|---|---|---|---|
| STORY-01 | CEL validation on existing unions | S | None | 2 |
| STORY-02 | MCPOIDCConfig CRD | L | None | 5 |
| STORY-03 | MCPTelemetryConfig CRD | L | None | 4 |
| STORY-04 | Deprecated field removal | M | None | 3 |
| STORY-05 | MCPRegistry status consolidation | M | None | 2 |
| STORY-06 | Printer columns | S | 02, 03 | 2 |
| STORY-07 | Workload CRD config ref updates | XL | 02, 03 | 5 |
| | **Total** | | | **23** |

## Suggested Execution Order

**Wave 1** (parallel — no dependencies):
- STORY-01: CEL validation on existing unions
- STORY-02: MCPOIDCConfig CRD
- STORY-03: MCPTelemetryConfig CRD
- STORY-04: Deprecated field removal
- STORY-05: MCPRegistry status consolidation

**Wave 2** (after STORY-02 and STORY-03 land):
- STORY-06: Printer columns (06-A needs MCPOIDCConfig and MCPTelemetryConfig types; 06-B can run in Wave 1)
- STORY-07: Workload CRD config ref updates

## Risks and Open Items

1. **API version strategy**: The RFC targets v1beta1, but work lands as v1alpha2. Confirm whether v1alpha2 types live in a new `api/v1alpha2/` package or are incremental changes to `api/v1alpha1/`. A new package means duplicating all types; in-place changes mean existing resources need manual recreation.

2. **RFC-0057 readiness**: STORY-07 sub-issue 07-C (MCPRemoteEndpoint) depends on RFC-0057's CRD types being merged. If MCPRemoteEndpoint types are not yet available, 07-C should be deferred.

3. **Telemetry type embedding**: Embedding `telemetry.Config` directly may surface new kubebuilder marker or deepcopy issues. STORY-03 should prototype this early and escalate if problems arise.

4. **Scope of inline support**: The RFC says inline config remains supported alongside refs. This doubles the code paths in workload controllers (STORY-07). Consider whether inline should be deprecated in v1alpha2 to reduce complexity.

5. **MCPRemoteProxy deprecated field removal**: MCPRemoteProxy is being deprecated by RFC-0057. STORY-04 includes removing `MCPRemoteProxy.spec.port`, but if MCPRemoteProxy is being removed entirely, this sub-issue may be unnecessary. Confirm scope.
