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
    description: "Deploy PR with user confirmation and notification"
    parameters:
      pr_number: {type: "integer"}
    steps:
      - id: "merge"
        tool: "github.merge_pr"
        arguments: {pr: "{{.params.pr_number}}"}

      # Elicitation step for user confirmation
      - id: "confirm_deploy"
        type: "elicitation"
        message: "PR {{.params.pr_number}} merged successfully. Proceed with deployment?"
        schema:
          type: "object"
          properties:
            environment:
              type: "string"
              enum: ["staging", "production"]
              enumNames: ["Staging", "Production"]
              description: "Target deployment environment"
            notify_team:
              type: "boolean"
              default: true
              description: "Send Slack notification to team"
          required: ["environment"]
        depends_on: ["merge"]
        on_decline:
          action: "skip_remaining"
        on_cancel:
          action: "abort"

      - id: "deploy"
        tool: "kubernetes.deploy"
        arguments:
          pr: "{{.params.pr_number}}"
          environment: "{{.steps.confirm_deploy.content.environment}}"
        depends_on: ["confirm_deploy"]
        condition: "{{.steps.confirm_deploy.action == 'accept'}}"

      - id: "notify"
        tool: "slack.send"
        arguments:
          text: "Deployed PR {{.params.pr_number}} to {{.steps.confirm_deploy.content.environment}}"
        depends_on: ["deploy"]
        condition: "{{.steps.confirm_deploy.content.notify_team}}"
```

#### Example: Incident Investigation Using Multiple Fetch Calls

This composite tool demonstrates calling the same backend tool (`fetch`) multiple times with different static URLs to gather incident data from various monitoring sources:

```yaml
composite_tools:
  - name: "investigate_incident"
    description: "Gather logs and metrics from multiple sources for incident analysis"
    parameters:
      incident_id: {type: "string"}
      time_range: {type: "string", default: "1h"}
    steps:
      - id: "fetch_app_logs"
        tool: "fetch.fetch"
        arguments:
          url: "https://logs.company.com/api/query?service=app&time={{.params.time_range}}"

      - id: "fetch_error_metrics"
        tool: "fetch.fetch"
        arguments:
          url: "https://metrics.company.com/api/errors?time={{.params.time_range}}"

      - id: "fetch_trace_data"
        tool: "fetch.fetch"
        arguments:
          url: "https://tracing.company.com/api/traces?time={{.params.time_range}}"

      - id: "fetch_infra_status"
        tool: "fetch.fetch"
        arguments:
          url: "https://monitoring.company.com/api/infrastructure/status"

      - id: "create_report"
        tool: "jira.create_issue"
        arguments:
          title: "Incident {{.params.incident_id}} Analysis"
          description: |
            Application Logs: {{.steps.fetch_app_logs.output}}
            Error Metrics: {{.steps.fetch_error_metrics.output}}
            Trace Data: {{.steps.fetch_trace_data.output}}
            Infrastructure: {{.steps.fetch_infra_status.output}}
```

This pattern allows a single generic `fetch` tool to be reused with different static endpoints, creating a purpose-built incident investigation workflow without needing separate tools for each data source.

```yaml
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

#### 6. Elicitation Support in Composite Tools

Composite tools can include elicitation steps to request additional information from users during workflow execution, following the [MCP elicitation specification](https://modelcontextprotocol.io/specification/2025-06-18/client/elicitation). This enables:

- **Interactive workflows**: Request user input between steps
- **Safety confirmations**: Require approval before destructive operations
- **Dynamic parameters**: Gather context-dependent information
- **Conditional execution**: Branch workflow based on user responses

Elicitation steps use JSON Schema to define the structure of requested data (limited to flat objects with primitive properties). The Virtual MCP server forwards elicitation requests to the client and captures responses in three forms:

- **accept**: User provided data (accessible via `{{.steps.step_id.content}}`)
- **decline**: User explicitly rejected the request
- **cancel**: User dismissed without choosing

Subsequent steps can reference elicitation results through template expansion and use the `condition` field to execute conditionally based on user responses. The `on_decline` and `on_cancel` handlers control workflow behavior for non-acceptance scenarios.

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
- Elicitation support in composite tools
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
6. **Elicitation**: Implement MCP elicitation protocol for composite tool user interaction

### Authentication Complexity

Different MCP servers may have vastly different authentication requirements:
- Public servers may need no auth
- Enterprise servers may require OAuth/OIDC
- Internal services may use service accounts
- Third-party APIs may need API keys

The Virtual MCP must handle this complexity transparently, maintaining separate authentication contexts per backend while presenting a unified interface to clients.

### Composite Tool State Persistence

Composite tools with elicitation require state persistence to handle long-running workflows that span multiple user interactions. The Virtual MCP will maintain workflow execution state (current step, completed steps, elicitation responses, and template variables) in memory with a clean storage interface to enable future migration to persistent backends. Each composite tool invocation receives a unique workflow ID, and the server checkpoints state after each step completion and elicitation interaction. Workflows have a default timeout of 30 minutes, after which they are automatically cleaned up and any pending elicitations are treated as cancelled. This approach keeps the implementation simple for Phase 2 while establishing patterns that can scale to distributed persistence later.

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
