# CRD Definitions

This document defines the three enterprise authorization CRDs: their Go types,
field semantics, validation rules, and status reporting. These CRDs live in the
closed-source enterprise repo under the `toolhive.stacklok.dev` API group.

Refer to [00-invariants.md](00-invariants.md) for the design constraints that
govern all decisions here, and [02-cedar-compilation.md](02-cedar-compilation.md)
for how these CRDs compile to Cedar policies and entities.

## 1. Overview

| CRD | Responsibility | Short names |
|-----|---------------|-------------|
| `ToolhivePlatformRole` | Defines WHAT a role can do (flat action list) | `tpr`, `platformrole` |
| `ToolhiveRoleBinding` | Maps WHO (IdP groups/roles) to WHICH platform roles | `trb`, `thvrolebinding` |
| `ToolhiveAuthorizationPolicy` | Binds roles to specific MCPServers, with optional restrictions and denials | `tap`, `authzpolicy` |

`ToolhivePlatformRole` and `ToolhiveRoleBinding` are **cluster-scoped** — they
define platform-wide concepts (what a role can do, which IdP groups map to
which roles) that apply uniformly across namespaces.
`ToolhiveAuthorizationPolicy` is **namespace-scoped** — it targets a specific
MCPServer by name within the same namespace. This follows the Kubernetes RBAC
pattern: `ClusterRole`/`ClusterRoleBinding` are cluster-scoped while
`RoleBinding` is namespace-scoped.

### Naming convention

Platform authorization CRDs use the `Toolhive` prefix (`ToolhivePlatformRole`,
`ToolhiveRoleBinding`, `ToolhiveAuthorizationPolicy`). This distinguishes them
from OSS `MCP`-prefixed CRDs which are per-server resources. The `Toolhive`
prefix signals that these are platform-wide concepts.

## 2. ToolhivePlatformRole

Defines the maximum set of actions a role permits. The system ships two
default roles (`reader`, `writer`) as Helm-managed CRD instances (see
section 7). Custom roles use the same CRD.

### Go types

```go
// Action constants define the valid actions for platform roles.
// Each action maps 1:1 to a Cedar action.
const (
    ActionCallTool      = "call_tool"
    ActionListTools     = "list_tools"
    ActionGetPrompt     = "get_prompt"
    ActionListPrompts   = "list_prompts"
    ActionReadResource  = "read_resource"
    ActionListResources = "list_resources"
    ActionWildcard      = "*"
)

// PlatformAction is a single MCP action string.
// +kubebuilder:validation:Enum=call_tool;list_tools;get_prompt;list_prompts;read_resource;list_resources;*
type PlatformAction string

// ToolhivePlatformRoleSpec defines the desired state of ToolhivePlatformRole.
// A platform role defines the maximum set of actions a principal may perform.
// Policies can restrict but never exceed the role's action set.
//
// +kubebuilder:validation:XValidation:rule="!(self.actions.exists(a, a == '*') && self.actions.size() > 1)",message="wildcard '*' must be the only action when present"
type ToolhivePlatformRoleSpec struct {
    // Actions is the flat list of MCP actions this role permits.
    // The wildcard '*' grants all actions and must be the sole entry when used.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=7
    Actions []PlatformAction `json:"actions"`

    // Description provides a human-readable description of what this role permits
    // +optional
    Description string `json:"description,omitempty"`

    // ToolHintFilter, when set, gates call_tool on MCP tool annotation hints.
    // Each non-nil field adds a condition: the tool must have the annotation
    // set to the specified value. Multiple fields are ANDed.
    // Only meaningful when actions includes call_tool.
    // +optional
    ToolHintFilter *ToolHintFilter `json:"toolHintFilter,omitempty"`
}

// ToolHintFilter gates call_tool on MCP tool annotation hints.
// Each field corresponds to an MCP tool annotation. When set (non-nil),
// the compiler emits a Cedar `when` clause requiring the tool's annotation
// to match the specified value. Nil fields are ignored (no filtering).
type ToolHintFilter struct {
    // ReadOnlyHint, when non-nil, gates call_tool on the tool's readOnlyHint
    // annotation. Set to true to restrict to read-only tools.
    // +optional
    ReadOnlyHint *bool `json:"readOnlyHint,omitempty"`

    // DestructiveHint, when non-nil, gates call_tool on the tool's
    // destructiveHint annotation. Set to false to exclude destructive tools.
    // Note: MCP defaults destructiveHint to true when absent.
    // +optional
    DestructiveHint *bool `json:"destructiveHint,omitempty"`

    // IdempotentHint, when non-nil, gates call_tool on the tool's
    // idempotentHint annotation. Set to true to restrict to idempotent tools.
    // +optional
    IdempotentHint *bool `json:"idempotentHint,omitempty"`

    // OpenWorldHint, when non-nil, gates call_tool on the tool's
    // openWorldHint annotation. Set to false to restrict to closed-world tools.
    // +optional
    OpenWorldHint *bool `json:"openWorldHint,omitempty"`
}

// ToolhivePlatformRoleStatus defines the observed state of ToolhivePlatformRole
type ToolhivePlatformRoleStatus struct {
    // Conditions represent the latest available observations of the role's state
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // ObservedGeneration is the most recent generation observed for this resource
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`

    // ReferencingPolicies lists ToolhiveAuthorizationPolicy resources that
    // reference this role. Helps track which policies need recompilation
    // when the role changes.
    // +optional
    ReferencingPolicies []string `json:"referencingPolicies,omitempty"`
}
```

### Root type and markers

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=tpr;platformrole
// +kubebuilder:printcolumn:name="Actions",type="string",JSONPath=".spec.actions",description="Permitted actions"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//
type ToolhivePlatformRole struct {
    metav1.TypeMeta   `json:",inline"` // nolint:revive
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   ToolhivePlatformRoleSpec   `json:"spec,omitempty"`
    Status ToolhivePlatformRoleStatus `json:"status,omitempty"`
}
```

