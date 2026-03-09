# Platform Authorization: Invariants and Design Constraints

This document captures the immutable truths and design constraints for the
platform authorization system. Every subsequent design document and
implementation must respect these invariants. If a constraint needs to change,
it must be explicitly discussed and this document updated first.

## 1. Architectural Invariants

### 1.1 Cedar is the runtime policy engine

All authorization decisions at MCP server request time are made by Cedar. The
CRD abstractions are a **compile-time** convenience — they produce Cedar
policies and entities that are injected into MCP server configurations. There
is no runtime dependency on the controller or the CRDs.

**Implication**: Once a Cedar policy is compiled from CRDs and injected into an
MCPServer, the authorization system works even if the controller is down. The
CRDs are the source of truth; Cedar is the runtime format.

### 1.2 The controller is closed-source; the Cedar runtime is open-source

The enterprise authorization controller (CRD reconciler, Cedar compiler,
MCPServer injector) lives in a closed-source repo. The Cedar authorizer and
entity model in `pkg/authz/authorizers/cedar/` remain open-source. The OSS
Cedar authorizer must be **capable** of evaluating the policies the controller
produces, but must not **require** the controller to function.

**Implication**: Any new Cedar entity types (e.g., `Role`, `Group`, `MCP`) or
evaluation capabilities needed by the compiled policies must be contributed
to this OSS repo. The OSS authorizer must not import or reference enterprise
types.

### 1.3 The existing cedarv1 authorizer type is extended, not replaced

The enterprise controller uses the existing `cedarv1` authorizer type. No
separate `enterprise-cedarv1` type is registered. The only OSS config change
is adding the `group_claim` field to `ConfigOptions`.

**Rationale**: A separate authorizer type would duplicate Cedar logic in the
enterprise repo, creating a maintenance burden with no capability gain. The
`cedarv1` authorizer can evaluate all enterprise-generated policies with a
single optional field addition.

### 1.4 The interface contract is entities.json + policies + group_claim

The enterprise controller communicates with the OSS Cedar authorizer through
three well-defined artifacts:

1. **Cedar policies** (string array) — permit/forbid statements
2. **entities.json** (JSON string) — entity hierarchy including Role, Group,
   and MCP server parent relationships
3. **group_claim** (string) — the JWT claim name to extract user groups from;
   supports dot-notation for nested claims (e.g., `realm_access.roles` for
   Keycloak)

These are stored in a **ConfigMap per MCPServer** (named
`{mcpserver-name}-enterprise-authz`). The enterprise controller SSA-patches the
MCPServer's `spec.authzConfig` to reference this ConfigMap. The ConfigMap
structure follows the existing `authz.Config` format:

```json
{
  "version": "1.0",
  "type": "cedarv1",
  "cedar": {
    "policies": ["permit(...);", "forbid(...);"],
    "entities_json": "[{\"uid\":{\"type\":\"Role\",\"id\":\"writer\"}, ...}]",
    "group_claim": "roles"
  }
}
```

A single `group_claim` field is sufficient for MVP. If multiple claim sources
are needed later, a `group_claims` (plural, list) field can be added alongside
without breaking the existing single-string field.

### 1.5 ConfigMap-per-server injection pattern

The enterprise controller writes one ConfigMap per targeted MCPServer and
SSA-patches `spec.authzConfig` to reference it. This gives:

- **Small SSA field ownership footprint** — the enterprise controller owns
  only three fields (`type`, `configMap.name`, `configMap.key`) on the
  MCPServer spec
- **Separation of concerns** — policy content lives in a dedicated resource;
  the MCPServer spec stays focused on infrastructure
- **Audit clarity** — ConfigMap changes are distinct K8s audit events,
  separate from server config changes

**Cleanup ordering**: When removing policies from a server, the controller
must clear the `spec.authzConfig` reference first, then delete the ConfigMap.
This prevents the OSS operator from failing on a dangling ConfigMap reference.

**Owner references**: The ConfigMap should have an owner reference to the
MCPServer for automatic garbage collection on server deletion.

