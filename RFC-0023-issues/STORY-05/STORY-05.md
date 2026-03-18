# MCPRegistry Status Consolidation

**As a** cluster operator monitoring MCPRegistry health,
**I want** a single conditions-based status instead of three separate phase enums,
**so that** I can use standard Kubernetes tooling (e.g., `kubectl wait --for=condition=SyncReady`) to check registry status.

**Size**: M
**Dependencies**: None
**Labels**: `operator`, `api`

## Context

MCPRegistry has three separate phase enums:
- `Status.Phase` (`MCPRegistryPhase`: Pending/Ready/Failed/Syncing/Terminating)
- `Status.SyncStatus.Phase` (`SyncPhase`: Syncing/Complete/Failed)
- `Status.APIStatus.Phase` (`APIPhase`: NotStarted/Deploying/Ready/Unhealthy/Error)

A `DeriveOverallPhase()` method (`mcpregistry_types.go:713`) computes the top-level phase from the others. This should be replaced with standard Kubernetes conditions where the top-level phase is derived from conditions, not independently set.

## Acceptance Criteria

- [ ] `Status.SyncStatus.Phase` (`SyncPhase`) and `Status.APIStatus.Phase` (`APIPhase`) enums are removed
- [ ] New conditions: `SyncReady` and `APIReady` replace the sub-phase enums
- [ ] `Status.Phase` is derived from conditions (not independently set)
- [ ] `DeriveOverallPhase()` method is updated to use conditions
- [ ] `SyncStatus` retains operational fields: `LastSyncTime`, `LastSyncHash`, `ServerCount`, `LastAttempt`, `AttemptCount`
- [ ] `APIStatus` retains operational fields: `Endpoint`, `Replicas`, `AvailableReplicas`
- [ ] Existing fields `LastManualSyncTrigger`, `LastAppliedFilterHash`, `StorageRef` are preserved
- [ ] Printer columns updated to reflect conditions
- [ ] MCPRegistry controller updated for new status shape
- [ ] Unit tests cover condition-based phase derivation and controller status transitions

## Sub-Issues

| ID | Title |
|---|---|
| [05-A](05-A.md) | Refactor MCPRegistry status types (with DeriveOverallPhase tests) |
| [05-B](05-B.md) | Update MCPRegistry controller for conditions-based status (with tests) |
