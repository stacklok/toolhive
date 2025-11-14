# Composite Tools Quick Reference

Quick reference for Virtual MCP Composite Tool workflows.

## Basic Workflow Structure

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPCompositeToolDefinition
metadata:
  name: my-workflow
  namespace: default
spec:
  name: my_workflow_name        # Tool name exposed to clients
  description: What it does      # Required description
  timeout: 30m                   # Optional: workflow timeout (default: 30m)
  failureMode: abort             # Optional: abort|continue|best_effort (default: abort)

  parameters:                    # Optional: input parameters
    schema:
      type: object
      properties:
        param_name:
          type: string

  steps:                         # Required: workflow steps
    - id: step1
      type: tool                 # tool|elicitation
      tool: workload.tool_name
      arguments:
        key: "{{.params.param_name}}"
```

## Parallel Execution

```yaml
# Independent steps run in parallel automatically
steps:
  - id: fetch_a                  # Level 1: Runs in parallel ─┐
  - id: fetch_b                  # Level 1: Runs in parallel ─┼─> aggregate
  - id: fetch_c                  # Level 1: Runs in parallel ─┘

  - id: aggregate                # Level 2: Waits for Level 1
    depends_on: [fetch_a, fetch_b, fetch_c]
```

## Step Dependencies

```yaml
steps:
  - id: step1

  - id: step2
    depends_on: [step1]          # Runs after step1 completes

  - id: step3
    depends_on: [step1, step2]   # Waits for both step1 AND step2
```

## Template Syntax

```yaml
# Access input parameters
"{{.params.parameter_name}}"

# Access step outputs
"{{.steps.step_id.output}}"
"{{.steps.step_id.output.field_name}}"
"{{.steps.step_id.status}}"     # completed|failed|skipped|running

# Conditional logic
condition: "{{eq .steps.step1.status \"completed\"}}"
condition: "{{.params.enabled}}"
condition: "{{gt .steps.step1.output.count 10}}"

# JSON encoding
arguments:
  data: "{{json .steps.step1.output}}"
```

## Error Handling

### Workflow-Level

```yaml
spec:
  failureMode: abort             # Stop on first error (default)
  failureMode: continue          # Log errors, continue workflow
  failureMode: best_effort       # Try all steps, aggregate errors
```

### Step-Level (Overrides Workflow)

```yaml
steps:
  # Abort on error (default)
  - id: critical
    tool: payment.charge
    # Uses workflow failureMode

  # Continue despite errors
  - id: optional
    tool: notification.send
    on_error:
      action: continue_on_error

  # Retry with exponential backoff
  - id: resilient
    tool: external.api
    on_error:
      action: retry
      retry_count: 3             # Max 3 retries (4 total attempts)
      retry_delay: 1s            # Initial delay: 1s, 2s, 4s, 8s...
```

## Timeouts

```yaml
spec:
  timeout: 30m                   # Workflow timeout (default: 30m)

  steps:
    - id: step1
      timeout: 5m                # Step timeout (default: 5m)
```

**Precedence**: Step timeout ≤ Workflow timeout

## Common Patterns

### Fan-Out / Fan-In

```yaml
steps:
  # Fan-out: Parallel collection
  - id: fetch_1
  - id: fetch_2
  - id: fetch_3

  # Fan-in: Aggregate
  - id: combine
    depends_on: [fetch_1, fetch_2, fetch_3]
```

### Sequential Pipeline

```yaml
steps:
  - id: fetch
  - id: transform
    depends_on: [fetch]
  - id: store
    depends_on: [transform]
```

### Diamond Pattern

```yaml
steps:
  - id: fetch

  - id: process_a
    depends_on: [fetch]
  - id: process_b
    depends_on: [fetch]

  - id: merge
    depends_on: [process_a, process_b]
```

### Retry with Fallback

```yaml
steps:
  - id: try_primary
    tool: primary.api
    on_error:
      action: retry
      retry_count: 2

  - id: use_fallback
    tool: secondary.api
    depends_on: [try_primary]
    condition: "{{ne .steps.try_primary.status \"completed\"}}"
```

## Validation Rules

- ✅ Workflow name: `^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$` (1-64 chars)
- ✅ Step IDs must be unique
- ✅ All `depends_on` step IDs must exist
- ✅ No circular dependencies
- ✅ Tool format: `workload_id.tool_name`
- ✅ Max retry count: 10
- ✅ Max workflow steps: 100

## Debugging

### Check Workflow Status

```yaml
# In VirtualMCPCompositeToolDefinition
status:
  validationStatus: Valid|Invalid
  validationErrors:
    - "error message here"
  referencedBy:
    - namespace: default
      name: vmcp-server-1
```

### Common Issues

| Error | Cause | Fix |
|-------|-------|-----|
| "output not found" | Missing `depends_on` | Add dependency |
| "circular dependency" | Cycle in `depends_on` | Remove cycle |
| "tool not found" | Invalid tool reference | Check `workload.tool` format |
| "template error" | Invalid Go template | Fix template syntax |

## Performance Tips

1. ✅ Remove unnecessary `depends_on` constraints
2. ✅ Group related steps in same execution level
3. ✅ Set realistic timeouts based on SLAs
4. ✅ Use retry for transient failures only
5. ✅ Keep steps focused (one responsibility)

## Links

- [Detailed Guide](virtualmcpcompositetooldefinition-guide.md)
- [Advanced Patterns](advanced-workflow-patterns.md)
- [Operator Installation](deploying-toolhive-operator.md)
