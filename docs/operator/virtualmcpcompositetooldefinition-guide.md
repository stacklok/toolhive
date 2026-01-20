# VirtualMCPCompositeToolDefinition Guide

## Overview

`VirtualMCPCompositeToolDefinition` is a Kubernetes Custom Resource Definition (CRD) that enables defining reusable composite workflows for Virtual MCP Servers. These workflows orchestrate multiple tool calls into complex operations that can be referenced by multiple `VirtualMCPServer` instances.

## Key Features

- **Reusable Workflows**: Define complex workflows once and reference them from multiple Virtual MCP Servers
- **Parameter Schema**: Define typed input parameters with validation
- **Template Support**: Use Go templates for dynamic argument values
- **Error Handling**: Configure retry logic and failure handling strategies
- **Dependency Management**: Define step dependencies with automatic cycle detection
- **Validation**: Automatic validation of workflow structure, templates, and dependencies
- **Status Tracking**: Track validation status and which Virtual MCP Servers reference each workflow

## Basic Workflow Structure

A `VirtualMCPCompositeToolDefinition` consists of:

1. **Metadata**: Standard Kubernetes metadata (name, namespace, labels, annotations)
2. **Spec**: Workflow definition including name, description, parameters, steps, timeout, and failure mode
3. **Status**: Validation status, errors, and references from Virtual MCP Servers

## Workflow Specification

### Name and Description

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPCompositeToolDefinition
metadata:
  name: deploy-app
  namespace: default
spec:
  # Workflow name exposed as a composite tool
  name: deploy_app

  # Human-readable description
  description: Deploy application to Kubernetes cluster

  # ... steps ...
```

**Validation Rules**:
- `name` must match pattern: `^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`
- `name` length: 1-64 characters
- `description` is required and cannot be empty

### Parameters

Parameters are defined using standard JSON Schema format, per the MCP specification. The top-level must be `type: object` with `properties` defining the individual parameters:

```yaml
spec:
  name: deploy_app
  description: Deploy application with configuration
  parameters:
    type: object
    properties:
      environment:
        type: string
        description: Target environment (dev, staging, prod)
      replicas:
        type: integer
        description: Number of pod replicas
        default: 3
      enable_monitoring:
        type: boolean
        description: Enable Prometheus monitoring
        default: true
    required:
      - environment
```

**Supported Property Types** (per JSON Schema):
- `string`
- `integer`
- `number`
- `boolean`
- `array`
- `object`

### Steps

Define workflow steps that execute tools:

```yaml
spec:
  steps:
    - id: validate_deployment
      type: tool
      tool: kubectl.validate
      arguments:
        namespace: "{{.params.environment}}"
        manifest: "deployment.yaml"

    - id: apply_deployment
      type: tool
      tool: kubectl.apply
      arguments:
        namespace: "{{.params.environment}}"
        replicas: "{{.params.replicas}}"
      dependsOn:
        - validate_deployment

    - id: verify_health
      type: tool
      tool: kubectl.wait
      arguments:
        resource: "deployment/myapp"
        condition: "available"
        timeout: "5m"
      dependsOn:
        - apply_deployment
```

**Step Types**:

#### tool (Phase 1)
Execute a backend tool. The `tool` field must be in format `workload.tool_name`.

```yaml
- id: deploy
  type: tool
  tool: kubectl.apply
  arguments:
    manifest: "{{.params.manifest}}"
```

#### elicitation (Phase 2)
Request user input during workflow execution.

```yaml
- id: confirm_production
  type: elicitation
  message: "Deploy to production? This will affect live users."
  schema:
    type: boolean
  timeout: 5m
  defaultResponse: false
```

### Dependencies

Define execution order using `dependsOn`:

```yaml
spec:
  steps:
    - id: step1
      type: tool
      tool: workload.tool_a

    - id: step2
      type: tool
      tool: workload.tool_b
      dependsOn:
        - step1

    - id: step3
      type: tool
      tool: workload.tool_c
      dependsOn:
        - step1
        - step2
```

**Validation**:
- Automatic cycle detection prevents circular dependencies
- All referenced step IDs must exist
- **DAG Execution**: Steps are executed using a Directed Acyclic Graph (DAG) model that automatically runs independent steps in parallel while respecting dependencies

> **Note**: For advanced workflow patterns including parallel execution, error handling strategies, and performance optimization, see the [Advanced Workflow Patterns Guide](advanced-workflow-patterns.md).

### Error Handling

Configure how steps handle errors:

```yaml
- id: flaky_operation
  tool: external.api_call
  onError:
    action: retry
    maxRetries: 3
  timeout: 30s

