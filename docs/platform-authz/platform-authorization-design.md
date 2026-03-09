# ToolHive Platform Authorization Design

## 1. Motivation

ToolHive manages MCP (Model Context Protocol) servers in Kubernetes. Today, authorization is per-server: administrators hand-write Cedar policies for individual MCPServers. This does not scale for enterprises with 10-30 MCP servers, hundreds of users across IdP groups, and compliance requirements for centralized access control.

**Platform Authorization** bridges the gap between IdP groups/roles and MCP server access control, letting administrators express policy in familiar RBAC terms while compiling down to Cedar for runtime enforcement.

### Design Principles

- **Cedar is the runtime engine.** CRDs are a compile-time convenience that produce Cedar policies and entities. No runtime dependency on the controller.
- **Extend, don't replace.** The existing `cedarv1` authorizer type gains a `group_claim` field. No separate enterprise authorizer.
- **OSS stays open.** The Cedar authorizer and entity model remain open-source. The enterprise controller (CRDs, compiler, injector) is closed-source.
- **Backward compatible.** Existing hand-written Cedar policies continue to work without modification.

### Repo Boundaries

| Concern | Lives in |
|---------|----------|
| CRD Go types, controller, Cedar compiler, ConfigMap injector, Helm chart | Closed-source enterprise repo |
| `group_claim` field, group extraction, `MCP` parent on entities, `readOnlyHint` attribute | OSS repo (`pkg/authz/authorizers/cedar/`) |

---

## 2. CRD Design

Three namespace-scoped CRDs under the `toolhive.stacklok.dev` API group, following the Kubernetes RBAC pattern (Role defines permissions, Binding assigns principals, Policy scopes to targets):

### ToolhivePlatformRole — defines WHAT a role can do

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhivePlatformRole
metadata:
  name: security-auditor
spec:
  description: "Can list and call tools, but not prompts or resources"
  actions:
    - call_tool
    - list_tools
```

Actions are a flat list mapping 1:1 to Cedar actions: `call_tool`, `list_tools`, `get_prompt`, `list_prompts`, `read_resource`, `list_resources`, plus `*` (all). The `readOnlyTools` field gates `call_tool` on the tool's `readOnlyHint` annotation.

Two default roles ship as Helm-managed `ToolhivePlatformRole` CRD instances (not hardcoded constants). They're discoverable via `kubectl get tpr` and follow the same code path as custom roles.

| Default Role | Actions | `readOnlyTools` |
|--------------|---------|-----------------|
| `reader` | All list/get/read + `call_tool` | `true` — `call_tool` gated on `readOnlyHint=true` |
| `writer` | All (equivalent to `*`) | `false` |

Deletion protection comes from Helm ownership — if accidentally deleted, the next Helm sync re-creates them. A `toolhive.stacklok.dev/built-in: "true"` annotation marks them as system defaults.

### ToolhiveRoleBinding — maps WHO to WHICH roles

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveRoleBinding
metadata:
  name: acme-roles
spec:
  bindings:
    # Simple OR: anyone in platform-eng OR sre-team gets writer
    - platformRole: writer
      from:
        - groups: [platform-eng]
        - groups: [sre-team]
    # AND condition: must be in engineering AND have team-lead role
    - platformRole: writer
      from:
        - groups: [engineering]
          roles: [team-lead]
    # All developers get reader
    - platformRole: reader
      from:
        - groups: [all-developers]
```

The `from` field uses **DNF (Disjunctive Normal Form)** matching:
- **OR** between conditions in the list — any matching condition assigns the role.
- **AND** within each condition — all specified groups and roles must be present.

Both `groups` and `roles` produce Cedar `Group` entities; the distinction is semantic (which JWT claim they come from).

### ToolhiveAuthorizationPolicy — binds roles to MCPServers

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-access
spec:
  targetRef:
    kind: MCPServer
    name: github
  bindings:
    - platformRole: writer
    - platformRole: reader
  deny:
    - actions: [call_tool]
      tools: [delete_repo, force_push]
```

Key features:

- **Grants** (`bindings`) compile to Cedar `permit` statements. Roles define the ceiling; optional `ruleRestrictions` narrow to specific tools/prompts/resources.
- **Denials** (`deny`) compile to Cedar `forbid` statements. Always override permits. No `*` wildcard allowed in deny rules (must be explicit).
- **Resource restrictions** are typed fields (`tools`, `prompts`, `resources`) because each Cedar action only applies to its matching resource type.
- **Multiple policies** targeting the same server are **unioned** (Cedar's native evaluation model). Adding a policy can only increase access; narrowing requires `forbid`.

#### Restricted grant example

```yaml
bindings:
  - platformRole: writer
    ruleRestrictions:
      - tools: [create_pr, list_issues]
      - prompts: [code_review]
