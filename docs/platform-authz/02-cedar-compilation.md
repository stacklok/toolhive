# Cedar Compilation

This document describes how the enterprise authorization controller compiles
CRDs into Cedar policies and entities. It covers the entity hierarchy, policy
shapes, the compilation algorithm, and the Cedar schema.

Refer to [00-invariants.md](00-invariants.md) for the design constraints that
govern all decisions here.

### CRD naming convention

Platform authorization CRDs use the `Toolhive` prefix (`ToolhiveAuthorizationPolicy`,
`ToolhiveRoleBinding`, `ToolhivePlatformRole`). This distinguishes them from OSS
`MCP`-prefixed CRDs which are per-server resources. The `Toolhive` prefix signals
that these are platform-wide concepts that live in the closed-source enterprise
repo under the `toolhive.stacklok.dev` API group.

## 1. Entity Type System

The authorization system uses seven Cedar entity types organized into two
independent hierarchies: a **principal hierarchy** for access control and a
**resource hierarchy** for server scoping.

### Principal Hierarchy

```
Client (dynamic, per-request from JWT)
  └─ parent: Group (dynamic, from JWT group claim)
                └─ parent: Role (static, from entities.json)
```

| Type | Source | Lifecycle | Purpose |
|------|--------|-----------|---------|
| `Client` | JWT `sub` claim | Created per-request | The authenticated user |
| `Group` | JWT group claim (name from `group_claim` config) | Created per-request; Role parents from static entities | IdP group membership |
| `Role` | `ToolhivePlatformRole` CRD or built-in | Static in entities.json | Platform role with action set |

### Resource Hierarchy

```
Tool / Prompt / Resource (dynamic, per-request from MCP method)
  └─ parent: MCP (static or from server name at authorizer init)
```

| Type | Source | Lifecycle | Purpose |
|------|--------|-----------|---------|
| `MCP` | MCPServer name | Static in entities.json | Server container for scoping |
| `Tool` | MCP `tools/call` or `tools/list` request | Created per-request | An MCP tool |
| `Prompt` | MCP `prompts/get` or `prompts/list` request | Created per-request | An MCP prompt |
| `Resource` | MCP `resources/read` or `resources/list` request | Created per-request | An MCP resource |

### Actions

Actions are referenced by UID in policies. They do not need entities in the
store because we use explicit action lists, not action groups.

| Action UID | MCP Method |
|------------|------------|
| `Action::"call_tool"` | `tools/call` |
| `Action::"list_tools"` | `tools/list` |
| `Action::"get_prompt"` | `prompts/get` |
| `Action::"list_prompts"` | `prompts/list` |
| `Action::"read_resource"` | `resources/read` |
| `Action::"list_resources"` | `resources/list` |

## 2. Static Entity Generation (entities.json)

The enterprise controller generates entities.json during reconciliation. This
JSON is stored in the ConfigMap alongside the compiled policies.

### What goes into entities.json

| Entity | Source CRD | When generated |
|--------|-----------|----------------|
| `Role` entities | `ToolhivePlatformRole` (custom) or built-in constants | On role create/update/delete |
| `Group` entities with Role parents | `ToolhiveRoleBinding` | On binding create/update/delete |
| `MCP` server entity | Target MCPServer name | On policy create targeting this server |

### Example

Given these CRDs:

```yaml
# Built-in roles (no CRD needed)
# reader: [list_tools, list_prompts, list_resources, get_prompt, read_resource, call_tool(readOnly)]
# writer: [*]

# Binding
kind: ToolhiveRoleBinding
spec:
  bindings:
    - platformRole: writer
      from:
        - groups: [platform-eng]
        - groups: [sre-team]
    - platformRole: reader
      from:
        - groups: [all-developers]
```

The controller generates:

```json
[
  {"uid": {"type": "MCP", "id": "github"}, "attrs": {}, "parents": []},
  {"uid": {"type": "Role", "id": "writer"}, "attrs": {}, "parents": []},
  {"uid": {"type": "Role", "id": "reader"}, "attrs": {}, "parents": []},
  {"uid": {"type": "Group", "id": "platform-eng"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]},
  {"uid": {"type": "Group", "id": "sre-team"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]},
  {"uid": {"type": "Group", "id": "all-developers"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "reader"}
  ]}
]
```

### Multi-role groups

A group can map to multiple roles. The `parents` array supports multiple
entries:

```json
{"uid": {"type": "Group", "id": "security-team"}, "attrs": {}, "parents": [
  {"type": "Role", "id": "reader"},
  {"type": "Role", "id": "writer"}
]}
```

Cedar's `in` operator traverses the full DAG. A user in `security-team`
matches both `principal in Role::"reader"` and `principal in Role::"writer"`.

### Missing groups degrade gracefully

If a user's JWT contains a group that has no corresponding `Group` entity in
entities.json, Cedar does not error. The hierarchy traversal stops at that
branch — the group simply contributes no role membership. Other valid group
paths still work. This is safe: unmapped groups grant nothing.

The enterprise controller should log a warning when it detects this condition
during status reconciliation.

## 3. Dynamic Entity Generation (per-request)

The OSS Cedar authorizer builds these entities at request time:

### Client entity

Built from the JWT. Parents are the user's groups, extracted from the claim
named by `group_claim`.

```json
{
  "uid": {"type": "Client", "id": "alice@acme.com"},
  "attrs": {
    "claim_sub": "alice@acme.com",
    "claim_email": "alice@acme.com"
  },
  "parents": [
    {"type": "Group", "id": "platform-eng"},
    {"type": "Group", "id": "sre-team"}
  ]
}
```

### Resource entity (Tool, Prompt, Resource)