- id: optional_notification
  tool: slack.notify
  onError:
    action: continue

- id: critical_step
  tool: database.migrate
  onError:
    action: abort  # Default behavior
```

**Error Handling Actions**:
- `abort`: Stop execution on error (default)
- `continue`: Continue to next step, ignoring error
- `retry`: Retry the step up to `maxRetries` times

### Default Results

When a step may be skipped (due to a condition) or may fail with `continue` error handling, you can specify `defaultResults` to provide fallback output values for downstream steps:

```yaml
- id: optional_enrichment
  type: tool
  tool: enrichment.service
  condition: "{{.params.enable_enrichment}}"
  arguments:
    data: "{{.params.input}}"
  # When skipped, use these default values as the step's output
  defaultResults:
    text: "no enrichment performed"

- id: use_result
  type: tool
  tool: processor.handle
  dependsOn:
    - optional_enrichment
  arguments:
    # This template works whether optional_enrichment ran or was skipped
    enriched_data: "{{.steps.optional_enrichment.output.text}}"
```

**When to Use `defaultResults`**:
- Step has a `condition` that may evaluate to false
- Step has `onError.action: continue` and may fail
- Downstream steps reference this step's output in templates

**Key Points**:
- `defaultResults` is a map where keys correspond to output field names
- Values must match the expected output structure from the backend tool
- Backend tool calls store text content under the `text` key, so use `defaultResults.text` for text outputs
- Validation will error if a skippable step's output is referenced but `defaultResults` is not specified for that field
- `defaultResults` do not need to be specified for outputs that are not referenced in the composite tool definition.

**Example with error handling**:

```yaml
- id: external_lookup
  type: tool
  tool: external.api
  onError:
    action: continue  # Continue workflow even if this fails
  defaultResults:
    text: "{\"status\": \"unavailable\", \"data\": null}"

- id: process_result
  type: tool
  tool: internal.process
  dependsOn:
    - external_lookup
  arguments:
    lookup_result: "{{.steps.external_lookup.output.text}}"
```

### Timeouts

Configure timeouts at workflow and step level:

```yaml
spec:
  name: timed_workflow
  description: Workflow with timeout constraints

  # Overall workflow timeout
  timeout: 30m

  steps:
    - id: quick_check
      tool: health.check
      timeout: 10s

    - id: long_operation
      tool: backup.create
      timeout: 20m
```

**Timeout Format**: Duration string like `30s`, `5m`, `1h`, `1h30m`

### Failure Modes

Control workflow behavior when steps fail:

```yaml
spec:
  name: resilient_deployment
  description: Deploy with multiple retries

  # Failure handling strategy
  failureMode: continue

  steps:
    - id: deploy_primary
      tool: kubectl.apply
      arguments:
        region: primary

    - id: deploy_backup
      tool: kubectl.apply
      arguments:
        region: backup
```

**Failure Modes**:
- `abort`: Stop on first failure (default)
- `continue`: Execute all steps regardless of failures

### Template Syntax

Use Go template syntax for dynamic values:

```yaml
arguments:
  # Access parameters
  namespace: "{{.params.environment}}"

  # Access previous step results (Phase 2)
  deployment_id: "{{.steps.deploy.output.id}}"

  # Conditional logic (Phase 2)
  enabled: "{{if .params.production}}true{{else}}false{{end}}"
```

**Available Template Context**:
- `.params.<name>`: Access workflow parameters
- `.steps.<step_id>.<field>`: Access step results (Phase 2)

**Available Template Functions**:

Composite Tools supports all the built-in functions from [text/template](https://pkg.go.dev/text/template#hdr-Functions) (`eq`, `ne`, `lt`, `le`, `gt`, `ge`, `and`, `or`, `not`, `index`, `len`, `printf`, etc.) plus custom functions:

- `json`: Encode a value as a JSON string
- `fromJson`: Parse a JSON string into a value (useful when tools return JSON as text)
- `quote`: Quote a string value

### Step Output Format

Backend tools can return results in two formats, which affects how you access the data in templates:

**Structured Content (Object Response)**

When a backend tool returns structured content (an object), fields are directly accessible:

```yaml
# If get_user returns: {"name": "Alice", "profile": {"email": "alice@example.com"}}
arguments:
  user_name: "{{.steps.get_user.output.name}}"
  email: "{{.steps.get_user.output.profile.email}}"
