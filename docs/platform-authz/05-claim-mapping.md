# Claim Mapping

This document describes how JWT claims from Identity Providers are extracted
and mapped to Cedar authorization entities at runtime. It covers IdP-specific
claim formats, the extraction algorithm, AND condition evaluation, and security
considerations.

Refer to [00-invariants.md](00-invariants.md) for constraints 1.6, 1.7, 3.6,
[01-crds.md](01-crds.md) for PrincipalCondition semantics, and
[04-oss-changes.md](04-oss-changes.md) for the extraction implementation.

## 1. IdP Claim Formats

There is no standard JWT claim name for group membership. Every IdP uses a
different name, nesting convention, and value format. The table below captures
the production-relevant patterns.

| IdP | Claim path | Format | Notes |
|-----|-----------|--------|-------|
| Keycloak (realm roles) | `realm_access.roles` | `[]string` | 2-level nesting; dot-notation required |
| Keycloak (client roles) | `resource_access.<client-id>.roles` | `[]string` | 3-level nesting; client ID varies per deployment |
| Keycloak (groups) | `groups` | `[]string` | Requires custom "Group Membership" mapper; values may include leading `/` |
| Okta | `groups` | `[]string` | Custom claim, name configurable in auth server; not included by default |
| Entra ID (groups) | `groups` | `[]string` | **GUIDs** by default, not display names; overage at >200 groups (see 7.3) |
| Entra ID (app roles) | `roles` | `[]string` | App role display names from manifest; no overage limit |
| Auth0 | `https://<namespace>/roles` | `[]string` | Namespace-prefixed URL; dots in claim name (see 3.3) |
| Generic OIDC | varies | `[]string` | No standard; `groups` is a de facto convention |

### Keycloak JWT example

Keycloak tokens can carry groups/roles in three distinct locations:

```json
{
  "sub": "f33d74d4-a5ed-4da0-817c-503e75a81465",
  "preferred_username": "jane",
  "email": "jane@example.com",

  "realm_access": {
    "roles": ["platform-admin", "offline_access", "uma_authorization"]
  },

  "resource_access": {
    "toolhive-app": {
      "roles": ["app-admin", "app-viewer"]
    },
    "account": {
      "roles": ["manage-account", "view-profile"]
    }
  },

  "groups": ["platform-eng", "sre-team"]
}
```

- `realm_access.roles`: included by default via the built-in `roles` client
  scope. No mapper configuration needed.
- `resource_access.<clientId>.roles`: also included by default. Client-specific
  roles scoped to individual applications.
- `groups`: **not included by default**. Requires a "Group Membership" protocol
  mapper (type `oidc-group-membership-mapper`) with `full_path = false`. With
  `full_path = true`, values include the path prefix (e.g., `/engineering/platform-eng`).

**Default roles warning**: Keycloak assigns default realm roles
(`offline_access`, `uma_authorization`) to ALL users. Bindings that reference
these roles will match every authenticated user. Only use custom realm roles
in `PrincipalCondition` values. The `full.group.path` mapper property defaults
to `true` in some Keycloak versions — when enabled, group values are
path-prefixed (e.g., `/engineering/platform-eng` instead of `platform-eng`).
Set it to `false` or use the full path in CRD bindings.

**Dot-notation caveat**: Client IDs containing dots (e.g., `com.example.app`)
would break `resource_access.com.example.app.roles` traversal. This is rare
in practice — Keycloak client IDs are typically simple strings. Not addressed
in MVP.

### Entra ID JWT example

```json
{
  "groups": ["87d349ed-44d7-43e1-9a83-5f2406dee5bd"],
  "roles": ["Task.Write", "Task.Read"]
}
```

### Auth0 JWT example

```json
{
  "https://myapp.example.com/roles": ["admin", "editor"]
}
```

## 2. The `group_claim` Configuration

A single string in `ConfigOptions` identifies which JWT claim to extract:

```go
GroupClaim string `json:"group_claim,omitempty"`
```

Despite the name, this field is used for **both** IdP groups and IdP roles.
Both `PrincipalCondition.Groups` and `PrincipalCondition.Roles` (from the
CRD) match against values from this same claim -- in Cedar, both produce
`Group` entities (the distinction is semantic only).

**Implication**: When an IdP uses separate claims for groups and roles (e.g.,
Entra ID `groups` vs `roles`), the administrator must choose one. The
multi-claim extension (section 6) addresses this.

### Dot-notation

The `.` character splits the path into segments for nested claims:

| `group_claim` | Resolves to |
|---------------|-------------|
| `groups` | `claims["groups"]` |
| `realm_access.roles` | `claims["realm_access"]["roles"]` |
| `resource_access.my-app.roles` | `claims["resource_access"]["my-app"]["roles"]` |

