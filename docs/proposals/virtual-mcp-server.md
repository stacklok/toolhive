# Virtual MCP Server

## Problem Statement

Organizations need to consolidate multiple MCP servers into a unified interface for complex workflows spanning multiple tools and services. Currently, clients must manage connections to individual MCP servers separately, leading to complexity in orchestration and authentication management.

## Goals

- Aggregate multiple MCP servers from a ToolHive group into a single virtual MCP server
- Enable tool namespace management and conflict resolution
- Support composite tools for multi-step workflows across backends
- Handle per-backend authentication/authorization requirements
- Maintain full MCP protocol compatibility

## Proposed Solution

### High-Level Design

The Virtual MCP Server (`thv virtual`) acts as an aggregation proxy that:
1. References an existing ToolHive group containing multiple MCP server workloads
2. Discovers and merges capabilities (tools, resources, prompts) from all workloads
3. Routes incoming MCP requests to appropriate backend workloads
4. Manages per-backend authentication requirements

```
MCP Client → Virtual MCP → [Workload A, Workload B, ... from Group]
```

### Configuration Schema

```yaml
# virtual-mcp-config.yaml
version: v1
name: "engineering-tools"
description: "Virtual MCP for engineering team"

# Reference existing ToolHive group (required)
group: "engineering-team"

# Tool aggregation using existing ToolHive constructs
aggregation:
  conflict_resolution: "prefix"  # prefix | priority | manual

  tools:
    - workload: "github"
      # Filter to only include specific tools (uses existing ToolsFilter)
      filter: ["create_pr", "merge_pr", "list_issues"]
      # Override tool names/descriptions (uses existing ToolOverride)
      overrides:
        create_pr:
          name: "gh_create_pr"
          description: "Create a GitHub pull request"

    - workload: "jira"
      # If no filter specified, all tools are included
      overrides:
        create_issue:
          name: "jira_create_issue"

    - workload: "slack"
      # No filter or overrides - include all tools as-is

# Per-backend authentication
backend_auth:
  github:
    type: "token_exchange"
    token_url: "https://github.com/oauth/token"
    audience: "github-api"
    client_id_ref:
      name: "github-oauth"
      key: "client_id"

  jira:
    type: "pass_through"

  internal_db:
    type: "service_account"
    credentials_ref:
      name: "db-creds"
      key: "token"

  slack:
    type: "header_injection"
    headers:
      - name: "Authorization"
        value_ref:
          name: "slack-creds"
          key: "bot_token"

# Composite tools (Phase 2)
composite_tools:
  - name: "deploy_and_notify"
    description: "Deploy PR and notify team"
    parameters:
      pr_number: {type: "integer"}
    steps:
      - id: "merge"
        tool: "github.merge_pr"
        arguments: {pr: "{{.params.pr_number}}"}

      - id: "notify"
        tool: "slack.send"
        arguments: {text: "Deployed PR {{.params.pr_number}}"}
        depends_on: ["merge"]

# Operational settings
operational:
  timeouts:
    default: 30s
    per_workload:
      github: 45s

  failover:
    strategy: "automatic"
```

### Key Features

#### 1. Group-Based Backend Management

Virtual MCP references a ToolHive group and automatically discovers all workloads within it:
- Leverages existing `groups.Manager` and `workloads.Manager`
- Dynamic workload discovery via `ListWorkloadsInGroup()`
- Inherits workload configurations from group

#### 2. Capability Aggregation

Merges MCP capabilities from all backends:
- **Tools**: Aggregated with namespace conflict resolution
- **Resources**: Combined from all backends
- **Prompts**: Merged with prefixing
- **Logging/Sampling**: Enabled if any backend supports it

#### 3. Tool Filtering and Overrides

Uses existing ToolHive constructs:
- **ToolsFilter**: Include only specific tools from a workload
- **ToolOverride**: Rename tools and update descriptions to avoid conflicts

#### 4. Per-Backend Authentication

Different backends may require different authentication strategies:
- **pass_through**: Forward client credentials unchanged
- **token_exchange**: Exchange incoming token for backend-specific token (RFC 8693)
- **service_account**: Use stored credentials for backend
- **header_injection**: Add authentication headers from secrets
- **mapped_claims**: Transform JWT claims for backend requirements

Example authentication flow:
```
Client → (Bearer token for Virtual MCP) → Virtual MCP
         ├→ GitHub backend (token exchanged for GitHub PAT)
         ├→ Jira backend (original token passed through)
         └→ Internal DB (service account token injected)
```

#### 5. Request Routing

Routes MCP protocol requests to appropriate backends:
- Tool calls routed based on tool-to-workload mapping
- Resource requests routed by resource namespace
- Prompt requests handled similarly
- Load balancing for duplicate capabilities

### CLI Usage

```bash
# Start virtual MCP for a group
thv virtual --group engineering-team --config virtual-config.yaml

# Quick start with defaults
thv virtual --group engineering-team

# With inline tool filtering
thv virtual --group engineering-team \
  --tools github:create_pr,merge_pr \
  --tools jira:create_issue

# With tool overrides file
thv virtual --group engineering-team \
  --tools-override overrides.yaml
```

### Implementation Phases

**Phase 1 (MVP)**: Basic aggregation
- Group-based workload discovery
- Tool aggregation with filter and override support
- Simple request routing
- Pass-through authentication

**Phase 2**: Advanced features
- Composite tool execution
- Per-backend authentication strategies
- Token exchange support
- Failover and load balancing

**Phase 3**: Enterprise features
- Dynamic configuration updates
- Multi-group support
- Advanced routing strategies
- Comprehensive observability

## Benefits

- **Simplified Client Integration**: Single MCP connection instead of multiple
- **Unified Authentication**: Handle diverse backend auth requirements centrally
- **Workflow Orchestration**: Composite tools enable cross-service workflows
- **Operational Efficiency**: Centralized monitoring and management
- **Backward Compatible**: Works with existing MCP clients and servers

## Implementation Notes

### Reusing Existing Components

The implementation will maximize reuse of existing ToolHive components:

1. **Tool Filtering**: Use existing `mcp.WithToolsFilter()` middleware
2. **Tool Overrides**: Use existing `mcp.WithToolsOverride()` middleware
3. **Groups**: Use `groups.Manager` for group operations
4. **Workloads**: Use `workloads.Manager` for workload discovery
5. **Authentication**: Extend existing auth middleware patterns

### Authentication Complexity

Different MCP servers may have vastly different authentication requirements:
- Public servers may need no auth
- Enterprise servers may require OAuth/OIDC
- Internal services may use service accounts
- Third-party APIs may need API keys

The Virtual MCP must handle this complexity transparently, maintaining separate authentication contexts per backend while presenting a unified interface to clients.

## Alternative Approaches Considered

1. **Kubernetes CRD**: More complex, requires operator changes
2. **Standalone Service**: Loses integration with ToolHive infrastructure
3. **LLM-generated backend**: Adds more randomness to the equation which is undesirable for agents.

## Open Questions

1. How to handle streaming responses across multiple backends?
2. Should we cache backend capabilities or query dynamically?
3. How to handle backend-specific rate limits?

## Success Criteria

- Aggregate 5+ MCP servers from a group with < 10ms routing overhead
- Support 100+ concurrent client connections
- Zero changes required to existing MCP servers or clients
- Successfully handle different authentication methods per backend
- Maintain existing tool filter and override semantics