```

**Unstructured Content (Text Response)**

When a backend tool returns text content, it is stored under the `text` key:

```yaml
# If echo_tool returns: "Hello, world!"
arguments:
  message: "{{.steps.echo_tool.output.text}}"
```

If a tool returns JSON as text content, use the `fromJson` function to parse it and access fields:

```yaml
# If api_call returns text: '{"user": {"name": "Alice", "email": "alice@example.com"}}'
arguments:
  name: "{{(fromJson .steps.api_call.output.text).user.name}}"
  email: "{{(fromJson .steps.api_call.output.text).user.email}}"
```

> **Important**: Structured content must be an object (map). If a tool returns an array, primitive, or other non-object type, it falls back to unstructured content handling.

### Numeric Values in Templates

All numeric values from JSON are unmarshaled as `float64`. When using numeric comparisons in templates, always use float literals:

```yaml
# Correct: use float literal (10.0)
value: '{{if ge .steps.get_stats.output.count 10.0}}high{{else}}low{{end}}'

# Incorrect: integer literal will cause type mismatch error
value: '{{if ge .steps.get_stats.output.count 10}}high{{else}}low{{end}}'
```

This applies to all numeric comparisons (`eq`, `ne`, `lt`, `le`, `gt`, `ge`) when comparing against step output values.

## Complete Examples

### Example 1: Simple Deployment

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPCompositeToolDefinition
metadata:
  name: simple-deploy
  namespace: production
spec:
  name: deploy_app
  description: Deploy application to Kubernetes

  parameters:
    type: object
    properties:
      environment:
        type: string
        description: Target environment
    required:
      - environment

  steps:
    - id: apply
      type: tool
      tool: kubectl.apply
      arguments:
        namespace: "{{.params.environment}}"
        manifest: "app.yaml"

  timeout: 5m
  failureMode: abort
```

### Example 2: Deploy with Verification

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPCompositeToolDefinition
metadata:
  name: deploy-and-verify
  namespace: production
spec:
  name: deploy_and_verify
  description: Deploy application and verify it's healthy

  parameters:
    type: object
    properties:
      environment:
        type: string
        description: Target deployment environment
      replicas:
        type: integer
        default: 3
      health_check_timeout:
        type: string
        default: "5m"
    required:
      - environment

  steps:
    - id: validate_config
      type: tool
      tool: kubectl.validate
      arguments:
        namespace: "{{.params.environment}}"
        manifest: "deployment.yaml"

    - id: apply_deployment
      type: tool
      tool: kubectl.apply
      arguments:
        namespace: "{{.params.environment}}"
        replicas: "{{.params.replicas}}"
        manifest: "deployment.yaml"
      dependsOn:
        - validate_config
      onError:
        action: retry
        maxRetries: 3

    - id: wait_for_ready
      type: tool
      tool: kubectl.wait
      arguments:
        namespace: "{{.params.environment}}"
        resource: "deployment/myapp"
        condition: "available"
        timeout: "{{.params.health_check_timeout}}"
      dependsOn:
        - apply_deployment

    - id: notify_success
      type: tool
      tool: slack.send
      arguments:
        channel: "#deployments"
        message: "Deployed to {{.params.environment}} successfully"
      dependsOn:
        - wait_for_ready
      onError:
        action: continue

  timeout: 30m
  failureMode: abort
