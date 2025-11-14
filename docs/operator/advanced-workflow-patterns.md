# Advanced Workflow Patterns for Virtual MCP Composite Tools

## Overview

This guide covers advanced workflow patterns and best practices for Virtual MCP Composite Tools, including parallel execution, dependency management, error handling strategies, and state management.

## Table of Contents

- [Parallel Execution with DAG](#parallel-execution-with-dag)
- [Step Dependencies](#step-dependencies)
- [Advanced Error Handling](#advanced-error-handling)
- [Workflow State Management](#workflow-state-management)
- [Performance Optimization](#performance-optimization)
- [Best Practices](#best-practices)
- [Common Patterns](#common-patterns)

---

## Parallel Execution with DAG

Virtual MCP Composite Tools use a Directed Acyclic Graph (DAG) execution model that automatically executes independent steps in parallel while respecting dependencies.

### How DAG Execution Works

1. **Execution Levels**: Steps are organized into levels based on dependencies
2. **Parallel Within Levels**: All steps in the same level execute concurrently
3. **Sequential Across Levels**: Each level waits for the previous level to complete
4. **Automatic Optimization**: The system automatically determines optimal parallelization

### Example: Parallel Data Fetching

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPCompositeToolDefinition
metadata:
  name: incident-investigation
spec:
  name: investigate_incident
  description: Investigate incident by gathering logs, metrics, and traces in parallel
  parameters:
    schema:
      type: object
      properties:
        incident_id:
          type: string
        time_range:
          type: string
  steps:
    # Level 1: These three steps run in parallel (no dependencies)
    - id: fetch_logs
      type: tool
      tool: splunk.fetch_logs
      arguments:
        incident_id: "{{.params.incident_id}}"
        time_range: "{{.params.time_range}}"

    - id: fetch_metrics
      type: tool
      tool: datadog.fetch_metrics
      arguments:
        incident_id: "{{.params.incident_id}}"
        time_range: "{{.params.time_range}}"

    - id: fetch_traces
      type: tool
      tool: jaeger.fetch_traces
      arguments:
        incident_id: "{{.params.incident_id}}"
        time_range: "{{.params.time_range}}"

    # Level 2: Waits for all Level 1 steps to complete
    - id: correlate
      type: tool
      tool: analysis.correlate_data
      depends_on: [fetch_logs, fetch_metrics, fetch_traces]
      arguments:
        logs: "{{.steps.fetch_logs.output}}"
        metrics: "{{.steps.fetch_metrics.output}}"
        traces: "{{.steps.fetch_traces.output}}"

    # Level 3: Waits for Level 2
    - id: create_report
      type: tool
      tool: jira.create_issue
      depends_on: [correlate]
      arguments:
        title: "Incident {{.params.incident_id}} Analysis"
        body: "{{.steps.correlate.output.summary}}"
```

**Execution Timeline**:
```
Time    Level 1 (Parallel)              Level 2         Level 3
0ms     fetch_logs    ─┐
0ms     fetch_metrics ─┼─> correlate ──> create_report
0ms     fetch_traces  ─┘
```

**Performance**: Fetching 3 data sources takes ~1x time instead of 3x (sequential).

---

## Step Dependencies

Use the `depends_on` field to define explicit dependencies between steps.

### Syntax

```yaml
steps:
  - id: step_name
    depends_on: [dependency1, dependency2, ...]
    # ... rest of step config
```

### Dependency Rules

1. **Multiple Dependencies**: Step waits for ALL dependencies to complete
2. **Transitive Dependencies**: Automatically handled (A→B→C works as expected)
3. **Cycle Detection**: Circular dependencies are detected and rejected at validation time
4. **Missing Dependencies**: Referencing non-existent steps fails validation

### Example: Diamond Pattern

```yaml
steps:
  # Level 1
  - id: fetch_data
    type: tool
    tool: api.fetch

  # Level 2: Both depend on fetch_data, can run in parallel
  - id: process_left
    type: tool
    tool: transform.left
    depends_on: [fetch_data]

  - id: process_right
    type: tool
    tool: transform.right
    depends_on: [fetch_data]

  # Level 3: Waits for both Level 2 steps
  - id: merge_results
    type: tool
    tool: combine.merge
    depends_on: [process_left, process_right]
```

**Execution Graph**:
```
       fetch_data
       /        \
process_left  process_right
       \        /
      merge_results
```

### Accessing Dependency Outputs

Use template syntax to access outputs from dependencies:

```yaml
- id: analyze
  depends_on: [fetch_logs, fetch_metrics]
  arguments:
    # Access specific fields from dependency outputs
    log_count: "{{.steps.fetch_logs.output.count}}"
    metric_avg: "{{.steps.fetch_metrics.output.average}}"

    # Pass entire output object
    raw_data: "{{.steps.fetch_logs.output}}"
```

---

## Advanced Error Handling

Configure sophisticated error handling at both workflow and step levels.

### Workflow-Level Failure Modes

Set the workflow's `failureMode` to control global error behavior:

```yaml
spec:
  name: resilient_workflow
  failureMode: continue  # Options: abort, continue, best_effort
  steps:
    # ...
```

**Failure Modes**:

| Mode | Behavior | Use Case |
|------|----------|----------|
| `abort` | Stop immediately on first error (default) | Critical workflows where partial completion is dangerous |
| `continue` | Log errors but continue executing remaining steps | Data collection where some failures are acceptable |
| `best_effort` | Try all steps, aggregate errors at end | Monitoring/reporting where you want maximum data |

### Step-Level Error Handling

Override workflow-level behavior for specific steps:

```yaml
steps:
  - id: optional_notification
    type: tool
    tool: slack.notify
    on_error:
      action: continue_on_error  # Don't fail workflow if Slack is down

  - id: critical_payment
    type: tool
    tool: stripe.charge
    # Inherits workflow failureMode (defaults to abort)
```

### Retry Logic with Exponential Backoff

Configure automatic retries for transient failures:

```yaml
steps:
  - id: fetch_external_api
    type: tool
    tool: external.fetch_data
    on_error:
      action: retry
      retry_count: 3           # Maximum 3 retries (4 total attempts)
      retry_delay: 1s          # Initial delay: 1 second
      # Exponential backoff: 1s, 2s, 4s
```

**Retry Behavior**:
- **Exponential Backoff**: Delay doubles each retry (1s → 2s → 4s → 8s...)
- **Maximum Retries**: Capped at 10 (configurable per step)
- **Context Aware**: Respects workflow timeout (won't retry if timeout exceeded)
- **Error Propagation**: Final error includes retry count in metadata

### Example: Combining Error Strategies

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPCompositeToolDefinition
metadata:
  name: robust-deployment
spec:
  name: deploy_with_resilience
  failureMode: abort  # Fail fast by default
  steps:
    # Retry transient network issues
    - id: fetch_artifact
      type: tool
      tool: s3.download
      on_error:
        action: retry
        retry_count: 3
        retry_delay: 2s

    - id: deploy
      type: tool
      tool: kubernetes.apply
      depends_on: [fetch_artifact]
      # Critical: uses workflow failureMode (abort)

    # Optional post-deployment tasks
    - id: notify_slack
      type: tool
      tool: slack.notify
      depends_on: [deploy]
      on_error:
        action: continue_on_error  # Don't fail if notification fails

    - id: update_dashboard
      type: tool
      tool: grafana.update
      depends_on: [deploy]
      on_error:
        action: continue_on_error
```

---

## Workflow State Management

Virtual MCP tracks workflow execution state for monitoring, debugging, and cancellation.

### State Tracking

The workflow engine automatically maintains state including:

- **Workflow ID**: Unique identifier (UUID) for each execution
- **Status**: Current state (pending, running, completed, failed, cancelled, timed_out)
- **Completed Steps**: List of successfully completed steps
- **Step Results**: Outputs and timing for each step
- **Pending Elicitations**: User interactions awaiting response
- **Timestamps**: Start time, end time, last update time

### Workflow Timeout

Configure maximum execution time to prevent runaway workflows:

```yaml
spec:
  name: time_sensitive_workflow
  timeout: 30m  # 30 minutes maximum
  steps:
    - id: long_running_task
      type: tool
      tool: data.process
      timeout: 5m  # Individual step timeout
```

**Timeout Behavior**:
- Workflow timeout applies to entire execution
- Step timeouts apply to individual steps
- Timeouts trigger graceful cancellation (context.DeadlineExceeded)
- State is saved with `timed_out` status

**Timeout Precedence**:
```
Workflow Timeout: 30m
  ├─ Step 1 (5m timeout)   ✓ Respects both
  ├─ Step 2 (10m timeout)  ✓ Respects both
  └─ Step 3 (40m timeout)  ✗ Limited by workflow timeout
```

### State Persistence

**In-Memory State Store** (Default):
- Suitable for single-instance deployments
- Automatic cleanup of completed workflows (configurable)
- Thread-safe for parallel step execution
- Workflow status available via API

**Future: Distributed State Store** (Redis/Database):
- For multi-instance deployments
- Workflow resumption after restart
- Cross-instance workflow visibility

### Example: Monitoring Workflow State

```yaml
# Query workflow status (future CLI support)
$ thv workflow status <workflow-id>

Workflow ID: a1b2c3d4-e5f6-7890-abcd-ef1234567890
Status: running
Started: 2025-01-15 10:30:00
Duration: 2m 15s

Completed Steps:
  ✓ fetch_logs    (1.2s)
  ✓ fetch_metrics (0.8s)
  ✓ fetch_traces  (1.5s)

In Progress:
  ⏳ correlate

Pending:
  ⋯ create_report
```

---

## Performance Optimization

### Concurrency Limits

The DAG executor limits parallel execution to prevent resource exhaustion:

```go
// Default: 10 concurrent steps maximum
// Configurable in workflow engine initialization
```

**Tuning Recommendations**:
- **I/O-bound workflows**: Higher concurrency (10-20 steps)
- **CPU-bound workflows**: Lower concurrency (2-5 steps)
- **Memory-intensive**: Monitor and adjust based on capacity

### Execution Statistics

The system tracks execution metrics:

```go
stats := {
  "total_levels":      3,     // Number of execution levels
  "total_steps":       8,     // Total steps in workflow
  "max_parallelism":   3,     // Max steps in any level
  "sequential_steps":  2,     // Steps that run alone
}
```

### Optimization Strategies

1. **Minimize Dependencies**: Reduce `depends_on` where possible
2. **Group Related Steps**: Steps with similar execution time work well in same level
3. **Split Large Steps**: Break monolithic steps into parallel sub-steps
4. **Use Conditional Execution**: Skip unnecessary steps with `condition` field

**Example: Optimized Data Pipeline**

```yaml
# Before: Sequential (9 seconds total)
steps:
  - id: fetch1     # 3s
  - id: fetch2     # 3s
  - id: fetch3     # 3s

# After: Parallel (3 seconds total)
steps:
  - id: fetch1     # 3s ─┐
  - id: fetch2     # 3s ─┼─ All run in parallel
  - id: fetch3     # 3s ─┘
```

---

## Best Practices

### 1. Design for Parallelism

✅ **DO**: Identify independent operations
```yaml
steps:
  - id: notify_slack
  - id: notify_email
  - id: notify_pagerduty
  # All independent, run in parallel
```

❌ **DON'T**: Create unnecessary dependencies
```yaml
steps:
  - id: notify_slack
  - id: notify_email
    depends_on: [notify_slack]  # Unnecessary!
  - id: notify_pagerduty
    depends_on: [notify_email]  # Creates false sequencing
```

### 2. Declare All Dependencies Explicitly

✅ **DO**: Be explicit about data dependencies
```yaml
- id: aggregate
  depends_on: [fetch_logs, fetch_metrics]  # Clear intent
  arguments:
    logs: "{{.steps.fetch_logs.output}}"
    metrics: "{{.steps.fetch_metrics.output}}"
```

❌ **DON'T**: Rely on implicit ordering
```yaml
# This will fail! process_data tries to access fetch_data output,
# but they run in parallel without depends_on
- id: fetch_data
  type: tool
  tool: api.fetch

- id: process_data  # ERROR: fetch_data may not have completed!
  type: tool
  tool: transform.process
  arguments:
    data: "{{.steps.fetch_data.output}}"
```

### 3. Use Appropriate Error Handling

✅ **DO**: Match error handling to business requirements
```yaml
steps:
  # Critical: must succeed
  - id: charge_payment
    type: tool
    tool: stripe.charge
    # Uses default abort behavior

  # Optional: nice to have
  - id: send_receipt
    type: tool
    tool: email.send
    depends_on: [charge_payment]
    on_error:
      action: continue_on_error
```

### 4. Set Realistic Timeouts

✅ **DO**: Set timeouts based on SLAs
```yaml
spec:
  timeout: 5m  # API SLA: 5 minutes
  steps:
    - id: external_api
      timeout: 30s  # Individual operation: 30 seconds
      on_error:
        action: retry
        retry_count: 3
```

### 5. Keep Steps Focused

✅ **DO**: One responsibility per step
```yaml
steps:
  - id: fetch_user
    tool: db.query_user
  - id: validate_permissions
    tool: auth.check_permissions
    depends_on: [fetch_user]
  - id: perform_action
    tool: api.execute
    depends_on: [validate_permissions]
```

❌ **DON'T**: Combine unrelated operations
```yaml
steps:
  - id: do_everything
    tool: monolith.execute  # Hard to parallelize, test, debug
```

---

## Common Patterns

### Pattern 1: Fan-Out / Fan-In

Parallel execution followed by aggregation.

```yaml
steps:
  # Fan-out: Parallel data collection
  - id: fetch_source_a
    type: tool
    tool: api.fetch_a

  - id: fetch_source_b
    type: tool
    tool: api.fetch_b

  - id: fetch_source_c
    type: tool
    tool: api.fetch_c

  # Fan-in: Aggregate results
  - id: aggregate
    type: tool
    tool: analysis.combine
    depends_on: [fetch_source_a, fetch_source_b, fetch_source_c]
```

**Use Cases**: Data aggregation, multi-source reporting, distributed search

### Pattern 2: Pipeline with Parallel Stages

Sequential stages with parallel operations within each stage.

```yaml
steps:
  # Stage 1: Fetch raw data
  - id: fetch
    type: tool
    tool: api.fetch

  # Stage 2: Parallel transformations
  - id: transform_format_a
    type: tool
    tool: transform.to_format_a
    depends_on: [fetch]

  - id: transform_format_b
    type: tool
    tool: transform.to_format_b
    depends_on: [fetch]

  # Stage 3: Parallel storage
  - id: store_warehouse
    type: tool
    tool: warehouse.store
    depends_on: [transform_format_a]

  - id: store_cache
    type: tool
    tool: cache.store
    depends_on: [transform_format_b]
```

**Use Cases**: ETL pipelines, data transformation, multi-target deployments

### Pattern 3: Conditional Parallel Execution

Use conditions to selectively enable parallel branches.

```yaml
steps:
  - id: fetch_user
    type: tool
    tool: db.query_user

  # Parallel conditional branches
  - id: notify_slack
    type: tool
    tool: slack.notify
    depends_on: [fetch_user]
    condition: "{{.steps.fetch_user.output.preferences.slack_enabled}}"

  - id: notify_email
    type: tool
    tool: email.send
    depends_on: [fetch_user]
    condition: "{{.steps.fetch_user.output.preferences.email_enabled}}"

  - id: notify_sms
    type: tool
    tool: sms.send
    depends_on: [fetch_user]
    condition: "{{.steps.fetch_user.output.preferences.sms_enabled}}"
```

**Use Cases**: Multi-channel notifications, feature flags, A/B testing

### Pattern 4: Retry with Fallback

Try primary service, retry on failure, fall back to secondary.

```yaml
steps:
  - id: try_primary
    type: tool
    tool: primary_api.call
    on_error:
      action: retry
      retry_count: 2
      retry_delay: 1s

  - id: use_fallback
    type: tool
    tool: fallback_api.call
    depends_on: [try_primary]
    condition: "{{ne .steps.try_primary.status \"completed\"}}"
```

**Use Cases**: High availability, disaster recovery, service degradation

### Pattern 5: Parallel Validation

Validate multiple aspects concurrently before proceeding.

```yaml
steps:
  # Parallel validations
  - id: validate_schema
    type: tool
    tool: validation.check_schema

  - id: validate_permissions
    type: tool
    tool: auth.check_permissions

  - id: validate_quota
    type: tool
    tool: billing.check_quota

  # Proceed only if all validations pass
  - id: execute_action
    type: tool
    tool: api.execute
    depends_on: [validate_schema, validate_permissions, validate_quota]
```

**Use Cases**: Pre-flight checks, authorization, resource validation

---

## Troubleshooting

### Debugging Parallel Execution

**Problem**: Step fails with "output not found" error

**Solution**: Add dependency to ensure step completes first
```yaml
# Before (broken)
- id: process
  arguments:
    data: "{{.steps.fetch.output}}"  # May run before fetch completes!

# After (fixed)
- id: process
  depends_on: [fetch]  # Explicit dependency
  arguments:
    data: "{{.steps.fetch.output}}"
```

### Detecting Circular Dependencies

**Problem**: Workflow validation fails with "circular dependency detected"

**Solution**: Review `depends_on` chains for cycles
```yaml
# Circular dependency (invalid)
- id: step_a
  depends_on: [step_b]
- id: step_b
  depends_on: [step_a]  # ❌ Cycle!

# Fixed (valid)
- id: step_a
- id: step_b
  depends_on: [step_a]  # ✓ Linear dependency
```

### Performance Issues

**Problem**: Workflow slower than expected despite parallel execution

**Checklist**:
1. Verify steps actually run in parallel (check execution levels)
2. Check for unnecessary `depends_on` constraints
3. Review concurrency limits (may be throttling)
4. Profile individual step execution times
5. Consider network/external service bottlenecks

---

## Migration from Sequential to Parallel

If you have existing sequential workflows, here's how to migrate:

### Step 1: Identify Independent Steps

Review your workflow and identify steps that:
- Don't use outputs from other steps
- Access different external services
- Perform independent validations or checks

### Step 2: Remove Unnecessary Dependencies

```yaml
# Before: Implicit sequential execution
steps:
  - id: step1
  - id: step2
  - id: step3

# After: Explicit independence (parallel)
steps:
  - id: step1  # No depends_on = runs in parallel
  - id: step2  # No depends_on = runs in parallel
  - id: step3  # No depends_on = runs in parallel
```

### Step 3: Add Required Dependencies

```yaml
# If step3 actually needs step1's output:
steps:
  - id: step1
  - id: step2
  - id: step3
    depends_on: [step1]  # Explicit data dependency
    arguments:
      data: "{{.steps.step1.output}}"
```

### Step 4: Test Incrementally

1. Start with one parallel group
2. Validate outputs and timing
3. Gradually parallelize more steps
4. Monitor for race conditions or dependency issues

---

## Additional Resources

- [VirtualMCPCompositeToolDefinition Guide](virtualmcpcompositetooldefinition-guide.md) - Basic workflow concepts
- [Architecture Documentation](../arch/README.md) - System architecture and design
- [Operator Guide](deploying-mcp-server-with-operator.md) - Kubernetes deployment

---

## Summary

Key takeaways for advanced workflows:

1. ✅ **Embrace Parallelism**: Design workflows for concurrent execution
2. ✅ **Explicit Dependencies**: Always declare data dependencies with `depends_on`
3. ✅ **Error Resilience**: Use retry for transient failures, continue for optional steps
4. ✅ **Set Timeouts**: Prevent runaway workflows with appropriate timeouts
5. ✅ **Monitor State**: Track workflow execution for debugging and optimization

The DAG execution model provides automatic parallelization while maintaining correctness through dependency management. Follow these patterns and practices to build efficient, reliable, and maintainable workflows.
