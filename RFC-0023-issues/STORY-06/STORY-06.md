# Add Printer Columns to All CRDs

**As a** cluster operator,
**I want** meaningful printer columns on all CRDs including the new config CRDs,
**so that** `kubectl get` output is informative without requiring `-o yaml`.

**Size**: S
**Dependencies**: STORY-02, STORY-03 (06-A adds printer columns to MCPOIDCConfig and MCPTelemetryConfig, which are created in those stories)
**Labels**: `operator`, `api`, `ux`

## Context

Some CRDs already have printer columns (MCPServer, MCPRegistry, MCPExternalAuthConfig, etc.), but coverage is inconsistent. New config CRDs (MCPOIDCConfig, MCPTelemetryConfig) need columns from the start, and existing CRDs should be audited for completeness.

## Acceptance Criteria

- [ ] `MCPOIDCConfig` has columns: Source (inline/configMap/kubernetesServiceAccount), Ready, References (count), Age
- [ ] `MCPTelemetryConfig` has columns: Endpoint, Ready, References (count), Age
- [ ] Existing CRDs with missing or incomplete columns are reviewed and updated
- [ ] All printer columns use JSONPath expressions that resolve correctly
- [ ] `kubectl get <resource>` produces useful output for all CRD types

## Sub-Issues

| ID | Title |
|---|---|
| [06-A](06-A.md) | Add printer columns to new config CRDs |
| [06-B](06-B.md) | Review and update printer columns on existing CRDs |