**Labels** on the ConfigMap:
```yaml
labels:
  toolhive.stacklok.dev/component: enterprise-authz
  toolhive.stacklok.dev/mcp-server: {mcpserver-name}
  toolhive.stacklok.dev/managed-by: enterprise-authz-controller
```

### 1.6 The OSS side must populate the entity hierarchy at request time

Static entities come from `entities.json` (compiled by the controller). But the
**principal entity** (the requesting user) and its parent relationships (which
Groups and Roles it belongs to) must be built at request time from the JWT
token claims.

The OSS Cedar authorizer must:
- Extract the user's groups from the JWT using the `group_claim` config field
- Support dot-notation for nested claims (e.g., `realm_access.roles` resolves
  to the `roles` array inside the `realm_access` object)
- Build Cedar `Group` entities for each extracted group value
- Set the principal's `Parents` to include those Group entities
- The Group entities already have Role parents (from entities.json), so Cedar's
  `in` operator transitively resolves `principal in Role::"writer"`

### 1.7 Claim names are not hardcoded

Different IdPs use different JWT claim names for group membership:

| IdP | Typical claim | Notes |
|-----|---------------|-------|
| Entra ID | `roles` or `groups` | Configurable in app registration |
| Okta | `groups` | Custom claim, name varies |
| Keycloak | `realm_access.roles` | Nested; requires dot-notation |
| Generic OIDC | varies | No standard |

The claim name is provided by the enterprise controller via the `group_claim`
config field. The OSS code extracts values from that claim — it never guesses
or hardcodes.

### 1.8 The policy is invisible to the MCPServer and the OSS controller

The `ToolhiveAuthorizationPolicy` CRD points to the MCPServer via `targetRef`
(Istio pattern). The MCPServer itself has no field referencing the policy.

The enterprise controller uses Server-Side Apply (SSA) to patch the
MCPServer's `spec.authzConfig` to reference the generated ConfigMap. From the
OSS controller's perspective, the authz config is opaque — it doesn't know or
care that it was generated from a policy CRD.

**Implication**: The OSS operator controller needs no changes to support
platform authorization. It already reads and applies authz config as-is.

## 2. CRD Design Invariants

### 2.1 Three CRDs, clear separation of concerns

| CRD | Responsibility |
|-----|---------------|
| **ToolhivePlatformRole** | Defines WHAT a role can do (flat action list) |
| **ToolhiveRoleBinding** | Maps WHO (IdP groups/roles) to WHICH platform roles |
| **ToolhiveAuthorizationPolicy** | Binds roles to specific MCPServers, with optional restrictions and denials |

This follows the Kubernetes RBAC pattern: Role defines permissions,
RoleBinding assigns principals, and the policy scopes it to targets.

### 2.2 Roles are a ceiling, policies can only narrow

A `ToolhivePlatformRole` defines the maximum set of actions. A
`ToolhiveAuthorizationPolicy` can restrict those permissions to specific
resources (via typed `tools`, `prompts`, `resources` fields) but can **never**
grant permissions beyond what the role defines.

Example: If a role allows `[call_tool, list_tools]`, a policy can restrict to
`tools: [list_issues]` but cannot add `read_resource` access.

### 2.3 Built-in roles use MCP tool annotations

The system provides two built-in roles based on MCP tool annotation semantics:

| Built-in Role | Actions | Rationale |
|---------------|---------|-----------|
| `reader` | `list_tools`, `list_prompts`, `list_resources`, `get_prompt`, `read_resource`, `call_tool` (only tools with `readOnlyHint=true`) | Read-only access; tools that don't modify state |
| `writer` | All of the above, plus `call_tool` for all tools | Full access to all operations including state-modifying tools |

**MCP tool annotations** (from MCP spec):
- `readOnlyHint` (default `false`): tool does not modify its environment
- `destructiveHint` (default `true`): tool may perform destructive updates

The `reader` role restriction on `readOnlyHint` is enforced **at the Cedar
policy level**, not by the controller querying tool lists. The compiled Cedar
policy for a reader uses a `when` clause:

```cedar
permit(
  principal in Role::"reader",
  action == Action::"call_tool",
  resource in MCP::"github"
) when { resource.readOnlyHint == true };
```

The OSS authorizer is responsible for populating `readOnlyHint` as a resource
entity attribute at request time, using cached tool annotation data from
`tools/list` responses.

Custom roles created via `ToolhivePlatformRole` CRD can define arbitrary
action sets.

### 2.4 Flat action model with `*` wildcard

Roles define permissions as a flat list of action strings. Each action maps
1:1 to an existing Cedar action. The only wildcard is `*` (all actions).

```yaml
kind: ToolhivePlatformRole
spec:
  actions: [call_tool, list_tools]
```

| CRD Action | Cedar Action | Scope |
|------------|-------------|-------|
| `call_tool` | `Action::"call_tool"` | Tools |
| `list_tools` | `Action::"list_tools"` | Tools |
| `get_prompt` | `Action::"get_prompt"` | Prompts |
| `list_prompts` | `Action::"list_prompts"` | Prompts |
| `read_resource` | `Action::"read_resource"` | Resources |
| `list_resources` | `Action::"list_resources"` | Resources |
| `*` | All of the above | All |

**Rationale**: The flat model was chosen over a two-axis (verb + resource type)
model because MCP's action space is sparse and irregular — only 6 of 12
possible verb+resource combinations are valid. The flat model eliminates
invalid combinations by construction, maps 1:1 to Cedar actions (no
compilation step), and is simpler to validate, debug, and audit. The full
action space is only 6 items, so enumeration is not burdensome.

### 2.5 Policies support both grants and denials

The `ToolhiveAuthorizationPolicy` CRD supports both `bindings` (which compile
to Cedar `permit` statements) and `deny` rules (which compile to Cedar `forbid`
statements).

```yaml
spec:
  targetRef:
    kind: MCPServer
    name: github
  bindings:
    - platformRole: writer
  deny:
    - actions: [call_tool]
      tools: [delete_repo, force_push]
```

This compiles to:

```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools", ...],
  resource in MCP::"github"
);

forbid(
  principal,
  action == Action::"call_tool",
  resource in [Tool::"delete_repo", Tool::"force_push"]
);
```

The `deny` field gives administrators CRD-level control over denials without
needing to hand-write Cedar. Cedar's `forbid` always overrides `permit`,
making denials absolute.

### 2.6 Multiple policies targeting the same server are unioned

When multiple `ToolhiveAuthorizationPolicy` resources target the same
MCPServer, their compiled Cedar statements are **unioned** into a single
policy set. This is Cedar's native and only evaluation model — there is no
priority ordering between policies. Any matching `permit` grants access; any
matching `forbid` denies it.

**Implication**: Adding a policy can only increase the set of permitted
actions. Removing a policy can only decrease it. Narrowing access requires
`forbid` (via the `deny` field or hand-written Cedar), not removal of
`permit` policies.

The enterprise controller must emit the effective permission set per server
in the `ToolhiveAuthorizationPolicy` status to make permit accumulation
visible to administrators.

### 2.7 Resource names are exact matches

Resource names in `ruleRestrictions` (typed `tools`, `prompts`, `resources`
fields) and `deny` rules are exact string matches against Cedar entity IDs.
Glob patterns are not supported.

### 2.8 Policies target MCPServers by name only (MVP)

The `ToolhiveAuthorizationPolicy` uses `targetRef` with an explicit MCPServer
name. Label-based `targetSelector` is deferred — it can be added as a
non-breaking additive CRD field alongside `targetRef` (mutually exclusive,
Istio pattern) when scale demands it.

**Rationale**: Enterprises will manage 10-30 MCPServers. Name-based targeting
is not burdensome at this scale, is consistent with every existing ToolHive
cross-resource reference (`groupRef`, `toolConfigRef`, `externalAuthConfigRef`),
and avoids controller complexity (label-change watches, overlapping selector
conflicts, accidental broad targeting).

### 2.9 The enterprise controller watches MCPServer creates/deletes only