```

### Example 3: Incident Investigation

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPCompositeToolDefinition
metadata:
  name: investigate-incident
  namespace: sre
spec:
  name: investigate_incident
  description: Gather diagnostic information for incident investigation

  parameters:
    type: object
    properties:
      service:
        type: string
        description: Service name to investigate
      namespace:
        type: string
        description: Kubernetes namespace
      time_range:
        type: string
        default: "1h"
        description: Time range for log collection
    required:
      - service
      - namespace

  steps:
    - id: get_pod_status
      type: tool
      tool: kubectl.get
      arguments:
        resource: "pods"
        namespace: "{{.params.namespace}}"
        selector: "app={{.params.service}}"

    - id: get_recent_logs
      type: tool
      tool: kubectl.logs
      arguments:
        namespace: "{{.params.namespace}}"
        selector: "app={{.params.service}}"
        since: "{{.params.time_range}}"
      dependsOn:
        - get_pod_status

    - id: check_recent_events
      type: tool
      tool: kubectl.events
      arguments:
        namespace: "{{.params.namespace}}"
        resource: "{{.params.service}}"
      dependsOn:
        - get_pod_status

    - id: query_metrics
      type: tool
      tool: prometheus.query
      arguments:
        query: "rate(http_requests_total{service=\"{{.params.service}}\"}[5m])"
        time: "now"
      dependsOn:
        - get_pod_status

    - id: create_report
      type: tool
      tool: jira.create_issue
      arguments:
        project: "SRE"
        summary: "Incident investigation for {{.params.service}}"
        description: "Automated diagnostic data collected"
      dependsOn:
        - get_recent_logs
        - check_recent_events
        - query_metrics
      onError:
        action: continue

  timeout: 15m
  failureMode: continue
```

### Example 4: Multi-Stage Deployment

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPCompositeToolDefinition
metadata:
  name: canary-deployment
  namespace: production
spec:
  name: canary_deployment
  description: Progressive canary deployment with rollback capability

  parameters:
    type: object
    properties:
      service:
        type: string
        description: Service name for canary deployment
      image:
        type: string
        description: Container image to deploy
      canary_percentage:
        type: integer
        default: 10
      success_threshold:
        type: number
        default: 0.99
    required:
      - service
      - image

  steps:
    - id: validate_image
      type: tool
      tool: registry.inspect
      arguments:
        image: "{{.params.image}}"

    - id: deploy_canary
      type: tool
      tool: kubectl.patch
      arguments:
        resource: "deployment/{{.params.service}}-canary"
        image: "{{.params.image}}"
        replicas: "{{.params.canary_percentage}}"
      dependsOn:
        - validate_image
      timeout: 5m

    - id: wait_canary_ready
      type: tool
      tool: kubectl.wait
      arguments:
        resource: "deployment/{{.params.service}}-canary"
        condition: "available"
        timeout: "10m"
      dependsOn:
        - deploy_canary

    - id: monitor_canary
      type: tool
      tool: prometheus.query
      arguments:
        query: "rate(http_requests_total{deployment=\"{{.params.service}}-canary\",status=\"200\"}[5m])"
        duration: "5m"
      dependsOn:
        - wait_canary_ready
      timeout: 10m

    - id: validate_metrics
      type: tool
      tool: metrics.evaluate
      arguments:
        success_rate: "{{.params.success_threshold}}"
        deployment: "{{.params.service}}-canary"
      dependsOn:
        - monitor_canary

    - id: promote_to_production
      type: tool
      tool: kubectl.patch
      arguments:
        resource: "deployment/{{.params.service}}"
        image: "{{.params.image}}"
      dependsOn:
        - validate_metrics
      onError:
        action: abort

    - id: notify_success
      type: tool
      tool: slack.send
      arguments:
        channel: "#deployments"
        message: "Canary deployment of {{.params.service}} promoted to production"
      dependsOn:
        - promote_to_production
      onError:
        action: continue

  timeout: 1h
  failureMode: abort
```

## Referencing Workflows from VirtualMCPServer

To use a composite workflow in a Virtual MCP Server:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: production-vmcp
  namespace: default
spec:
  config:
    groupRef: production-backends

  # Reference composite tool definitions
  compositeToolRefs:
    - name: deploy-app
    - name: deploy-and-verify
    - name: investigate-incident
    - name: canary-deployment
```

The workflows will be exposed as tools in the Virtual MCP Server with their configured names (e.g., `deploy_app`, `investigate_incident`).

## Status and Validation

Check workflow validation status:

```bash
kubectl get virtualmcpcompositetooldefinition deploy-app -o yaml
```

```yaml
status:
  validationStatus: Valid
  observedGeneration: 1
  referencingVirtualServers:
    - production-vmcp
    - staging-vmcp
  conditions:
    - type: Ready
      status: "True"
      reason: WorkflowReady
      message: Workflow is valid and ready to use
      lastTransitionTime: "2024-01-15T10:00:00Z"
    - type: WorkflowValidated
      status: "True"
      reason: ValidationSuccess
      message: All validation checks passed
      lastTransitionTime: "2024-01-15T10:00:00Z"
```