### Validation rules

| Rule | Layer | Rationale |
|------|-------|-----------|
| Action values from enum `[call_tool, list_tools, get_prompt, list_prompts, read_resource, list_resources, *]` | kubebuilder Enum on `PlatformAction` type | Static set, catches typos at admission |
| `*` must be the only action when present | CEL on spec | `["*", "call_tool"]` is ambiguous |
| At least 1 action, at most 7 | kubebuilder MinItems/MaxItems | 6 concrete actions + `*` |
| `toolHintFilter` only with `call_tool` | Controller-time warning | Hint filters only meaningful when actions includes `call_tool` |

### Example

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhivePlatformRole
metadata:
  name: security-auditor
spec:
  description: "Can list and call tools, but cannot access prompts or resources"
  actions:
    - call_tool
    - list_tools
```

### Default role instances

The `reader` and `writer` roles are shipped as Helm-managed
`ToolhivePlatformRole` CRD instances. They follow the same resolution path as
custom roles — no special-case code in the compiler.

```yaml
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhivePlatformRole
metadata:
  name: reader
  annotations:
    toolhive.stacklok.dev/built-in: "true"
spec:
  description: "Read-only access; call_tool restricted to readOnlyHint tools"
  actions:
    - list_tools
    - list_prompts
    - list_resources
    - get_prompt
    - read_resource
    - call_tool
  toolHintFilter:
    readOnlyHint: true
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhivePlatformRole
metadata:
  name: writer
  annotations:
    toolhive.stacklok.dev/built-in: "true"
spec:
  description: "Full access to all MCP operations"
  actions: ["*"]
```

The `toolhive.stacklok.dev/built-in: "true"` annotation marks these as
system-provided defaults. The controller does not treat this annotation
specially — it is for documentation and tooling. Deletion protection comes
from Helm ownership: if the roles are accidentally deleted, the next Helm
sync (via ArgoCD, Flux, or manual `helm upgrade`) re-creates them.

### kubectl output

```
$ kubectl get tpr
NAME               ACTIONS                                                              AGE
reader             [list_tools,list_prompts,list_resources,get_prompt,read_resource,…]   1h
writer             [*]                                                                   1h
security-auditor   [call_tool,list_tools]                                                5m
```

## 3. ToolhiveRoleBinding

Maps IdP groups and roles to platform roles, creating Cedar Group-to-Role
parent relationships in the entity hierarchy. The `from` field is a list of
conditions (OR semantics); within each condition, all fields must match (AND
semantics). This gives administrators DNF (Disjunctive Normal Form) matching:
any single condition matching is sufficient, but all fields within that
condition must be satisfied. Both `groups` and `roles` create Cedar `Group`
entities — the distinction is semantic (which JWT claim they come from at
runtime).

### Go types

```go
// ToolhiveRoleBindingSpec defines the desired state of ToolhiveRoleBinding.
// A role binding maps IdP principals (groups, roles) to platform roles,
// creating the Cedar Group-to-Role parent relationships in the entity hierarchy.
type ToolhiveRoleBindingSpec struct {
    // Bindings maps IdP principals (groups, roles) to platform roles.
    // Each entry creates Cedar Group entities with the specified platform role
    // as a parent.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinItems=1
    Bindings []RoleBindingEntry `json:"bindings"`
}

// RoleBindingEntry maps a set of IdP principals to a single platform role.
type RoleBindingEntry struct {
    // PlatformRole is the name of the platform role to assign.
    // References a ToolhivePlatformRole by name (including the default
    // reader and writer roles shipped as Helm-managed instances).
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=253
    // +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
    PlatformRole string `json:"platformRole"`

    // From defines the IdP principal conditions that qualify for this role.
    // The list has OR semantics: if ANY condition matches, the role is assigned.
    // Within each condition, all specified fields must match (AND semantics).
    // This gives DNF (Disjunctive Normal Form) matching.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinItems=1
    From []PrincipalCondition `json:"from"`
}

