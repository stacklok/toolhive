# Platform Authorization Design

This directory contains the design for the ToolHive Platform Authorization
system. The authorization system bridges the gap between IdP groups/roles and
MCP server access control, letting administrators express policy in familiar
RBAC terms while compiling down to Cedar for runtime enforcement.

## Documents

| Document | Status | Description |
|----------|--------|-------------|
| [Invariants](00-invariants.md) | Complete | Immutable truths and design constraints |
| [CRDs](01-crds.md) | Complete | CRD definitions and field semantics |
| [Cedar Compilation](02-cedar-compilation.md) | Complete | How CRDs compile to Cedar policies and entities |
| [Controller Design](03-controller.md) | Complete | Reconciliation loops, SSA, watch predicates |
| [OSS Changes](04-oss-changes.md) | Complete | Changes needed in this repo (ToolHive OSS) |
| [Claim Mapping](05-claim-mapping.md) | Planned | IdP-specific claim extraction and group resolution |

## Key Design Decisions

Resolved during invariants design:

| Decision | Outcome | Rationale |
|----------|---------|-----------|
| Authorizer type | Extend existing `cedarv1` | No duplication; ~30 lines OSS change |
| Policy injection | ConfigMap per MCPServer | Small SSA footprint; clean audit trail |
| Policy targeting | `targetRef` by name (MVP) | `targetSelector` additive later |
| Server scoping | Include `MCP` entity from day one | vMCP already has centralized auth; policies are self-contained |
| Action model | Flat actions with `*` wildcard | MCP action space is sparse; flat avoids invalid combinations |
| Built-in roles | `reader` and `writer` from MCP annotations | `readOnlyHint` enforced via Cedar `when` clause |
| Policy conflict | Union of permits (Cedar native) | Narrowing via `forbid`/`deny` field |
| Nested claims | Dot-notation in `group_claim` | Keycloak support from day one |
| Multiple claims | Single `group_claim` for MVP | `group_claims` plural additive later |

## Repo Boundaries

| Concern | Lives in |
|---------|----------|
| CRD Go types, deepcopy, swagger | Closed-source enterprise repo |
| Authorization controller (reconciler, Cedar compiler) | Closed-source enterprise repo |
| MCPServer injection controller (SSA of Cedar policies) | Closed-source enterprise repo |
| ConfigMap generation per MCPServer | Closed-source enterprise repo |
| Helm chart for CRDs and controller | Closed-source enterprise repo |
| `group_claim` field in Cedar `ConfigOptions` | **This repo** (OSS) |
| Group extraction + entity parent population | **This repo** (OSS) |
| `MCP` parent on resource entities (`EntityFactory`) | **This repo** (OSS) |
| `readOnlyHint` attribute on resource entities | **This repo** (OSS) |
| Dot-notation claim extraction | **This repo** (OSS) |

## Quick Reference

- Proposal: `config-server-proposal.md` at repo root, section "Platform Authorization"
- WIP design session notes: `~/Downloads/claude-design-session.txt`
- Existing Cedar authorizer: `pkg/authz/authorizers/cedar/`
- Identity type: `pkg/auth/identity.go`
- Cedar entity factory: `pkg/authz/authorizers/cedar/entity.go`
- Cedar config: `pkg/authz/authorizers/cedar/core.go` (`ConfigOptions` struct)
- Authz config resolution: `cmd/thv-operator/pkg/controllerutil/authz.go`
- vMCP auth factory: `pkg/vmcp/auth/factory/incoming.go`