```

#### Server-wide deny (safety rails)

```yaml
deny:
  - actions: [call_tool]  # no tools listed = deny on ALL tools for this server
```

---

## 3. Cedar Compilation

### Entity Hierarchy

Two independent hierarchies resolved by Cedar's transitive `in` operator:

```
Principal hierarchy (access control):
  Client (dynamic, from JWT)
    └─ Group (dynamic UID, static Role parents from entities.json)
         └─ Role (static)

Resource hierarchy (server scoping):
  Tool / Prompt / Resource (dynamic, per-request)
    └─ MCP (static, server container)
```

| Entity | Source | Lifecycle |
|--------|--------|-----------|
| `Client` | JWT `sub` claim | Per-request |
| `Group` | JWT group claim + static entities.json | UIDs per-request; Role parents static |
| `Role` | ToolhivePlatformRole CRD (including defaults) | Static in entities.json |
| `MCP` | MCPServer name | Static in entities.json |
| `Tool`/`Prompt`/`Resource` | MCP request | Per-request |

**Example entities.json** (generated by controller, stored in ConfigMap):

```json
[
  {"uid": {"type": "MCP", "id": "github"}, "attrs": {}, "parents": []},
  {"uid": {"type": "Role", "id": "writer"}, "attrs": {}, "parents": []},
  {"uid": {"type": "Role", "id": "reader"}, "attrs": {}, "parents": []},
  {"uid": {"type": "Group", "id": "platform-eng"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]},
  {"uid": {"type": "Group", "id": "all-developers"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "reader"}
  ]}
]
```

### Policy Shapes

The controller compiles CRDs into 5 Cedar policy shapes:

**Shape 1 — Unrestricted grant (server-scoped):**
```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools", ...],
  resource in MCP::"github"
);
```

**Shape 2 — Restricted grant (typed resources, server-scoped):**
```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools"],
  resource
) when {
  resource in MCP::"github" &&
  resource in [Tool::"list_issues", Tool::"create_pr"]
};
```

**Shape 3 — Role with `readOnlyTools: true` (readOnlyHint gate):**
```cedar
// List/browse everything
permit(
  principal in Role::"reader",
  action in [Action::"list_tools", Action::"list_prompts", ...],
  resource in MCP::"github"
);

// Call only read-only tools (because reader has readOnlyTools: true)
permit(
  principal in Role::"reader",
  action == Action::"call_tool",
  resource in MCP::"github"
) when { resource has readOnlyHint && resource.readOnlyHint == true };
```

**Shape 4 — Deny rule (server-scoped):**
```cedar
forbid(
  principal,
  action == Action::"call_tool",
  resource == Tool::"delete_repo"
) when { resource in MCP::"github" };
```

**Shape 4a — Server-wide deny (no resource names):**
```cedar
forbid(
  principal,
  action == Action::"call_tool",
  resource in MCP::"github"
);
```

### AND Condition Compilation

DNF matching requires per-condition `when` clauses in the compiled Cedar. This prevents the entity hierarchy's OR nature from bypassing AND gates:

```cedar
// Condition 0: simple group (still needs when clause)
permit(principal, action in [...], resource in MCP::"github")
when { principal in Group::"platform-eng" };

// Condition 1: AND (engineering AND team-lead)
permit(principal, action in [...], resource in MCP::"github")
when {
  principal in Group::"engineering" &&
  principal in Group::"team-lead"
};
```

### Design Invariants

- **All permits are server-scoped** via `resource in MCP::"<server>"` (defense-in-depth).
- **Forbid always overrides permit** (Cedar native).
- **Entity IDs are case-sensitive** (no normalization; mismatches surfaced via status conditions).
- **Validation against Cedar schema** before writing ConfigMap; failed validation preserves last known-good policy.

---

## 4. Controller Architecture

### Fan-in reconciliation (MCPServer as primary)

All CRD changes fan in to MCPServer reconciliation via `MapFunc`s:

```
ToolhiveAuthorizationPolicy ──┐
ToolhiveRoleBinding ──────────┤ MapFunc → enqueue MCPServer(s)
ToolhivePlatformRole ─────────┤
MCPServer (create/delete) ────┘
                                   │
                    EnterpriseAuthzReconciler.Reconcile(MCPServer)
                                   │
                    ├─ Collect policies targeting this server
                    ├─ Resolve roles and bindings
                    ├─ Compile Cedar policies + entities
                    ├─ Write ConfigMap
                    ├─ SSA-patch MCPServer spec.authzConfig
                    └─ Update policy status conditions