// PrincipalCondition defines a single matching condition for IdP principals.
// All specified fields within a condition must match (AND semantics).
// At least one of groups or roles must be specified.
//
// Example: {groups: ["engineering"], roles: ["team-lead"]} means the user
// must have "engineering" in the groups_claim AND "team-lead" in the
// roles_claim of their JWT token.
//
// Each field maps to a different JWT claim:
//   - Groups → matched against groups_claim from the platform IdP ConfigMap
//   - Roles  → matched against roles_claim from the platform IdP ConfigMap
//
// Both produce Cedar Group entities. The distinction is the JWT claim source,
// not the Cedar entity type. When roles_claim is not configured, values in
// the Roles field will never match (safe default-deny); the controller emits
// a warning in this case.
//
// +kubebuilder:validation:XValidation:rule="(has(self.groups) && self.groups.size() > 0) || (has(self.roles) && self.roles.size() > 0)",message="at least one of groups or roles must be non-empty"
type PrincipalCondition struct {
    // Groups is a list of IdP group names that the user must belong to.
    // When multiple groups are specified, the user must be in ALL of them (AND).
    // Values are matched against the JWT claim identified by groups_claim
    // in the platform IdP ConfigMap. Group names are case-sensitive.
    // +optional
    Groups []string `json:"groups,omitempty"`

    // Roles is a list of IdP role names that the user must have.
    // When multiple roles are specified, the user must have ALL of them (AND).
    // Values are matched against the JWT claim identified by roles_claim
    // in the platform IdP ConfigMap. In Cedar, IdP roles are represented as
    // Group entities (same entity type as groups — the distinction is the
    // JWT claim source, not the Cedar type).
    // +optional
    Roles []string `json:"roles,omitempty"`
}

// ToolhiveRoleBindingStatus defines the observed state of ToolhiveRoleBinding
type ToolhiveRoleBindingStatus struct {
    // Conditions represent the latest available observations of the binding's state
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // ObservedGeneration is the most recent generation observed for this resource
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`

    // ResolvedRoles lists the platform roles resolved from this binding.
    // Includes both default and custom roles.
    // +optional
    ResolvedRoles []string `json:"resolvedRoles,omitempty"`

    // GroupCount is the total number of unique IdP groups/roles mapped
    // +optional
    GroupCount int `json:"groupCount,omitempty"`
}
```

### Root type and markers

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=trb;thvrolebinding
// +kubebuilder:printcolumn:name="Roles",type="string",JSONPath=".status.resolvedRoles",description="Resolved platform roles"
// +kubebuilder:printcolumn:name="Groups",type="integer",JSONPath=".status.groupCount",description="Mapped group count"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type ToolhiveRoleBinding struct {
    metav1.TypeMeta   `json:",inline"` // nolint:revive
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   ToolhiveRoleBindingSpec   `json:"spec,omitempty"`
    Status ToolhiveRoleBindingStatus `json:"status,omitempty"`
}
```

### Validation rules

| Rule | Layer | Rationale |
|------|-------|-----------|
| At least one binding entry | kubebuilder MinItems | Empty binding is meaningless |
| `platformRole` matches DNS subdomain pattern | kubebuilder Pattern | Syntactic validity (no spaces, special chars) |
| At least one condition in `from` | kubebuilder MinItems | Empty condition list is meaningless |
| At least one of `groups` or `roles` in each condition must be non-empty | CEL on `PrincipalCondition` | Empty condition is meaningless; `has()` alone is true for `[]` |
| `platformRole` references an existing role | Controller-time | CEL cannot do cross-resource lookups |

**Role resolution is controller-time**: The controller resolves `platformRole`
by looking up the `ToolhivePlatformRole` CRD by name during compilation. If
the role does not exist, the controller sets `RolesResolved: False` with a
message like `"unknown platform role: security-auditor"`. This allows applying
the binding before the role CRD exists (Kubernetes eventual consistency).

### Example

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveRoleBinding
metadata:
  name: acme-roles
spec:
  bindings:
    # Simple: anyone in platform-eng OR sre-team gets writer
    - platformRole: writer
      from:
        - groups: [platform-eng]
        - groups: [sre-team]
    # AND condition: must be in engineering group AND have team-lead role
    - platformRole: writer
      from:
        - groups: [engineering]
          roles: [team-lead]  # AND: groups from groups_claim, roles from roles_claim
    # Simple: all developers get reader
    - platformRole: reader
      from:
        - groups: [all-developers]
    # IdP role claim only
    - platformRole: security-auditor
      from:
        - roles: [security-analyst]
```

The `from` list uses OR semantics — any matching condition assigns the role.
Within each condition, all fields use AND semantics — the user must satisfy
all specified groups and roles simultaneously.

### Cedar entity generation

This binding creates the following entities in `entities.json`:

```json
[
  {"uid": {"type": "Group", "id": "platform-eng"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]},
  {"uid": {"type": "Group", "id": "sre-team"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]},
  {"uid": {"type": "Group", "id": "engineering"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]},
  {"uid": {"type": "Group", "id": "team-lead"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "writer"}
  ]},
  {"uid": {"type": "Group", "id": "all-developers"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "reader"}
  ]},
  {"uid": {"type": "Group", "id": "security-analyst"}, "attrs": {}, "parents": [
    {"type": "Role", "id": "security-auditor"}
  ]}
]
```

Both `groups` and `roles` produce `Group` entities in Cedar (same entity
type; the distinction is the JWT claim source — `groups_claim` vs
`roles_claim`). A group can appear in multiple bindings and gain multiple
Role parents (multi-role groups, see 02-cedar-compilation.md section 2).