Built from the MCP request. Parent is the MCP server entity (from the
authorizer's stored server name).

```json
{
  "uid": {"type": "Tool", "id": "list_issues"},
  "attrs": {
    "name": "list_issues",
    "readOnlyHint": true
  },
  "parents": [{"type": "MCP", "id": "github"}]
}
```

The `readOnlyHint` attribute is populated from cached tool annotation data.
When the annotation is unavailable, the attribute is omitted (not set to
false). Policies must use a `has` guard before accessing it (see section 4).

**Cold-start behavior**: If a tool is called before its annotations are
cached, `readOnlyHint` is absent and the reader's `call_tool` policy does
not match — the call is denied. In normal MCP flows, `tools/list` is called
before `tools/call`, which populates the cache. This is a known MVP
limitation; pre-populating the cache at authorizer init can be added later
if it causes friction.

### Entity merging

The authorizer merges static entities (from entities.json, loaded at init)
with dynamic entities (created per-request) into a single `EntityMap`. The
existing merge logic in `core.go` (lines 293-308) already handles this:
dynamic entities override static entities with the same UID.

## 4. Policy Compilation Shapes

The enterprise controller compiles `ToolhiveAuthorizationPolicy` CRDs into
Cedar policies. Each policy shape follows specific Cedar syntax rules.

### Policy annotations

Every compiled policy carries an `@id` annotation for traceability. The
format is `{policy-crd-name}/{role}` for grants and
`{policy-crd-name}/deny/{index}` for deny rules. When the reader role splits
into two policies, the call_tool policy is suffixed with `/readOnly`.

```cedar
@id("github-access/writer")
permit(
  principal in Role::"writer",
  action in [...],
  resource in MCP::"github"
);

@id("github-access/reader")
permit(
  principal in Role::"reader",
  action in [...],
  resource in MCP::"github"
);

@id("github-access/reader/readOnly")
permit(
  principal in Role::"reader",
  action == Action::"call_tool",
  resource in MCP::"github"
) when { resource has readOnlyHint && resource.readOnlyHint == true };

@id("github-access/deny/0")
forbid(
  principal,
  action == Action::"call_tool",
  resource == Tool::"delete_repo"
) when { resource in MCP::"github" };
```

These annotations appear in Cedar evaluation diagnostics, making it possible
to trace a permit or deny decision back to the source CRD and binding.

### Cedar syntax constraints

These constraints affect the compiled policy shapes:

1. **Resource scope head**: `resource in <entity>` accepts only a **single
   entity** in the policy head (scope), not a set. Sets must go in a `when`
   clause.
2. **Action scope head**: `action in [...]` accepts a **set of entities** in
   the policy head. This is an exception — only actions support set literals
   in the head.
3. **Optional attributes**: Accessing an optional attribute (like
   `readOnlyHint`) without a `has` guard causes a Cedar evaluation error.
   Due to skip-on-error semantics, the policy is silently skipped. Always
   use `resource has attr && resource.attr == value`.

### Shape 1: Unrestricted grant (server-scoped)

**CRD input:**
```yaml
spec:
  targetRef: {name: github}
  bindings:
    - platformRole: writer
```

**Compiled Cedar:**
```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools",
             Action::"get_prompt", Action::"list_prompts",
             Action::"read_resource", Action::"list_resources"],
  resource in MCP::"github"
);
```

The action list is the role's action set. For `writer` (built-in, `*`), all
six actions are expanded.

### Shape 2: Restricted grant (typed resource names)

Restrictions are **typed** in the CRD — `tools`, `prompts`, and `resources`
are separate fields. This is required because each Cedar action only applies
to its matching resource type (e.g., `call_tool` applies to `Tool` and `MCP`,
not to `Prompt`). Using untyped resource names would produce policies that
fail Cedar schema validation.

The compiler emits **one policy per resource type**, each containing only the
actions compatible with that type.

**CRD input (tools only — common case):**
```yaml
spec:
  targetRef: {name: github}
  bindings:
    - platformRole: writer
      ruleRestrictions:
        - tools: [list_issues, create_pr]
```

**Compiled Cedar:**
```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools"],
  resource
) when {
  resource in [Tool::"list_issues", Tool::"create_pr"]
};
```

Only `call_tool` and `list_tools` are emitted — these are the actions whose
schema `resourceTypes` includes `Tool`.

**CRD input (single tool):**
```yaml
ruleRestrictions:
  - tools: [create_pr]
```

```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools"],
  resource == Tool::"create_pr"
);
```

When there is exactly one name, use `resource == Tool::"name"` in the head
(simpler, no `when` clause).

**CRD input (mixed types):**
```yaml
spec:
  targetRef: {name: github}
  bindings:
    - platformRole: writer
      ruleRestrictions:
        - tools: [create_pr]
        - prompts: [code_review]
        - resources: [config.yaml]
```

**Compiled Cedar (one policy per type):**
```cedar
permit(
  principal in Role::"writer",
  action in [Action::"call_tool", Action::"list_tools"],
  resource == Tool::"create_pr"
);

permit(
  principal in Role::"writer",
  action in [Action::"get_prompt", Action::"list_prompts"],
  resource == Prompt::"code_review"
);

permit(
  principal in Role::"writer",
  action in [Action::"read_resource", Action::"list_resources"],
  resource == Resource::"config.yaml"
);
```

### Action-to-resource-type mapping

The compiler uses this mapping to select actions per resource type:

| Resource type | Compatible actions |
|---------------|-------------------|
| `Tool` | `call_tool`, `list_tools` |
| `Prompt` | `get_prompt`, `list_prompts` |
| `Resource` | `read_resource`, `list_resources` |
| `MCP` | all (server-scoped grants only) |

When a role's action set does not include any compatible action for a
restricted type, no policy is emitted for that type. For example, a custom
role with only `[call_tool, list_tools]` restricted to `prompts: [x]` emits
nothing — the role has no prompt actions.

### Shape 3: Reader role (readOnlyHint)

**CRD input:**
```yaml
spec:
  targetRef: {name: github}
  bindings:
    - platformRole: reader
```

**Compiled Cedar:**
```cedar
// Reader can list and browse everything on the server
permit(
  principal in Role::"reader",
  action in [Action::"list_tools", Action::"list_prompts",
             Action::"list_resources", Action::"get_prompt",
             Action::"read_resource"],
  resource in MCP::"github"
);

// Reader can call tools that are read-only
permit(
  principal in Role::"reader",
  action == Action::"call_tool",
  resource in MCP::"github"
) when {
  resource has readOnlyHint && resource.readOnlyHint == true
};
```

The reader role compiles to **two** policies: one for list/get/read actions
(unrestricted within the server), and one for `call_tool` gated on
`readOnlyHint`. This is because `readOnlyHint` only applies to tool calls,
not to list/get/read operations.

### Shape 4: Deny rule

Deny rules use the same typed resource fields as grants (`tools`, `prompts`,
`resources`). They are always **scoped to the target server** via a `when`
clause to prevent cross-server interference.

**CRD input:**
```yaml
spec:
  targetRef: {name: github}
  deny:
    - actions: [call_tool]
      tools: [delete_repo, force_push]
```

**Compiled Cedar (single resource):**
```cedar
forbid(
  principal,
  action == Action::"call_tool",
  resource == Tool::"delete_repo"
) when { resource in MCP::"github" };
```

**Compiled Cedar (multiple resources):**
```cedar
forbid(
  principal,
  action == Action::"call_tool",
  resource
) when {
  resource in MCP::"github" &&
  resource in [Tool::"delete_repo", Tool::"force_push"]
};
```

Deny rules compile to `forbid` policies. They are **unscoped on principal**
(apply to everyone) and scoped to the target server and specific resources.
Cedar's `forbid` always overrides `permit`.

Server scoping is critical: without it, a deny on `Tool::"delete_repo"` would
block that tool name on every MCPServer, not just the one referenced by
`targetRef`.

**Deny with multiple actions (typed):**
```yaml
deny:
  - actions: [call_tool]
    tools: [dangerous_tool]
  - actions: [get_prompt]
    prompts: [dangerous_prompt]
```

```cedar
forbid(
  principal,
  action == Action::"call_tool",
  resource == Tool::"dangerous_tool"
) when { resource in MCP::"github" };

forbid(
  principal,
  action == Action::"get_prompt",
  resource == Prompt::"dangerous_prompt"
) when { resource in MCP::"github" };
```

### Shape 5: Custom role

**CRD input:**
```yaml
kind: ToolhivePlatformRole
metadata:
  name: security-auditor
spec:
  actions: [list_tools, list_prompts, list_resources, call_tool]
```

```yaml
spec:
  targetRef: {name: github}
  bindings:
    - platformRole: security-auditor
```

**Compiled Cedar:**
```cedar
permit(
  principal in Role::"security-auditor",
  action in [Action::"list_tools", Action::"list_prompts",
             Action::"list_resources", Action::"call_tool"],
  resource in MCP::"github"
);
```

Custom roles map their action list directly to Cedar action UIDs. No special
handling — the action strings in the CRD are the Cedar action IDs.

## 5. Compilation Algorithm

The enterprise controller runs this algorithm when reconciling authorization
CRDs for a given MCPServer:

### Step 1: Collect inputs

For a target MCPServer `S`:
1. Find all `ToolhiveAuthorizationPolicy` resources with `targetRef.name == S`
2. For each policy, resolve the referenced `platformRole` to its action set
   (built-in constant or `ToolhivePlatformRole` CRD)
3. Resolve all `ToolhiveRoleBinding` resources to build the Group→Role mapping
4. Read the IdP claim configuration from the platform ConfigMap

### Step 2: Generate entities.json

```
entities = []

# MCP server entity
entities.append(MCP entity for server S)

# Role entities (deduplicated across all policies)
for each unique role referenced by any policy targeting S:
    entities.append(Role entity)

# Group entities with Role parents (from all bindings)
for each ToolhiveRoleBinding:
    for each binding entry:
        role = binding.platformRole
        for each group in binding.from.groups:
            if Group entity already exists:
                add Role as additional parent
            else:
                create Group entity with Role parent

# Group entities from role bindings (from all bindings)
for each ToolhiveRoleBinding:
    for each binding entry:
        role = binding.platformRole
        for each role_name in binding.from.roles:
            # IdP roles are treated as groups for entity hierarchy
            if Group entity already exists:
                add Role as additional parent
            else:
                create Group entity with Role parent
```

Note: both `from.groups` and `from.roles` in the RoleBinding create `Group`
entities in Cedar. The distinction between IdP "groups" and IdP "roles" is
which JWT claim they come from (configured in the IdP ConfigMap), not how
they are represented in Cedar.

### Step 3: Generate policies

```
# Action-to-resource-type compatibility
ACTIONS_FOR_TYPE = {
    "Tool":     ["call_tool", "list_tools"],
    "Prompt":   ["get_prompt", "list_prompts"],
    "Resource": ["read_resource", "list_resources"],
}

policies = []

for each ToolhiveAuthorizationPolicy targeting S:
    # Grants
    for each binding in policy.bindings:
        role = resolve(binding.platformRole)
        actions = expand_actions(role)  # '*' expands to all 6

        if role is "reader":
            # Split into two policies
            non_call_actions = actions - [call_tool]
            policies.append(permit(Role, non_call_actions, MCP::S))
            policies.append(permit(Role, [call_tool], MCP::S,
                                   when: readOnlyHint))
        else if binding has ruleRestrictions:
            for each restriction:
                # Emit one policy per typed field (tools, prompts, resources)
                for each (type, names) in restriction.typed_fields():
                    compatible = intersect(actions, ACTIONS_FOR_TYPE[type])
                    if len(compatible) == 0:
                        continue  # role has no actions for this type
                    if len(names) == 1:
                        policies.append(permit(Role, compatible,
                                               resource == Type::names[0]))
                    else:
                        policies.append(permit(Role, compatible,
                                               when: resource in names))
        else:
            policies.append(permit(Role, actions, MCP::S))

    # Denials (always server-scoped via targetRef)
    for each deny_rule in policy.deny:
        deny_actions = deny_rule.actions  # direct, no expansion
        for each (type, names) in deny_rule.typed_fields():
            compatible = intersect(deny_actions, ACTIONS_FOR_TYPE[type])
            if len(compatible) == 0:
                continue
            if len(names) == 1:
                policies.append(forbid(compatible,
                                       resource == Type::names[0],
                                       when: resource in MCP::S))
            else:
                policies.append(forbid(compatible,
                                       when: resource in MCP::S &&
                                             resource in names))
```

### Step 4: Validate

1. Validate all generated policies against the Cedar schema (section 7)
2. Check for duplicate or conflicting policies (warn, don't fail)

**Validation failure handling**: If schema validation fails, the controller:
1. Sets `Compiled: False` condition on the `ToolhiveAuthorizationPolicy` with
   the validation error message
2. Emits a Kubernetes event with the error details
3. **Does not write the ConfigMap** — the previous valid version stays in
   place, preserving the last known-good policy at runtime

A bad policy update never reaches the OSS authorizer. The admin must fix the
CRD before it takes effect.

**Entity ID normalization**: Entity IDs are **not normalized**. They are
stored and compared exactly as specified in CRDs (for static entities) and
exactly as received from JWT claims (for dynamic entities). Cedar entity IDs
are case-sensitive — if a `ToolhiveRoleBinding` specifies group `Platform-Eng`
but the IdP sends `platform-eng` in the JWT, they will not match. This is
intentional: normalization would hide misconfiguration. The controller should
validate that group names in bindings match the IdP's actual group names and
surface mismatches via status conditions.

### Step 5: Write ConfigMap

Assemble into the ConfigMap format:

```json
{
  "version": "1.0",
  "type": "cedarv1",
  "cedar": {
    "policies": ["permit(...);", "permit(...);", "forbid(...);"],
    "entities_json": "[{...}, {...}]",
    "group_claim": "roles"
  }
}
```

Write ConfigMap `{server-name}-enterprise-authz`. SSA-patch the MCPServer's
`spec.authzConfig` to reference it.

## 6. Policy Assembly for Multi-Policy Servers

When multiple `ToolhiveAuthorizationPolicy` resources target the same
MCPServer, the controller aggregates them into a single ConfigMap.

### Assembly rules

1. **Policies are unioned**: all `permit` and `forbid` statements from all
   policies go into a single policy list. Cedar evaluation is
   **order-independent** — all policies are evaluated and the result is the
   union of permits minus any forbids. Array position in the ConfigMap does
   not affect authorization outcomes.
2. **Entities are merged**: Group entities may gain additional Role parents
   from different bindings. The controller deduplicates by UID and merges
   parent sets.
3. **Role entities are deduplicated**: if both Policy A and Policy B reference
   the `writer` role, only one `Role::"writer"` entity is emitted
4. **MCP entity is singular**: one `MCP` entity per server, regardless of how
   many policies target it

### Deduplication and redundancy

Policies are compared by their Cedar text after normalization. Identical
policies are emitted only once. This can happen when two policies reference
the same role on the same server without restrictions.

**Redundant policies** (where a restricted grant is strictly subsumed by an
unrestricted grant for the same role on the same server) are not removed.
Cedar unions all permits, so redundancy is harmless — it does not broaden
access. The controller emits a warning event to alert the admin:

```
Warning  RedundantPolicy  ToolhiveAuthorizationPolicy/github-restricted
  Grant for 'writer' restricted to [create_pr] is subsumed by unrestricted
  grant in ToolhiveAuthorizationPolicy/github-access on MCPServer 'github'
```

### Status reporting

The controller writes the effective permission set into each
`ToolhiveAuthorizationPolicy`'s status:

```yaml
status:
  conditions:
    - type: Compiled
      status: "True"
  effectivePermissions:
    - role: writer
      actions: [call_tool, list_tools, get_prompt, ...]
      scope: "MCP::github"
    - role: reader
      actions: [list_tools, list_prompts, ..., "call_tool (readOnlyHint)"]
      scope: "MCP::github"
  policyCount: 3
  denyCount: 1
```

When a new policy broadens access on a server that already has policies, the
controller emits a Kubernetes event warning:

```
Warning  AccessBroadened  ToolhiveAuthorizationPolicy/new-policy
  Policy adds 'writer' role to MCPServer 'github' which already has policies
```

## 7. Cedar Schema

The schema validates all compiled policies and entity relationships. The
enterprise controller validates against this schema before writing the
ConfigMap.

```json
{
  "": {
    "entityTypes": {
      "MCP": {
        "memberOfTypes": [],
        "shape": {
          "type": "Record",
          "attributes": {}
        }
      },
      "Role": {
        "memberOfTypes": [],
        "shape": {
          "type": "Record",
          "attributes": {}
        }
      },
      "Group": {
        "memberOfTypes": ["Role"],
        "shape": {
          "type": "Record",
          "attributes": {}
        }
      },
      "Client": {
        "memberOfTypes": ["Group"],
        "shape": {
          "type": "Record",
          "attributes": {
            "claim_sub": {
              "type": "String"
            },
            "claim_email": {
              "type": "String",
              "required": false
            }
          }
        }
      },
      "Tool": {
        "memberOfTypes": ["MCP"],
        "shape": {
          "type": "Record",
          "attributes": {
            "name": {
              "type": "String"
            },
            "readOnlyHint": {
              "type": "Boolean",
              "required": false
            }
          }
        }
      },
      "Prompt": {
        "memberOfTypes": ["MCP"],
        "shape": {
          "type": "Record",
          "attributes": {
            "name": {
              "type": "String"
            }
          }
        }
      },
      "Resource": {
        "memberOfTypes": ["MCP"],
        "shape": {
          "type": "Record",
          "attributes": {
            "name": {
              "type": "String"
            }
          }
        }
      }
    },
    "actions": {
      "call_tool": {
        "appliesTo": {
          "principalTypes": ["Client"],
          "resourceTypes": ["Tool", "MCP"]
        }
      },
      "list_tools": {
        "appliesTo": {
          "principalTypes": ["Client"],
          "resourceTypes": ["Tool", "MCP"]
        }
      },
      "get_prompt": {
        "appliesTo": {
          "principalTypes": ["Client"],
          "resourceTypes": ["Prompt", "MCP"]
        }
      },
      "list_prompts": {
        "appliesTo": {
          "principalTypes": ["Client"],
          "resourceTypes": ["Prompt", "MCP"]
        }
      },
      "read_resource": {
        "appliesTo": {
          "principalTypes": ["Client"],
          "resourceTypes": ["Resource", "MCP"]
        }
      },
      "list_resources": {
        "appliesTo": {
          "principalTypes": ["Client"],
          "resourceTypes": ["Resource", "MCP"]
        }
      }
    }
  }
}
```

### Schema design notes

- **Default namespace `""`**: The schema uses the Cedar default (empty)
  namespace. This avoids collision with the `MCP` entity type name and
  matches the OSS Cedar authorizer's behavior, which uses unqualified entity
  type references like `MCP::"github"` (type=`MCP`, id=`github`). The
  namespace is a schema-only concept — end users never see it since all
  Cedar policies are generated by the enterprise controller.

- **`principalTypes` lists only `Client`**: The requesting principal is always
  a `Client`. Cedar's schema validator understands that `principal in
  Role::"writer"` is valid because `Client` → `Group` → `Role` is declared
  via `memberOfTypes`.

- **`resourceTypes` includes both leaf type AND `MCP`**: For `call_tool`, the
  resource can be a specific `Tool` (restricted grant) or the container
  `MCP` (server-scoped grant). Both must be listed for schema validation to
  pass on both policy shapes.

- **`readOnlyHint` is `required: false`**: Not all tools have this annotation.
  Policies accessing it must use `resource has readOnlyHint && ...`.

- **`Client` attributes are minimal**: Only `claim_sub` is required. Other
  claims are available as attributes but not declared in the schema to avoid
  over-constraining. The existing Cedar authorizer adds all claims with a
  `claim_` prefix.

## 8. Built-in Role Definitions

Built-in roles are constants in the controller code. They do not require
`ToolhivePlatformRole` CRDs.

### reader

```go
var ReaderActions = []string{
    "list_tools", "list_prompts", "list_resources",
    "get_prompt", "read_resource",
    "call_tool",  // gated on readOnlyHint in compiled policy
}
```

Compiles to two policies (see Shape 3 in section 4).

### writer

```go
var WriterActions = []string{
    "call_tool", "list_tools",
    "get_prompt", "list_prompts",
    "read_resource", "list_resources",
}
```

Equivalent to `*`. Compiles to a single server-scoped permit.

## 9. End-to-End Example

### Input CRDs

```yaml
---
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
---
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-restricted
spec:
  targetRef:
    kind: MCPServer
    name: github
  bindings:
    - platformRole: writer
      ruleRestrictions:
        - tools: [create_pr]
```

### Compiled ConfigMap `github-enterprise-authz`

```json
{
  "version": "1.0",
  "type": "cedarv1",
  "cedar": {
    "policies": [
      "permit(principal in Role::\"writer\", action in [Action::\"call_tool\", Action::\"list_tools\", Action::\"get_prompt\", Action::\"list_prompts\", Action::\"read_resource\", Action::\"list_resources\"], resource in MCP::\"github\");",

      "permit(principal in Role::\"reader\", action in [Action::\"list_tools\", Action::\"list_prompts\", Action::\"list_resources\", Action::\"get_prompt\", Action::\"read_resource\"], resource in MCP::\"github\");",

      "permit(principal in Role::\"reader\", action == Action::\"call_tool\", resource in MCP::\"github\") when { resource has readOnlyHint && resource.readOnlyHint == true };",

      "forbid(principal, action == Action::\"call_tool\", resource == Tool::\"delete_repo\") when { resource in MCP::\"github\" };",

      "permit(principal in Role::\"writer\", action in [Action::\"call_tool\", Action::\"list_tools\"], resource == Tool::\"create_pr\");"
    ],
    "entities_json": "[{\"uid\":{\"type\":\"MCP\",\"id\":\"github\"},\"attrs\":{},\"parents\":[]},{\"uid\":{\"type\":\"Role\",\"id\":\"writer\"},\"attrs\":{},\"parents\":[]},{\"uid\":{\"type\":\"Role\",\"id\":\"reader\"},\"attrs\":{},\"parents\":[]},{\"uid\":{\"type\":\"Group\",\"id\":\"platform-eng\"},\"attrs\":{},\"parents\":[{\"type\":\"Role\",\"id\":\"writer\"}]},{\"uid\":{\"type\":\"Group\",\"id\":\"all-developers\"},\"attrs\":{},\"parents\":[{\"type\":\"Role\",\"id\":\"reader\"}]}]",
    "group_claim": "groups"
  }
}
```

Note the key changes from the untyped design:
- Policy 4 (forbid): now includes `when { resource in MCP::"github" }` for
  server scoping
- Policy 5 (restricted grant): only `call_tool` and `list_tools` actions
  (compatible with `Tool` type), not all 6

### Runtime evaluation

User `alice@acme.com` (groups: `[platform-eng]`) calls `tools/call` with
tool `create_pr` on the `github` server:

1. OSS authorizer extracts groups from JWT claim `groups` → `[platform-eng]`
2. Builds `Client::"alice@acme.com"` with parent `Group::"platform-eng"`
3. Builds `Tool::"create_pr"` with parent `MCP::"github"`
4. Merges with static entities (Role, Group, MCP from entities.json)
5. Cedar evaluates:
   - Policy 1 (writer, all actions, MCP::github): `Client in Role::"writer"`?
     → Client → Group::"platform-eng" → Role::"writer" → **yes**
     → `Tool::"create_pr" in MCP::"github"`? → **yes** → **PERMIT**
   - Policy 4 (forbid delete_repo): resource is `create_pr`, not `delete_repo`
     → **no match**
6. Result: **PERMIT**

User `bob@acme.com` (groups: `[all-developers]`) calls `tools/call` with
tool `delete_repo` on the `github` server:

1. Builds `Client::"bob@acme.com"` with parent `Group::"all-developers"`
2. Builds `Tool::"delete_repo"` with parent `MCP::"github"`
3. Cedar evaluates:
   - Policy 1 (writer): `Client in Role::"writer"`? → Client →
     Group::"all-developers" → Role::"reader" (not writer) → **no match**
   - Policy 2 (reader, non-call): action is `call_tool`, not in non-call set
     → **no match**
   - Policy 3 (reader, call_tool with readOnlyHint): `Client in
     Role::"reader"`? → **yes**. `readOnlyHint == true`? → assume
     `delete_repo` has `readOnlyHint: false` → **no match**
   - Policy 4 (forbid delete_repo): `resource == Tool::"delete_repo"`? → **yes**,
     `resource in MCP::"github"`? → **yes** → **FORBID** (overrides any permit)
4. Result: **DENY**