The authorization controller uses SSA to inject Cedar policies into MCPServer
resources. To avoid conflicts with the OSS operator controller (which also
reconciles MCPServer), the authorization controller uses **watch predicates**
to react only to Create and Delete events on MCPServer, not Updates. It also
reacts to label changes (which could affect future `targetSelector`). This is
an established pattern (cert-manager, Strimzi).

## 3. Cedar Compilation Invariants

### 3.1 Entity hierarchy structure

The entity hierarchy has three layers:

**Layer 1: Server container** (static, from entities.json)

```json
{"uid": {"type": "MCP", "id": "github"}, "attrs": {}, "parents": []}
```

**Layer 2: Group-to-Role mapping** (static, from entities.json)

```json
[
  {"uid": {"type": "Role", "id": "writer"}, "attrs": {}, "parents": []},
  {"uid": {"type": "Group", "id": "platform-eng"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]},
  {"uid": {"type": "Group", "id": "sre-team"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]}
]
```

**Layer 3: User and resource entities** (dynamic, built at request time)

```
Client::"user@example.com"
  └─ parent: Group::"platform-eng"     (from JWT claim)
                └─ parent: Role::"writer"  (from entities.json)

Tool::"list_issues"
  └─ parent: MCP::"github"             (from server name)
```

Cedar's `in` operator traverses both hierarchies:
- `principal in Role::"writer"` — resolves via Client → Group → Role
- `resource in MCP::"github"` — resolves via Tool → MCP

### 3.2 Server scoping with MCP container entities

All compiled Cedar policies include server scoping using the `MCP` container
entity type. Resource entities (tools, prompts, resources) are children of
their server's `MCP` entity.

```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools"],
  resource in MCP::"github"
);
```

**Rationale**: Although each MCPServer currently has an isolated Cedar
authorizer, the vMCP (Virtual MCP Server) already runs a **centralized**
authorization middleware before routing to backends. In that architecture,
server scoping is the only policy-level boundary between backends. Including
it from day one also makes policies self-contained, portable, and auditable
without deployment topology knowledge.

The OSS `EntityFactory` receives the server name (already a parameter in
`CreateAuthorizer`) and sets it as the `MCP` parent on resource entities.
When no server name is provided, entities have empty parents (backwards
compatible).

### 3.3 Unrestricted policies scope to the server

When a `ToolhiveAuthorizationPolicy` does not specify `ruleRestrictions`, the
compiled Cedar policy uses the server as the resource scope:

```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools"],
  resource in MCP::"github"
);
```

### 3.4 Restricted policies scope to specific resources

When `ruleRestrictions` with typed resource fields (`tools`, `prompts`,
`resources`) are specified, the compiled Cedar policy scopes to those specific
resources:

```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools"],
  resource in [Tool::"list_issues"]
);
```

### 3.5 The controller validates compiled policies against a Cedar schema

The enterprise controller must validate all compiled Cedar policies against a
Cedar schema before injecting them into MCPServer resources. This catches typos
in entity types, invalid action references, and type mismatches that could
cause policies to be silently skipped (Cedar's skip-on-error semantics mean a
malformed policy is ignored, not rejected).

### 3.6 The group_claim field is the only new OSS config surface

Adding `group_claim` to the `ConfigOptions` struct is the **only** change to
the OSS Cedar authorizer's config surface. The field is optional — when absent,
no group extraction happens and the principal has no parents (current behavior).

Implementation path in the OSS repo:
1. Add `GroupClaim string` to `ConfigOptions`
2. Store it in the `Authorizer` struct
3. In `CreateEntitiesForRequest`, extract groups from `claimsMap[groupClaim]`
   with dot-notation support for nested claims
4. Build `Group` entities and set them as principal's `Parents`
5. Merge with static entities from `entities_json`
6. Set `MCP` parent on resource entities when `serverName` is provided

## 4. Security Invariants

### 4.1 Default deny

Cedar's default behavior is deny-unless-explicitly-permitted. The platform
authorization does not change this. If no policy matches a request, it is
denied.

### 4.2 Forbid takes precedence