```

### ConfigMap-per-server injection

The controller writes one ConfigMap per targeted MCPServer and SSA-patches the MCPServer's `spec.authzConfig` to reference it:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: github-enterprise-authz
  labels:
    toolhive.stacklok.dev/component: enterprise-authz
    toolhive.stacklok.dev/mcp-server: github
    toolhive.stacklok.dev/managed-by: enterprise-authz-controller
  ownerReferences:
    - kind: MCPServer
      name: github
data:
  authz.json: |
    {
      "version": "1.0",
      "type": "cedarv1",
      "cedar": {
        "policies": ["permit(...);", "forbid(...);"],
        "entities_json": "[...]",
        "group_claim": "groups"
      }
    }
```

### SSA and conflict avoidance with OSS operator

- Enterprise controller owns `spec.authzConfig` + a `policy-hash` annotation via SSA with `ForceOwnership`.
- The policy-hash annotation changes on every recompilation, bumping the MCPServer generation and triggering the OSS operator to redeploy with updated policies.
- MCPServer predicate filters: react to create/delete/label-change/authzConfig-drift only — not to OSS operator status updates.
- Cleanup ordering: clear `spec.authzConfig` first, then delete ConfigMap (prevents dangling references).
- ConfigMap has owner reference to MCPServer for automatic garbage collection.

### Error handling

- **Compilation failure**: Sets `Compiled: False` condition, emits event, does NOT update ConfigMap (last known-good preserved).
- **MCPServer deleted**: Return early; ConfigMap cleaned via owner reference.
- **Access broadening**: Warning event when a new policy adds roles to a server with existing policies.

---

## 5. OSS Changes

Seven additive, backward-compatible changes to `pkg/authz/authorizers/cedar/`:

| # | Change | Summary |
|---|--------|---------|
| 1 | `GroupClaim` in `ConfigOptions` | Optional `group_claim` field; empty = no group extraction |
| 2 | `serverName` on `Authorizer` | Store already-passed server name; empty = current behavior |
| 3 | Parent support in entity factory | Variadic `parents ...cedar.EntityUID` on `CreatePrincipalEntity`/`CreateResourceEntity` |
| 4 | Group extraction + Client parents | Extract groups from JWT using `group_claim`; set as Client parent UIDs |
| 5 | MCP parent on resource entities | `Tool`/`Prompt`/`Resource` entities get `MCP::"<server>"` parent |
| 6 | `readOnlyHint` on Tool entities | Tool annotation attribute for reader role's `call_tool` gate |
| 7 | `MCP` entity for list operations | Use `MCP::"<server>"` instead of `FeatureType` when `serverName` is set |

### Key implementation details

**Group extraction** uses dot-notation for nested claims with an exact-match-first strategy (handles Auth0 URLs containing dots):

```go
func resolveNestedClaim(claims jwt.MapClaims, path string) interface{} {
    // Fast path: exact top-level match (handles Auth0 URLs with dots)
    if v, exists := claims[path]; exists {
        return v
    }
    // Slow path: dot-notation traversal (handles Keycloak nesting)
    parts := strings.Split(path, ".")
    // ... traverse nested objects ...
}
```

**Critical merge-order hazard**: Dynamic entities overwrite static ones during merging. Group entities must NOT be created dynamically — only the Client entity gets Group parent UIDs. The static Group entities (from entities.json) carry the Role parents.

---

## 6. Claim Mapping

### IdP claim formats

There is no standard JWT claim name for group membership:

| IdP | Claim path | Notes |
|-----|-----------|-------|
| Keycloak (realm roles) | `realm_access.roles` | 2-level nesting; dot-notation required |
| Keycloak (groups) | `groups` | Requires Group Membership mapper |
| Okta | `groups` | Custom claim, not included by default |
| Entra ID (groups) | `groups` | **GUIDs** by default; overage at >200 groups |
| Entra ID (app roles) | `roles` | Display names; no overage limit |
| Auth0 | `https://<namespace>/roles` | URL with dots; exact-match-first handles this |

### Configuration examples

```yaml
# Keycloak realm roles
group_claim: "realm_access.roles"

# Keycloak groups (requires mapper)
group_claim: "groups"

# Entra ID application roles (recommended over GUIDs)
group_claim: "roles"

# Auth0
group_claim: "https://myapp.example.com/roles"
```