**AND condition compilation**: When a condition has both `groups` and `roles`,
the controller must ensure ALL specified values are present in the user's
JWT claims for the role assignment to apply. This is enforced at request time
during entity hierarchy construction — the Cedar `Client` entity only gets a
`Group` parent if the claim is present in the token, and the Cedar `permit`
uses `principal in Role::"X"` which requires the transitive `in` relationship
through all the groups in the condition. See
[02-cedar-compilation.md](02-cedar-compilation.md) for the compilation
algorithm.

### kubectl output

```
$ kubectl get trb
NAME         ROLES                              GROUPS   AGE
acme-roles   [writer,reader,security-auditor]   5        3m
```

## 4. ToolhiveAuthorizationPolicy

Binds platform roles to a specific MCPServer, optionally with typed resource
restrictions and explicit deny rules. Multiple policies targeting the same
server are unioned into a single Cedar policy set.

### Go types

```go
// ToolhiveAuthorizationPolicySpec defines the desired state of
// ToolhiveAuthorizationPolicy. A policy binds platform roles to a specific
// MCPServer with optional restrictions and denials.
//
// +kubebuilder:validation:XValidation:rule="(has(self.bindings) && self.bindings.size() > 0) || (has(self.deny) && self.deny.size() > 0) || (has(self.rawPolicies) && self.rawPolicies.size() > 0)",message="at least one of bindings, deny, or rawPolicies must be non-empty"
type ToolhiveAuthorizationPolicySpec struct {
    // TargetRef identifies the MCPServer this policy applies to.
    // The referenced MCPServer must be in the same namespace.
    // +kubebuilder:validation:Required
    TargetRef PolicyTargetRef `json:"targetRef"`

    // Bindings maps platform roles to this server, optionally with restrictions.
    // Each binding compiles to one or more Cedar permit statements.
    // +optional
    Bindings []PolicyBinding `json:"bindings,omitempty"`

    // Deny defines explicit denial rules that compile to Cedar forbid statements.
    // Deny rules always override permits (Cedar semantics).
    // Typed deny rules (with tool/prompt/resource names) omit server scoping
    // to ensure fail-closed behavior. Server-wide deny rules (no resource
    // names) use server scoping via resource in MCP::"<server>".
    // +optional
    Deny []DenyRule `json:"deny,omitempty"`

    // RawPolicies contains literal Cedar policy strings. Each string must be a
    // valid complete Cedar policy statement (permit or forbid). The controller
    // validates each string against the Cedar schema before writing the
    // ConfigMap — validation failure sets Compiled: False and preserves the
    // last known-good policy. The controller injects an @id annotation
    // (rawPolicies/0, rawPolicies/1, ...) into each policy for traceability.
    // Use this field to migrate existing hand-written Cedar policies or express
    // authorization patterns the CRD abstraction cannot represent.
    // +optional
    // +kubebuilder:validation:MaxItems=10
    RawPolicies []string `json:"rawPolicies,omitempty"`
}
```

### PolicyTargetRef

```go
// PolicyTargetRef identifies the target MCPServer by name within the same
// namespace. Label-based targetSelector may be added as a non-breaking
// additive field in the future (see invariant 5.5).
type PolicyTargetRef struct {
    // Kind is the resource kind. Currently only MCPServer is supported.
    // +kubebuilder:validation:Enum=MCPServer
    // +kubebuilder:default=MCPServer
    // +optional
    Kind string `json:"kind,omitempty"`

    // Name is the name of the MCPServer in the same namespace
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=253
    Name string `json:"name"`
}
```

The `kind` field defaults to `MCPServer` and currently only permits that value.
This follows the Istio `targetRef` pattern and allows future expansion to
`VirtualMCPServer` without breaking changes.

### PolicyBinding

```go
// PolicyBinding maps a platform role to the target server, optionally with
// typed resource restrictions.
type PolicyBinding struct {
    // PlatformRole is the name of the platform role to grant.
    // References a ToolhivePlatformRole by name (including the default
    // reader and writer roles shipped as Helm-managed instances).
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=253
    // +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
    PlatformRole string `json:"platformRole"`

    // RuleRestrictions optionally narrows the role's permissions to specific
    // typed resources. When omitted, the role's full action set applies to
    // the entire server. Each entry is additive (union): multiple entries
    // expand the set of permitted resources. Within a single entry, the
    // tools, prompts, and resources fields are also independent — they
    // produce separate Cedar policies, one per resource type.
    // +optional
    RuleRestrictions []TypedResourceRestriction `json:"ruleRestrictions,omitempty"`
}
```

### TypedResourceRestriction