Cedar evaluates `forbid` policies before `permit`. A `forbid` always wins.
The platform authorization CRDs can generate both `permit` (from `bindings`)
and `forbid` (from `deny`) policies. Hand-written Cedar policies can also
be used for advanced denial rules.

### 4.3 No credential material in CRDs or Cedar artifacts

The CRDs and compiled Cedar policies/entities must never contain secrets,
tokens, or other credential material. Group names and role names are
non-sensitive metadata.

### 4.4 The OSS authorizer must not trust claim values blindly

Group claim values from the JWT build entity parents, but the Cedar policy is
the authority. A user claiming membership in group "admin-team" only gets admin
access if the entities.json maps `Group::"admin-team"` as a parent of
`Role::"admin"`. Unmapped groups produce Group entities with no Role parents —
they grant nothing.

### 4.5 Permit accumulation is an inherent property

In a permit-union model, adding more `ToolhiveAuthorizationPolicy` resources
targeting the same MCPServer can only **increase** the set of authorized
actions. No `permit` policy can reduce access granted by another `permit`.

This is an inherent property of Cedar's evaluation model, not a bug. It means:
- Removing a policy is always safe (can only decrease access)
- Adding a policy requires review (can only increase access)
- Narrowing requires `forbid`, not `permit` removal

**Mitigations**:
- The controller emits effective permission summaries in policy status
- The controller emits warning events when a new policy broadens access on a
  server that already has policies
- Administrators use the `deny` field for explicit denials

### 4.6 CRD creation is a privileged operation

Anyone who can create `ToolhiveAuthorizationPolicy` or `ToolhiveRoleBinding`
CRDs can grant access to any MCPServer or map IdP groups to platform roles.
This is a privilege escalation vector if Kubernetes RBAC is not properly
configured.

**This is an inherent truth of any policy-as-CRD system** (Istio, OPA
Gatekeeper, Kyverno all have the same property). It must be documented clearly
in the operator deployment guide.

**Mitigations**:
- Kubernetes RBAC must restrict `create`/`update`/`delete` on authorization
  CRDs to authorized platform administrators
- The `ToolhiveRoleBinding` CRD is equally sensitive — it maps IdP groups to
  platform roles and must be similarly restricted
- Consider a validating admission webhook in future that verifies the policy
  author has authority to grant the referenced roles on the referenced servers

### 4.7 Entity identifiers must be normalized

Cedar entity IDs derived from tool names, group names, and role names must be
consistently normalized. Unnormalized identifiers can bypass `forbid` rules
(e.g., `Tool::"Delete_Repo"` vs `Tool::"delete_repo"` are different entities
in Cedar). The enterprise controller must normalize all entity IDs, and the
OSS authorizer must apply the same normalization to runtime entity IDs.

## 5. Compatibility Invariants

### 5.1 Backwards compatible with existing Cedar configs

MCPServers that use hand-written Cedar policies (without the enterprise
controller) continue to work exactly as before. The `group_claim` field is
optional — when absent, no group extraction happens. The `MCP` parent on
resource entities is only set when a server name is provided.

### 5.2 No breaking changes to the Authorizer interface

The existing `Authorizer` interface in `pkg/authz/authorizers/core.go` must not
change signature. Group extraction, parent population, and server scoping
happen inside the Cedar authorizer's implementation.

### 5.3 The Identity.Groups field is not a dependency

`Identity.Groups` exists but is not populated today. The platform authorization
populates it when `group_claim` is set, but no code path outside the
authorization system should depend on it being populated.

### 5.4 Future multi-claim support is non-breaking

The MVP uses a single `group_claim` (string) field. If multiple claim sources
are needed later, a `group_claims` (plural, string list) field can be added
alongside. The OSS authorizer checks `group_claims` first, falls back to
`group_claim`. No existing config breaks.

### 5.5 Future targetSelector is non-breaking

The MVP uses `targetRef` (by name) only. Adding `targetSelector` (by label)
later is a non-breaking additive CRD field change. The two fields would be
mutually exclusive (CEL validation rule). Existing `targetRef`-only policies
continue to validate unchanged.
