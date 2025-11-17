# vMCP Future Enhancements

This file tracks planned improvements and enhancements for the Virtual MCP Server that are not yet implemented.

## Authorization-Aware Composite Workflows

**Context**: PR #2572 integrated composite tool workflows with the vMCP request pipeline. Currently, workflows fail hard if they reference tools that users lack access to.

**Current Behavior**:
- Composite tools are registered per-session based on session-discovered tools
- If a workflow references tools that a user lacks access to, registration fails with an error
- This prevents privilege escalation but may impact user experience

**Future Enhancement**:
- Gracefully disable workflows with missing required tools
- Log disabled workflows for audit purposes
- Add workflow metadata to distinguish required vs optional tools:
  ```go
  type WorkflowDefinition struct {
      RequiredTools []string  // Must have access to all
      OptionalTools []string  // Nice-to-have, workflow can adapt
      // ...
  }
  ```
- Return user-friendly messages when workflows are unavailable due to authorization

**Security Considerations**:
- Must prevent information leakage about tools users can't access
- Maintain audit trail of authorization decisions
- Default to fail-closed for security

**References**:
- PR #2572 review comment by @jhrozek
- File: `pkg/vmcp/server/adapter/capability_adapter.go:164-168`

## Workflow Execution Timeouts

**Context**: Added in PR #2572 - CreateCompositeToolHandler needs timeout handling.

**Current Implementation**:
- Workflow definitions have a `Timeout` field (default: 30 minutes)
- Context timeout should be applied if not already set
- Need to respect workflow-specific timeout configuration

**Implementation Location**:
- `pkg/vmcp/server/adapter/handler_factory.go:243-276` (CreateCompositeToolHandler)
- Apply timeout before workflow execution
- Handle `context.DeadlineExceeded` errors with user-friendly messages

**Security Benefit**:
- Prevents DoS via long-running workflows
- Protects against resource exhaustion

## Rate Limiting for Composite Workflows

**Context**: Mentioned in PR #2572 security review.

**Future Work**:
- Add rate limiting for workflow execution
- Per-user and per-workflow rate limits
- Protection against workflow abuse
- Metrics and monitoring for workflow execution patterns

## Enhanced Workflow Validation

**Context**: PR #2572 added fail-fast validation at load time.

**Future Enhancements**:
- Validation mode for checking configs without running them
- More detailed validation error messages
- Validation UI/CLI tool for configuration authors
- Static analysis of workflow definitions

## Session-Specific Workflow Customization

**Future Work**:
- Allow workflows to adapt based on available tools
- Dynamic workflow generation based on session capabilities
- Workflow templates with conditional steps
- Better handling of partial tool availability