```go
// TypedResourceRestriction narrows a role's permissions to specific resources.
// At least one of tools, prompts, or resources must be specified.
//
// Fields are typed because each Cedar action only applies to its matching
// resource type: call_tool/list_tools apply to Tool, get_prompt/list_prompts
// apply to Prompt, read_resource/list_resources apply to Resource. Using
// untyped resource names would produce policies that fail Cedar schema
// validation (see 02-cedar-compilation.md section 4).
//
// +kubebuilder:validation:XValidation:rule="(has(self.tools) && self.tools.size() > 0) || (has(self.prompts) && self.prompts.size() > 0) || (has(self.resources) && self.resources.size() > 0)",message="at least one of tools, prompts, or resources must be non-empty"
type TypedResourceRestriction struct {
    // Tools restricts to specific tool names. Generates Cedar policies with
    // call_tool and list_tools actions against Tool entities.
    // Names are exact matches against Cedar entity IDs.
    // +optional
    Tools []string `json:"tools,omitempty"`

    // Prompts restricts to specific prompt names. Generates Cedar policies with
    // get_prompt and list_prompts actions against Prompt entities.
    // Names are exact matches against Cedar entity IDs.
    // +optional
    Prompts []string `json:"prompts,omitempty"`

    // Resources restricts to specific resource names. Generates Cedar policies
    // with read_resource and list_resources actions against Resource entities.
    // Names are exact matches against Cedar entity IDs.
    // +optional
    Resources []string `json:"resources,omitempty"`
}
```

### DenyRule

```go
// DenyAction is a single MCP action string for deny rules.
// The wildcard '*' is not permitted in deny rules; actions must be explicit.
// +kubebuilder:validation:Enum=call_tool;list_tools;get_prompt;list_prompts;read_resource;list_resources
type DenyAction string

// DenyRule defines an explicit denial that compiles to a Cedar forbid statement.
// Deny rules always override permits (Cedar semantics) and are always scoped
// to the target server to prevent cross-server interference.
//
// When typed resource fields (tools, prompts, resources) are omitted, the deny
// applies to ALL resources of all types on the target server for the specified
// actions. This is a powerful pattern for server-wide denials (e.g., making a
// server completely read-only by denying call_tool without specifying tools).
type DenyRule struct {
    // Actions is the list of actions to deny.
    // The wildcard '*' is NOT permitted; actions must be explicit to ensure
    // deny rules are auditable and intentional.
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinItems=1
    Actions []DenyAction `json:"actions"`

    // Tools lists tool names to deny the specified actions on.
    // Only meaningful when actions includes call_tool or list_tools.
    // The compiler silently skips incompatible action-type combinations
    // and emits a warning event.
    // +optional
    Tools []string `json:"tools,omitempty"`

    // Prompts lists prompt names to deny the specified actions on.
    // Only meaningful when actions includes get_prompt or list_prompts.
    // +optional
    Prompts []string `json:"prompts,omitempty"`

    // Resources lists resource names to deny the specified actions on.
    // Only meaningful when actions includes read_resource or list_resources.
    // +optional
    Resources []string `json:"resources,omitempty"`
}
```

**No `*` in deny actions**: The `DenyAction` enum intentionally omits `*`.
Deny rules must be explicit about which actions to deny. This prevents
accidental blanket denials and makes deny rules auditable. A `forbid` with
`actions: [*]` and no resource names would deny everything on the server —
while technically valid, it's too dangerous for a single CRD field to express.

**Deny without resource names**: When all typed fields are omitted, the deny
applies to all resources on the target server for the specified actions. The
compiled Cedar uses only server scoping:

```cedar
forbid(
  principal,
  action == Action::"call_tool",
  resource
) when { resource in MCP::"github" };
```

**Action-type compatibility in deny rules**: If a deny specifies
`actions: [call_tool]` with `prompts: [x]`, the compiler finds no compatible
actions for `Prompt` type (`call_tool` applies to `Tool`, not `Prompt`) and
emits nothing for that combination. The controller emits a warning event:
`"deny rule 0: actions [call_tool] have no effect on prompts; skipped"`.
This is handled at compile-time, not CRD validation, because encoding the
action-type compatibility matrix in CEL would be fragile.

### Status

```go
// EffectivePermission represents the resolved permissions for a role on a
// target server. Written to policy status for administrator visibility.
type EffectivePermission struct {
    // Role is the platform role name
    Role string `json:"role"`

    // Actions is the list of permitted actions for this role.
    // For the reader role, call_tool is annotated with "(readOnlyHint)" to
    // indicate it is gated on the tool's readOnlyHint attribute.
    Actions []string `json:"actions"`

    // Scope is the Cedar resource scope (e.g., "MCP::github")
    Scope string `json:"scope"`
}

// ToolhiveAuthorizationPolicyStatus defines the observed state of
// ToolhiveAuthorizationPolicy
type ToolhiveAuthorizationPolicyStatus struct {
    // Conditions represent the latest available observations of the policy's state.
    // The Compiled condition indicates whether Cedar policies were compiled
    // and injected successfully.
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // ObservedGeneration is the most recent generation observed for this resource
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`

    // EffectivePermissions shows the resolved permission set per role on
    // the target server. Makes permit accumulation visible to administrators.
    // +optional
    EffectivePermissions []EffectivePermission `json:"effectivePermissions,omitempty"`

    // PolicyCount is the number of Cedar permit policies compiled from this resource
    // +optional
    PolicyCount int `json:"policyCount,omitempty"`

    // DenyCount is the number of Cedar forbid policies compiled from this resource
    // +optional
    DenyCount int `json:"denyCount,omitempty"`

    // TargetServer is the resolved MCPServer name (from spec.targetRef.name)
    // +optional
    TargetServer string `json:"targetServer,omitempty"`
}
```

### Root type and markers

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tap;authzpolicy
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.targetRef.name",description="Target MCPServer"
// +kubebuilder:printcolumn:name="Compiled",type="string",JSONPath=".status.conditions[?(@.type=='Compiled')].status",description="Compilation status"
// +kubebuilder:printcolumn:name="Policies",type="integer",JSONPath=".status.policyCount",description="Permit policy count"
// +kubebuilder:printcolumn:name="Denies",type="integer",JSONPath=".status.denyCount",description="Forbid policy count"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type ToolhiveAuthorizationPolicy struct {
    metav1.TypeMeta   `json:",inline"` // nolint:revive
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   ToolhiveAuthorizationPolicySpec   `json:"spec,omitempty"`
    Status ToolhiveAuthorizationPolicyStatus `json:"status,omitempty"`
}
```