### Single-claim limitation

Both `PrincipalCondition.Groups` and `.Roles` match against the same `group_claim`. AND conditions spanning groups and roles from different JWT claims (e.g., Entra ID `groups` vs `roles`) are not satisfiable until the `group_claims` (plural) extension is implemented.

### Future: multi-claim support

Adding `group_claims []string` alongside `group_claim string` is non-breaking. All listed claims are extracted and unioned. Existing single-claim configs continue to work.

---

## 7. Security Model

| Property | How it works |
|----------|-------------|
| **Default deny** | Cedar denies unless explicitly permitted. No policy = no access. |
| **Forbid overrides permit** | `forbid` always wins. CRD `deny` rules are absolute. |
| **Unmapped groups are inert** | JWT groups with no matching static Group entity contribute no role membership. |
| **No credentials in policies** | CRDs and Cedar artifacts contain only group/role names (non-sensitive metadata). |
| **Permit accumulation** | Adding policies can only increase access. Narrowing requires `forbid`. Controller emits warnings when access broadens. |
| **CRD creation is privileged** | RBAC must restrict authorization CRD mutations to platform admins. |
| **Case-sensitive matching** | No normalization; mismatches surfaced via status conditions. |
| **Empty group_claim is safe** | No parents on Client entity; default deny applies. |

### Claim freshness

JWTs are self-contained — claims don't update when group membership changes. Mitigations: short token lifetimes (5-15 min), enforced token refresh, emergency `forbid` rules.

### Entra ID group overage

When >200 groups, Entra ID replaces the `groups` claim with a Graph API reference. Users are silently denied. MVP recommendation: use application roles (`roles` claim) or filter groups below 200.

---

## 8. End-to-End Example

### Input CRDs

```yaml
kind: ToolhiveRoleBinding
metadata:
  name: acme-roles
spec:
  bindings:
    - platformRole: writer
      from:
        - groups: [platform-eng]
    - platformRole: reader
      from:
        - groups: [all-developers]
---
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-access
spec:
  targetRef:
    kind: MCPServer
    name: github
  bindings:
    - platformRole: writer
    - platformRole: reader
  deny:
    - actions: [call_tool]
      tools: [delete_repo]
```

### Compiled output (ConfigMap)

```json
{
  "version": "1.0",
  "type": "cedarv1",
  "cedar": {
    "policies": [
      "permit(principal in Role::\"writer\", action in [...all 6...], resource in MCP::\"github\");",
      "permit(principal in Role::\"reader\", action in [...non-call...], resource in MCP::\"github\");",
      "permit(principal in Role::\"reader\", action == Action::\"call_tool\", resource in MCP::\"github\") when { resource has readOnlyHint && resource.readOnlyHint == true };",
      "forbid(principal, action == Action::\"call_tool\", resource == Tool::\"delete_repo\") when { resource in MCP::\"github\" };"
    ],
    "entities_json": "[MCP::github, Role::writer, Role::reader, Group::platform-eng→Role::writer, Group::all-developers→Role::reader]",
    "group_claim": "groups"
  }
}
```

### Runtime evaluation

**Alice** (groups: `[platform-eng]`) calls `tools/call` with tool `create_pr`:

1. Extract groups from JWT claim `groups` → `[platform-eng]`
2. Build `Client::"alice"` with parent `Group::"platform-eng"`
3. Build `Tool::"create_pr"` with parent `MCP::"github"`
4. Cedar evaluates: Client → Group::"platform-eng" → Role::"writer" → permit matches → **PERMIT**

**Bob** (groups: `[all-developers]`) calls `tools/call` with tool `delete_repo`:

1. Build `Client::"bob"` with parent `Group::"all-developers"`
2. Build `Tool::"delete_repo"` with parent `MCP::"github"`
3. Cedar: writer permit doesn't match (bob is reader). Reader call_tool requires `readOnlyHint=true` → `delete_repo` is not read-only → no match. Forbid on `delete_repo` matches → **DENY**

---

## 9. Extensibility Path

| Phase | What | Breaking? |
|-------|------|-----------|
| MVP | MCPServer only | — |
| Phase 2 | Add `VirtualMCPServer` to `targetRef.kind` enum | No (additive) |
| Phase 3 | Add non-MCP domains (e.g., MCPRegistry) | No (new actions in enum, new controller) |
| Future | `targetSelector` (by label) alongside `targetRef` | No (mutually exclusive fields) |
| Future | `group_claims` (plural) for multi-claim extraction | No (falls back to singular) |
