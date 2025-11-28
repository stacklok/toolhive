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
  failureMode: abort             # Optional: abort|continue (default: abort)

  parameters:                    # Optional: input parameters
    param_name:
      type: string
      description: Description of the parameter
      required: false

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
    dependsOn: [fetch_a, fetch_b, fetch_c]
```

## Step Dependencies

```yaml
steps:
  - id: step1

  - id: step2
    dependsOn: [step1]          # Runs after step1 completes

  - id: step3
    dependsOn: [step1, step2]   # Waits for both step1 AND step2
```

## Template Syntax

Workflows use Go's [text/template](https://pkg.go.dev/text/template) syntax with additional context variables and functions.

### Basic Access

```yaml
# Access input parameters
"{{.params.parameter_name}}"

# Access step outputs
"{{.steps.step_id.output}}"
"{{.steps.step_id.output.field_name}}"
"{{.steps.step_id.status}}"     # completed|failed|skipped|running

# Access workflow-scoped variables
"{{.vars.variable_name}}"

# Access step errors
"{{.steps.step_id.error}}"
```

### Functions

```yaml
# JSON encoding
arguments:
  data: "{{json .steps.step1.output}}"

# String quoting
arguments:
  quoted: "{{quote .params.value}}"
```

### Conditional Logic

```yaml
# Comparison operators (eq, ne, lt, le, gt, ge)
condition: "{{eq .steps.step1.status \"completed\"}}"
condition: "{{ne .steps.step1.status \"failed\"}}"
condition: "{{gt .steps.step1.output.count 10}}"

# Boolean operators (and, or, not)
condition: "{{and .params.enabled (eq .steps.step1.status \"completed\")}}"
condition: "{{or .params.force (gt .steps.check.output.count 0)}}"
condition: "{{not .params.disabled}}"
```

### Advanced Features

All Go template built-ins are available: `index`, `len`, `range`, `with`, `printf`, etc. See [Go text/template documentation](https://pkg.go.dev/text/template) for complete reference.

## Error Handling

### Workflow-Level

```yaml
spec:
  failureMode: abort             # Stop on first error (default)
  failureMode: continue          # Log errors, continue workflow
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
      action: continue

  # Retry with exponential backoff
  - id: resilient
    tool: external.api
    on_error:
      action: retry
      maxRetries: 3             # Max 3 retries (4 total attempts)
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
    dependsOn: [fetch_1, fetch_2, fetch_3]
```

### Sequential Pipeline

```yaml
steps:
  - id: fetch
  - id: transform
    dependsOn: [fetch]
  - id: store
    dependsOn: [transform]
```

### Diamond Pattern

```yaml
steps:
  - id: fetch

  - id: process_a
    dependsOn: [fetch]
  - id: process_b
    dependsOn: [fetch]

  - id: merge
    dependsOn: [process_a, process_b]
```

### Retry with Fallback

```yaml
steps:
  - id: try_primary
    tool: primary.api
    on_error:
      action: retry
      maxRetries: 2

  - id: use_fallback
    tool: secondary.api
    dependsOn: [try_primary]
    condition: "{{ne .steps.try_primary.status \"completed\"}}"
```

## Validation Rules

- ✅ Workflow name: `^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$` (1-64 chars)
- ✅ Step IDs must be unique
- ✅ All `dependsOn` step IDs must exist
- ✅ No circular dependencies
- ✅ Tool format: `workload_id.tool_name`
- ✅ Max retry count: 10 (runtime capped - values > 10 are silently reduced with warning)
- ✅ Max workflow steps: 100 (runtime enforced - workflows > 100 steps fail validation)

**Note**: Max retry and max steps limits are currently enforced at runtime. Future work may add CRD-level validation (`+kubebuilder:validation:MaxItems=100`) and webhook validation to fail at submission time rather than execution time.

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
| "output not found" | Missing `dependsOn` | Add dependency |
| "circular dependency" | Cycle in `dependsOn` | Remove cycle |
| "tool not found" | Invalid tool reference | Check `workload.tool` format |
| "template error" | Invalid Go template | Fix template syntax |

## Performance Tips

1. ✅ Remove unnecessary `dependsOn` constraints
2. ✅ Group related steps in same execution level
3. ✅ Set realistic timeouts based on SLAs
4. ✅ Use retry for transient failures only
5. ✅ Keep steps focused (one responsibility)

## Links

- [Detailed Guide](virtualmcpcompositetooldefinition-guide.md)
- [Advanced Patterns](advanced-workflow-patterns.md)
- [Operator Installation](../kind/deploying-toolhive-operator.md)
