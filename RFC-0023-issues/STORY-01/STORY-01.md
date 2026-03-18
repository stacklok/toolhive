# Add CEL Validation to Existing Union Types

**As a** platform operator,
**I want** `OIDCConfigRef` and `AuthzConfigRef` to reject invalid combinations at admission time,
**so that** misconfigurations are caught immediately by the API server instead of surfacing as confusing controller errors.

**Size**: S
**Dependencies**: None
**Labels**: `operator`, `api`

## Context

`OIDCConfigRef` uses a `type` discriminator field (`kubernetes`, `configMap`, `inline`) but has no CEL validation rules — nothing prevents setting both `configMap` and `inline` simultaneously. `AuthzConfigRef` has the same gap. Validation only happens at controller reconciliation time, producing confusing error conditions instead of immediate API rejection.

Compare with `MCPExternalAuthConfig` which already has CEL rules (`mcpexternalauthconfig_types.go:44-51`).

## Acceptance Criteria

- [ ] `OIDCConfigRef` has a CEL rule enforcing exactly one of `kubernetesServiceAccount`, `configMapRef`, or `inline` is set
- [ ] `AuthzConfigRef` has a CEL rule enforcing exactly one of `configMapRef` or `inline` is set
- [ ] Applying a manifest with both `configMapRef` and `inline` set is rejected by the API server
- [ ] Applying a manifest with neither set is rejected by the API server
- [ ] Existing valid manifests continue to work unchanged
- [ ] Unit tests cover all valid/invalid combinations

## Sub-Issues

| ID | Title |
|---|---|
| [01-A](01-A.md) | Add CEL validation rule to `OIDCConfigRef` (with integration tests) |
| [01-B](01-B.md) | Add CEL validation rule to `AuthzConfigRef` (with integration tests) |
