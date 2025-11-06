# PR Inline Review Examples

Common use cases and examples for submitting PR reviews with inline comments.

## Example 1: Simple Inline Review (No Suggestions)

**Use case**: Pointing out issues that require discussion or complex fixes

**Command:**
```bash
gh api -X POST repos/stacklok/toolhive/pulls/2165/reviews --input /tmp/pr-review-comments.json
```

**JSON:**
```json
{
  "body": "Found several architectural concerns that need discussion",
  "event": "COMMENT",
  "comments": [
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 605,
      "body": "This diagram doesn't accurately reflect the actual architecture. The Workload struct only contains metadata, not direct references to Runtime and Transport. These relationships are managed by WorkloadManager and Runner.\n\nWe should discuss how to simplify this while keeping it accurate.\n\nEvidence: pkg/core/workload.go, pkg/workloads/manager.go"
    },
    {
      "path": "pkg/runner/config.go",
      "line": 136,
      "body": "The documentation mentions only 8 fields but RunConfig has 39 serializable fields. Should we document all of them or create a categorized reference?\n\nEvidence: pkg/runner/config.go:32-157"
    }
  ]
}
```

**When to use:**
- Issues require discussion or design decisions
- Changes are too complex for inline suggestions
- Multiple files need coordinated changes
- User needs to provide context or make choices

---

## Example 2: Quick Fixes with Suggestions

**Use case**: Simple corrections that can be committed directly

**JSON:**
```json
{
  "body": "Documentation corrections with suggested fixes",
  "event": "COMMENT",
  "comments": [
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 238,
      "body": "File path reference is incorrect: `pkg/registry/registry.go` does not exist.\n\n```suggestion\n- Registry manager: `pkg/registry/provider.go`\n```\n\nThe registry functionality is split across multiple files in `pkg/registry/`.\n\nEvidence: Verified via codebase exploration"
    },
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 597,
      "body": "File path is incorrect.\n\n```suggestion\n- Health checker: `pkg/healthcheck/healthcheck.go`\n```\n\nEvidence: Verified via codebase exploration"
    },
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 127,
      "body": "Middleware type name is incorrect. The code uses `authorization`, not `authz`.\n\n```suggestion\n7. **Authorization** (`authorization`) - Cedar policy evaluation\n```\n\nEvidence: pkg/authz/middleware.go:211"
    }
  ]
}
```

**When to use:**
- Typos or incorrect file paths
- Simple one-line corrections
- Version numbers or constants
- Formatting fixes

---

## Example 3: Mixed Review (Some with Suggestions, Some Without)

**Use case**: Combination of quick fixes and items needing discussion

**JSON:**
```json
{
  "body": "Documentation review: found quick fixes and items for discussion",
  "event": "COMMENT",
  "comments": [
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 329,
      "body": "Command examples are incorrect:\n\n```suggestion\n- `thv client list-registered` - List all registered clients\n- `thv client setup` - Interactively setup clients\n- `thv client status` - Show installation status\n- `thv client register <client>` - Register a specific client\n- `thv client remove <client>` - Remove a client\n```\n\nEvidence: cmd/thv/app/client.go:36-41"
    },
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 136,
      "body": "The key fields list is incomplete. RunConfig has 39 serializable fields, but only 8 are listed here.\n\nNotable missing fields include: `name`, `cmdArgs`, `secrets`, `oidcConfig`, `authzConfig`, `auditConfig`, `telemetryConfig`, `group`, `toolsFilter`, `toolsOverride`, `isolateNetwork`, `proxyMode`, and many others.\n\nShould we either:\n1. Categorize fields by purpose (Identity, Security, Middleware, etc.), or\n2. Add a reference to the complete list in `05-runconfig-and-permissions.md`?\n\nEvidence: pkg/runner/config.go:32-157"
    },
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 627,
      "body": "The request flow diagram is incomplete. It shows only 4 middleware types but there are 8 middleware types defined in the codebase.\n\nMissing middleware: Token Exchange, Tool Filter, Tool Call Filter, and Telemetry.\n\nComplete flow should include:\n`Auth → [Token Exchange] → [Tool Filter] → [Tool Call Filter] → Parser → [Telemetry] → [Authorization] → [Audit] → Container`\n\n(Brackets indicate conditional middleware that are only present if configured)\n\nEvidence: pkg/runner/middleware.go:16-27"
    }
  ]
}
```