### Validation rules

| Rule | Layer | Rationale |
|------|-------|-----------|
| At least one of `bindings`, `deny`, or `rawPolicies` must be non-empty | CEL on spec | Empty policy is meaningless; `has()` alone is true for `[]` |
| `rawPolicies` at most 10 entries | kubebuilder MaxItems | Escape hatch, not a bulk import mechanism |
| Each `rawPolicies` entry is a valid Cedar policy | Controller-time (Cedar schema validation) | Prevents malformed policies reaching the authorizer; failed validation sets `Compiled: False` |
| `targetRef.kind` is `MCPServer` | kubebuilder Enum | Only supported target for MVP |
| `targetRef.name` is non-empty | kubebuilder MinLength | Required field |
| `platformRole` matches DNS subdomain pattern | kubebuilder Pattern | Syntactic validity |
| Each `ruleRestrictions` entry has at least one non-empty typed field | CEL on `TypedResourceRestriction` | Empty restriction is ambiguous (see section 5); checks `size() > 0` |
| Deny actions from enum (no `*`) | kubebuilder Enum on `DenyAction` | Deny must be explicit |
| `platformRole` references an existing role | Controller-time | CEL cannot do cross-resource lookups |
| `targetRef.name` references an existing MCPServer | Controller-time | Eventual consistency |
| Action-type compatibility in deny rules | Controller-time + warning | Fragile to encode in CEL |

### Example: Grants and denials

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-access
  namespace: default
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

### Example: Restricted grant

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-restricted
  namespace: default
spec:
  targetRef:
    name: github
  bindings:
    - platformRole: writer
      ruleRestrictions:
        - tools: [create_pr, list_issues]
        - prompts: [code_review]
```

This produces Cedar permits split by item-specific and list actions (list
actions use `MCP::` as the resource, not typed entities):

```cedar
// Item-specific: call_tool restricted to named tools
permit(principal in Role::"writer",
       action == Action::"call_tool",
       resource) when { resource in [Tool::"create_pr", Tool::"list_issues"] };
// List: unrestricted on server
permit(principal in Role::"writer",
       action == Action::"list_tools",
       resource in MCP::"github");

// Item-specific: get_prompt restricted to named prompt
permit(principal in Role::"writer",
       action == Action::"get_prompt",
       resource == Prompt::"code_review");
// List: unrestricted on server
permit(principal in Role::"writer",
       action == Action::"list_prompts",
       resource in MCP::"github");
```

### Example: Deny-only policy (safety rails)

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-safety-rails
  namespace: default
spec:
  targetRef:
    name: github
  deny:
    - actions: [call_tool]
      tools: [delete_repo, force_push, transfer_repo]
```

A deny-only policy (no `bindings`) is valid. It compiles to `forbid` statements
that block specific actions regardless of what other policies permit. This is
the correct pattern for organization-wide safety rails.

### Example: Server-wide read-only

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-readonly
  namespace: default
spec:
  targetRef:
    name: github
  deny:
    - actions: [call_tool]
```

Omitting resource names means "deny this action for all resources on this
server." This makes the server effectively read-only — users can list and
browse but not call any tools.

### kubectl output

```
$ kubectl get tap
NAME                  TARGET   COMPILED   POLICIES   DENIES   AGE
github-access         github   True       3          1        10m
github-restricted     github   True       2          0        5m
github-safety-rails   github   True       0          3        2m
```

### rawPolicies: escape hatch for literal Cedar

The `rawPolicies` field allows administrators to supply literal Cedar policy
strings when the CRD abstraction cannot express the required pattern. This is
the correct mechanism for migrating existing hand-written Cedar policies —
**not** a separate ConfigMap or out-of-band configuration, since the enterprise
controller owns the entire ConfigMap via SSA and overwrites it on every
reconcile.

**Semantics**:
- Each string must be a complete, syntactically valid Cedar policy (one
  `permit(...)` or `forbid(...)` statement)
- The controller validates each string against the Cedar schema before writing
  the ConfigMap; a single invalid policy sets `Compiled: False` on the whole
  resource and preserves the last known-good policy
- The controller injects an `@id` annotation (`rawPolicies/0`, `rawPolicies/1`,
  ...) into each policy for traceability in Cedar evaluation diagnostics
- Raw policies are assembled with CRD-generated policies into a single policy
  set; Cedar's order-independent evaluation applies
- Maximum 10 entries (`MaxItems: 10`) — this is an escape hatch, not a bulk
  import mechanism

**Example: adding a custom role policy**

```yaml
spec:
  targetRef:
    name: github
  bindings:
    - platformRole: writer
  rawPolicies:
    - |
      permit(
        principal in Role::"auditor",
        action == Action::"list_tools",
        resource in MCP::"github"
      );