### Validation Errors

If validation fails:

```yaml
status:
  validationStatus: Invalid
  validationErrors:
    - "spec.steps[1].dependsOn references unknown step \"nonexistent\""
    - "spec.steps[2].tool must be in format 'workload.tool_name'"
  conditions:
    - type: Ready
      status: "False"
      reason: WorkflowNotReady
      message: Workflow has validation errors
    - type: WorkflowValidated
      status: "False"
      reason: ValidationFailed
      message: Validation failed with 2 errors
```

## Validation Rules

The CRD includes comprehensive validation:

### Name Validation
- Pattern: `^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`
- Length: 1-64 characters
- Lowercase letters, numbers, hyphens, underscores only

### Step Validation
- Unique step IDs
- Valid step types (`tool`, `elicitation`)
- Tool references in format `workload.tool_name`
- Valid Go template syntax in arguments
- No circular dependencies

### Parameter Validation
- Valid parameter types
- Required type field

### Duration Validation
- Pattern: `^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
- Examples: `30s`, `5m`, `1h30m`

## Best Practices

1. **Use Descriptive Names**: Choose clear, descriptive workflow names that indicate their purpose
2. **Document Parameters**: Provide clear descriptions for all parameters
3. **Set Appropriate Timeouts**: Configure realistic timeouts for workflows and steps
4. **Handle Errors Gracefully**: Use appropriate error handling strategies (retry, continue, abort)
5. **Validate Early**: Add validation steps early in the workflow
6. **Keep Workflows Focused**: Create single-purpose workflows rather than monolithic ones
7. **Use Dependencies**: Define step dependencies to ensure correct execution order
8. **Template Testing**: Test template syntax carefully to avoid runtime errors
9. **Monitor References**: Check status.referencingVirtualServers to understand workflow usage
10. **Version Workflows**: Use labels or annotations to version workflows

## Troubleshooting

### Workflow Not Valid

**Problem**: `validationStatus: Invalid`

**Solution**: Check `status.validationErrors` for detailed error messages. Common issues:
- Invalid tool reference format (must be `workload.tool_name`)
- Circular dependencies in `dependsOn`
- Invalid template syntax
- Unknown step IDs in dependencies

### Workflow Not Referenced

**Problem**: Workflow defined but not appearing in Virtual MCP Server

**Solution**:
1. Ensure `compositeToolRefs` includes the workflow in VirtualMCPServer spec
2. Check that namespace matches between resources
3. Verify workflow has `validationStatus: Valid`

### Template Errors

**Problem**: Runtime errors in template evaluation

**Solution**:
1. Validate template syntax using Go template parser
2. Ensure referenced parameters exist in `spec.parameters`
3. Check template expressions for typos

## Phase 2 Features

Phase 2 implementation status:

### ✅ Completed

- ✅ **DAG Execution**: Parallel execution of independent steps via dependency graph
- ✅ **Step Output Access**: Reference previous step outputs in templates
- ✅ **Advanced Retry Policies**: Exponential backoff with configurable retry count and delay
- ✅ **Workflow State Management**: In-memory state tracking with pluggable backend interface
- ✅ **Advanced Error Handling**: Per-step and workflow-level error strategies (abort, continue, retry)
- ✅ **Workflow Timeouts**: Configurable timeouts at workflow and step levels
- ✅ **Conditional Execution**: Skip steps based on template conditions

See the [Advanced Workflow Patterns Guide](advanced-workflow-patterns.md) for detailed documentation and examples.

### 🚧 Planned (Phase 2 Remaining)

The following Phase 2 features are planned for future releases:

- **Distributed State Store**: Redis/Database backend for multi-instance deployments
- **Step Caching**: Cache step results based on cache keys
- **Output Transformation**: Advanced output transformation using templates
- **Workflow Resumption**: Resume workflows after system restart

## API Reference

For complete API reference including all fields and validation rules, see the [CRD API documentation](./crd-api.md#virtualmcpcompositetooldefinition).

## Related Resources

- [VirtualMCPServer Guide](./virtualmcpserver-guide.md)
- [Composite Tools Proposal](../proposals/THV-2106-virtual-mcp-server.md)
- [Operator Installation Guide](./installation.md)