**When to use:**
- Mix of simple and complex issues
- Some items have clear fixes, others need discussion
- Want to provide suggestions where possible but leave complex items open

---

## Example 4: Multi-line Suggestion

**Use case**: Fixing multiple lines or a larger code block

**JSON:**
```json
{
  "body": "Correcting middleware list with complete and accurate information",
  "event": "COMMENT",
  "comments": [
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 110,
      "body": "The middleware list should include all 8 types with the correct name for Authorization:\n\n```suggestion\n**Eight middleware types:**\n\n1. **Authentication** (`auth`) - JWT token validation\n2. **Token Exchange** (`tokenexchange`) - OAuth token exchange\n3. **MCP Parser** (`mcp-parser`) - JSON-RPC parsing\n4. **Tool Filter** (`tool-filter`) - Filter and override tools in `tools/list` responses\n5. **Tool Call Filter** (`tool-call-filter`) - Validate and map `tools/call` requests\n6. **Telemetry** (`telemetry`) - OpenTelemetry instrumentation\n7. **Authorization** (`authorization`) - Cedar policy evaluation\n8. **Audit** (`audit`) - Request logging\n```\n\nEvidence: pkg/runner/middleware.go:16-27, pkg/authz/middleware.go:211"
    }
  ]
}
```

**When to use:**
- Correcting lists or tables
- Updating code blocks
- Fixing multiple related lines together
- Ensuring consistent formatting across lines

---

## Example 5: Request Changes (Blocking Review)

**Use case**: Critical issues that must be fixed before merge

**JSON:**
```json
{
  "body": "Critical inaccuracies found in documentation that must be corrected before merge",
  "event": "REQUEST_CHANGES",
  "comments": [
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 238,
      "body": "**CRITICAL**: This file path does not exist and will break documentation links.\n\n```suggestion\n- Registry manager: `pkg/registry/provider.go`\n```\n\nEvidence: Verified via codebase exploration"
    },
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 329,
      "body": "**CRITICAL**: These commands don't exist and users will get errors if they try to use them.\n\n```suggestion\n- `thv client list-registered` - List all registered clients\n- `thv client setup` - Interactively setup clients\n- `thv client status` - Show installation status\n```\n\nEvidence: cmd/thv/app/client.go:36-41"
    }
  ]
}
```

**When to use:**
- Critical bugs or security issues
- Documentation that will mislead users
- Breaking changes without proper migration
- Must be fixed before merge

---

## Example 6: Approval with Minor Suggestions

**Use case**: Approving PR but offering optional improvements

**JSON:**
```json
{
  "body": "LGTM! Just a few minor suggestions for improvement.",
  "event": "APPROVE",
  "comments": [
    {
      "path": "docs/arch/02-core-concepts.md",
      "line": 597,
      "body": "Minor: This file path could be more accurate.\n\n```suggestion\n- Health checker: `pkg/healthcheck/healthcheck.go`\n```\n\n(Not blocking - can be fixed in a follow-up if preferred)\n\nEvidence: Verified via codebase exploration"
    }
  ]
}
```

**When to use:**
- PR is generally good, minor improvements available
- Non-blocking suggestions for quality improvements
- Optional refactoring or cleanup suggestions
- Style or consistency improvements

---

## Tips for Each Scenario

### For Simple Reviews (No Suggestions)
- Focus on clear problem descriptions
- Ask questions when context is needed
- Provide references to relevant code
- Suggest next steps or alternatives

### For Reviews with Suggestions
- Always read the current content first
- Match the existing formatting exactly
- Test the suggestion if possible
- Keep suggestions focused and minimal

### For Mixed Reviews
- Put suggestions first (quick wins)
- Group related comments together
- Use clear markdown formatting
- Distinguish between blocking and non-blocking issues

### For Blocking Reviews
- Use `REQUEST_CHANGES` event
- Mark critical items clearly (e.g., **CRITICAL**)
- Provide suggestions where possible for faster resolution
- Explain impact of not fixing the issue

### For Approvals
- Use `APPROVE` event
- Mark suggestions as optional/non-blocking
- Acknowledge good work in the summary
- Keep suggestions truly minor/optional