```

**Policy migration note**: Existing standalone Cedar policies are largely
drop-in compatible with the enterprise entity model. The only construct that
requires rewriting is `resource == FeatureType::"tool"` for list operations —
replace with an action check (`action == Action::"list_tools"`). All other
constructs — action names, `Client` principal references, `Tool`/`Prompt`
resource names, and context fields — are unchanged.

## 5. Design Decisions

### Empty restriction entries are rejected

An empty `ruleRestrictions` entry (`{}` with no `tools`, `prompts`, or
`resources`) is rejected by CEL validation. Two possible interpretations exist:

- **"No restriction"** (equivalent to omitting `ruleRestrictions`) — grants
  access to everything on the server
- **"Empty set"** (grants nothing) — which is what the compiler would produce

Both are surprising. Rejecting eliminates ambiguity. To grant unrestricted
access, omit `ruleRestrictions` entirely. To restrict, explicitly list names.

### Multiple ruleRestrictions entries are unioned

Each `ruleRestrictions` entry is additive (union). Multiple entries expand the
set of permitted resources. Within a single entry, the `tools`, `prompts`, and
`resources` fields are also independent — they produce separate Cedar policies.

This means `ruleRestrictions: [{tools: [a]}, {tools: [b]}]` is equivalent to
`ruleRestrictions: [{tools: [a, b]}]`. Both produce the same Cedar permits.

### Role references are resolved at controller-time

The `platformRole` field in both `ToolhiveRoleBinding` and
`ToolhiveAuthorizationPolicy` is validated syntactically at admission (non-empty,
valid characters) but resolved semantically at controller-time. This allows:

- Applying a binding before the role CRD exists (eventual consistency)
- Clear error feedback via status conditions and events

### Deny actions exclude wildcard

The `DenyAction` type intentionally omits `*` from its enum. Deny rules must
explicitly list each action to deny. A `forbid` with all 6 actions and no
resource names would deny everything on the server — equivalent to removing all
access. This is too dangerous for a single CRD field. Administrators who need
this pattern should create individual deny rules per action, making the intent
explicit and auditable.

### All generated permits must be server-scoped

Every Cedar `permit` emitted by the compilation algorithm must include a
`resource in MCP::"<server-name>"` condition, regardless of whether the binding
has `ruleRestrictions`. While the ConfigMap-per-MCPServer injection pattern
provides implicit runtime isolation (each server only loads its own policies),
defense-in-depth requires that the policies themselves are scoped. This guards
against bugs in ConfigMap injection, manual policy application, or future
architectural changes that might load policies from a shared store.

The `targetRef.name` in `ToolhiveAuthorizationPolicy` provides the server name.
See [02-cedar-compilation.md](02-cedar-compilation.md) for the compilation
algorithm that must enforce this invariant.

### `toolHintFilter` roles and resource restrictions

When a binding references a role with a `toolHintFilter` (e.g., the default
`reader` role with `readOnlyHint: true`) and also specifies
`ruleRestrictions`, the compilation algorithm must intersect the hint filter
conditions with the resource restrictions. Without this intersection, the
restrictions would be silently dropped and the role would grant access to all
matching tools on the server. The compilation algorithm in
[02-cedar-compilation.md](02-cedar-compilation.md) must handle this case
explicitly — see that document for the resolution.

### Deny without resource restrictions means server-wide deny

A deny rule that lists actions but omits `tools`, `prompts`, and `resources`
means "forbid these actions on **all** resources for this server." The compiler
must emit a `forbid` with `resource in MCP::"<server-name>"` and no
resource-name condition. This is the mechanism for safety rails like "no one may
call tools on this server" without enumerating every tool name.

Note: the `TypedResourceRestriction` CEL validation (requiring at least one
non-empty field) applies only to `ruleRestrictions` in bindings, not to deny
rules. Deny rules intentionally allow omitting all resource fields to express
server-wide denials.

### No tool hint filters in deny rules

Deny rules cannot reference tool hint annotations. While "forbid `call_tool`
on all tools where `readOnlyHint=false`" is a valid Cedar pattern, it adds
complexity to the CRD schema and controller for a niche use case.
Administrators who need hint-based denials can use the `rawPolicies` field
to supply literal Cedar `forbid` statements.

### Multi-domain extensibility

The current CRDs are MCP-server-specific (actions, typed resource restrictions,
default roles). The design supports extending to other domains (e.g., MCP
registries) without breaking changes, using this strategy:

- **`ToolhivePlatformRole` and `ToolhiveRoleBinding` are domain-agnostic.**
  They define actions and map IdP groups to roles. A role with actions from
  multiple domains (e.g., `[call_tool, read_catalog]`) is valid — each domain's
  controller only compiles the actions it recognizes.
- **`ToolhiveAuthorizationPolicy.targetRef.kind` is the domain discriminator.**
  Adding a new kind (e.g., `MCPRegistry`) to the enum is a non-breaking
  additive CRD change.
- **Each domain gets its own controller** that watches
  `ToolhiveAuthorizationPolicy` filtered by `targetRef.kind`. Each controller
  owns its Cedar schema, entity types, and compilation algorithm.
- **`PlatformAction` enum grows** as domains are added (union of all domain
  actions). The total is expected to stay under 30 values. If it outgrows the
  enum, relaxing to pattern validation is non-breaking.
- **`ruleRestrictions` typed fields remain MCP-specific** until a second domain
  needs fine-grained resource restrictions. The generalization path is a generic
  `{type, names}` pattern added as a new field alongside the existing typed
  fields.

Extension phases:
1. **Phase 1 (MVP):** MCPServer only. Current design.
2. **Phase 2:** Add `VirtualMCPServer` to `targetRef.kind` enum. Same
   compilation logic (same MCP action space).
3. **Phase 3:** Add non-MCP domain (e.g., `MCPRegistry`). New actions in enum,
   new controller deployment, shared roles and bindings.

## 6. Condition Types and Event Reasons

### Constants

```go
// Condition types for ToolhivePlatformRole
const (
    ConditionTypePlatformRoleValid = "Valid"
)