## 3. Claim Extraction Algorithm

### 3.1 Core logic

Two functions handle extraction (see [04-oss-changes.md](04-oss-changes.md)
change 4 for full code):

1. `resolveNestedClaim(claims, path)` -- traverses the dot-separated path
2. `extractGroups(claims, groupClaim)` -- resolves the claim, coerces to
   `[]string`

Extraction runs in `AuthorizeWithJWTClaims` **before** `preprocessClaims`
adds the `claim_` prefix, because `groupClaim` matches the raw JWT claim
name.

**Implementation hazard**: `extractGroups` must only produce a `[]string` of
group names for use as `Client` parent UIDs. It must NOT create intermediate
`Group` entities. The static entity store (`entities.json`) already contains
`Group` entities with `Role` parents. The merge order in `core.go` overwrites
static entities with dynamic ones — creating dynamic `Group` entities with
empty parents would destroy the Group-to-Role linkage and cause all role-based
authorization to silently fail. See [04-oss-changes.md](04-oss-changes.md)
change 4 for the corrected approach.

### 3.2 Exact-match-first for Auth0

Auth0 namespace-prefixed claims like `https://myapp.example.com/roles`
contain dots that the naive `strings.Split` would interpret as nesting.
The resolver tries an exact top-level match first:

```go
func resolveNestedClaim(claims jwt.MapClaims, path string) interface{} {
    // Fast path: exact top-level match (handles Auth0 URLs)
    if v, exists := claims[path]; exists {
        return v
    }

    // Slow path: dot-notation traversal (handles Keycloak nesting)
    parts := strings.Split(path, ".")
    if len(parts) == 1 {
        return nil
    }

    var current interface{} = map[string]interface{}(claims)
    for _, part := range parts {
        m, ok := current.(map[string]interface{})
        if !ok {
            return nil
        }
        current = m[part]
    }
    return current
}
```

If a claim name literally matches a top-level key, it wins. Dot-notation
traversal is only attempted when the exact match fails.

**Security note**: The exact-match-first strategy means a top-level claim
named `realm_access.roles` (a literal string with dots) would shadow the
nested `realm_access` -> `roles` path. This is a theoretical claim injection
vector if the IdP allows custom claims with arbitrary names. Mitigations:
(1) OIDC middleware validates the JWT signature, so only the IdP can set
claims; (2) IdP admins control which custom claims are allowed. If this
becomes a concern, a future enhancement could require the administrator to
declare the path format (literal vs nested) explicitly.

### 3.3 Edge cases

| Case | Behavior | Rationale |
|------|----------|-----------|
| Missing claim | Returns `nil`; no parents on Client; default deny | Safe: no silent privilege escalation |
| Single string (not array) | Returns `nil` | No silent coercion; admin must fix IdP config |
| Empty array `[]` | Returns `[]string{}`; no parents | Same as missing -- default deny |
| Nested traversal hits non-object | Returns `nil` | Graceful degradation |
| Numeric array values `[1001, 1002]` | Non-string elements skipped | Coercing `float64` to string is ambiguous; IdP should emit strings |
| Case mismatch (`Platform-Eng` vs `platform-eng`) | No match | Intentional: normalization hides misconfiguration (see 7.1) |

## 4. AND Condition Evaluation

**Single-claim limitation**: Both `PrincipalCondition.Groups` and `.Roles`
are matched against the same `group_claim` JWT claim (see section 2). AND
conditions that span both fields (e.g., `groups: [engineering]` AND
`roles: [team-lead]`) are only satisfiable if both values appear in that
single claim. If your IdP uses separate claims for groups and roles (e.g.,
Entra ID `groups` vs `roles`), cross-field AND conditions will never match
until the `group_claims` (plural) extension is implemented (section 6).

### The problem

A `PrincipalCondition` with both fields means AND:

```yaml
from:
  - groups: [engineering]
    roles: [team-lead]    # user must have BOTH
```

Both `engineering` and `team-lead` produce `Group` entities with the same
`Role` parent. But Cedar's `in` operator follows ANY path -- a user in just
one of these groups already reaches the Role via that single path. The AND
is not enforced by the entity hierarchy alone.

### Solution: Cedar `when` clauses

The enterprise controller compiles each condition into a separate `permit`
policy with a `when` clause that explicitly checks all required group
memberships:

```cedar
@id("acme-roles/writer/cond-0")
permit(
  principal,
  action in [Action::"call_tool", Action::"list_tools",
             Action::"get_prompt", Action::"list_prompts",
             Action::"read_resource", Action::"list_resources"],
  resource in MCP::"github"
) when {
  principal in Group::"engineering" &&
  principal in Group::"team-lead"
};
```

The individual groups get `Group` entities in `entities.json` (for Cedar's
`in` operator to resolve against the Client's parents), but the `when` clause
is the actual gate controlling role assignment.

This approach keeps AND evaluation entirely in compiled Cedar — the OSS
authorizer needs no special logic for AND conditions.

### Why ALL conditions need `when` clauses

Cedar's `in` operator follows ANY path through the entity hierarchy. If
multiple conditions in a binding produce `Group → Role` parent relationships,
a policy without a `when` clause would match any user who has ANY of those
groups — defeating the AND semantics of other conditions.

Example: given these two conditions for the `writer` role:

```yaml
from:
  - groups: [platform-eng]          # simple OR
  - groups: [engineering]           # AND
    roles: [team-lead]
```

All three groups (`platform-eng`, `engineering`, `team-lead`) get `Group`
entities with `Role::"writer"` as a parent. If condition 0 generated a policy
*without* a `when` clause, a user in `engineering` alone would match it (via
`Group::"engineering" → Role::"writer"`) — bypassing the AND gate of
condition 1.

**Rule**: The compiler must generate a per-condition `when` clause for every
condition in a binding. Even simple single-group conditions get an explicit
`when { principal in Group::"X" }` check. This is correct regardless of how
the entity hierarchy maps groups to roles:

```cedar
// Condition 0: simple (one group) — still needs when clause
@id("acme-roles/writer/cond-0")
permit(
  principal,
  action in [...],
  resource in MCP::"github"
) when {
  principal in Group::"platform-eng"
};

// Condition 1: AND (engineering AND team-lead)
@id("acme-roles/writer/cond-1")
permit(
  principal,
  action in [...],
  resource in MCP::"github"
) when {
  principal in Group::"engineering" &&
  principal in Group::"team-lead"
};
```

Now a user in `engineering` alone fails both conditions. A user in
`platform-eng` matches condition 0. A user in both `engineering` and
`team-lead` matches condition 1. Correct DNF evaluation.

### Multiple values within a field

Multiple values within a single field also use AND:

```yaml
from:
  - groups: [engineering, security]   # must be in BOTH
```

Compiles identically:

```cedar
permit(...) when {
  principal in Group::"engineering" &&
  principal in Group::"security"
};
```

### OR semantics (the list level)

The `from` list uses OR — each condition is a separate Cedar `permit` policy.
DNF (Disjunctive Normal Form) falls out naturally from Cedar's
union-of-permits.

### Graceful degradation for missing groups

Cedar's `in` operator was verified in the cedar-go v1.5.2 source
(`internal/eval/evalers.go`, `entityInOne`): when a parent UID references an
entity that does not exist in the store, the branch is silently skipped — no
error, no permission granted. Other valid paths still work. A JWT group with
no matching static `Group` entity contributes no role membership.

## 5. IdP Configuration Examples

### Keycloak (groups — recommended)

Requires a Group Membership mapper with `full_path = false`.

```yaml
group_claim: "groups"
```

```yaml
spec:
  bindings:
    - platformRole: writer
      from:
        - groups: [platform-eng]    # matches group name exactly
```

### Keycloak (realm roles — no mapper needed)

```yaml
group_claim: "realm_access.roles"
```

```yaml
spec:
  bindings:
    - platformRole: writer
      from:
        - groups: [platform-admin]  # matches realm role name exactly
```

### Keycloak (client roles — app-scoped)

```yaml
group_claim: "resource_access.toolhive-app.roles"
```

```yaml
spec:
  bindings:
    - platformRole: writer
      from:
        - groups: [app-admin]
```

### Okta

```yaml
group_claim: "groups"
```

```yaml
spec:
  bindings:
    - platformRole: writer
      from:
        - groups: [Engineering]     # case-sensitive match to Okta group name
```

### Entra ID (application roles, preferred over GUIDs)

```yaml
group_claim: "roles"
```

```yaml
spec:
  bindings:
    - platformRole: writer
      from:
        - roles: [Task.Write]
    - platformRole: reader
      from:
        - roles: [Task.Read]
```

### Auth0

```yaml
group_claim: "https://myapp.example.com/roles"
```

```yaml
spec:
  bindings:
    - platformRole: writer
      from:
        - roles: [admin]
```

## 6. Multi-Claim Future (`group_claims`)

### Motivation

Several scenarios require multiple JWT claims simultaneously:
- Entra ID: groups from `groups`, roles from `roles`
- Keycloak: realm roles from `realm_access.roles`, client roles from
  `resource_access.<id>.roles`
- AND conditions across claims (section 4)

### Proposed extension

Add `GroupClaims` (plural) alongside `GroupClaim` (singular):

```go
GroupClaims []string `json:"group_claims,omitempty"`
```

The authorizer checks `GroupClaims` first. If present, all listed claims are
extracted and their values unioned (deduplicated) into a single group set.
If absent, fall back to `GroupClaim`. No existing config breaks.

```json
{
  "cedar": {
    "group_claim": "groups",
    "group_claims": ["groups", "roles"]
  }
}
```

### Typed claim routing (deferred further)

A more precise approach would map `PrincipalCondition.Groups` to one claim
and `PrincipalCondition.Roles` to another. This adds config complexity with
marginal benefit over the union approach. Deferred unless the union proves
insufficient.

## 7. Security Considerations

### 7.1 Case sensitivity

Entity IDs are NOT normalized (see [02-cedar-compilation.md](02-cedar-compilation.md)
step 4). `Group::"Admins"` and `Group::"admins"` are different Cedar entities.
This is intentional: normalization hides misconfiguration. The CRD must match
the IdP's exact casing. The enterprise controller should surface mismatches
via status conditions.

### 7.2 Claim freshness

JWTs are self-contained -- claims are set at issuance and do not update when
group membership changes. This is inherent to JWT-based authorization, not
specific to the `group_claim` mechanism.

Mitigations: short token lifetimes (5-15 minutes), enforced token refresh,
and emergency `forbid` rules targeting specific users.

### 7.3 Entra ID group overage

When a user belongs to >200 groups, Entra ID replaces the `groups` claim
with a Graph API reference (`_claim_names`/`_claim_sources`). The extraction
function finds no array and returns `nil` -- the user is silently denied.

**MVP**: Not handled. Administrators should use Entra ID application roles
(`roles` claim, no overage limit) or configure a groups filter to keep the
count below 200.

**Future**: A Graph API callout to resolve overage, requiring a service
principal credential.

### 7.4 Unmapped groups are inert

Groups from the JWT that have no matching `Group` entity in `entities.json`
contribute no role membership. Cedar's hierarchy traversal stops at the
leaf -- unmapped groups grant nothing. Access requires both the claim (runtime)
AND the entity mapping (compile-time).

### 7.5 Empty `group_claim` is safe

When not configured, `extractGroups` returns `nil`. No parents on the
`Client` entity. Under default-deny, no access unless policies explicitly
permit the specific Client UID. This is backward-compatible with existing
deployments.

### 7.6 Token size with large group/role sets

Keycloak and most OIDC providers have no overage mechanism — all groups are
included in the JWT. Large group counts inflate token size and can exceed HTTP
header limits (typically 8KB). Mitigations:
- Use a Keycloak Group Membership mapper with a filter to include only
  authorization-relevant groups
- Prefer realm roles (typically fewer) over groups
- Use short-lived access tokens with refresh token rotation
- For Entra ID, prefer application roles (`roles` claim) over groups to avoid
  the 200-group overage threshold

## 8. Diagnostic Guidance

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| User denied despite correct group | `group_claim` not set in ConfigMap | Add `group_claim` field |
| User denied with `group_claim` set | Claim name mismatch | Decode JWT (jwt.io), verify claim name |
| User denied with correct claim | Case mismatch | Match CRD group name to IdP exactly |
| Keycloak users all denied | Missing dot-notation | Use `realm_access.roles` not `roles` |
| Entra ID users with many groups denied | Group overage (>200) | Use `roles` claim or filter groups |
| Auth0 users all denied | Dot-notation splitting URL | Verify `group_claim` is exact claim name |
| Some groups work, others do not | Non-string values in array | Check IdP for numeric group IDs |
| User in exactly one group always denied | IdP returns single string instead of array | Configure IdP to return array format |
| Keycloak groups not matching despite correct claim | Group mapper `full.group.path = true` | Set to `false` or use full path in CRD |
| Keycloak realm roles missing from token | `roles` client scope removed from client | Re-add `roles` to Default Client Scopes |
| Keycloak client roles missing | Wrong client ID in `group_claim` | Verify client ID matches `azp` claim in JWT |
| AND condition never matches | Groups and roles from different JWT claims | Both must appear in single `group_claim`; see section 6 |
| AND condition compiled but user denied | `when` clause missing in compiled Cedar | Check compiled policies for `when` clause |

Enable Cedar debug logging (`TOOLHIVE_LOG_LEVEL=debug`) to see the principal
entity and its parents in authorization decisions.