// Condition reasons for ToolhivePlatformRole
const (
    ConditionReasonPlatformRoleValid          = "RoleValid"
    ConditionReasonPlatformRoleInvalidActions = "InvalidActions"
)

// Condition types for ToolhiveRoleBinding
const (
    ConditionTypeRoleBindingValid         = "Valid"
    ConditionTypeRoleBindingRolesResolved = "RolesResolved"
)

// Condition reasons for ToolhiveRoleBinding
const (
    ConditionReasonRoleBindingValid            = "BindingValid"
    ConditionReasonRoleBindingInvalid          = "BindingInvalid"
    ConditionReasonRoleBindingAllRolesResolved = "AllRolesResolved"
    ConditionReasonRoleBindingRoleNotFound     = "RoleNotFound"
)

// Condition types for ToolhiveAuthorizationPolicy
const (
    ConditionTypeCompiled = "Compiled"
)

// Condition reasons for ToolhiveAuthorizationPolicy
const (
    ConditionReasonCompilationSucceeded = "CompilationSucceeded"
    ConditionReasonCompilationFailed    = "CompilationFailed"
    ConditionReasonTargetNotFound       = "TargetNotFound"
    ConditionReasonRoleNotFound         = "RoleNotFound"
    ConditionReasonValidationFailed     = "ValidationFailed"
)

// Event reasons (emitted on MCPServer for operational visibility)
const (
    EventReasonAccessBroadened    = "AccessBroadened"
    EventReasonRedundantPolicy    = "RedundantPolicy"
    EventReasonConfigMapUpdated   = "ConfigMapUpdated"
    EventReasonAuthzConfigCleared = "AuthzConfigCleared"
    EventReasonAuthzConfigDrift   = "AuthzConfigDrift"
)
```

### Status condition lifecycle

| CRD | Condition | Set when |
|-----|-----------|----------|
| `ToolhivePlatformRole` | `Valid: True` | Actions are valid |
| `ToolhiveRoleBinding` | `Valid: True` | Binding spec is syntactically valid |
| `ToolhiveRoleBinding` | `RolesResolved: True` | All referenced platform roles exist |
| `ToolhiveRoleBinding` | `RolesResolved: False` | One or more roles not found |
| `ToolhiveAuthorizationPolicy` | `Compiled: True` | Cedar policies compiled and injected into ConfigMap |
| `ToolhiveAuthorizationPolicy` | `Compiled: False` | Compilation failed (role not found, validation error, etc.) |

All conditions include `ObservedGeneration` to detect stale status (see
03-controller.md section 7).

## 7. Complete YAML Reference

```yaml
---
# Default roles (Helm-managed)
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhivePlatformRole
metadata:
  name: reader
  annotations:
    toolhive.stacklok.dev/built-in: "true"
spec:
  description: "Read-only access; call_tool restricted to readOnlyHint tools"
  actions: [list_tools, list_prompts, list_resources, get_prompt, read_resource, call_tool]
  toolHintFilter:
    readOnlyHint: true
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhivePlatformRole
metadata:
  name: writer
  annotations:
    toolhive.stacklok.dev/built-in: "true"
spec:
  description: "Full access to all MCP operations"
  actions: ["*"]
---
# Custom role
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhivePlatformRole
metadata:
  name: security-auditor
spec:
  description: "Can list everything and call read-only tools"
  actions:
    - call_tool
    - list_tools
    - list_prompts
    - list_resources
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveRoleBinding
metadata:
  name: acme-roles
spec:
  bindings:
    - platformRole: writer
      from:
        - groups: [platform-eng]
        - groups: [sre-team]
    - platformRole: reader
      from:
        - groups: [all-developers]
    - platformRole: security-auditor
      from:
        - roles: [security-analyst]
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-access
  namespace: default
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
---
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: ToolhiveAuthorizationPolicy
metadata:
  name: github-restricted
  namespace: default
spec:
  targetRef:
    name: github
  bindings:
    - platformRole: security-auditor
      ruleRestrictions:
        - tools: [list_issues, get_issue]
```
